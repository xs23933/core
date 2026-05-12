// gateway/etcd_gateway.go
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/etcd"
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

	mu       sync.RWMutex
	routes   map[string]*Route
	handlers map[string]core.HandlerFunc

	watchCtx    context.Context
	watchCancel context.CancelFunc

	// 使用框架的 Discovery 获取服务地址
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
		handlers:    make(map[string]core.HandlerFunc),
		watchCtx:    ctx,
		watchCancel: cancel,
		proxies:     make(map[string]*ReflectionProxy),
	}

	// 1. 加载现有路由
	if err := gw.loadAllRoutes(); err != nil {
		core.D("警告: 加载现有路由失败: %v", err)
	}

	gw.discoverAndConnectServices()

	// 2. 监听路由变化
	go gw.watchRoutes()

	// 3. 管理 API
	gw.setupAdminAPI()

	// 4. 优雅关闭
	app.OnShutdown(func() {
		gw.Close()
	})

	core.D("✅ Etcd 网关启动成功")
	return gw, nil
}

// discoverAndConnectServices 启动时发现并连接所有服务
func (gw *EtcdGateway) discoverAndConnectServices() {
	log.Printf("[Gateway] 开始发现 etcd 中的所有服务...")

	// ✅ 直接从 etcd 扫描所有服务，而不是依赖路由
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := gw.etcdCli.Get(ctx, "/services/", clientv3.WithPrefix())
	if err != nil {
		log.Printf("[Gateway] 扫描服务失败: %v", err)
		return
	}

	// 收集所有服务名（去重）
	serviceSet := make(map[string]string) // serviceName -> addr
	for _, kv := range resp.Kvs {
		key := string(kv.Key)
		// key 格式: /services/auth-service/auth-service-1
		parts := strings.Split(key, "/")
		if len(parts) >= 3 {
			serviceName := parts[2]
			var info struct {
				Addr string `json:"addr"`
			}
			if err := json.Unmarshal(kv.Value, &info); err == nil && info.Addr != "" {
				serviceSet[serviceName] = info.Addr
				log.Printf("[Gateway] 发现 etcd 服务: %s -> %s", serviceName, info.Addr)
			}
		}
	}

	if len(serviceSet) == 0 {
		log.Printf("[Gateway] etcd 中没有发现任何服务")
		return
	}

	// 为每个服务创建代理
	for serviceName, serviceAddr := range serviceSet {
		log.Printf("[Gateway] 连接服务 %s (%s)", serviceName, serviceAddr)

		proxy, err := NewReflectionProxy(serviceAddr)
		if err != nil {
			log.Printf("[Gateway] 创建代理失败 %s: %v", serviceName, err)
			continue
		}

		gw.proxyMu.Lock()
		gw.proxies[serviceName] = proxy
		gw.proxyMu.Unlock()

		log.Printf("[Gateway] ✅ 服务 %s 已连接，方法已自动注册", serviceName)
	}

	// 启动服务监听，当新服务注册时自动连接
	go gw.watchEtcdServices()
}

// watchEtcdServices 监听 etcd 中的服务变化
func (gw *EtcdGateway) watchEtcdServices() {
	log.Printf("[Gateway] 开始监听 etcd 服务变化...")

	watchChan := gw.etcdCli.Watch(gw.watchCtx, "/services/", clientv3.WithPrefix())

	for resp := range watchChan {
		for _, ev := range resp.Events {
			switch ev.Type {
			case clientv3.EventTypePut:
				// 新服务注册
				key := string(ev.Kv.Key)
				parts := strings.Split(key, "/")
				if len(parts) < 3 {
					continue
				}
				serviceName := parts[2]

				var info struct {
					Addr string `json:"addr"`
				}
				if err := json.Unmarshal(ev.Kv.Value, &info); err != nil {
					continue
				}

				log.Printf("[Gateway] 检测到新服务: %s -> %s", serviceName, info.Addr)

				// 检查是否已经连接
				gw.proxyMu.RLock()
				_, exists := gw.proxies[serviceName]
				gw.proxyMu.RUnlock()

				if exists {
					continue
				}

				// 连接新服务
				proxy, err := NewReflectionProxy(info.Addr)
				if err != nil {
					log.Printf("[Gateway] 连接新服务失败 %s: %v", serviceName, err)
					continue
				}

				gw.proxyMu.Lock()
				gw.proxies[serviceName] = proxy
				gw.proxyMu.Unlock()

				log.Printf("[Gateway] ✅ 新服务 %s 已自动连接", serviceName)

			case clientv3.EventTypeDelete:
				// 服务注销
				key := string(ev.Kv.Key)
				parts := strings.Split(key, "/")
				if len(parts) < 3 {
					continue
				}
				serviceName := parts[2]

				gw.proxyMu.Lock()
				if proxy, exists := gw.proxies[serviceName]; exists {
					proxy.Close()
					delete(gw.proxies, serviceName)
					log.Printf("[Gateway] 服务 %s 已断开", serviceName)
				}
				gw.proxyMu.Unlock()
			}
		}
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
				key := string(ev.Kv.Key)
				routeID := strings.TrimPrefix(key, gw.prefix)
				gw.removeRouteByID(routeID)
			}
		}
	}
}

