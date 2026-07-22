package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/xs23933/core/v3"
	coreetcd "github.com/xs23933/core/v3/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func TestHTTPProxyPreservesRequest(t *testing.T) {
	type receivedRequest struct {
		method      string
		escapedPath string
		rawQuery    string
		body        string
		header      []string
	}
	received := make(chan receivedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
		}
		received <- receivedRequest{
			method:      req.Method,
			escapedPath: req.URL.EscapedPath(),
			rawQuery:    req.URL.RawQuery,
			body:        string(body),
			header:      append([]string(nil), req.Header.Values("X-Multi")...),
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	app := core.New()
	gw := &EtcdGateway{app: app, connPool: NewConnectionPool()}
	gw.storeHTTPInstances(make(map[string]*httpServiceInstances))
	if err := gw.addHTTPInstance("files", "files-a", upstream.URL); err != nil {
		t.Fatal(err)
	}
	gw.registerRoute(&Route{
		Protocol:    RouteProtocolHTTP,
		Method:      http.MethodPatch,
		Path:        "/api/files/:name",
		ServiceName: "files",
		Enabled:     true,
	})

	const requestTarget = "/api/files/a%2Bb?tag=z&first=1&tag=a&tag=b"
	const requestBody = `{"name":"a+b"}`
	request := httptest.NewRequest(http.MethodPatch, requestTarget, strings.NewReader(requestBody))
	request.Header["X-Multi"] = []string{"one", "two"}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code < http.StatusOK || response.Code >= http.StatusMultipleChoices {
		t.Fatalf("status = %d, want successful proxy response: %s", response.Code, response.Body.String())
	}
	got := <-received
	if got.method != http.MethodPatch {
		t.Fatalf("upstream method = %q, want %q", got.method, http.MethodPatch)
	}
	if got.escapedPath != "/api/files/a%2Bb" {
		t.Fatalf("upstream escaped path = %q, want /api/files/a%%2Bb", got.escapedPath)
	}
	if got.rawQuery != "tag=z&first=1&tag=a&tag=b" {
		t.Fatalf("upstream raw query = %q, want original order", got.rawQuery)
	}
	if got.body != requestBody {
		t.Fatalf("upstream body = %q, want %q", got.body, requestBody)
	}
	if !reflect.DeepEqual(got.header, []string{"one", "two"}) {
		t.Fatalf("upstream X-Multi = %#v, want both original values", got.header)
	}
}

func TestHTTPServiceOriginValidation(t *testing.T) {
	for _, addr := range []string{
		"http://127.0.0.1:8080",
		"https://example.com/",
	} {
		t.Run("valid_"+strings.ReplaceAll(addr, "/", "_"), func(t *testing.T) {
			target, err := parseHTTPServiceURL(addr)
			if err != nil {
				t.Fatalf("parseHTTPServiceURL(%q): %v", addr, err)
			}
			if target.Scheme == "" || target.Host == "" {
				t.Fatalf("target = %#v, want origin", target)
			}
		})
	}

	for _, addr := range []string{
		"127.0.0.1:8080",
		"ftp://example.com",
		"http://example.com/base",
		"http://example.com?debug=1",
		"http://example.com#fragment",
		"http://:8080",
		"http:///missing-host",
	} {
		t.Run("invalid_"+strings.ReplaceAll(addr, "/", "_"), func(t *testing.T) {
			if _, err := parseHTTPServiceURL(addr); err == nil {
				t.Fatalf("parseHTTPServiceURL(%q) error = nil, want invalid origin", addr)
			}
		})
	}
}

func TestHTTPRouteIDUsesOnlyTransportRouteIdentity(t *testing.T) {
	base := &Route{
		Protocol:    RouteProtocolHTTP,
		Method:      http.MethodGet,
		Path:        "/api/users/:id",
		ServiceName: "users",
	}
	redundant := cloneRouteDefinition(base)
	redundant.UpstreamPath = redundant.Path
	if got, want := routeID(redundant), routeID(base); got != want {
		t.Fatalf("HTTP route ID with redundant upstream_path = %q, want %q", got, want)
	}
}

