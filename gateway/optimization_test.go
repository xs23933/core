package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/gateway/route"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestGatewayRejectsMalformedJSONBeforeRPC(t *testing.T) {
	var calls atomic.Int32
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, &responseMetadataServer{write: func(context.Context, *healthpb.HealthCheckRequest) error { calls.Add(1); return nil }})
	app := metadataTestGateway(t, metadataTestProxy(t, srv))
	for _, body := range []string{`{"service":`, `{"service":"ok"} trailing`, `[1]`, `"value"`} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest("POST", "/check", strings.NewReader(body)))
		if rec.Code != 400 || calls.Load() != 0 {
			t.Fatalf("body=%q status=%d upstream_calls=%d", body, rec.Code, calls.Load())
		}
	}
	// Empty bodies, null and objects retain their existing request semantics.
	for _, body := range []string{"", `null`, `{}`, `{"service":"ok"}`} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest("POST", "/check", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("body=%q status=%d", body, rec.Code)
		}
	}
	if calls.Load() != 4 {
		t.Fatal(calls.Load())
	}
}

func TestHTTPProxyBodyReplay(t *testing.T) {
	for _, mode := range []string{"unreplayable", "replayable", "replay-error"} {
		t.Run(mode, func(t *testing.T) {
			firstBody := make(chan string, 1)
			secondBody := make(chan string, 1)
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				firstBody <- string(data)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				if err := conn.Close(); err != nil {
					t.Error(err)
				}
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				secondBody <- string(data)
				_, err = w.Write([]byte("ok"))
				if err != nil {
					t.Error(err)
				}
			}))
			defer second.Close()
			app := core.New(core.Options{"debug": false})
			gw := &EtcdGateway{app: app, connPool: NewConnectionPool()}
			for id, addr := range map[string]string{"a": first.URL, "b": second.URL} {
				if err := gw.addHTTPInstance("svc", id, addr); err != nil {
					t.Fatal(err)
				}
			}
			app.GET("/", gw.createHTTPProxyHandler(&Route{ServiceName: "svc"}))
			req := httptest.NewRequest("GET", "/", strings.NewReader("payload"))
			if mode == "replayable" {
				req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("payload")), nil }
			}
			if mode == "replay-error" {
				req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("replay failed") }
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if got := <-firstBody; got != "payload" {
				t.Fatal(got)
			}
			if mode == "replayable" {
				if rec.Code != 200 {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				if got := <-secondBody; got != "payload" {
					t.Fatal(got)
				}
			} else {
				if rec.Code != 502 {
					t.Fatalf("status=%d", rec.Code)
				}
				select {
				case body := <-secondBody:
					t.Fatalf("unsafe retry reached alternate: %q", body)
				default:
				}
			}
		})
	}
}

func TestGatewayRouteReplacementNeverDisappears(t *testing.T) {
	app := core.New(core.Options{"debug": false})
	gw := &EtcdGateway{app: app, connPool: NewConnectionPool()}
	a := &Route{Method: "GET", Path: "/swap", Protocol: RouteProtocolHTTP, ServiceName: "a", Enabled: true}
	b := &Route{Method: "GET", Path: "/swap", Protocol: RouteProtocolHTTP, ServiceName: "b", Enabled: true}
	gw.registerRoute(a)
	stop := make(chan struct{})
	done := make(chan struct{})
	ready := make(chan struct{})
	missing := make(chan int, 1)
	go func() {
		defer close(done)
		close(ready)
		for {
			select {
			case <-stop:
				return
			default:
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest("GET", "/swap", nil))
			if rec.Code != 503 {
				select {
				case missing <- rec.Code:
				default:
				}
				return
			}
		}
	}()
	<-ready
	for i := 0; i < 1000; i++ {
		gw.applyActiveRouteChanges([]activeRouteChange{{Old: a, New: b}})
		a, b = b, a
	}
	close(stop)
	<-done
	select {
	case code := <-missing:
		t.Fatalf("route replacement returned %d, want 503", code)
	default:
	}
}

