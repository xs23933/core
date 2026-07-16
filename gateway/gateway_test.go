package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type capturedRoutePut struct {
	key   string
	value any
}

type captureRouteStore struct {
	puts []capturedRoutePut
}

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

	config = &Config{Namespace: "xpay", RoutePrefix: "/custom/routes/"}
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

func (s *captureRouteStore) Put(_ context.Context, key string, value any) error {
	s.puts = append(s.puts, capturedRoutePut{key: key, value: value})
	return nil
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
	gw.storeRoutes(make(map[string]*Route))
	gw.storeHTTPInstances(make(map[string]httpServiceInstances))
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

func TestPublishHTTPRouteUsesStableKey(t *testing.T) {
	store := &captureRouteStore{}
	route := &Route{
		Method:       http.MethodGet,
		Path:         "/trace.js",
		ServiceName:  "analytics",
		UpstreamPath: "/trace.js",
	}

	if err := publishHTTPRoute(context.Background(), store, "/gateway/routes/", route); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if err := publishHTTPRoute(context.Background(), store, "/gateway/routes/", route); err != nil {
		t.Fatalf("second publish: %v", err)
	}

	if len(store.puts) != 2 {
		t.Fatalf("puts = %d, want 2", len(store.puts))
	}
	if store.puts[0].key == "" || store.puts[0].key != store.puts[1].key {
		t.Fatalf("route keys = %q, %q; want same non-empty key", store.puts[0].key, store.puts[1].key)
	}
}

func TestHTTPRouteRegisteredButMissingInstanceReturnsUnavailable(t *testing.T) {
	app := core.New()
	gw := &EtcdGateway{}
	gw.app = app
	gw.connPool = NewConnectionPool()
	gw.storeRoutes(make(map[string]*Route))
	gw.storeHTTPInstances(make(map[string]httpServiceInstances))
	gw.storeHTTPIndexes(make(map[string]*atomic.Uint64))

	gw.addOrUpdateRoute(&Route{
		Protocol:     RouteProtocolHTTP,
		Method:       http.MethodGet,
		Path:         "/trace.js",
		ServiceName:  "analytics",
		UpstreamPath: "/trace.js",
		Enabled:      true,
	})

	req := httptest.NewRequest(http.MethodGet, "/trace.js", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; route should match before upstream lookup", rec.Code)
	}
}

func TestRemovingOldRouteIDKeepsReplacementForSameMethodPath(t *testing.T) {
	app := core.New()
	gw := &EtcdGateway{}
	gw.app = app
	gw.connPool = NewConnectionPool()
	gw.storeRoutes(make(map[string]*Route))
	gw.storeHTTPInstances(make(map[string]httpServiceInstances))
	gw.storeHTTPIndexes(make(map[string]*atomic.Uint64))

	gw.addOrUpdateRoute(&Route{
		ID:           "old-trace-route",
		Protocol:     RouteProtocolHTTP,
		Method:       http.MethodGet,
		Path:         "/trace.js",
		ServiceName:  "analytics",
		UpstreamPath: "/old-trace.js",
		Enabled:      true,
	})
	gw.addOrUpdateRoute(&Route{
		ID:           "new-trace-route",
		Protocol:     RouteProtocolHTTP,
		Method:       http.MethodGet,
		Path:         "/trace.js",
		ServiceName:  "analytics",
		UpstreamPath: "/trace.js",
		Enabled:      true,
	})

	gw.removeRouteByID("old-trace-route")

	req := httptest.NewRequest(http.MethodGet, "/trace.js", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; replacement route should remain matched", rec.Code)
	}
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
	gw.storeHTTPInstances(make(map[string]httpServiceInstances))
	gw.addHTTPInstance("task-service", "task-2", "127.0.0.1:8082")
	gw.addHTTPInstance("task-service", "task-1", "127.0.0.1:8081")

	first := gw.getHTTPInstance("task-service")
	second := gw.getHTTPInstance("task-service")
	if first == "" || second == "" || first == second {
		t.Fatalf("round-robin instances = %q, %q; want two non-empty different addresses", first, second)
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
	gw.storeHTTPInstances(make(map[string]httpServiceInstances))
	gw.addHTTPInstance("task-service", "task-1", "127.0.0.1:8081")
	gw.addHTTPInstance("task-service", "task-2", "127.0.0.1:8082")

	gw.removeHTTPInstance("task-service", "task-1")

	for i := 0; i < 4; i++ {
		if got := gw.getHTTPInstance("task-service"); got != "127.0.0.1:8082" {
			t.Fatalf("instance after removal = %q, want remaining address", got)
		}
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

func TestConnectInstanceGuardSuppressesDuplicateInFlightAttempts(t *testing.T) {
	gw := &EtcdGateway{}

	key, ok := gw.beginConnectInstance("billing-service", "billing-1")
	if !ok {
		t.Fatal("first connect attempt should start")
	}
	if _, ok := gw.beginConnectInstance("billing-service", "billing-1"); ok {
		t.Fatal("duplicate in-flight connect attempt should be suppressed")
	}

	gw.finishConnectInstance(key)
	if _, ok := gw.beginConnectInstance("billing-service", "billing-1"); !ok {
		t.Fatal("connect attempt should be allowed after previous attempt finishes")
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

func TestPrepareHTTPProxyRequestRewritesTargetPathAndHeaders(t *testing.T) {
	target, err := parseHTTPServiceURL("http://127.0.0.1:8081/base")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/42?debug=1", nil)
	route := &Route{
		UpstreamPath: "/tasks/:id",
		Headers:      map[string]string{"X-Gateway": "core"},
	}

	prepareHTTPProxyRequest(req, target, route, map[string]string{"id": "42"})

	if req.URL.Scheme != "http" || req.URL.Host != "127.0.0.1:8081" {
		t.Fatalf("target = %s://%s, want http://127.0.0.1:8081", req.URL.Scheme, req.URL.Host)
	}
	if req.URL.Path != "/base/tasks/42" {
		t.Fatalf("path = %q, want /base/tasks/42", req.URL.Path)
	}
	if req.URL.RawQuery != "debug=1" {
		t.Fatalf("query = %q, want debug=1", req.URL.RawQuery)
	}
	if got := req.Header.Get("X-Gateway"); got != "core" {
		t.Fatalf("X-Gateway = %q, want core", got)
	}
}
