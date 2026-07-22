package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xs23933/core/v3"
	gatewayroute "github.com/xs23933/core/v3/gateway/route"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestGatewayNamespacePrefixes(t *testing.T) {
	tests := []struct {
		name, namespace, serviceRoot, routePrefix string
	}{
		{name: "legacy", serviceRoot: "/services/", routePrefix: "/gateway/routes/"},
		{name: "namespaced", namespace: " /xpay//dev/ ", serviceRoot: "/xpay/dev/services/", routePrefix: "/xpay/dev/gateway/routes/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gatewayServiceRoot(tt.namespace); got != tt.serviceRoot {
				t.Fatalf("gatewayServiceRoot(%q) = %q, want %q", tt.namespace, got, tt.serviceRoot)
			}
			if got := defaultRoutePrefix(tt.namespace); got != tt.routePrefix {
				t.Fatalf("defaultRoutePrefix(%q) = %q, want %q", tt.namespace, got, tt.routePrefix)
			}
		})
	}
}

func TestGatewayPrefixDefaultsPreserveExplicitRoutePrefix(t *testing.T) {
	config := &Config{Namespace: "xpay"}
	applyGatewayPrefixDefaults(config)
	if config.RoutePrefix != "/xpay/gateway/routes/" {
		t.Fatalf("derived RoutePrefix = %q, want /xpay/gateway/routes/", config.RoutePrefix)
	}

	config = &Config{
		Namespace:   "xpay",
		RoutePrefix: "/custom/routes/",
	}
	applyGatewayPrefixDefaults(config)
	if config.RoutePrefix != "/custom/routes/" {
		t.Fatalf("explicit RoutePrefix = %q, want /custom/routes/", config.RoutePrefix)
	}
}

func TestGatewayParseServiceKeyUsesExpectedRoot(t *testing.T) {
	service, id, ok := parseServiceKey("/xpay/services/auth/auth-1", "/xpay/services/")
	if service != "auth" || id != "auth-1" || !ok {
		t.Fatalf("parse namespaced key = (%q, %q, %v), want (auth, auth-1, true)", service, id, ok)
	}
	if _, _, ok := parseServiceKey("/union/services/auth/auth-1", "/xpay/services/"); ok {
		t.Fatal("foreign namespace key should be rejected")
	}
}