func TestHTTPReconcileReusesUnchangedState(t *testing.T) {
	gw := &EtcdGateway{connPool: NewConnectionPool()}
	gw.initializeLifecycle()
	defer gw.Close()
	desired := serviceSnapshot{
		"a/1": {ServiceName: "a", InstanceID: "1", HTTPAddr: "http://127.0.0.1:8081", HTTPOnly: true},
		"a/2": {ServiceName: "a", InstanceID: "2", HTTPAddr: "http://127.0.0.1:8082", HTTPOnly: true},
		"b/1": {ServiceName: "b", InstanceID: "1", HTTPAddr: "http://127.0.0.1:8083", HTTPOnly: true},
	}
	gw.reconcileServiceSnapshot(desired)
	old := gw.loadHTTPInstances()
	index := gw.loadHTTPIndexes()["a"]
	gw.reconcileServiceSnapshot(desired)
	if gw.loadHTTPInstances()["a"] != old["a"] || gw.loadHTTPInstances()["b"] != old["b"] {
		t.Fatal("unchanged service snapshot rebuilt")
	}
	changed := desired["a/2"]
	changed.HTTPAddr = "http://127.0.0.1:8084"
	desired["a/2"] = changed
	gw.reconcileServiceSnapshot(desired)
	next := gw.loadHTTPInstances()
	if next["a"] == old["a"] || next["a"].byID["1"] != old["a"].byID["1"] || next["b"] != old["b"] {
		t.Fatal("changed snapshot did not preserve unaffected instances")
	}
	if old["a"].byID["2"].Addr != "http://127.0.0.1:8082" || next["a"].instances[0].ID != "1" || gw.loadHTTPIndexes()["a"] != index {
		t.Fatal("immutability, ordering or round robin index changed")
	}
	changed.NotReady = true
	desired["a/2"] = changed
	delete(desired, "b/1")
	gw.reconcileServiceSnapshot(desired)
	if len(gw.loadHTTPInstances()["a"].instances) != 1 || gw.loadHTTPInstances()["b"] != nil || gw.loadHTTPIndexes()["b"] != nil {
		t.Fatal("readiness/delete not reconciled")
	}
	changed = desired["a/1"]
	changed.HTTPAddr = "http://bad/path"
	desired["a/1"] = changed
	gw.reconcileServiceSnapshot(desired)
	if gw.getHTTPInstance("a") != nil {
		t.Fatal("invalid replacement retained old address")
	}
}

func TestRouteSnapshotReuseAndOwnership(t *testing.T) {
	a := minimalRouteStateDefinition("a", route.SourceManual, "", "a", "/a")
	b := minimalRouteStateDefinition("b", route.SourceManual, "", "b", "/b")
	s := newRouteSnapshot(map[string]*routeSourceValue{"a": {Definition: a}, "b": {Definition: b}})
	same := s.withRouteBatch([]routeEvent{{StorageKey: "a", Value: &routeSourceValue{Definition: cloneRouteDefinition(a)}}})
	if same.sources["a"] != s.sources["a"] {
		t.Fatal("identical event cloned source")
	}
	changed := cloneRouteDefinition(a)
	changed.ServiceName = "new"
	next := s.withRouteBatch([]routeEvent{{StorageKey: "a", Value: &routeSourceValue{Definition: changed}}})
	changed.ServiceName = "caller-mutated"
	if next.sources["a"].Definition.ServiceName != "new" || s.sources["a"].Definition.ServiceName != "a" || next.sources["b"] != s.sources["b"] {
		t.Fatal("snapshot ownership or sharing broken")
	}
	changes := activeRouteChanges(s, next)
	if len(changes) != 1 || changes[0].Old.ServiceName != "a" || changes[0].New.ServiceName != "new" {
		t.Fatal(changes)
	}
	removed := next.withRouteBatch([]routeEvent{{StorageKey: "a", Deleted: true}})
	if removed.sources["a"] != nil || next.sources["a"] == nil {
		t.Fatal("delete mutated old snapshot")
	}
}

