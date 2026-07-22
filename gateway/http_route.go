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
	"github.com/xs23933/core/v3/etcd"
	gatewayroute "github.com/xs23933/core/v3/gateway/route"
)

// RegisterHTTPRoute explicitly publishes an HTTP route definition to etcd.
// The service must already have enabled etcd registry/discovery.
func RegisterHTTPRoute(app *core.Core, value *Route, routePrefix ...string) error {
	if len(routePrefix) == 0 || routePrefix[0] == "" {
		return RegisterHTTPRoutes(app, value)
	}
	if app == nil || app.EtcdDiscovery == nil {
		return errors.New("gateway: etcd discovery is not enabled")
	}
	serviceName, _, ok := app.EtcdRegistryIdentity()
	if !ok {
		return errors.New("gateway: etcd registry is not enabled")
	}
	return publishManualHTTPRoutes(app.Ctx, serviceName, publisherForDiscovery(app.EtcdDiscovery, routePrefix[0]), value)
}

// RegisterHTTPRoutes validates and publishes a complete batch of manual HTTP
// routes. It performs no periodic republishing and starts no worker.
func RegisterHTTPRoutes(app *core.Core, routes ...*Route) error {
	if app == nil || app.EtcdDiscovery == nil {
		return errors.New("gateway: etcd discovery is not enabled")
	}
	serviceName, namespace, ok := app.EtcdRegistryIdentity()
	if !ok {
		return errors.New("gateway: etcd registry is not enabled")
	}
	return publishManualHTTPRoutes(app.Ctx, serviceName, publisherForDiscovery(app.EtcdDiscovery, defaultRoutePrefix(namespace)), routes...)
}

func publisherForDiscovery(discovery *etcd.Discovery, routePrefix ...string) gatewayroute.Publisher {
	namespace := ""
	if discovery != nil {
		namespace = discovery.Namespace()
	}
	prefix := defaultRoutePrefix(namespace)
	if len(routePrefix) > 0 && routePrefix[0] != "" {
		prefix = routePrefix[0]
	}
	return gatewayroute.Publisher{
		Store:       discoveryRouteStore{discovery: discovery},
		RoutePrefix: prefix,
	}
}

func publishManualHTTPRoutes(ctx context.Context, serviceName string, publisher gatewayroute.Publisher, routes ...*Route) error {
	prepared := make([]*Route, len(routes))
	for index, value := range routes {
		if value == nil {
			continue
		}
		copy := cloneRouteDefinition(value)
		copy.Protocol = RouteProtocolHTTP
		if copy.ServiceName == "" {
			copy.ServiceName = serviceName
		}
		copy.Enabled = true
		if err := normalizeAndValidateRoute(copy); err != nil {
			return err
		}
		if copy.ID == "" {
			copy.ID = routeID(copy)
		}
		prepared[index] = copy
	}
	return publisher.PublishManual(ctx, prepared...)
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
	route.Path = strings.TrimSpace(route.Path)
	route.UpstreamPath = strings.TrimSpace(route.UpstreamPath)
	if err := gatewayroute.Validate(route); err != nil {
		return err
	}
	route.Path = normalizeRoutePath(route.Path)
	route.UpstreamPath = normalizeOptionalRoutePath(route.UpstreamPath)
	slot, err := gatewayroute.SlotID(route.Method, route.Path)
	if err != nil {
		return err
	}
	route.Slot = slot
	return nil
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
		target = ""
	}
	raw := fmt.Sprintf("%s|%s|%s|%s|%s", route.Protocol, route.ServiceName, route.Method, route.Path, target)
	return core.SHA256(raw)
}

