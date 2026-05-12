package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/xs23933/core/v3"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/metadata"
)

type Route struct {
	ID          string            `json:"id"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	ServiceName string            `json:"service_name"`
	GRPCMethod  string            `json:"grpc_method"`
	Description string            `json:"description"`
	Headers     map[string]string `json:"headers"`
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
		return nil, fmt.Errorf("创建 etcd 客户端失败: %w", err)
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
		core.D("[Gateway] 加载现有路由失败: %v", err)
	}

	gw.discoverAndConnectServices()

	go gw.watchRoutes()

	gw.setupAdminAPI()

	app.OnShutdown(func() { gw.Close() })

	core.D("[Gateway] ✅ Etcd 网关启动成功")
	return gw, nil
}

// discoverAndConnectServices 启动时发现并连接所有服务
func (gw *EtcdGateway) discoverAndConnectServices() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := gw.etcdCli.Get(ctx, "/services/", clientv3.WithPrefix())
	if err != nil {
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
		core.D("[Gateway] etcd 中没有发现任何服务")
		return
	}

	for serviceName, serviceAddr := range serviceSet {
		gw.connectService(serviceName, serviceAddr)
	}

	go gw.watchEtcdServices()
}

// connectService 连接服务并自动注册路由（支持重连）
func (gw *EtcdGateway) connectService(serviceName, serviceAddr string) {
	proxy, err := NewReflectionProxy(serviceAddr)
	if err != nil {
		return
	}

	gw.proxyMu.Lock()
	gw.proxies[serviceName] = proxy
	gw.proxyMu.Unlock()

	// 清除旧路由后重新注册
	gw.removeAutoRoutes(serviceName)
	gw.autoRegisterRoutes(serviceName, proxy)

	core.D("[Gateway] ✅ 服务 %s 已连接，方法已自动注册", serviceName)
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

				// 关闭旧 proxy，重新连接以获取新方法
				gw.proxyMu.Lock()
				if old, ok := gw.proxies[serviceName]; ok {
					old.Close()
					delete(gw.proxies, serviceName)
				}
				gw.proxyMu.Unlock()

				gw.connectService(serviceName, info.Addr)

			case clientv3.EventTypeDelete:
				gw.proxyMu.Lock()
				if proxy, ok := gw.proxies[serviceName]; ok {
					proxy.Close()
					delete(gw.proxies, serviceName)
				}
				gw.proxyMu.Unlock()

				core.D("[Gateway] 服务 %s 已断开", serviceName)
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

	for fullMethod := range methods {
		routeID := fmt.Sprintf("auto-%s-%s", serviceName, strings.TrimPrefix(fullMethod, "/"))

		gw.routes[routeID] = &Route{
			ID:          routeID,
			Method:      "POST",
			Path:        fullMethod,
			ServiceName: serviceName,
			GRPCMethod:  fullMethod,
			Description: fmt.Sprintf("auto-registered from %s", serviceName),
			Enabled:     true,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		gw.registerRoute(gw.routes[routeID])
	}
}

func (gw *EtcdGateway) loadAllRoutes() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := gw.etcdCli.Get(ctx, gw.prefix, clientv3.WithPrefix())
	if err != nil {
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

	core.Info("注册路由: %s %s -> %s", route.Method, route.Path, route.GRPCMethod)

	switch strings.ToUpper(route.Method) {
	case "GET":
		gw.app.GET(route.Path, handler)
	case "POST":
		gw.app.POST(route.Path, handler)
	case "PUT":
		gw.app.PUT(route.Path, handler)
	case "DELETE":
		gw.app.DELETE(route.Path, handler)
	}
}

func (gw *EtcdGateway) unregisterRoute(route *Route) {
	// TODO: 框架层面需要支持路由注销
}

func (gw *EtcdGateway) createProxyHandler(route *Route) core.HandlerFunc {
	return func(ctx core.Ctx) error {
		proxy, err := gw.getOrCreateProxy(route.ServiceName)
		if err != nil {
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
				for k, v := range bodyMap {
					reqBody[k] = v
				}
			}
		}

		jsonReq, err := sonic.Marshal(reqBody)
		if err != nil {
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
			return ctx.Status(http.StatusInternalServerError).JSON(core.Map{
				"code": 500, "message": err.Error(),
			})
		}

		var result interface{}
		sonic.Unmarshal(jsonResp, &result)

		return ctx.JSON(result)
	}
}

func (gw *EtcdGateway) getOrCreateProxy(serviceName string) (*ReflectionProxy, error) {
	gw.proxyMu.RLock()
	proxy, ok := gw.proxies[serviceName]
	gw.proxyMu.RUnlock()
	if ok {
		return proxy, nil
	}

	gw.proxyMu.Lock()
	defer gw.proxyMu.Unlock()

	if proxy, ok = gw.proxies[serviceName]; ok {
		return proxy, nil
	}

	if gw.app.EtcdDiscovery == nil {
		return nil, fmt.Errorf("etcd discovery not enabled")
	}

	services := gw.app.EtcdDiscovery.GetServices(serviceName)
	if len(services) == 0 {
		return nil, fmt.Errorf("no instance found for service: %s", serviceName)
	}

	proxy, err := NewReflectionProxy(services[0].Addr)
	if err != nil {
		return nil, err
	}

	gw.proxies[serviceName] = proxy
	return proxy, nil
}

// setupAdminAPI 管理 API
func (gw *EtcdGateway) setupAdminAPI() {
	gw.app.POST("/admin/gateway/routes", gw.createRoute)
	gw.app.PUT("/admin/gateway/routes/:id", gw.updateRoute)
	gw.app.DELETE("/admin/gateway/routes/:id", gw.deleteRoute)
	gw.app.GET("/admin/gateway/routes", gw.listRoutes)
	gw.app.GET("/admin/gateway/routes/:id", gw.getRoute)
}

func (gw *EtcdGateway) createRoute(ctx core.Ctx) error {
	var route Route
	if err := ctx.Bind(&route); err != nil {
		return ctx.Status(400).JSON(core.Map{"code": 400, "message": err.Error()})
	}

	route.ID = fmt.Sprintf("%d", time.Now().UnixNano())
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
	return ctx.JSON(core.Map{"code": 0, "data": route})
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