func TestGatewayConnectRetryDelayCapsAtFiveSeconds(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 0},
		{attempt: 1, want: 500 * time.Millisecond},
		{attempt: 4, want: 2 * time.Second},
		{attempt: 20, want: 5 * time.Second},
	}

	for _, tt := range tests {
		if got := gatewayConnectRetryDelay(tt.attempt); got != tt.want {
			t.Fatalf("delay attempt %d = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestGatewayConnectRetryAttemptsAllowsServiceStartupWindow(t *testing.T) {
	if gatewayConnectRetryAttempts < 10 {
		t.Fatalf("gatewayConnectRetryAttempts = %d, want at least 10", gatewayConnectRetryAttempts)
	}
}

func TestGRPCServiceExcludedFromAutoRoutes(t *testing.T) {
	config := &Config{GRPCServiceExcludes: []string{
		"payment.provider.v1.PaymentProviderService",
		"game.provider.v1.GameProviderService",
	}}

	for _, service := range config.GRPCServiceExcludes {
		if !grpcServiceExcluded(config, service) {
			t.Fatalf("service %s should be excluded", service)
		}
	}
	if grpcServiceExcluded(config, "api.v1.BillingService") {
		t.Fatal("api.v1.BillingService should remain public")
	}
}

func TestUserServicePutAutoRouteUsesServiceRoot(t *testing.T) {
	method, path := grpcToHTTP("api.v1", "api.v1.UserService", "Put")
	if method != http.MethodPut {
		t.Fatalf("method = %q, want %q", method, http.MethodPut)
	}

	app := core.New()
	gw := &EtcdGateway{app: app, connPool: NewConnectionPool()}
	gw.storeHTTPInstances(make(map[string]*httpServiceInstances))
	gw.storeHTTPIndexes(make(map[string]*atomic.Uint64))
	route := &Route{
		Protocol:    RouteProtocolGRPC,
		Method:      method,
		Path:        path,
		ServiceName: "auth",
		GRPCMethod:  "/api.v1.UserService/Put",
		Enabled:     true,
	}
	gw.registerRoute(route)

	if route.Path != "/api/v1/user" {
		t.Fatalf("registered path = %q, want %q", route.Path, "/api/v1/user")
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/user", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; normalized route should match", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestAppendHTTPHeadersToMetadataIncludesCustomHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Add("X-Sign", "sig-1")
	headers.Add("X-Sign", "sig-2")
	headers.Set("X-App-Name", "web")
	headers.Set("Authorization", "Bearer token")
	headers.Set("Connection", "close")
	headers.Set("Content-Length", "10")
	headers.Set("Content-Type", "application/json")

	md := metadata.New(nil)
	appendHTTPHeadersToMetadata(md, headers)

	for key, want := range map[string][]string{
		"x-sign":        {"sig-1", "sig-2"},
		"x-app-name":    {"web"},
		"authorization": {"Bearer token"},
	} {
		if got := md.Get(key); !reflect.DeepEqual(got, want) {
			t.Fatalf("metadata %s = %#v, want %#v", key, got, want)
		}
	}
	for _, key := range []string{"connection", "content-length", "content-type"} {
		if got := md.Get(key); len(got) > 0 {
			t.Fatalf("metadata %s = %#v, want omitted transport header", key, got)
		}
	}
}

func TestBuildGRPCRequestBodyIncludesRawBodyAndHeaders(t *testing.T) {
	rawBody := []byte(`{"merchant_oid":"r1","status":"SUCCESS"}`)
	headers := http.Header{}
	headers.Set("Signature", "merchant,signature")
	headers.Add("X-Sign", "sig-1")
	headers.Add("X-Sign", "sig-2")

	got := buildGRPCRequestBody(
		map[string]string{"direct": "collect"},
		map[string][]string{"debug": {"1"}},
	)
	appendGatewayRequestMetadata(got, headers, rawBody)

	if got["direct"] != "collect" {
		t.Fatalf("direct = %v, want collect", got["direct"])
	}
	if got["debug"] != "1" {
		t.Fatalf("debug = %v, want 1", got["debug"])
	}
	if got["raw_body"] != base64.StdEncoding.EncodeToString(rawBody) {
		t.Fatalf("raw_body = %v, want base64 raw body", got["raw_body"])
	}
	gotHeaders, ok := got["headers"].(map[string]string)
	if !ok {
		t.Fatalf("headers = %#v, want map[string]string", got["headers"])
	}
	if gotHeaders["signature"] != "merchant,signature" {
		t.Fatalf("signature header = %q, want merchant,signature", gotHeaders["signature"])
	}
	if gotHeaders["x-sign"] != "sig-1,sig-2" {
		t.Fatalf("x-sign header = %q, want sig-1,sig-2", gotHeaders["x-sign"])
	}
}

func TestMergeGRPCRequestBodyPathParamsOverrideQuerySpoofing(t *testing.T) {
	got := mergeGRPCRequestBody(
		map[string]string{"action": "get"},
		map[string][]string{"action": {"transferinout"}},
		nil,
	)

	if got["action"] != "get" {
		t.Fatalf("action = %v, want authoritative path value get", got["action"])
	}
}

func TestMergeGRPCRequestBodyPathParamsOverrideJSONBodySpoofing(t *testing.T) {
	got := mergeGRPCRequestBody(
		map[string]string{"action": "get"},
		nil,
		core.Map{"action": "transferinout"},
	)

	if got["action"] != "get" {
		t.Fatalf("action = %v, want authoritative path value get", got["action"])
	}
}

func TestReadAndResetBodyEnforcesLimitAndPreservesExactBytes(t *testing.T) {
	t.Run("boundary", func(t *testing.T) {
		raw := []byte("{ \"amount\" : 1 }")
		req := httptest.NewRequest(http.MethodPost, "/callback", bytes.NewReader(raw))

		got, err := readAndResetBody(req, int64(len(raw)))
		if err != nil {
			t.Fatalf("read boundary body: %v", err)
		}
		if !bytes.Equal(got, raw) {
			t.Fatalf("body = %q, want exact %q", got, raw)
		}
		reset, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read reset body: %v", err)
		}
		if !bytes.Equal(reset, raw) {
			t.Fatalf("reset body = %q, want exact %q", reset, raw)
		}
	})

	t.Run("over limit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/callback", strings.NewReader("12345"))
		_, err := readAndResetBody(req, 4)
		if !errors.Is(err, errRequestBodyTooLarge) {
			t.Fatalf("error = %v, want errRequestBodyTooLarge", err)
		}
	})
}