func TestServiceInstanceHTTPAddrRequiresExplicitMetadata(t *testing.T) {
	withoutHTTP := &serviceInstance{Addr: "127.0.0.1:9000"}
	if got := withoutHTTP.HTTPAddr(); got != "" {
		t.Fatalf("HTTPAddr without metadata = %q, want empty", got)
	}
	withHTTP := &serviceInstance{
		Addr:     "127.0.0.1:9000",
		Metadata: map[string]string{"http_addr": "http://127.0.0.1:8080"},
	}
	if got := withHTTP.HTTPAddr(); got != "http://127.0.0.1:8080" {
		t.Fatalf("HTTPAddr with metadata = %q, want configured origin", got)
	}
}

func TestHTTPInstanceRoundRobinAndRemoval(t *testing.T) {
	gw := &EtcdGateway{}
	for _, instance := range []struct {
		id   string
		addr string
	}{
		{id: "B", addr: "http://127.0.0.1:8082"},
		{id: "C", addr: "https://127.0.0.1:8083"},
		{id: "A", addr: "http://127.0.0.1:8081"},
	} {
		if err := gw.addHTTPInstance("tasks", instance.id, instance.addr); err != nil {
			t.Fatalf("add %s: %v", instance.id, err)
		}
	}

	for index, want := range []string{"A", "B", "C", "A"} {
		if got := gw.getHTTPInstance("tasks"); got == nil || got.ID != want {
			t.Fatalf("selection %d = %#v, want instance %s", index, got, want)
		}
	}

	gw.removeHTTPInstance("tasks", "B")
	for index, want := range []string{"A", "C", "A", "C"} {
		if got := gw.getHTTPInstance("tasks"); got == nil || got.ID != want {
			t.Fatalf("selection after removal %d = %#v, want instance %s", index, got, want)
		}
	}
}

func TestHTTPInstanceUpdateSharesUnaffectedServiceSnapshot(t *testing.T) {
	gw := &EtcdGateway{}
	if err := gw.addHTTPInstance("service-x", "X-A", "http://127.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	if err := gw.addHTTPInstance("service-y", "Y-A", "http://127.0.0.1:9081"); err != nil {
		t.Fatal(err)
	}
	before := gw.loadHTTPInstances()
	xBefore := before["service-x"]
	yBefore := before["service-y"]

	if err := gw.addHTTPInstance("service-y", "Y-B", "http://127.0.0.1:9082"); err != nil {
		t.Fatal(err)
	}
	after := gw.loadHTTPInstances()
	if after["service-x"] != xBefore {
		t.Fatal("updating service-y cloned service-x snapshot")
	}
	if after["service-y"] == yBefore {
		t.Fatal("updating service-y reused its mutated snapshot")
	}
	if len(after["service-y"].instances) != 2 {
		t.Fatalf("service-y instances = %d, want 2", len(after["service-y"].instances))
	}
}