func (gw *EtcdGateway) createHTTPProxyHandler(route *Route) core.HandlerFunc {
	proxy := &httputil.ReverseProxy{
		Director: func(*http.Request) {},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
			core.Erro("[Gateway] HTTP proxy %s failed: %v", route.ServiceName, proxyErr)
			w.Header().Set(core.HeaderContentType, core.MIMEApplicationJSONCharsetUTF8)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"code":502,"msg":"bad gateway"}`))
		},
	}
	return func(ctx core.Ctx) error {
		instance := gw.getHTTPInstance(route.ServiceName)
		if instance == nil {
			return ctx.Status(http.StatusServiceUnavailable).JSON(core.Map{
				"code": http.StatusServiceUnavailable,
				"msg":  fmt.Sprintf("service %s unavailable", route.ServiceName),
			})
		}

		req := ctx.Request().Clone(ctx.Context())
		setHTTPProxyTarget(req, instance.Target)
		proxy.ServeHTTP(ctx.Response(), req)
		return nil
	}
}

func setHTTPProxyTarget(req *http.Request, target *url.URL) {
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
}

func parseHTTPServiceURL(addr string) (*url.URL, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, errors.New("gateway: empty HTTP service address")
	}
	target, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("gateway: invalid HTTP service address %q: %w", addr, err)
	}
	target.Scheme = strings.ToLower(target.Scheme)
	if (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" || target.User != nil || target.Opaque != "" {
		return nil, fmt.Errorf("gateway: invalid HTTP service origin %q", addr)
	}
	if target.Path != "" && target.Path != "/" {
		return nil, fmt.Errorf("gateway: HTTP service origin %q must not contain a path", addr)
	}
	if target.RawPath != "" && target.EscapedPath() != "/" {
		return nil, fmt.Errorf("gateway: HTTP service origin %q must not contain an escaped path", addr)
	}
	if target.RawQuery != "" || target.ForceQuery {
		return nil, fmt.Errorf("gateway: HTTP service origin %q must not contain a query", addr)
	}
	if target.Fragment != "" || target.RawFragment != "" {
		return nil, fmt.Errorf("gateway: HTTP service origin %q must not contain a fragment", addr)
	}
	return target, nil
}

// HTTPInstance is an immutable HTTP service target prepared from a registry
// event. Target is parsed once and reused by the request hot path.
type HTTPInstance struct {
	ID     string
	Addr   string
	Target *url.URL
}

type httpServiceInstances struct {
	byID      map[string]*HTTPInstance
	instances []*HTTPInstance
}

func newHTTPServiceInstances(byID map[string]*HTTPInstance) *httpServiceInstances {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	instances := make([]*HTTPInstance, 0, len(ids))
	for _, id := range ids {
		instances = append(instances, byID[id])
	}
	return &httpServiceInstances{byID: byID, instances: instances}
}

func (gw *EtcdGateway) loadHTTPInstances() map[string]*httpServiceInstances {
	if gw == nil {
		return nil
	}
	if value, ok := gw.httpInstances.Load().(map[string]*httpServiceInstances); ok && value != nil {
		return value
	}
	return nil
}

func (gw *EtcdGateway) storeHTTPInstances(instances map[string]*httpServiceInstances) {
	gw.httpInstances.Store(instances)
}

func (gw *EtcdGateway) loadHTTPIndexes() map[string]*atomic.Uint64 {
	if gw == nil {
		return nil
	}
	if value, ok := gw.httpIndexes.Load().(map[string]*atomic.Uint64); ok && value != nil {
		return value
	}
	return nil
}

func (gw *EtcdGateway) storeHTTPIndexes(indexes map[string]*atomic.Uint64) {
	gw.httpIndexes.Store(indexes)
}

func (gw *EtcdGateway) ensureHTTPIndex(serviceName string) {
	gw.httpIndexMu.Lock()
	defer gw.httpIndexMu.Unlock()

	indexes := gw.loadHTTPIndexes()
	if indexes[serviceName] != nil {
		return
	}

	next := make(map[string]*atomic.Uint64, len(indexes)+1)
	for name, index := range indexes {
		next[name] = index
	}
	next[serviceName] = &atomic.Uint64{}
	gw.storeHTTPIndexes(next)
}

func (gw *EtcdGateway) removeHTTPIndex(serviceName string) {
	gw.httpIndexMu.Lock()
	defer gw.httpIndexMu.Unlock()

	indexes := gw.loadHTTPIndexes()
	if indexes[serviceName] == nil {
		return
	}
	next := make(map[string]*atomic.Uint64, len(indexes)-1)
	for name, index := range indexes {
		if name != serviceName {
			next[name] = index
		}
	}
	gw.storeHTTPIndexes(next)
}

func copyHTTPInstances(instances map[string]*httpServiceInstances, capacity int) map[string]*httpServiceInstances {
	next := make(map[string]*httpServiceInstances, len(instances)+capacity)
	for serviceName, serviceInstances := range instances {
		next[serviceName] = serviceInstances
	}
	return next
}

func copyHTTPInstancesByID(serviceInstances *httpServiceInstances, capacity int) map[string]*HTTPInstance {
	size := capacity
	if serviceInstances != nil {
		size += len(serviceInstances.byID)
	}
	byID := make(map[string]*HTTPInstance, size)
	if serviceInstances != nil {
		for instanceID, instance := range serviceInstances.byID {
			byID[instanceID] = instance
		}
	}
	return byID
}

func (gw *EtcdGateway) addHTTPInstance(serviceName, instanceID, addr string) error {
	if gw == nil {
		return errors.New("gateway: HTTP gateway is nil")
	}
	serviceName = strings.TrimSpace(serviceName)
	instanceID = strings.TrimSpace(instanceID)
	addr = strings.TrimSpace(addr)
	if serviceName == "" {
		return errors.New("gateway: HTTP service_name is required")
	}
	if instanceID == "" {
		return errors.New("gateway: HTTP service_id is required")
	}
	if addr == "" {
		gw.removeHTTPInstance(serviceName, instanceID)
		return nil
	}
	target, err := parseHTTPServiceURL(addr)
	if err != nil {
		gw.removeHTTPInstance(serviceName, instanceID)
		return err
	}
	instance := &HTTPInstance{ID: instanceID, Addr: addr, Target: target}

	gw.httpMu.Lock()
	defer gw.httpMu.Unlock()
	current := gw.loadHTTPInstances()
	next := copyHTTPInstances(current, 1)
	byID := copyHTTPInstancesByID(current[serviceName], 1)
	byID[instanceID] = instance
	next[serviceName] = newHTTPServiceInstances(byID)
	gw.ensureHTTPIndex(serviceName)
	gw.storeHTTPInstances(next)
	return nil
}

func (gw *EtcdGateway) removeHTTPInstance(serviceName, instanceID string) {
	if gw == nil {
		return
	}
	gw.httpMu.Lock()
	defer gw.httpMu.Unlock()
	current := gw.loadHTTPInstances()
	serviceInstances := current[serviceName]
	if serviceInstances == nil || serviceInstances.byID[instanceID] == nil {
		return
	}

	next := copyHTTPInstances(current, 0)
	byID := copyHTTPInstancesByID(serviceInstances, 0)
	delete(byID, instanceID)
	if len(byID) == 0 {
		delete(next, serviceName)
		gw.storeHTTPInstances(next)
		gw.removeHTTPIndex(serviceName)
		gw.circuitStates.Delete(serviceName)
		return
	}
	next[serviceName] = newHTTPServiceInstances(byID)
	gw.storeHTTPInstances(next)
}

func (gw *EtcdGateway) getHTTPInstance(serviceName string) *HTTPInstance {
	serviceInstances := gw.loadHTTPInstances()[serviceName]
	if serviceInstances == nil || len(serviceInstances.instances) == 0 {
		return nil
	}
	index := gw.loadHTTPIndexes()[serviceName]
	if index == nil {
		return nil
	}
	position := index.Add(1) - 1
	return serviceInstances.instances[int(position%uint64(len(serviceInstances.instances)))]
}