func TestGRPCProxyRejectsOversizedBodyWithStatusRequestEntityTooLarge(t *testing.T) {
	app := core.New()
	gw := &EtcdGateway{
		app:      app,
		config:   &Config{MaxRequestBodyBytes: 4},
		connPool: NewConnectionPool(),
	}
	gw.storeHTTPInstances(make(map[string]*httpServiceInstances))
	gw.storeHTTPIndexes(make(map[string]*atomic.Uint64))
	gw.registerRoute(&Route{
		Protocol:    RouteProtocolGRPC,
		Method:      http.MethodPost,
		Path:        "/callback/:action",
		ServiceName: "callback",
		GRPCMethod:  "/callback.CashService/Post",
	})

	req := httptest.NewRequest(http.MethodPost, "/callback/get", strings.NewReader("12345"))
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestGatewayConfigRequestBodyLimitDefaultsAndPreservesExplicitValue(t *testing.T) {
	config := &Config{}
	applyGatewayConfigDefaults(config)
	if config.MaxRequestBodyBytes != defaultMaxRequestBodyBytes {
		t.Fatalf("default max request body bytes = %d, want %d", config.MaxRequestBodyBytes, defaultMaxRequestBodyBytes)
	}

	config = &Config{MaxRequestBodyBytes: 1024}
	applyGatewayConfigDefaults(config)
	if config.MaxRequestBodyBytes != 1024 {
		t.Fatalf("explicit max request body bytes = %d, want 1024", config.MaxRequestBodyBytes)
	}
}

func TestAppendGatewayRequestMetadataOverridesBodyFields(t *testing.T) {
	rawBody := []byte(`{"status":"SUCCESS"}`)
	reqBody := core.Map{
		"raw_body": "spoofed",
		"headers":  map[string]string{"signature": "spoofed"},
	}
	headers := http.Header{"Signature": {"real"}}

	appendGatewayRequestMetadata(reqBody, headers, rawBody)

	if reqBody["raw_body"] != base64.StdEncoding.EncodeToString(rawBody) {
		t.Fatalf("raw_body = %v, want gateway raw body", reqBody["raw_body"])
	}
	gotHeaders := reqBody["headers"].(map[string]string)
	if gotHeaders["signature"] != "real" {
		t.Fatalf("signature header = %q, want real", gotHeaders["signature"])
	}
}

func TestHTTPRouteRegisteredButMissingInstanceReturnsUnavailable(t *testing.T) {
	app := core.New()
	gw := &EtcdGateway{}
	gw.app = app
	gw.connPool = NewConnectionPool()
	gw.storeHTTPInstances(make(map[string]*httpServiceInstances))
	gw.storeHTTPIndexes(make(map[string]*atomic.Uint64))

	definition := &Route{
		Protocol:     RouteProtocolHTTP,
		Method:       http.MethodGet,
		Path:         "/trace.js",
		ServiceName:  "analytics",
		UpstreamPath: "/trace.js",
		Enabled:      true,
	}
	if err := prepareRoute(definition); err != nil {
		t.Fatal(err)
	}
	gw.applyRouteBatch([]routeEvent{{
		StorageKey: "test:" + definition.ID,
		Value:      &routeSourceValue{Definition: definition},
	}})

	req := httptest.NewRequest(http.MethodGet, "/trace.js", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; route should match before upstream lookup", rec.Code)
	}
}

func TestGatewayRouteAdminOutputIncludesActivationAndConflict(t *testing.T) {
	app := core.New()
	gw := &EtcdGateway{app: app, prefix: "/gateway/routes/", connPool: NewConnectionPool()}
	gw.storeHTTPInstances(make(map[string]*httpServiceInstances))
	gw.storeHTTPIndexes(make(map[string]*atomic.Uint64))
	manual := minimalRouteStateDefinition("manual", gatewayroute.SourceManual, "admin", "manual-service", "/api/users/:id")
	autoA := minimalRouteStateDefinition("auto-a", gatewayroute.SourceAutoHTTP, "service-a", "auto-a", "/api/users/:id")
	autoB := minimalRouteStateDefinition("auto-b", gatewayroute.SourceAutoHTTP, "service-b", "auto-b", "/api/users/:id")
	gw.storeRouteSnapshot(newRouteSnapshot(map[string]*routeSourceValue{
		"/gateway/routes/manual/slot": {Definition: manual},
		minimalAutomaticCatalogKey("/gateway/routes/", "service-a"): {
			Catalog: &gatewayroute.Catalog{ServiceName: "service-a", Routes: []*gatewayroute.Definition{autoA}},
		},
		minimalAutomaticCatalogKey("/gateway/routes/", "service-b"): {
			Catalog: &gatewayroute.Catalog{ServiceName: "service-b", Routes: []*gatewayroute.Definition{autoB}},
		},
	}))
	gw.setupAdminAPI()

	request := httptest.NewRequest(http.MethodGet, "/admin/gateway/routes", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode route list: %v", err)
	}
	var found map[string]any
	for _, item := range payload.Data {
		if item["id"] == "manual" {
			found = item
			break
		}
	}
	if found == nil {
		t.Fatalf("manual route absent from %#v", payload.Data)
	}
	if found["slot"] != manual.Slot || found["source"] != string(gatewayroute.SourceManual) || found["owner"] != "admin" {
		t.Fatalf("identity fields = slot:%v source:%v owner:%v", found["slot"], found["source"], found["owner"])
	}
	if found["active"] != true {
		t.Fatalf("active = %v, want true", found["active"])
	}
	conflict, ok := found["conflict"].(map[string]any)
	if !ok {
		t.Fatalf("conflict = %#v, want object", found["conflict"])
	}
	if records, ok := conflict["records"].([]any); !ok || len(records) != 2 {
		t.Fatalf("conflict records = %#v, want two automatic declarations", conflict["records"])
	}
}

func TestGatewayRouteAdminCreatePublishesManualSlotKey(t *testing.T) {
	client, _ := startGatewayRouteStoreEtcd(t)
	app := core.New()
	gw := &EtcdGateway{
		app:     app,
		etcdCli: client,
		prefix:  "/gateway/routes/",
		config:  &Config{},
	}
	gw.storeRouteSnapshot(emptyRouteSnapshot())
	gw.setupAdminAPI()

	body := `{"protocol":"http","method":"GET","path":"/api/users/:id","service_name":"users"}`
	request := httptest.NewRequest(http.MethodPost, "/admin/gateway/routes", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", response.Code, response.Body.String())
	}

	slot, err := gatewayroute.SlotID(http.MethodGet, "/api/users/:id")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := client.Get(context.Background(), "/gateway/routes/", clientv3.WithPrefix())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Kvs) != 1 || string(stored.Kvs[0].Key) != "/gateway/routes/manual/"+slot {
		t.Fatalf("stored route keys = %v, want manual slot key", routeKVKeys(stored))
	}
}