func TestHTTPInstanceFinalRemovalClearsAuxiliaryState(t *testing.T) {
	gw := &EtcdGateway{}
	if err := gw.addHTTPInstance("tasks", "A", "http://127.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	gw.circuitStates.Store("tasks", &CircuitBreaker{state: circuitOpen})

	gw.removeHTTPInstance("tasks", "A")

	if _, exists := gw.loadHTTPInstances()["tasks"]; exists {
		t.Fatal("final removal retained HTTP service snapshot")
	}
	if _, exists := gw.loadHTTPIndexes()["tasks"]; exists {
		t.Fatal("final removal retained HTTP round-robin index")
	}
	if _, exists := gw.circuitStates.Load("tasks"); exists {
		t.Fatal("final removal retained circuit state")
	}
}

func TestHTTPInstanceRejectsInvalidOriginBeforePublishingSnapshot(t *testing.T) {
	gw := &EtcdGateway{}
	if err := gw.addHTTPInstance("tasks", "A", "http://127.0.0.1:8081/base"); err == nil {
		t.Fatal("addHTTPInstance invalid origin error = nil")
	}
	if got := gw.getHTTPInstance("tasks"); got != nil {
		t.Fatalf("invalid origin published instance %#v", got)
	}
	if len(gw.loadHTTPIndexes()) != 0 {
		t.Fatalf("invalid origin created index: %#v", gw.loadHTTPIndexes())
	}
}

func TestHTTPInstancePutReplacesExistingTarget(t *testing.T) {
	gw := &EtcdGateway{}
	if err := gw.addHTTPInstance("tasks", "A", "http://127.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	if err := gw.addHTTPInstance("tasks", "A", "https://127.0.0.1:8443"); err != nil {
		t.Fatal(err)
	}

	got := gw.getHTTPInstance("tasks")
	if got == nil || got.ID != "A" || got.Addr != "https://127.0.0.1:8443" {
		t.Fatalf("replacement instance = %#v, want A at new target", got)
	}
	if instances := gw.loadHTTPInstances()["tasks"].instances; len(instances) != 1 {
		t.Fatalf("replacement retained %d instances, want 1", len(instances))
	}
}

func TestHTTPInstancePutWithoutOriginRemovesExistingTarget(t *testing.T) {
	gw := &EtcdGateway{}
	if err := gw.addHTTPInstance("tasks", "A", "http://127.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	gw.circuitStates.Store("tasks", &CircuitBreaker{state: circuitOpen})

	if err := gw.addHTTPInstance("tasks", "A", ""); err != nil {
		t.Fatalf("empty HTTP origin reconciliation: %v", err)
	}
	assertHTTPServiceStateRemoved(t, gw, "tasks")
}

func TestHTTPInstancePutWithInvalidOriginRemovesExistingTarget(t *testing.T) {
	gw := &EtcdGateway{}
	if err := gw.addHTTPInstance("tasks", "A", "http://127.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	gw.circuitStates.Store("tasks", &CircuitBreaker{state: circuitOpen})

	if err := gw.addHTTPInstance("tasks", "A", "http://127.0.0.1:8081/base"); err == nil {
		t.Fatal("invalid HTTP origin reconciliation error = nil")
	}
	assertHTTPServiceStateRemoved(t, gw, "tasks")
}

func assertHTTPServiceStateRemoved(t *testing.T, gw *EtcdGateway, serviceName string) {
	t.Helper()
	if got := gw.getHTTPInstance(serviceName); got != nil {
		t.Fatalf("HTTP service %s retained instance %#v", serviceName, got)
	}
	if _, exists := gw.loadHTTPInstances()[serviceName]; exists {
		t.Fatalf("HTTP service %s retained snapshot", serviceName)
	}
	if _, exists := gw.loadHTTPIndexes()[serviceName]; exists {
		t.Fatalf("HTTP service %s retained round-robin index", serviceName)
	}
	if _, exists := gw.circuitStates.Load(serviceName); exists {
		t.Fatalf("HTTP service %s retained circuit state", serviceName)
	}
}

func BenchmarkHTTPInstanceSelection(b *testing.B) {
	gw := &EtcdGateway{}
	if err := gw.addHTTPInstance("tasks", "A", "http://127.0.0.1:8081"); err != nil {
		b.Fatal(err)
	}
	if err := gw.addHTTPInstance("tasks", "B", "http://127.0.0.1:8082"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = gw.getHTTPInstance("tasks")
	}
}

func TestRegisterHTTPRoutesPublishesBatchOnceWithoutWorker(t *testing.T) {
	app, client := newHTTPRouteTestApp(t, "batch", "users")
	routes := make([]*Route, 100)
	for index := range routes {
		routes[index] = &Route{
			Method: http.MethodGet,
			Path:   fmt.Sprintf("/api/users/%d", index),
		}
	}

	before, err := client.Get(context.Background(), "/batch/gateway/routes/", clientv3.WithPrefix())
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterHTTPRoutes(app, routes...); err != nil {
		t.Fatalf("RegisterHTTPRoutes: %v", err)
	}

	response, err := client.Get(context.Background(), "/batch/gateway/routes/", clientv3.WithPrefix())
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Kvs) != len(routes) {
		t.Fatalf("published PUTs = %d, want %d", len(response.Kvs), len(routes))
	}
	if delta := response.Header.Revision - before.Header.Revision; delta != int64(len(routes)) {
		t.Fatalf("etcd revision delta = %d, want exactly %d PUTs", delta, len(routes))
	}
	for index, route := range routes {
		if route.ServiceName != "" || route.Protocol != "" || route.Enabled {
			t.Fatalf("route %d mutated by registration: %#v", index, route)
		}
	}
}

func TestRegisterHTTPRoutesDefaultsToSuccessfulRegistryIdentity(t *testing.T) {
	client, endpoint := startGatewayRouteStoreEtcd(t)
	app := core.New(core.Options{
		"etcd": core.Options{
			"service_name": "configured-name",
		},
	})
	if err := app.EnableEtcdRegistry(&coreetcd.Options{
		Endpoints:   []string{endpoint},
		DialTimeout: 3 * time.Second,
		Namespace:   "registry-identity",
		ServiceName: "registered-name",
		ServiceAddr: "127.0.0.1:39001",
		ServiceID:   "registry-identity-test",
		TTL:         10,
	}); err != nil {
		t.Fatalf("EnableEtcdRegistry: %v", err)
	}
	t.Cleanup(func() { _ = app.ShutdownWithTimeout(time.Second) })

	if err := RegisterHTTPRoutes(app, &Route{Method: http.MethodGet, Path: "/identity"}); err != nil {
		t.Fatalf("RegisterHTTPRoutes: %v", err)
	}
	_, stored := singleStoredRoute(t, client, "/registry-identity/gateway/routes/")
	if stored.ServiceName != "registered-name" {
		t.Fatalf("stored ServiceName = %q, want last successful registry identity", stored.ServiceName)
	}
}

func TestRegisterHTTPRoutesSingleWrapperMatchesBatch(t *testing.T) {
	app, client := newHTTPRouteTestApp(t, "equivalent", "users")
	value := &Route{
		Method: http.MethodGet,
		Path:   "/api/users/:id",
	}

	if err := RegisterHTTPRoutes(app, value); err != nil {
		t.Fatalf("RegisterHTTPRoutes: %v", err)
	}
	if err := RegisterHTTPRoute(app, value, "/legacy/routes/"); err != nil {
		t.Fatalf("RegisterHTTPRoute: %v", err)
	}

	batchKey, batch := singleStoredRoute(t, client, "/equivalent/gateway/routes/")
	legacyKey, legacy := singleStoredRoute(t, client, "/legacy/routes/")
	if strings.TrimPrefix(batchKey, "/equivalent/gateway/routes/") != strings.TrimPrefix(legacyKey, "/legacy/routes/") {
		t.Fatalf("single wrapper key %q differs from batch key %q", legacyKey, batchKey)
	}
	if !reflect.DeepEqual(batch, legacy) {
		t.Fatalf("single wrapper value differs from batch:\nbatch  = %#v\nwrapper= %#v", batch, legacy)
	}
	if batch.ID == "" || strings.Contains(batch.ID, "/") {
		t.Fatalf("stable route ID = %q, want non-empty legacy-compatible ID without slash", batch.ID)
	}
}

func TestRegisterHTTPRoutesValidatesCompleteBatchBeforeWrite(t *testing.T) {
	app, client := newHTTPRouteTestApp(t, "validate", "users")
	valid := &Route{Method: http.MethodGet, Path: "/valid"}
	invalid := &Route{Method: http.MethodGet, Path: "not-absolute"}

	if err := RegisterHTTPRoutes(app, valid, invalid); err == nil {
		t.Fatal("RegisterHTTPRoutes error = nil, want validation failure")
	}
	response, err := client.Get(context.Background(), "/validate/gateway/routes/", clientv3.WithPrefix())
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Kvs) != 0 {
		t.Fatalf("invalid batch wrote %d routes", len(response.Kvs))
	}
}

func TestGatewayRouteAdminAddressesProgrammaticStableID(t *testing.T) {
	app, client := newHTTPRouteTestApp(t, "admin-id", "users")
	value := &Route{
		Method: http.MethodGet,
		Path:   "/api/users/:id",
	}
	if err := RegisterHTTPRoutes(app, value); err != nil {
		t.Fatalf("RegisterHTTPRoutes: %v", err)
	}
	storageKey, stored := singleStoredRoute(t, client, "/admin-id/gateway/routes/")
	if stored.ID == "" || strings.Contains(stored.ID, "/") {
		t.Fatalf("programmatic route ID = %q, want non-empty legacy-compatible ID without slash", stored.ID)
	}
	wantID := routeID(&Route{
		Protocol:    RouteProtocolHTTP,
		Method:      http.MethodGet,
		Path:        "/api/users/:id",
		ServiceName: "users",
	})
	if stored.ID != wantID {
		t.Fatalf("programmatic route ID = %q, want deterministic legacy ID %q", stored.ID, wantID)
	}
	if value.ID != "" {
		t.Fatalf("caller-owned route ID mutated to %q", value.ID)
	}

	adminApp := core.New()
	gw := &EtcdGateway{
		app:     adminApp,
		etcdCli: client,
		prefix:  "/admin-id/gateway/routes/",
		config:  &Config{},
	}
	gw.storeRouteSnapshot(newRouteSnapshot(map[string]*routeSourceValue{
		storageKey: {Definition: &stored},
	}))
	gw.setupAdminAPI()

	get := httptest.NewRequest(http.MethodGet, "/admin/gateway/routes/"+stored.ID, nil)
	get.RemoteAddr = "127.0.0.1:12345"
	getResponse := httptest.NewRecorder()
	adminApp.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("admin get status = %d, want 200: %s", getResponse.Code, getResponse.Body.String())
	}

	updateBody := strings.NewReader(`{"protocol":"http","method":"GET","path":"/api/v2/users/:id","service_name":"users","enabled":true}`)
	update := httptest.NewRequest(http.MethodPut, "/admin/gateway/routes/"+stored.ID, updateBody)
	update.Header.Set("Content-Type", "application/json")
	update.RemoteAddr = "127.0.0.1:12345"
	updateResponse := httptest.NewRecorder()
	adminApp.ServeHTTP(updateResponse, update)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("admin update status = %d, want 200: %s", updateResponse.Code, updateResponse.Body.String())
	}
	_, updated := singleStoredRoute(t, client, "/admin-id/gateway/routes/")
	if updated.ID != stored.ID || updated.Path != "/api/v2/users/:id" {
		t.Fatalf("updated route ID/path = %q %q, want %q /api/v2/users/:id", updated.ID, updated.Path, stored.ID)
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/admin/gateway/routes/"+stored.ID, nil)
	deleteRequest.RemoteAddr = "127.0.0.1:12345"
	deleteResponse := httptest.NewRecorder()
	adminApp.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("admin delete status = %d, want 200: %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	deleted, err := client.Get(context.Background(), storageKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted.Kvs) != 0 {
		t.Fatalf("admin delete retained route key %s", storageKey)
	}
}

