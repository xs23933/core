package gateway

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	core "github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/etcd"
	gatewayroute "github.com/xs23933/core/v3/gateway/route"
)

// TestServiceCapabilityDecoding 只按显式能力字段排除 gRPC，HTTP 地址不改变旧注册语义。
func TestServiceCapabilityDecoding(t *testing.T) {
	for _, test := range []struct{ capability, grpc string }{{"", "127.0.0.1:9000"}, {"true", "127.0.0.1:9000"}, {"false", ""}} {
		value, _ := json.Marshal(serviceInstance{Addr: "127.0.0.1:9000", Metadata: map[string]string{etcd.MetadataGRPCEnabled: test.capability, "http_addr": "http://127.0.0.1:9001"}})
		instance, ok := decodeServiceSnapshotValue("/services/", "/services/demo/one", value)
		if !ok || instance.GRPCAddr != test.grpc || instance.HTTPAddr != "http://127.0.0.1:9001" || instance.HTTPOnly != (test.capability == "false") {
			t.Fatalf("decoded capability %q: %+v", test.capability, instance)
		}
	}
}

// TestGRPCToHTTPOnlyRemovesReflectionPool 验证能力更新撤销旧连接，同时保留 HTTP 上游。
func TestGRPCToHTTPOnlyRemovesReflectionPool(t *testing.T) {
	var calls atomic.Int32
	gw := newLifecycleTestGateway(func(_ context.Context, _ *core.Core, _, addr string, _ *ReflectionSchema) (*ReflectionProxy, error) {
		calls.Add(1)
		return lifecycleTestProxy(addr, &ReflectionSchema{methods: map[string]*MethodDescriptor{"/api.v1.DemoService/GetItem": {Package: "api.v1", Service: "api.v1.DemoService", Method: "GetItem"}}}, nil), nil
	})
	defer gw.Close()
	instance := registeredServiceInstance{ServiceName: "demo", InstanceID: "one", GRPCAddr: "grpc", HTTPAddr: "http://127.0.0.1:9001"}
	gw.reconcileServiceSnapshot(serviceSnapshot{"demo/one": instance})
	eventuallyGateway(t, func() bool {
		return gw.connPool.Get("demo").Size() == 1 && len(gw.loadRouteSnapshot().activeSlots) == 1
	})
	if len(gw.loadRouteSnapshot().activeSlots) != 1 {
		t.Fatal("gRPC route was not installed")
	}
	manual := minimalRouteStateDefinition("manual", gatewayroute.SourceManual, "", "demo", "/manual")
	automaticHTTP := minimalRouteStateDefinition("http", gatewayroute.SourceAutoHTTP, "demo", "demo", "/http")
	gw.applyRouteBatch([]routeEvent{{StorageKey: "manual", Value: &routeSourceValue{Definition: manual}}, {StorageKey: "http", Value: &routeSourceValue{Definition: automaticHTTP}}})
	instance.GRPCAddr = ""
	instance.HTTPOnly = true
	gw.reconcileServiceSnapshot(serviceSnapshot{"demo/one": instance})
	gw.taskWG.Wait()
	if pool := gw.connPool.Get("demo"); pool != nil && pool.Size() != 0 {
		t.Fatal("HTTP-only retained gRPC proxy")
	}
	if len(gw.loadRouteSnapshot().activeSlots) != 2 {
		t.Fatal("HTTP-only did not preserve exactly manual and HTTP routes")
	}
	if gw.getHTTPInstance("demo") == nil {
		t.Fatal("HTTP-only lost HTTP origin")
	}
	if calls.Load() != 1 {
		t.Fatalf("reflection attempts=%d", calls.Load())
	}
}