func TestGatewayRouteAdminUpdateDoesNotOverwriteAutomaticRecord(t *testing.T) {
	client, _ := startGatewayRouteStoreEtcd(t)
	automatic := minimalRouteStateDefinition("shared-id", gatewayroute.SourceAutoHTTP, "users", "automatic", "/api/users/:id")
	automaticKey := minimalAutomaticCatalogKey("/gateway/routes/", "users")
	automaticCatalog := &gatewayroute.Catalog{ServiceName: "users", Routes: []*gatewayroute.Definition{automatic}}
	automaticValue, err := json.Marshal(automaticCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(context.Background(), automaticKey, string(automaticValue)); err != nil {
		t.Fatal(err)
	}

	app := core.New()
	gw := &EtcdGateway{
		app:     app,
		etcdCli: client,
		prefix:  "/gateway/routes/",
		config:  &Config{},
	}
	gw.storeRouteSnapshot(newRouteSnapshot(map[string]*routeSourceValue{
		automaticKey: {Catalog: automaticCatalog},
	}))
	gw.setupAdminAPI()

	body := `{"protocol":"http","method":"GET","path":"/api/users/:id","service_name":"manual","enabled":true}`
	request := httptest.NewRequest(http.MethodPut, "/admin/gateway/routes/shared-id", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200: %s", response.Code, response.Body.String())
	}

	storedAutomatic, err := client.Get(context.Background(), automaticKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedAutomatic.Kvs) != 1 || !bytes.Equal(storedAutomatic.Kvs[0].Value, automaticValue) {
		t.Fatalf("automatic record was overwritten or deleted: %#v", storedAutomatic.Kvs)
	}
	manualKey := "/gateway/routes/manual/" + automatic.Slot
	storedManual, err := client.Get(context.Background(), manualKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedManual.Kvs) != 1 {
		t.Fatalf("manual override key %s was not published", manualKey)
	}
}

func TestGatewayRouteAdminGetAndDeleteSupportsLegacyAndManualKeys(t *testing.T) {
	client, _ := startGatewayRouteStoreEtcd(t)
	legacy := minimalRouteStateDefinition("legacy-id", "", "", "legacy", "/api/legacy")
	legacy.Path = "/api/legacy"
	legacy.Slot, _ = gatewayroute.SlotID(legacy.Method, legacy.Path)
	manual := minimalRouteStateDefinition("manual-id", gatewayroute.SourceManual, "", "manual", "/api/manual")
	manual.Path = "/api/manual"
	manual.Slot, _ = gatewayroute.SlotID(manual.Method, manual.Path)
	legacyKey := "/gateway/routes/legacy-id"
	manualKey := "/gateway/routes/manual/" + manual.Slot
	for storageKey, definition := range map[string]*gatewayroute.Definition{legacyKey: legacy, manualKey: manual} {
		value, err := json.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Put(context.Background(), storageKey, string(value)); err != nil {
			t.Fatal(err)
		}
	}

	app := core.New()
	gw := &EtcdGateway{app: app, etcdCli: client, prefix: "/gateway/routes/"}
	gw.storeRouteSnapshot(newRouteSnapshot(map[string]*routeSourceValue{
		legacyKey: {Definition: legacy},
		manualKey: {Definition: manual},
	}))
	gw.setupAdminAPI()

	get := httptest.NewRequest(http.MethodGet, "/admin/gateway/routes/manual-id", nil)
	get.RemoteAddr = "127.0.0.1:12345"
	getResponse := httptest.NewRecorder()
	app.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), `"active":true`) {
		t.Fatalf("manual get status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}

	for id, storageKey := range map[string]string{"legacy-id": legacyKey, "manual-id": manualKey} {
		request := httptest.NewRequest(http.MethodDelete, "/admin/gateway/routes/"+id, nil)
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("delete %s status=%d body=%s", id, response.Code, response.Body.String())
		}
		stored, err := client.Get(context.Background(), storageKey)
		if err != nil {
			t.Fatal(err)
		}
		if len(stored.Kvs) != 0 {
			t.Fatalf("delete %s retained key %s", id, storageKey)
		}
	}
}