func newHTTPRouteTestApp(t *testing.T, namespace, serviceName string) (*core.Core, *clientv3.Client) {
	t.Helper()
	client, endpoint := startGatewayRouteStoreEtcd(t)
	app := core.New()
	if err := app.EnableEtcdRegistry(&coreetcd.Options{
		Endpoints:   []string{endpoint},
		DialTimeout: 3 * time.Second,
		Namespace:   namespace,
		ServiceName: serviceName,
		ServiceAddr: "127.0.0.1:39002",
		ServiceID:   namespace + "-http-route-test",
		TTL:         10,
	}); err != nil {
		t.Fatalf("EnableEtcdRegistry: %v", err)
	}
	t.Cleanup(func() { _ = app.ShutdownWithTimeout(time.Second) })
	return app, client
}

func singleStoredRoute(t *testing.T, client *clientv3.Client, prefix string) (string, Route) {
	t.Helper()
	response, err := client.Get(context.Background(), prefix, clientv3.WithPrefix())
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Kvs) != 1 {
		t.Fatalf("stored routes under %s = %d, want 1", prefix, len(response.Kvs))
	}
	var value Route
	if err := json.Unmarshal(response.Kvs[0].Value, &value); err != nil {
		t.Fatal(err)
	}
	return string(response.Kvs[0].Key), value
}
