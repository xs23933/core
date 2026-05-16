package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/xs23933/core/v3"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Route struct {
	ID          string            `json:"id"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	ServiceName string            `json:"service_name"`
	GRPCMethod  string            `json:"grpc_method"`
	Description string            `json:"description"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     bool              `json:"enabled"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// CircuitBreaker 熔断器
type CircuitBreaker struct {
	mu           sync.Mutex
	lastFailTime time.Time
	state        int // 0: closed, 1: open, 2: half-open
	failCount    int
}

const (
	circuitClosed   = 0
	circuitOpen     = 1
	circuitHalfOpen = 2
)

const (
	circuitFailThreshold = 5                // 连续失败 5 次，熔断
	circuitCooldown      = 30 * time.Second // 熔断后 30s 尝试恢复
)

// serviceInstance 用于解析 etcd 中注册的服务实例信息
type serviceInstance struct {
	Addr    string `json:"addr"`
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}

// EtcdGateway etcd 网关
type EtcdGateway struct {
	app     *core.Core
	etcdCli *clientv3.Client
	prefix  string
	config  *Config

	mu     sync.RWMutex
	routes map[string]*Route
	// grpcServiceRoutes 按 gRPC 服务名索引路由，用于清理已删除方法的旧路由
	grpcServiceRoutes map[string]map[string]bool

	watchCtx    context.Context
	watchCancel context.CancelFunc

	circuitStates sync.Map // map[string]*CircuitBreaker

	// 连接池
	connPool *ConnectionPool
}

type Config struct {
	EtcdEndpoints   []string      `yaml:"etcd_endpoints"`
	EtcdDialTimeout time.Duration `yaml:"etcd_dial_timeout"`
	RoutePrefix     string        `yaml:"route_prefix"`
	HTTPAddr        string        `yaml:"http_addr"`
}

// circuitBreakerCheck 检查熔断器状态，返回 true 表示可以尝试连接
func (gw *EtcdGateway) circuitBreakerCheck(serviceName string) bool {
	val, ok := gw.circuitStates.Load(serviceName)
	if !ok {
		return true
	}
	cb := val.(*CircuitBreaker)
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == circuitClosed {
		return true
	}

	if cb.state == circuitOpen {
		if time.Since(cb.lastFailTime) > circuitCooldown {
			cb.state = circuitHalfOpen
			core.D("[Gateway] circuit breaker for %s entering half-open state", serviceName)
			return true
		}
		return false
	}

	return true
}

func (gw *EtcdGateway) circuitBreakerRecordSuccess(serviceName string) {
	val, ok := gw.circuitStates.Load(serviceName)
	if !ok {
		return
	}
	cb := val.(*CircuitBreaker)
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == circuitHalfOpen || cb.state == circuitOpen {
		core.D("[Gateway] circuit breaker for %s closed (recovered)", serviceName)
	}
	cb.state = circuitClosed
	cb.failCount = 0
}

func (gw *EtcdGateway) circuitBreakerRecordFailure(serviceName string) {
	val, _ := gw.circuitStates.LoadOrStore(serviceName, &CircuitBreaker{})
	cb := val.(*CircuitBreaker)
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failCount++
	cb.lastFailTime = time.Now()

	if cb.failCount >= circuitFailThreshold && cb.state == circuitClosed {
		cb.state = circuitOpen
		core.D("[Gateway] circuit breaker for %s opened (tripped after %d failures)", serviceName, cb.failCount)
	}
}

// grpcStatusToHTTP 将 gRPC status code 映射为 HTTP 状态码
//
// 如果错误码大于999，则返回200状态码 由客户端判断code 信息处理错误
func grpcStatusToHTTP(err error) int {
	st, ok := status.FromError(err)
	if !ok {
		return core.StatusInternalServerError
	}

	if st.Code() > 999 {
		return core.StatusOK
	}

	switch st.Code() {
	case codes.InvalidArgument:
		return core.StatusBadRequest
	case codes.NotFound:
		return core.StatusNotFound
	case codes.AlreadyExists:
		return core.StatusConflict
	case codes.PermissionDenied:
		return core.StatusForbidden
	case codes.Unauthenticated:
		return core.StatusUnauthorized
	case codes.ResourceExhausted:
		return core.StatusTooManyRequests
	case codes.Unavailable:
		return core.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		return core.StatusGatewayTimeout
	case codes.Canceled:
		return 499 // client disconnected
	default:
		return core.StatusInternalServerError
	}
}

// grpcErrorToResponse 将 gRPC 错误转为前端友好的 JSON 响应
func grpcErrorToResponse(err error) core.Map {
	st, ok := status.FromError(err)
	if !ok {
		return core.Map{"code": 500, "msg": "internal server error"}
	}

	code := int(st.Code())
	msg := st.Message()

	// 特殊处理：隐藏内部错误细节
	switch st.Code() {
	case codes.Internal:
		msg = "internal server error"
	case codes.Unavailable:
		msg = "service unavailable"
	}

	return core.Map{"code": code, "msg": msg}
}

func NewEtcdGateway(app *core.Core, conf ...*Config) (*EtcdGateway, error) {
	var config *Config
	if len(conf) > 0 {
		config = conf[0]
	}
	if config == nil {
		config = &Config{}
		config.EtcdEndpoints = app.Conf.GetStrings("etcd.endpoints")
		config.EtcdDialTimeout = time.Duration(app.Conf.GetInt64("etcd.dial_timeout", 5)) * time.Second
	}
	if config.RoutePrefix == "" {
		config.RoutePrefix = "/gateway/routes/"
	}
	if config.EtcdDialTimeout == 0 {
		config.EtcdDialTimeout = 5 * time.Second
	}

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   config.EtcdEndpoints,
		DialTimeout: config.EtcdDialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create etcd client failed: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	gw := &EtcdGateway{
		app:               app,
		etcdCli:           cli,
		prefix:            config.RoutePrefix,
		config:            config,
		routes:            make(map[string]*Route),
		grpcServiceRoutes: make(map[string]map[string]bool),
		watchCtx:          ctx,
		watchCancel:       cancel,
		connPool:          NewConnectionPool(),
	}

	if err := gw.loadAllRoutes(); err != nil {
		core.D("[Gateway] load all routes failed: %v", err)
	}

	gw.discoverAndConnectServices()

	app.ErrGroup().Go(func() error {
		gw.watchRoutes()
		return nil
	})

	gw.setupAdminAPI()

	app.OnShutdown(func() { gw.Close() })

	core.D("[Gateway] ✅ Etcd Gateway started")
	return gw, nil
}

// parseServiceKey 解析 etcd key，提取 serviceName 和 instanceID
// key 格式: /services/{serviceName}/{instanceID}
func parseServiceKey(key string) (serviceName, instanceID string, ok bool) {
	parts := strings.Split(key, "/")
	// /services/auth-service/auth-service-1 -> ["", "services", "auth-service", "auth-service-1"]
	if len(parts) < 4 {
		return "", "", false
	}
	return parts[2], parts[3], true
}

// parseServiceInstance 从 etcd value 解析服务实例信息
func parseServiceInstance(data []byte) (*serviceInstance, error) {
	var info serviceInstance
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	if info.Addr == "" {
		return nil, fmt.Errorf("empty addr")
	}
	// 如果 value 中没有 ID，从 key 中提取
	return &info, nil
}

// discoverAndConnectServices 启动时发现并连接所有服务
func (gw *EtcdGateway) discoverAndConnectServices() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := gw.etcdCli.Get(ctx, "/services/", clientv3.WithPrefix())
	if err != nil {
		core.Erro("[Gateway] discover services failed: %v", err)
		return
	}

	// 按 serviceName 分组实例
	type inst struct{ id, addr string }
	serviceInstances := make(map[string][]inst)

	for _, kv := range resp.Kvs {
		serviceName, instanceID, ok := parseServiceKey(string(kv.Key))
		if !ok {
			continue
		}
		info, err := parseServiceInstance(kv.Value)
		if err != nil {
			continue
		}
		if info.ID == "" {
			info.ID = instanceID
		}
		serviceInstances[serviceName] = append(serviceInstances[serviceName], inst{id: info.ID, addr: info.Addr})
	}

	if len(serviceInstances) == 0 {
		core.D("[Gateway] etcd has no services")
	} else {
		for serviceName, instances := range serviceInstances {
			go func(sn string, insts []inst) {
				pool := gw.connPool.GetOrCreate(gw.app, sn)
				var healthyProxy *ReflectionProxy
				for _, i := range insts {
					proxy, _, err := pool.AddOrUpdateInstance(i.id, i.addr)
					if err != nil {
						core.D("[Gateway] connect instance %s/%s at %s failed: %v", sn, i.id, i.addr, err)
						continue
					}
					if healthyProxy == nil {
						healthyProxy = proxy
					}
				}
				if healthyProxy != nil {
					gw.autoRegisterRoutes(sn, healthyProxy)
					core.D("[Gateway] ✅ service %s connected (instances: %d)", sn, pool.Size())
				}
			}(serviceName, instances)
		}
	}

	gw.app.ErrGroup().Go(func() error {
		gw.watchEtcdServices()
		return nil
	})
}

// connectInstance 连接单个服务实例（watch PUT 触发），带重试
func (gw *EtcdGateway) connectInstance(serviceName, instanceID, addr string) {
	if !gw.circuitBreakerCheck(serviceName) {
		return
	}

	pool := gw.connPool.GetOrCreate(gw.app, serviceName)

	// 尝试连接，带重试（服务可能 etcd 注册了但 gRPC 还没启动）
	var proxy *ReflectionProxy
	var changed bool
	var err error

	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*200) * time.Millisecond)
		}
		proxy, changed, err = pool.AddOrUpdateInstance(instanceID, addr)
		if err == nil {
			break
		}
	}

	if err != nil {
		core.D("[Gateway] connect instance %s/%s at %s failed: %v", serviceName, instanceID, addr, err)
		gw.circuitBreakerRecordFailure(serviceName)
		return
	}

	gw.circuitBreakerRecordSuccess(serviceName)

	if changed {
		core.D("[Gateway] ✅ instance %s/%s connected at %s", serviceName, instanceID, addr)
		gw.autoRegisterRoutes(serviceName, proxy)
	}
}

// removeInstance 移除单个服务实例（watch DELETE 触发）
func (gw *EtcdGateway) removeInstance(serviceName, instanceID string) {
	pool := gw.connPool.Get(serviceName)
	if pool == nil {
		return
	}

	pool.RemoveInstance(instanceID)
	core.D("[Gateway] instance %s/%s removed", serviceName, instanceID)

	// 如果没有实例了，移除整个池
	if pool.Size() == 0 {
		gw.connPool.Remove(serviceName)
		core.D("[Gateway] service %s fully disconnected", serviceName)
	}
}

// watchEtcdServices 监听 etcd 中的服务变化
func (gw *EtcdGateway) watchEtcdServices() {
	watchChan := gw.etcdCli.Watch(gw.watchCtx, "/services/", clientv3.WithPrefix())

	for resp := range watchChan {
		for _, ev := range resp.Events {
			key := string(ev.Kv.Key)
			serviceName, instanceID, ok := parseServiceKey(key)
			if !ok {
				continue
			}

			switch ev.Type {
			case clientv3.EventTypePut:
				info, err := parseServiceInstance(ev.Kv.Value)
				if err != nil {
					continue
				}
				if info.ID == "" {
					info.ID = instanceID
				}
				// core.D("[Gateway] watch: %s/%s registered at %s", serviceName, info.ID, info.Addr)

				go gw.connectInstance(serviceName, info.ID, info.Addr)

			case clientv3.EventTypeDelete:
				core.D("[Gateway] watch: %s/%s deregistered", serviceName, instanceID)
				go gw.removeInstance(serviceName, instanceID)
			}
		}
	}
}

// autoRegisterRoutes 自动为 gRPC 方法注册 HTTP 路由，同时清理已删除方法的旧路由
func (gw *EtcdGateway) autoRegisterRoutes(serviceName string, proxy *ReflectionProxy) {
	methods := proxy.Methods()

	gw.mu.Lock()
	defer gw.mu.Unlock()

	// 收集本次注册的所有 routeHash，按 gRPC 服务名分组
	newHashesByService := make(map[string]map[string]bool)

	for fullMethod, desc := range methods {
		httpMethod, httpPath := grpcToHTTP(desc.Package, desc.Service, desc.Method)

		serviceParts := strings.Split(desc.Service, ".")
		shortService := serviceParts[len(serviceParts)-1]
		grpcServiceName := strings.ToLower(strings.TrimSuffix(shortService, "Service"))

		routeID := fmt.Sprintf("auto-%s-%s", grpcServiceName, strings.TrimPrefix(fullMethod, "/"))
		routeIDHash := core.SHA256(routeID)

		if newHashesByService[grpcServiceName] == nil {
			newHashesByService[grpcServiceName] = make(map[string]bool)
		}
		newHashesByService[grpcServiceName][routeIDHash] = true

		// 先注销旧路由再注册（避免重复）
		if old, exists := gw.routes[routeIDHash]; exists {
			gw.unregisterRoute(old)
		}

		gw.routes[routeIDHash] = &Route{
			ID:          routeIDHash,
			Method:      httpMethod,
			Path:        httpPath,
			ServiceName: serviceName,
			GRPCMethod:  fullMethod,
			Description: fmt.Sprintf("auto-registered from %s", serviceName),
			Enabled:     true,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		gw.registerRoute(gw.routes[routeIDHash])
	}

	// 清理已删除方法的旧路由：该 gRPC 服务以前有但现在没有的路由
	for grpcSvc, newHashes := range newHashesByService {
		if oldHashes, exists := gw.grpcServiceRoutes[grpcSvc]; exists {
			for oldHash := range oldHashes {
				if !newHashes[oldHash] {
					if route, ok := gw.routes[oldHash]; ok {
						gw.unregisterRoute(route)
						delete(gw.routes, oldHash)
					}
				}
			}
		}
		gw.grpcServiceRoutes[grpcSvc] = newHashes
	}
}

// grpcToHTTP 将 gRPC 方法转换为 HTTP 路由
// e.g. ("v1.auth", "v1.auth.UserService", "PostLogin") -> ("POST", "/v1/auth/user/login")
func grpcToHTTP(pkg, service, method string) (httpMethod, path string) {
	httpMethod = "POST"
	methodName := method
	for _, prefix := range []string{"Post", "Get", "Put", "Delete"} {
		if strings.HasPrefix(method, prefix) {
			httpMethod = strings.ToUpper(prefix)
			methodName = strings.TrimPrefix(method, prefix)
			break
		}
	}

	pkgPath := "/" + strings.ReplaceAll(pkg, ".", "/")

	serviceParts := strings.Split(service, ".")
	shortService := serviceParts[len(serviceParts)-1]
	svcName := strings.ToLower(strings.TrimSuffix(shortService, "Service"))

	methodPath := parseMethodName(methodName)

	return httpMethod, fmt.Sprintf("%s/%s/%s", pkgPath, svcName, methodPath)
}

var re = regexp.MustCompile(`(?i)by/?`)

// parseMethodName 解析方法名，处理 By 关键字
func parseMethodName(methodName string) string {
	base := camelToKebab(methodName)

	result := re.ReplaceAllStringFunc(base, func(match string) string {
		return ":"
	})

	return result
}

// camelToKebab CamelCase -> kebab-case, underscore -> hyphen
func camelToKebab(s string) string {
	var result []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			result = append(result, '-')
			if i+1 < len(s) && 'A' <= s[i+1] && s[i+1] <= 'Z' {
				result = append(result, s[i+1]+32)
				i++
			}
		} else if i > 0 && 'A' <= c && c <= 'Z' {
			result = append(result, '/')
			result = append(result, c+32)
		} else if 'A' <= c && c <= 'Z' {
			result = append(result, c+32)
		} else {
			result = append(result, c)
		}
	}
	return string(result)
}

func (gw *EtcdGateway) loadAllRoutes() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := gw.etcdCli.Get(ctx, gw.prefix, clientv3.WithPrefix())
	if err != nil {
		core.Erro("[Gateway] load routes failed: %v", err)
		return err
	}

	for _, kv := range resp.Kvs {
		var route Route
		if err := json.Unmarshal(kv.Value, &route); err != nil {
			continue
		}
		if route.Enabled {
			gw.addOrUpdateRoute(&route)
		}
	}
	return nil
}

func (gw *EtcdGateway) watchRoutes() {
	watchChan := gw.etcdCli.Watch(gw.watchCtx, gw.prefix, clientv3.WithPrefix())
	for resp := range watchChan {
		for _, ev := range resp.Events {
			switch ev.Type {
			case clientv3.EventTypePut:
				var route Route
				if err := json.Unmarshal(ev.Kv.Value, &route); err != nil {
					continue
				}
				if route.Enabled {
					gw.addOrUpdateRoute(&route)
				} else {
					gw.removeRouteByID(route.ID)
				}
			case clientv3.EventTypeDelete:
				routeID := strings.TrimPrefix(string(ev.Kv.Key), gw.prefix)
				gw.removeRouteByID(routeID)
			}
		}
	}
}

func (gw *EtcdGateway) addOrUpdateRoute(route *Route) {
	gw.mu.Lock()
	defer gw.mu.Unlock()

	if old, exists := gw.routes[route.ID]; exists {
		gw.unregisterRoute(old)
	}

	gw.routes[route.ID] = route
	gw.registerRoute(route)
}

func (gw *EtcdGateway) removeRouteByID(routeID string) {
	gw.mu.Lock()
	defer gw.mu.Unlock()

	route, exists := gw.routes[routeID]
	if !exists {
		return
	}
	gw.unregisterRoute(route)
	delete(gw.routes, routeID)
}

func (gw *EtcdGateway) registerRoute(route *Route) {
	handler := gw.createProxyHandler(route)

	route.Path = strings.TrimSuffix(route.Path, "/")
	core.D("Add Route ✅: %s %s -> %s", route.Method, route.Path, route.GRPCMethod)

	switch strings.ToUpper(route.Method) {
	case "GET":
		gw.app.AddHandle([]string{"GET"}, route.Path, nil, handler)
	case "POST":
		gw.app.AddHandle([]string{"POST"}, route.Path, nil, handler)
	case "PUT":
		gw.app.AddHandle([]string{"PUT"}, route.Path, nil, handler)
	case "DELETE":
		gw.app.AddHandle([]string{"DELETE"}, route.Path, nil, handler)
	}
}

func (gw *EtcdGateway) unregisterRoute(route *Route) {
	gw.app.RemoveHandle([]string{strings.ToUpper(route.Method)}, strings.TrimSuffix(route.Path, "/"))
}

func (gw *EtcdGateway) createProxyHandler(route *Route) core.HandlerFunc {
	return func(ctx core.Ctx) error {
		proxy := gw.doGetProxy(route.ServiceName)
		if proxy == nil {
			core.Erro("[Gateway] proxy not available for service: %s", route.ServiceName)
			return ctx.Status(http.StatusServiceUnavailable).JSON(core.Map{
				"code":    503,
				"message": fmt.Sprintf("service %s unavailable", route.ServiceName),
			})
		}

		reqBody := make(core.Map, 8)
		for k, v := range ctx.ParamsMaps() {
			reqBody[k] = v
		}
		for k, v := range ctx.Request().URL.Query() {
			if len(v) > 0 {
				reqBody[k] = v[0]
			}
		}
		if ctx.Method() != "GET" {
			var bodyMap core.Map
			if err := ctx.Bind(&bodyMap); err == nil {
				maps.Copy(reqBody, bodyMap)
			}
		}

		jsonReq, err := sonic.Marshal(reqBody)
		if err != nil {
			return ctx.Status(http.StatusBadRequest).JSON(core.Map{
				"code": 400, "message": "invalid request body",
			})
		}

		md := metadata.New(nil)
		for _, h := range []string{"authorization", "x-request-id", "x-user-id"} {
			if val := ctx.GetHeader(h); val != "" {
				md.Set(h, val)
			}
		}

		for k, v := range ctx.Vars() {
			md.Set(k, fmt.Sprintf("%v", v))
		}

		grpcCtx := metadata.NewOutgoingContext(context.Background(), md)

		callCtx, cancel := context.WithTimeout(grpcCtx, 10*time.Second)
		defer cancel()

		jsonResp, err := proxy.Invoke(callCtx, route.GRPCMethod, jsonReq)
		if err != nil {
			return ctx.Status(grpcStatusToHTTP(err)).JSON(grpcErrorToResponse(err))
		}
		return ctx.Type("json").Send(jsonResp)
	}
}

func (gw *EtcdGateway) doGetProxy(serviceName string) *ReflectionProxy {
	if !gw.circuitBreakerCheck(serviceName) {
		core.D("[Gateway] doGetProxy: %s circuit open, fast fail", serviceName)
		return nil
	}

	pool := gw.connPool.Get(serviceName)
	if pool != nil {
		proxy := pool.Get()
		if proxy != nil {
			gw.circuitBreakerRecordSuccess(serviceName)
			return proxy
		}
	}

	// 池为空，尝试通过 EtcdDiscovery 发现
	if gw.app.EtcdDiscovery == nil {
		return nil
	}
	services := gw.app.EtcdDiscovery.GetServices(serviceName)
	if len(services) == 0 {
		return nil
	}

	// 创建池并添加实例
	newPool := gw.connPool.GetOrCreate(gw.app, serviceName)
	for _, s := range services {
		instanceID := s.ID
		if instanceID == "" {
			instanceID = s.Addr // fallback
		}
		newPool.AddOrUpdateInstance(instanceID, s.Addr)
	}

	proxy := newPool.Get()
	if proxy == nil {
		gw.circuitBreakerRecordFailure(serviceName)
		return nil
	}

	gw.circuitBreakerRecordSuccess(serviceName)
	return proxy
}

func (gw *EtcdGateway) setupAdminAPI() {
	gw.app.POST("/admin/gateway/routes", gw.localOnly(gw.createRoute))
	gw.app.PUT("/admin/gateway/routes/:id", gw.localOnly(gw.updateRoute))
	gw.app.DELETE("/admin/gateway/routes/:id", gw.localOnly(gw.deleteRoute))
	gw.app.GET("/admin/gateway/routes", gw.localOnly(gw.listRoutes))
	gw.app.GET("/admin/gateway/routes/:id", gw.localOnly(gw.getRoute))
}

func (gw *EtcdGateway) localOnly(fn func(core.Ctx) error) func(core.Ctx) error {
	return func(ctx core.Ctx) error {
		if !isLoopbackRemoteAddr(ctx.Request().RemoteAddr) {
			return ctx.Status(403).JSON(core.Map{"code": 403, "message": "forbidden"})
		}
		return fn(ctx)
	}
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (gw *EtcdGateway) createRoute(ctx core.Ctx) error {
	var route Route
	if err := ctx.Bind(&route); err != nil {
		return ctx.Status(400).JSON(core.Map{"code": 400, "message": err.Error()})
	}

	routeID := fmt.Sprintf("auto-%s-%s", route.ServiceName, strings.TrimPrefix(route.GRPCMethod, "/"))
	route.ID = core.SHA256(routeID)
	route.CreatedAt = time.Now()
	route.UpdatedAt = time.Now()
	route.Enabled = true

	data, err := json.Marshal(route)
	if err != nil {
		return ctx.Status(400).JSON(core.Map{"code": 400, "message": err.Error()})
	}
	putCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := gw.etcdCli.Put(putCtx, gw.prefix+route.ID, string(data)); err != nil {
		return ctx.Status(500).JSON(core.Map{"code": 500, "message": err.Error()})
	}

	return ctx.Status(201).JSON(core.Map{"code": 0, "message": "success", "data": route})
}

func (gw *EtcdGateway) updateRoute(ctx core.Ctx) error {
	routeID := ctx.Params("id")
	var route Route
	if err := ctx.Bind(&route); err != nil {
		return ctx.Status(400).JSON(core.Map{"code": 400, "message": err.Error()})
	}

	route.ID = routeID
	route.UpdatedAt = time.Now()

	data, err := json.Marshal(route)
	if err != nil {
		return ctx.Status(400).JSON(core.Map{"code": 400, "message": err.Error()})
	}
	putCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := gw.etcdCli.Put(putCtx, gw.prefix+routeID, string(data)); err != nil {
		return ctx.Status(500).JSON(core.Map{"code": 500, "message": err.Error()})
	}

	return ctx.JSON(core.Map{"code": 0, "message": "success"})
}

func (gw *EtcdGateway) deleteRoute(ctx core.Ctx) error {
	routeID := ctx.Params("id")
	deleteCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := gw.etcdCli.Delete(deleteCtx, gw.prefix+routeID); err != nil {
		return ctx.Status(500).JSON(core.Map{"code": 500, "message": err.Error()})
	}
	return ctx.ToJSONCode("success")
}

func (gw *EtcdGateway) listRoutes(ctx core.Ctx) error {
	gw.mu.RLock()
	routes := make([]*Route, 0, len(gw.routes))
	for _, r := range gw.routes {
		routes = append(routes, r)
	}
	gw.mu.RUnlock()

	return ctx.JSON(core.Map{"code": 0, "data": routes, "total": len(routes)})
}

func (gw *EtcdGateway) getRoute(ctx core.Ctx) error {
	routeID := ctx.Params("id")
	gw.mu.RLock()
	route, exists := gw.routes[routeID]
	gw.mu.RUnlock()

	if !exists {
		return ctx.Status(404).JSON(core.Map{"code": 404, "message": "route not found"})
	}
	return ctx.ToJSONCode(route)
}

func (gw *EtcdGateway) Close() error {
	if gw.watchCancel != nil {
		gw.watchCancel()
	}
	if gw.connPool != nil {
		gw.connPool.Close()
	}
	if gw.etcdCli != nil {
		return gw.etcdCli.Close()
	}
	return nil
}
