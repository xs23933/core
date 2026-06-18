package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	core "github.com/xs23933/core/v3"
)

type RouteProtocol string

const (
	RouteProtocolGRPC RouteProtocol = "grpc"
	RouteProtocolHTTP RouteProtocol = "http"
)

type routeStore interface {
	Put(ctx context.Context, key string, value any) error
}

type discoveryRouteStore struct {
	app *core.Core
}

func (s discoveryRouteStore) Put(_ context.Context, key string, value any) error {
	if s.app == nil || s.app.EtcdDiscovery == nil {
		return errors.New("gateway: etcd discovery is not enabled")
	}
	return s.app.EtcdDiscovery.Put(key, value)
}

// RegisterHTTPRoute explicitly publishes an HTTP route definition to etcd.
// The service must already have enabled etcd registry/discovery.
func RegisterHTTPRoute(app *core.Core, route *Route, routePrefix ...string) error {
	if app == nil {
		return errors.New("gateway: app is nil")
	}
	if route == nil {
		return errors.New("gateway: route is nil")
	}
	if route.ServiceName == "" {
		route.ServiceName = app.Conf.GetString("etcd.service_name", "")
	}
	prefix := "/gateway/routes/"
	if len(routePrefix) > 0 && routePrefix[0] != "" {
		prefix = routePrefix[0]
	}
	return registerHTTPRoute(context.Background(), discoveryRouteStore{app: app}, prefix, route)
}

func registerHTTPRoute(ctx context.Context, store routeStore, prefix string, route *Route) error {
	if store == nil {
		return errors.New("gateway: route store is nil")
	}
	if route == nil {
		return errors.New("gateway: route is nil")
	}
	route.Protocol = RouteProtocolHTTP
	if err := prepareRoute(route); err != nil {
		return err
	}
	prefix = strings.TrimSuffix(prefix, "/") + "/"
	return store.Put(ctx, prefix+route.ID, route)
}

func prepareRoute(route *Route) error {
	if err := normalizeAndValidateRoute(route); err != nil {
		return err
	}
	if route.ID == "" {
		route.ID = routeID(route)
	}
	now := time.Now()
	if route.CreatedAt.IsZero() {
		route.CreatedAt = now
	}
	route.UpdatedAt = now
	route.Enabled = true
	return nil
}

func normalizeAndValidateRoute(route *Route) error {
	if route == nil {
		return errors.New("gateway: route is nil")
	}
	route.Protocol = RouteProtocol(strings.ToLower(strings.TrimSpace(string(route.Protocol))))
	if route.Protocol == "" {
		route.Protocol = RouteProtocolGRPC
	}
	route.Method = strings.ToUpper(strings.TrimSpace(route.Method))
	route.Path = normalizeRoutePath(route.Path)
	route.UpstreamPath = normalizeOptionalRoutePath(route.UpstreamPath)

	if route.ServiceName == "" {
		return errors.New("gateway: service_name is required")
	}
	if route.Method == "" {
		return errors.New("gateway: method is required")
	}
	if route.Path == "" {
		return errors.New("gateway: path is required")
	}
	if !supportedHTTPMethod(route.Method) {
		return fmt.Errorf("gateway: unsupported method %s", route.Method)
	}

	switch route.Protocol {
	case RouteProtocolGRPC:
		if route.GRPCMethod == "" {
			return errors.New("gateway: grpc_method is required for grpc route")
		}
	case RouteProtocolHTTP:
		if route.UpstreamPath == "" {
			route.UpstreamPath = route.Path
		}
	case "":
		return errors.New("gateway: protocol is required")
	default:
		return fmt.Errorf("gateway: unsupported protocol %s", route.Protocol)
	}
	return nil
}

func supportedHTTPMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

func normalizeRoutePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}

func normalizeOptionalRoutePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return normalizeRoutePath(path)
}

func routeID(route *Route) string {
	target := route.GRPCMethod
	if route.Protocol == RouteProtocolHTTP {
		target = route.UpstreamPath
	}
	raw := fmt.Sprintf("%s|%s|%s|%s|%s", route.Protocol, route.ServiceName, route.Method, route.Path, target)
	return core.SHA256(raw)
}