func routeKVKeys(response *clientv3.GetResponse) []string {
	keys := make([]string, 0, len(response.Kvs))
	for _, value := range response.Kvs {
		keys = append(keys, string(value.Key))
	}
	return keys
}

func TestNextRouteWatchRevisionStartsAfterLoadedSnapshot(t *testing.T) {
	if got := nextRouteWatchRevision(42); got != 43 {
		t.Fatalf("nextRouteWatchRevision(42) = %d, want 43", got)
	}
	if got := nextRouteWatchRevision(0); got != 0 {
		t.Fatalf("nextRouteWatchRevision(0) = %d, want 0", got)
	}
}

func TestGRPCErrorToResponseIncludesStatusMessage(t *testing.T) {
	got := grpcErrorToResponse(status.Error(codes.InvalidArgument, "coupon issue conditions not matched"))

	if got["msg"] != "coupon issue conditions not matched" {
		t.Fatalf("msg = %v, want coupon issue conditions not matched", got["msg"])
	}
	if got["code"] != int(codes.InvalidArgument) {
		t.Fatalf("code = %v, want %d", got["code"], codes.InvalidArgument)
	}
}

func TestGRPCErrorToResponseHidesNonStatusError(t *testing.T) {
	got := grpcErrorToResponse(errors.New("connection reset by peer"))

	if got["msg"] != "internal server error" {
		t.Fatalf("msg = %v, want internal server error", got["msg"])
	}
	if got["code"] != 500 {
		t.Fatalf("code = %v, want 500", got["code"])
	}
}

