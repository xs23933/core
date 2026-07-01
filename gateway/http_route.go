package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
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
	proxy := &httputil.ReverseProxy{
		Director: func(*http.Request) {},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
			core.Erro("[Gateway] HTTP proxy %s failed: %v", route.ServiceName, proxyErr)
			w.Header().Set(core.HeaderContentType, core.MIMEApplicationJSONCharsetUTF8)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"code":502,"message":"bad gateway"}`))
		},
	}
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
		prepareHTTPProxyRequest(req, target, route, ctx.ParamsMaps())

		proxy.ServeHTTP(ctx.Response(), req)
		return nil
	}
}

func prepareHTTPProxyRequest(req *http.Request, target *url.URL, route *Route, params map[string]string) {
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = joinHTTPProxyPath(target.Path, resolveHTTPUpstreamPath(route.UpstreamPath, req.URL.Path, params))
	req.URL.RawPath = ""
	if target.RawQuery == "" || req.URL.RawQuery == "" {
		req.URL.RawQuery = target.RawQuery + req.URL.RawQuery
	} else {
		req.URL.RawQuery = target.RawQuery + "&" + req.URL.RawQuery
	}
	for key, value := range route.Headers {
		req.Header.Set(key, value)
	}
}

func joinHTTPProxyPath(basePath, reqPath string) string {
	if basePath == "" || basePath == "/" {
		return normalizeRoutePath(reqPath)
	}
	if reqPath == "" || reqPath == "/" {
		return normalizeRoutePath(basePath)
	}
	return strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(reqPath, "/")
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

type httpServiceInstances struct {
	byID  map[string]string
	addrs []string
}

func newHTTPServiceInstances(byID map[string]string) httpServiceInstances {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	addrs := make([]string, 0, len(ids))
	for _, id := range ids {
		addrs = append(addrs, byID[id])
	}
	return httpServiceInstances{byID: byID, addrs: addrs}
}

func (gw *EtcdGateway) loadHTTPInstances() map[string]httpServiceInstances {
	if value, ok := gw.httpInstances.Load().(map[string]httpServiceInstances); ok && value != nil {
		return value
	}
	return nil
}

func (gw *EtcdGateway) storeHTTPInstances(instances map[string]httpServiceInstances) {
	gw.httpInstances.Store(instances)
}

func (gw *EtcdGateway) loadHTTPIndexes() map[string]*atomic.Uint64 {
	if value, ok := gw.httpIndexes.Load().(map[string]*atomic.Uint64); ok && value != nil {
		return value
	}
	return nil
}

func (gw *EtcdGateway) storeHTTPIndexes(indexes map[string]*atomic.Uint64) {
	gw.httpIndexes.Store(indexes)
}

func (gw *EtcdGateway) getHTTPIndex(serviceName string) *atomic.Uint64 {
	if index := gw.loadHTTPIndexes()[serviceName]; index != nil {
		return index
	}

	gw.httpIndexMu.Lock()
	defer gw.httpIndexMu.Unlock()

	indexes := gw.loadHTTPIndexes()
	if index := indexes[serviceName]; index != nil {
		return index
	}

	next := make(map[string]*atomic.Uint64, len(indexes)+1)
	for name, index := range indexes {
		next[name] = index
	}
	index := &atomic.Uint64{}
	next[serviceName] = index
	gw.storeHTTPIndexes(next)
	return index
}

func copyHTTPInstances(instances map[string]httpServiceInstances) map[string]httpServiceInstances {
	next := make(map[string]httpServiceInstances, len(instances))
	for serviceName, serviceInstances := range instances {
		byID := make(map[string]string, len(serviceInstances.byID))
		for instanceID, addr := range serviceInstances.byID {
			byID[instanceID] = addr
		}
		next[serviceName] = newHTTPServiceInstances(byID)
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
	byID := instances[serviceName].byID
	if byID == nil {
		byID = make(map[string]string)
	}
	byID[instanceID] = addr
	instances[serviceName] = newHTTPServiceInstances(byID)
	gw.storeHTTPInstances(instances)
}

func (gw *EtcdGateway) removeHTTPInstance(serviceName, instanceID string) {
	if gw == nil {
		return
	}
	gw.httpMu.Lock()
	defer gw.httpMu.Unlock()
	instances := copyHTTPInstances(gw.loadHTTPInstances())
	serviceInstances := instances[serviceName]
	if serviceInstances.byID == nil {
		return
	}
	delete(serviceInstances.byID, instanceID)
	if len(serviceInstances.byID) == 0 {
		delete(instances, serviceName)
	} else {
		instances[serviceName] = newHTTPServiceInstances(serviceInstances.byID)
	}
	gw.storeHTTPInstances(instances)
}

func (gw *EtcdGateway) getHTTPInstance(serviceName string) string {
	serviceInstances := gw.loadHTTPInstances()[serviceName]
	addrs := serviceInstances.addrs
	if len(addrs) == 0 {
		return ""
	}
	idx := gw.getHTTPIndex(serviceName).Add(1) - 1
	return addrs[int(idx%uint64(len(addrs)))]
}