func resolveHTTPUpstreamPath(template, requestPath string, params map[string]string) string {
	path := template
	if path == "" {
		path = requestPath
	}
	for key, value := range params {
		path = strings.ReplaceAll(path, ":"+key, url.PathEscape(value))
	}
	return normalizeRoutePath(path)
}

func (gw *EtcdGateway) createHTTPProxyHandler(route *Route) core.HandlerFunc {
	return func(ctx core.Ctx) error {
		addr := gw.getHTTPInstance(route.ServiceName)
		if addr == "" {
			return ctx.Status(http.StatusServiceUnavailable).JSON(core.Map{
				"code":    http.StatusServiceUnavailable,
				"message": fmt.Sprintf("service %s unavailable", route.ServiceName),
			})
		}

		target, err := parseHTTPServiceURL(addr)
		if err != nil {
			return ctx.Status(http.StatusBadGateway).JSON(core.Map{
				"code": http.StatusBadGateway, "message": err.Error(),
			})
		}

		req := ctx.Request().Clone(ctx.Context())
		req.URL.Path = resolveHTTPUpstreamPath(route.UpstreamPath, req.URL.Path, ctx.ParamsMaps())
		req.URL.RawPath = ""
		for key, value := range route.Headers {
			req.Header.Set(key, value)
		}

		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
			core.Erro("[Gateway] HTTP proxy %s failed: %v", route.ServiceName, proxyErr)
			w.Header().Set(core.HeaderContentType, core.MIMEApplicationJSONCharsetUTF8)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"code":502,"message":"bad gateway"}`))
		}
		proxy.ServeHTTP(ctx.Response(), req)
		return nil
	}
}

func parseHTTPServiceURL(addr string) (*url.URL, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, errors.New("gateway: empty HTTP service address")
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	target, err := url.Parse(addr)
	if err != nil || target.Host == "" {
		return nil, fmt.Errorf("gateway: invalid HTTP service address %q", addr)
	}
	return target, nil
}

func (gw *EtcdGateway) loadHTTPInstances() map[string]map[string]string {
	if value, ok := gw.httpInstances.Load().(map[string]map[string]string); ok && value != nil {
		return value
	}
	return nil
}

func (gw *EtcdGateway) storeHTTPInstances(instances map[string]map[string]string) {
	gw.httpInstances.Store(instances)
}

func copyHTTPInstances(instances map[string]map[string]string) map[string]map[string]string {
	next := make(map[string]map[string]string, len(instances))
	for serviceName, serviceInstances := range instances {
		copied := make(map[string]string, len(serviceInstances))
		for instanceID, addr := range serviceInstances {
			copied[instanceID] = addr
		}
		next[serviceName] = copied
	}
	return next
}

func (gw *EtcdGateway) addHTTPInstance(serviceName, instanceID, addr string) {
	if gw == nil || serviceName == "" || instanceID == "" || addr == "" {
		return
	}
	gw.httpMu.Lock()
	defer gw.httpMu.Unlock()
	instances := copyHTTPInstances(gw.loadHTTPInstances())
	if instances[serviceName] == nil {
		instances[serviceName] = make(map[string]string)
	}
	instances[serviceName][instanceID] = addr
	gw.storeHTTPInstances(instances)
}

func (gw *EtcdGateway) removeHTTPInstance(serviceName, instanceID string) {
	if gw == nil {
		return
	}
	gw.httpMu.Lock()
	defer gw.httpMu.Unlock()
	instances := copyHTTPInstances(gw.loadHTTPInstances())
	if instances[serviceName] == nil {
		return
	}
	delete(instances[serviceName], instanceID)
	if len(instances[serviceName]) == 0 {
		delete(instances, serviceName)
	}
	gw.storeHTTPInstances(instances)
}

func (gw *EtcdGateway) getHTTPInstance(serviceName string) string {
	instances := gw.loadHTTPInstances()[serviceName]
	if len(instances) == 0 {
		return ""
	}
	all := make([]string, 0, len(instances))
	for _, addr := range instances {
		all = append(all, addr)
	}
	value, _ := gw.httpIndexes.LoadOrStore(serviceName, &atomic.Uint64{})
	idx := value.(*atomic.Uint64).Add(1) - 1
	return all[int(idx%uint64(len(all)))]
}