func TestHTTPInstanceSelectionUsesSnapshotWithoutAllocating(t *testing.T) {
	gw := &EtcdGateway{}
	gw.storeHTTPInstances(make(map[string]*httpServiceInstances))
	if err := gw.addHTTPInstance("task-service", "task-2", "http://127.0.0.1:8082"); err != nil {
		t.Fatal(err)
	}
	if err := gw.addHTTPInstance("task-service", "task-1", "http://127.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}

	first := gw.getHTTPInstance("task-service")
	second := gw.getHTTPInstance("task-service")
	if first == nil || second == nil || first.ID == second.ID {
		t.Fatalf("round-robin instances = %#v, %#v; want two non-empty different addresses", first, second)
	}

	_ = gw.getHTTPInstance("task-service")
	allocs := testing.AllocsPerRun(1000, func() {
		_ = gw.getHTTPInstance("task-service")
	})
	if allocs != 0 {
		t.Fatalf("getHTTPInstance allocations = %v, want 0", allocs)
	}
}

func TestHTTPInstanceRemovalRebuildsSnapshot(t *testing.T) {
	gw := &EtcdGateway{}
	gw.storeHTTPInstances(make(map[string]*httpServiceInstances))
	if err := gw.addHTTPInstance("task-service", "task-1", "http://127.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	if err := gw.addHTTPInstance("task-service", "task-2", "http://127.0.0.1:8082"); err != nil {
		t.Fatal(err)
	}

	gw.removeHTTPInstance("task-service", "task-1")

	for i := 0; i < 4; i++ {
		if got := gw.getHTTPInstance("task-service"); got == nil || got.ID != "task-2" {
			t.Fatalf("instance after removal = %#v, want remaining instance", got)
		}
	}
}

func TestHTTPServiceDeleteThenPutKeepsLatestTarget(t *testing.T) {
	gw := &EtcdGateway{connPool: NewConnectionPool()}
	if err := gw.addHTTPInstance("task-service", "task-1", "http://127.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	pool := gw.connPool.GetOrCreate(nil, "task-service")
	pool.storeState(servicePoolState{
		byID: map[string]*ReflectionProxy{"task-1": {}},
		all:  []*ReflectionProxy{{}},
	})

	// The watch applies the HTTP delete synchronously. Model a delayed gRPC
	// cleanup by running it only after the replacement HTTP PUT.
	gw.removeHTTPInstance("task-service", "task-1")
	if err := gw.addHTTPInstance("task-service", "task-1", "http://127.0.0.1:8082"); err != nil {
		t.Fatal(err)
	}
	gw.removeGRPCInstance("task-service", "task-1")
	if gw.connPool.Get("task-service") != nil {
		t.Fatal("delayed gRPC cleanup retained an empty pool")
	}
	got := gw.getHTTPInstance("task-service")
	if got == nil || got.Addr != "http://127.0.0.1:8082" {
		t.Fatalf("latest HTTP target = %#v, want re-added target", got)
	}
}

func TestServicePoolGetUsesSnapshotWithoutAllocating(t *testing.T) {
	pool := NewServicePool(nil, "billing-service")
	pool.storeState(servicePoolState{
		byID: map[string]*ReflectionProxy{
			"billing-1": {addr: "127.0.0.1:9001"},
		},
		all: []*ReflectionProxy{{addr: "127.0.0.1:9001"}},
	})

	if got := pool.Get(); got == nil || got.addr != "127.0.0.1:9001" {
		t.Fatalf("pool.Get() = %#v, want billing proxy", got)
	}

	allocs := testing.AllocsPerRun(1000, func() {
		_ = pool.Get()
	})
	if allocs != 0 {
		t.Fatalf("ServicePool.Get allocations = %v, want 0", allocs)
	}
}

func TestProxyLookupDebugStateReportsEmptyPoolAndMissingDiscovery(t *testing.T) {
	gw := &EtcdGateway{connPool: NewConnectionPool()}

	got := gw.proxyLookupDebugState("auth")
	for _, want := range []string{
		"service=auth",
		"pool=missing",
		"discovery=disabled",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("debug state = %q, want to contain %q", got, want)
		}
	}
}

func TestCircuitBreakerHalfOpenFailureReopensCircuit(t *testing.T) {
	gw := &EtcdGateway{}
	gw.circuitStates.Store("billing-service", &CircuitBreaker{state: circuitHalfOpen})

	gw.circuitBreakerRecordFailure("billing-service")

	value, ok := gw.circuitStates.Load("billing-service")
	if !ok {
		t.Fatal("missing circuit breaker")
	}
	cb := value.(*CircuitBreaker)
	if cb.state != circuitOpen {
		t.Fatalf("circuit state = %d, want open", cb.state)
	}
	if gw.circuitBreakerCheck("billing-service") {
		t.Fatal("open circuit should reject requests during cooldown")
	}
}

func TestSetHTTPProxyTargetOnlyChangesDestination(t *testing.T) {
	target, err := parseHTTPServiceURL("http://127.0.0.1:8081")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/a%2Bb?tag=z&tag=a", nil)
	req.Header["X-Multi"] = []string{"one", "two"}
	wantPath := req.URL.Path
	wantRawPath := req.URL.RawPath
	wantRawQuery := req.URL.RawQuery

	setHTTPProxyTarget(req, target)

	if req.URL.Scheme != "http" || req.URL.Host != "127.0.0.1:8081" {
		t.Fatalf("target = %s://%s, want http://127.0.0.1:8081", req.URL.Scheme, req.URL.Host)
	}
	if req.URL.Path != wantPath || req.URL.RawPath != wantRawPath {
		t.Fatalf("path = %q raw_path=%q, want unchanged %q %q", req.URL.Path, req.URL.RawPath, wantPath, wantRawPath)
	}
	if req.URL.RawQuery != wantRawQuery {
		t.Fatalf("query = %q, want unchanged %q", req.URL.RawQuery, wantRawQuery)
	}
	if got := req.Header.Values("X-Multi"); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("X-Multi = %#v, want unchanged", got)
	}
}
