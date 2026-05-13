package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/xs23933/core/v3"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
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

type EtcdGateway struct {
	app     *core.Core
	etcdCli *clientv3.Client
	prefix  string

	mu     sync.RWMutex
	routes map[string]*Route

	watchCtx    context.Context
	watchCancel context.CancelFunc

	proxies map[string]*ReflectionProxy
	proxyMu sync.RWMutex
}

type Config struct {
	EtcdEndpoints   []string      `yaml:"etcd_endpoints"`
	EtcdDialTimeout time.Duration `yaml:"etcd_dial_timeout"`
	RoutePrefix     string        `yaml:"route_prefix"`
	HTTPAddr        string        `yaml:"http_addr"`
}

func NewEtcdGateway(app *core.Core, config *Config) (*EtcdGateway, error) {
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
		core.Erro("[Gateway] connectService failed: %v", err)
		return nil, fmt.Errorf("created etcd client failed: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	gw := &EtcdGateway{
		app:         app,
		etcdCli:     cli,
		prefix:      config.RoutePrefix,
		routes:      make(map[string]*Route),
		watchCtx:    ctx,
		watchCancel: cancel,
		proxies:     make(map[string]*ReflectionProxy),
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

// discoverAndConnectServices 启动时发现并连接所有服务
func (gw *EtcdGateway) discoverAndConnectServices() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := gw.etcdCli.Get(ctx, "/services/", clientv3.WithPrefix())
	if err != nil {
		core.Erro("[Gateway] connectService failed: %v", err)
		return
	}

	serviceSet := make(map[string]string, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		key := string(kv.Key)
		parts := strings.Split(key, "/")
		if len(parts) < 3 {
			continue
		}
		serviceName := parts[2]
		var info struct {
			Addr string `json:"addr"`
		}
		if err := json.Unmarshal(kv.Value, &info); err == nil && info.Addr != "" {
			serviceSet[serviceName] = info.Addr
		}
	}

	if len(serviceSet) == 0 {
		core.D("[Gateway] etcd has no services")
		return
	}

	for serviceName, serviceAddr := range serviceSet {
		gw.connectService(serviceName, serviceAddr)
	}

	gw.app.ErrGroup().Go(func() error {
		gw.watchEtcdServices()
		return nil
	})
}

// connectService 连接服务并自动注册路由（支持重连和重试）
func (gw *EtcdGateway) connectService(serviceName, serviceAddr string) {
	var proxy *ReflectionProxy
	var err error

	// 重试 3 次，每次等待递增时间
	for i := 0; i < 3; i++ {
		proxy, err = NewReflectionProxy(serviceAddr)
		if err == nil {
			break
		}
		// core.D("[Gateway] connectService retry %d/3 for %s at %s: %v", i+1, serviceName, serviceAddr, err)
		time.Sleep(time.Duration(i+1) * 200 * time.Millisecond)
	}
	if err != nil {
		core.Erro("[Gateway] connectService failed: %v", err)
		return
	}

	gw.proxyMu.Lock()
	gw.proxies[serviceName] = proxy
	gw.proxyMu.Unlock()

	// 清除旧路由后重新注册
	gw.removeAutoRoutes(serviceName)
	gw.autoRegisterRoutes(serviceName, proxy)

	core.D("[Gateway] ✅ service %s connected ", serviceName)
}

// watchEtcdServices 监听 etcd 中的服务变化
func (gw *EtcdGateway) watchEtcdServices() {
	watchChan := gw.etcdCli.Watch(gw.watchCtx, "/services/", clientv3.WithPrefix())

	for resp := range watchChan {
		for _, ev := range resp.Events {
			key := string(ev.Kv.Key)
			parts := strings.Split(key, "/")
			if len(parts) < 3 {
				continue
			}
			serviceName := parts[2]

			switch ev.Type {
			case clientv3.EventTypePut:
				var info struct {
					Addr string `json:"addr"`
				}
				if err := json.Unmarshal(ev.Kv.Value, &info); err != nil || info.Addr == "" {
					continue
				}
				core.D("[Gateway] watch: service %s registered at %s", serviceName, info.Addr)

				gw.proxyMu.Lock()
				if old, ok := gw.proxies[serviceName]; ok {
					old.Close()
					delete(gw.proxies, serviceName)
				}
				gw.proxyMu.Unlock()

				gw.connectService(serviceName, info.Addr)

			case clientv3.EventTypeDelete:
				core.D("[Gateway] watch: service %s deleted", serviceName)
				gw.proxyMu.Lock()
				if proxy, ok := gw.proxies[serviceName]; ok {
					proxy.Close()
					delete(gw.proxies, serviceName)
				}
				gw.proxyMu.Unlock()

				core.D("[Gateway] service %s disconnected", serviceName)
			}
		}
	}
}

// removeAutoRoutes 清除指定服务的自动注册路由
func (gw *EtcdGateway) removeAutoRoutes(serviceName string) {
	gw.mu.Lock()
	defer gw.mu.Unlock()

	for id, route := range gw.routes {
		if strings.HasPrefix(id, "auto-"+serviceName+"-") {
			gw.unregisterRoute(route)
			delete(gw.routes, id)
		}
	}
}

// autoRegisterRoutes 自动为 gRPC 方法注册 HTTP 路由
func (gw *EtcdGateway) autoRegisterRoutes(serviceName string, proxy *ReflectionProxy) {
	methods := proxy.Methods()

	gw.mu.Lock()
	defer gw.mu.Unlock()

	for fullMethod, desc := range methods {
		httpMethod, httpPath := grpcToHTTP(desc.Package, desc.Service, desc.Method)

		routeID := fmt.Sprintf("auto-%s-%s", serviceName, strings.TrimPrefix(fullMethod, "/"))
		routeIDHash := core.SHA256(routeID)

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
}

// grpcToHTTP 将 gRPC 方法转换为 HTTP 路由
// e.g. ("v1.auth", "v1.auth.UserService", "PostLogin") -> ("POST", "/v1/auth/user/login")
func grpcToHTTP(pkg, service, method string) (httpMethod, path string) {
	// 1. HTTP method from method name prefix
	httpMethod = "POST"
	methodName := method
	for _, prefix := range []string{"Post", "Get", "Put", "Delete"} {
		if strings.HasPrefix(method, prefix) {
			httpMethod = strings.ToUpper(prefix)
			methodName = strings.TrimPrefix(method, prefix)
			break
		}
	}

	// 2. Package to path: "v1.auth" -> "/v1/auth"
	pkgPath := "/" + strings.ReplaceAll(pkg, ".", "/")

	// 3. Service name: extract short name (last part), remove "Service" suffix
	serviceParts := strings.Split(service, ".")
	shortService := serviceParts[len(serviceParts)-1] // "UserService"
	svcName := strings.ToLower(strings.TrimSuffix(shortService, "Service"))

	// 4. Method name: parse By keyword or CamelCase to kebab-case
	methodPath := parseMethodName(methodName)

	return httpMethod, fmt.Sprintf("%s/%s/%s", pkgPath, svcName, methodPath)
}

// parseMethodName 解析方法名，处理 By 关键字
// e.g. "UserInfoById" -> "user/info/:id"
func parseMethodName(methodName string) string {
	before, after, ok := strings.Cut(methodName, "By")
	if !ok {
		return camelToKebab(methodName)
	}

	resource := before // "UserInfo"
	param := after     // "Id"

	// resource: CamelCase -> camel/case
	resourcePath := camelToSlash(resource)
	// param: -> :param
	paramPath := ":" + strings.ToLower(param)

	return resourcePath + paramPath
}

// camelToSlash CamelCase -> camel/case
func camelToSlash(s string) string {
	var result []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i > 0 && 'A' <= c && c <= 'Z' {
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

// camelToKebab CamelCase -> kebab-case, underscore -> hyphen
func camelToKebab(s string) string {
	var result []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			result = append(result, '-')
			// 下划线后不管大小写都直接小写，不作为新 CamelCase 段
			if i+1 < len(s) && 'A' <= s[i+1] && s[i+1] <= 'Z' {
				result = append(result, s[i+1]+32)
				i++ // skip next char
			}
		} else if i > 0 && 'A' <= c && c <= 'Z' {
			result = append(result, '-')
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
		core.Erro("[Gateway] connectService failed: %v", err)
		return err
	}

	for _, kv := range resp.Kvs {
		var route Route
		if err := json.Unmarshal(kv.Value, &route); err != nil {
			continue
		}
		if route.Enabled {
			gw.addRoute(&route)
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

func (gw *EtcdGateway) addRoute(route *Route) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
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
	// TODO: 框架层面需要支持路由注销
}

func (gw *EtcdGateway) createProxyHandler(route *Route) core.HandlerFunc {
	return func(ctx core.Ctx) error {
		proxy, err := gw.getOrCreateProxy(route.ServiceName)
		if err != nil {
			core.Erro("[Gateway] connectService failed: %v", err)
			return ctx.Status(http.StatusServiceUnavailable).JSON(core.Map{
				"code":    503,
				"message": fmt.Sprintf("service %s unavailable", route.ServiceName),
			})
		}

		// 构建请求参数
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
			core.Erro("[Gateway] connectService failed: %v", err)
			return ctx.Status(http.StatusBadRequest).JSON(core.Map{
				"code": 400, "message": "invalid request body",
			})
		}

		// 构建 metadata
		md := metadata.New(nil)
		for _, h := range []string{"authorization", "x-request-id", "x-user-id"} {
			if val := ctx.GetHeader(h); val != "" {
				md.Set(h, val)
			}
		}
		grpcCtx := metadata.NewOutgoingContext(context.Background(), md)

		callCtx, cancel := context.WithTimeout(grpcCtx, 10*time.Second)
		defer cancel()

		jsonResp, err := proxy.Invoke(callCtx, route.GRPCMethod, jsonReq)
		if err != nil {
			return ctx.ToJSONCode(nil, err)
		}

		core.Info("[Gateway] received response: %s", string(jsonResp))
		var result any
		sonic.Unmarshal(jsonResp, &result)

		return ctx.JSON(result)
	}
}

func (gw *EtcdGateway) getOrCreateProxy(serviceName string) (*ReflectionProxy, error) {
	const maxRetries = 3
	for range maxRetries {
		proxy := gw.doGetProxy(serviceName)
		if proxy != nil {
			return proxy, nil
		}
		gw.removeProxy(serviceName)
	}
	return nil, fmt.Errorf("service %s unavailable", serviceName)
}

func (gw *EtcdGateway) doGetProxy(serviceName string) *ReflectionProxy {
	// 优先使用缓存，但检查连接状态
	gw.proxyMu.RLock()
	proxy, ok := gw.proxies[serviceName]
	gw.proxyMu.RUnlock()
	if ok && proxy != nil && proxy.conn.GetState() == connectivity.Ready {
		// core.D("[Gateway] doGetProxy: returning cached proxy for %s, state=%v", serviceName, proxy.conn.GetState())
		return proxy
	}

	// 连接不健康或缓存为空，从 etcd 拉新地址
	if gw.app.EtcdDiscovery == nil {
		return nil
	}
	services := gw.app.EtcdDiscovery.GetServices(serviceName)
	if len(services) == 0 {
		return nil
	}
	proxy, err := NewReflectionProxy(services[0].Addr)
	if err != nil {
		core.Erro("[Gateway] create proxy for %s failed: %v", serviceName, err)
		return nil
	}
	// core.D("[Gateway] doGetProxy: created new proxy for %s at %s", serviceName, services[0].Addr)
	gw.proxyMu.Lock()
	gw.proxies[serviceName] = proxy
	gw.proxyMu.Unlock()
	return proxy
}

func (gw *EtcdGateway) removeProxy(serviceName string) {
	gw.proxyMu.Lock()
	defer gw.proxyMu.Unlock()
	if proxy, ok := gw.proxies[serviceName]; ok {
		proxy.Close()
		delete(gw.proxies, serviceName)
	}
}

// setupAdminAPI 管理 API（仅限本地访问） Added an Admin API, accessible only from local addresses.
func (gw *EtcdGateway) setupAdminAPI() {
	gw.app.POST("/admin/gateway/routes", gw.localOnly(gw.createRoute))
	gw.app.PUT("/admin/gateway/routes/:id", gw.localOnly(gw.updateRoute))
	gw.app.DELETE("/admin/gateway/routes/:id", gw.localOnly(gw.deleteRoute))
	gw.app.GET("/admin/gateway/routes", gw.localOnly(gw.listRoutes))
	gw.app.GET("/admin/gateway/routes/:id", gw.localOnly(gw.getRoute))
}

// localOnly 限制仅本地访问
func (gw *EtcdGateway) localOnly(fn func(core.Ctx) error) func(core.Ctx) error {
	return func(ctx core.Ctx) error {
		ip := ctx.Request().RemoteAddr
		if !strings.HasPrefix(ip, "127.") && !strings.HasPrefix(ip, "::1") && ip != "localhost" && ip != "[::1]" {
			return ctx.Status(403).JSON(core.Map{"code": 403, "message": "forbidden"})
		}
		return fn(ctx)
	}
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

	data, _ := json.Marshal(route)
	if _, err := gw.etcdCli.Put(context.Background(), gw.prefix+route.ID, string(data)); err != nil {
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

	data, _ := json.Marshal(route)
	if _, err := gw.etcdCli.Put(context.Background(), gw.prefix+routeID, string(data)); err != nil {
		return ctx.Status(500).JSON(core.Map{"code": 500, "message": err.Error()})
	}

	return ctx.JSON(core.Map{"code": 0, "message": "success"})
}

func (gw *EtcdGateway) deleteRoute(ctx core.Ctx) error {
	routeID := ctx.Params("id")
	if _, err := gw.etcdCli.Delete(context.Background(), gw.prefix+routeID); err != nil {
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
	for _, p := range gw.proxies {
		p.Close()
	}
	if gw.etcdCli != nil {
		return gw.etcdCli.Close()
	}
	return nil
}