func BenchmarkGatewaySimpleJSONDecode(b *testing.B) {
	data := []byte(`{"service":"` + strings.Repeat("x", 1024) + `"}`)
	b.Run("Gateway", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := protoJSONUnmarshal(data, new(healthpb.HealthCheckRequest), nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ProtoJSON", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := protojson.Unmarshal(data, new(healthpb.HealthCheckRequest)); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkGatewayRouteUpdate(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			values := map[string]*routeSourceValue{}
			for i := 0; i < n; i++ {
				r := &Route{Source: route.SourceManual, Protocol: RouteProtocolHTTP, Method: "GET", Path: fmt.Sprintf("/r%d", i), ServiceName: "svc", Enabled: true}
				if err := normalizeAndValidateRoute(r); err != nil {
					b.Fatal(err)
				}
				values[fmt.Sprint(i)] = &routeSourceValue{Definition: r}
			}
			s := newRouteSnapshot(values)
			for _, changed := range []bool{false, true} {
				b.Run(fmt.Sprintf("changed=%t", changed), func(b *testing.B) {
					value := cloneRouteSourceValue(values["0"])
					if changed {
						value.Definition.ServiceName = "new"
					}
					events := []routeEvent{{StorageKey: "0", Value: value}}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						_ = s.withRouteBatch(events)
					}
				})
			}
		})
	}
}

func BenchmarkGatewayServiceReconcile(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			gw := &EtcdGateway{connPool: NewConnectionPool()}
			snapshot := serviceSnapshot{}
			for i := 0; i < n; i++ {
				id := fmt.Sprint(i)
				snapshot["svc/"+id] = registeredServiceInstance{ServiceName: "svc", InstanceID: id, HTTPAddr: "http://127.0.0.1:8081", HTTPOnly: true}
			}
			gw.reconcileServiceSnapshot(snapshot)
			defer gw.Close()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				gw.reconcileServiceSnapshot(snapshot)
			}
		})
	}
}

func TestSimpleJSONDecodeAllocationBudget(t *testing.T) {
	data := []byte(`{"service":"` + strings.Repeat("x", 1024) + `"}`)
	direct := testing.AllocsPerRun(100, func() {
		if err := protojson.Unmarshal(data, new(healthpb.HealthCheckRequest)); err != nil {
			t.Fatal(err)
		}
	})
	wrapped := testing.AllocsPerRun(100, func() {
		if err := protoJSONUnmarshal(data, new(healthpb.HealthCheckRequest), nil); err != nil {
			t.Fatal(err)
		}
	})
	// Simple schemas should add only small dispatch overhead, not two JSON maps.
	if wrapped > direct+8 {
		t.Fatalf("simple decode allocations=%g, direct=%g", wrapped, direct)
	}
}

func TestHTTPReconcileResetsRetiredServiceState(t *testing.T) {
	gw := &EtcdGateway{connPool: NewConnectionPool()}
	gw.initializeLifecycle()
	defer gw.Close()
	desired := serviceSnapshot{
		"svc/1": {ServiceName: "svc", InstanceID: "1", HTTPAddr: "http://127.0.0.1:8081"},
		"svc/2": {ServiceName: "svc", InstanceID: "2", HTTPAddr: "http://127.0.0.1:8082"},
	}
	gw.reconcileHTTPInstances(desired)
	trip := func() {
		for i := 0; i < circuitFailThreshold; i++ {
			gw.circuitBreakerRecordFailure("svc")
		}
		if gw.circuitBreakerCheck("svc") {
			t.Fatal("circuit did not open")
		}
	}
	trip()
	index := gw.loadHTTPIndexes()["svc"]
	delete(desired, "svc/1")
	gw.reconcileHTTPInstances(desired)
	if gw.circuitBreakerCheck("svc") || gw.loadHTTPIndexes()["svc"] != index {
		t.Fatal("remaining instance lost circuit/index state")
	}
	delete(desired, "svc/2")
	gw.reconcileHTTPInstances(desired)
	if _, ok := gw.circuitStates.Load("svc"); ok || gw.loadHTTPIndexes()["svc"] != nil {
		t.Fatal("retired service state retained")
	}
	desired["svc/3"] = registeredServiceInstance{ServiceName: "svc", InstanceID: "3", HTTPAddr: "http://127.0.0.1:8083"}
	gw.reconcileHTTPInstances(desired)
	if !gw.circuitBreakerCheck("svc") {
		t.Fatal("replacement inherited open circuit")
	}
	trip()
	index = gw.loadHTTPIndexes()["svc"]
	delete(desired, "svc/3")
	desired["svc/4"] = registeredServiceInstance{ServiceName: "svc", InstanceID: "4", HTTPAddr: "http://127.0.0.1:8084"}
	gw.reconcileHTTPInstances(desired)
	if !gw.circuitBreakerCheck("svc") || gw.loadHTTPIndexes()["svc"] == index {
		t.Fatal("complete ID replacement retained retired service state")
	}
}