func (gw *EtcdGateway) addOrUpdateRoute(route *Route) {
	gw.mu.Lock()
	defer gw.mu.Unlock()

	oldRoute, exists := gw.routes[route.ID]
	if exists {
		gw.unregisterRoute(oldRoute)
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
	handlerKey := fmt.Sprintf("%s:%s", route.Method, route.Path)
	gw.handlers[handlerKey] = handler

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
	default:
		core.D("不支持的方法: %s", route.Method)
	}
}

func (gw *EtcdGateway) unregisterRoute(route *Route) {
	handlerKey := fmt.Sprintf("%s:%s", route.Method, route.Path)
	delete(gw.handlers, handlerKey)
}

// getOrCreateProxy 获取或创建反射代理（使用框架的 Discovery）
// gateway/etcd_gateway.go - 修改 getOrCreateProxy
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

	// 使用框架的 Discovery 获取服务地址
	services := gw.app.EtcdDiscovery.GetServices(serviceName)
	if len(services) == 0 {
		return nil, fmt.Errorf("no instance found for service: %s", serviceName)
	}

	serviceAddr := services[0].Addr
	core.D("[Gateway] 发现服务 %s 地址: %s", serviceName, serviceAddr)

	// 创建反射代理（内部会自动发现并注册所有方法）
	proxy, err := NewReflectionProxy(serviceAddr)
	if err != nil {
		return nil, fmt.Errorf("create reflection proxy: %w", err)
	}

	gw.proxies[serviceName] = proxy
	core.D("[Gateway] ✅ 反射代理初始化成功: %s", serviceName)

	return proxy, nil
}

// getProtoServiceName 获取 proto 服务名
func (gw *EtcdGateway) getProtoServiceName(serviceName string, svc *etcd.ServiceInfo) string {
	// 优先从 metadata 获取
	if svc.Metadata != nil {
		if protoName := svc.Metadata["proto_name"]; protoName != "" {
			return protoName
		}
	}
	// 默认：auth-service -> auth.v1.UserService（需要维护映射）
	// 或者通过反射发现
	return serviceName
}

// createProxyHandler 创建 HTTP 处理器
func (gw *EtcdGateway) createProxyHandler(route *Route) core.HandlerFunc {
	return func(ctx core.Ctx) error {
		startTime := time.Now()

		proxy, err := gw.getOrCreateProxy(route.ServiceName)
		if err != nil {
			core.D("获取代理失败: %v", err)
			return ctx.Status(http.StatusServiceUnavailable).JSON(core.Map{
				"code":    503,
				"message": fmt.Sprintf("service %s unavailable", route.ServiceName),
			})
		}

		// 构建请求参数
		reqBody := make(core.Map)
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
		headers := []string{"authorization", "x-request-id", "x-user-id"}
		for _, h := range headers {
			if val := ctx.GetHeader(h); val != "" {
				md.Set(h, val)
			}
		}
		grpcCtx := metadata.NewOutgoingContext(context.Background(), md)

		// 调用 gRPC
		callCtx, cancel := context.WithTimeout(grpcCtx, 10*time.Second)
		defer cancel()

		jsonResp, err := proxy.Invoke(callCtx, route.GRPCMethod, jsonReq)
		if err != nil {
			core.D("gRPC 调用失败: %v", err)
			return ctx.Status(http.StatusInternalServerError).JSON(core.Map{
				"code": 500, "message": err.Error(),
			})
		}

		var result interface{}
		json.Unmarshal(jsonResp, &result)

		core.D("%s %s -> %s, 耗时: %v", ctx.Method(), ctx.Path(), route.GRPCMethod, time.Since(startTime))
		return ctx.JSON(result)
	}
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
	key := gw.prefix + route.ID
	_, err := gw.etcdCli.Put(context.Background(), key, string(data))
	if err != nil {
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
	key := gw.prefix + routeID
	_, err := gw.etcdCli.Put(context.Background(), key, string(data))
	if err != nil {
		return ctx.Status(500).JSON(core.Map{"code": 500, "message": err.Error()})
	}

	return ctx.JSON(core.Map{"code": 0, "message": "success"})
}

func (gw *EtcdGateway) deleteRoute(ctx core.Ctx) error {
	routeID := ctx.Params("id")
	key := gw.prefix + routeID
	_, err := gw.etcdCli.Delete(context.Background(), key)
	if err != nil {
		return ctx.Status(500).JSON(core.Map{"code": 500, "message": err.Error()})
	}
	return ctx.ToJSONCode("success")
}

func (gw *EtcdGateway) listRoutes(ctx core.Ctx) error {
	gw.mu.RLock()
	defer gw.mu.RUnlock()

	routes := make([]*Route, 0, len(gw.routes))
	for _, r := range gw.routes {
		routes = append(routes, r)
	}
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

func (gw *EtcdGateway) isRouteEqual(r1, r2 *Route) bool {
	return r1.Method == r2.Method && r1.Path == r2.Path &&
		r1.ServiceName == r2.ServiceName && r1.GRPCMethod == r2.GRPCMethod
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
