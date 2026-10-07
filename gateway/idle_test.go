package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Real reflection, an installed auto route and a real ClientConn exercise the
// first HTTP request after idle. No extra registration event or HTTP retry is
// allowed to hide a failed wake-up.
func TestGatewayFirstRequestWakesIdleConnection(t *testing.T) {
	address := reserveGatewayURL(t).Host
	startRestartRPC(t, address, "idle-resumed", []string{"GetSummary"}, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	var connections atomic.Int32
	gw := newLifecycleTestGateway(func(ctx context.Context, app *core.Core, _ string, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
		connections.Add(1)
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithIdleTimeout(50*time.Millisecond))
		if err != nil {
			return nil, err
		}
		return newReflectionProxyFromConnContext(ctx, app, addr, conn, schema)
	})
	t.Cleanup(func() { _ = gw.Close(); _ = gw.app.ShutdownWithTimeout(time.Second) })
	gw.reconcileServiceSnapshot(serviceSnapshot{"demo/a": {ServiceName: "demo", InstanceID: "a", GRPCAddr: address}})
	gw.taskWG.Wait()
	pool := gw.connPool.Get("demo")
	if pool == nil || pool.Size() != 1 {
		t.Fatal("reflection did not install the desired instance")
	}
	proxy := pool.loadState().byID["a"]
	if gw.app.EtcdDiscovery != nil {
		t.Fatal("test must wake the installed pool independently of fallback discovery")
	}
	for cycle := range 3 {
		eventuallyGateway(t, func() bool { return proxy.conn.GetState() == connectivity.Idle })
		rec := httptest.NewRecorder()
		gw.app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/restart/summary", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("first request after idle cycle %d: status=%d body=%s", cycle, rec.Code, rec.Body.String())
		}
		var body struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Value != "idle-resumed" {
			t.Fatalf("idle response: value=%q err=%v", body.Value, err)
		}
		if connections.Load() != 1 || pool.loadState().byID["a"] != proxy {
			t.Fatal("idle wake-up replaced or reflected the installed connection")
		}
	}
}

func idleSelectionTestPool(proxies ...*ReflectionProxy) *ServicePool {
	pool := NewServicePool(nil, "idle-test")
	pool.storeState(servicePoolState{
		byID:    map[string]*ReflectionProxy{},
		all:     proxies,
		methods: map[string][]*ReflectionProxy{"rpc": proxies},
	})
	return pool
}

func idleSelectionTestConn(t *testing.T, options ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	options = append(options, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithIdleTimeout(0))
	conn, err := grpc.NewClient("passthrough:///idle-test", options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestServicePoolMethodSelectionIdleBoundaries(t *testing.T) {
	idle := &ReflectionProxy{conn: idleSelectionTestConn(t)}
	ready := &ReflectionProxy{conn: &grpc.ClientConn{}, ready: func() bool { return true }}
	notReadyMock := &ReflectionProxy{conn: &grpc.ClientConn{}, ready: func() bool { return false }}
	pool := idleSelectionTestPool(idle, ready, notReadyMock)
	for range 6 {
		if got := pool.GetForMethod("rpc", nil); got != ready {
			t.Fatal("Ready must take priority over Idle")
		}
	}
	if got := pool.GetForMethod("rpc", ready); got != nil {
		t.Fatal("alternate retry must not select Idle or a not-ready mock")
	}
	if got := pool.GetForMethod("rpc", idle); got != ready {
		t.Fatal("alternate retry must retain a Ready candidate")
	}
	pool = idleSelectionTestPool(notReadyMock, idle)
	if got := pool.GetForMethod("rpc", nil); got != idle {
		t.Fatal("first request should select Idle without inspecting the mock conn")
	}
	if got := pool.GetForMethod("missing", nil); got != nil {
		t.Fatal("Idle must not bypass method eligibility")
	}
	if got := pool.GetForMethod("rpc", idle); got != nil {
		t.Fatal("excluded Idle must not be selected")
	}
	if idle.conn.GetState() != connectivity.Idle {
		t.Fatal("pool selection must not actively connect")
	}
	if got := idleSelectionTestPool(notReadyMock).GetForMethod("rpc", nil); got != nil {
		t.Fatal("readiness hook must override the mock ClientConn")
	}
}

func TestServicePoolMethodSelectionRejectsFailedAndClosedConnections(t *testing.T) {
	t.Run("shutdown", func(t *testing.T) {
		conn := idleSelectionTestConn(t)
		_ = conn.Close()
		if conn.GetState() != connectivity.Shutdown {
			t.Fatal("closed connection did not enter Shutdown")
		}
		if got := idleSelectionTestPool(&ReflectionProxy{conn: conn}).GetForMethod("rpc", nil); got != nil {
			t.Fatal("Shutdown connection selected")
		}
	})
	t.Run("transient-failure", func(t *testing.T) {
		conn := idleSelectionTestConn(t, grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return nil, errors.New("test connection unavailable") }))
		conn.Connect()
		eventuallyGateway(t, func() bool { return conn.GetState() == connectivity.TransientFailure })
		if got := idleSelectionTestPool(&ReflectionProxy{conn: conn}).GetForMethod("rpc", nil); got != nil {
			t.Fatal("TransientFailure connection selected")
		}
	})
	t.Run("connecting", func(t *testing.T) {
		conn := idleSelectionTestConn(t, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }))
		conn.Connect()
		eventuallyGateway(t, func() bool { return conn.GetState() == connectivity.Connecting })
		if got := idleSelectionTestPool(&ReflectionProxy{conn: conn}).GetForMethod("rpc", nil); got != nil {
			t.Fatal("Connecting connection selected")
		}
	})
}

func TestServicePoolMethodSelectionDoesNotAllocate(t *testing.T) {
	idle := &ReflectionProxy{conn: idleSelectionTestConn(t)}
	ready := &ReflectionProxy{ready: func() bool { return true }}
	notReady := &ReflectionProxy{conn: &grpc.ClientConn{}, ready: func() bool { return false }}
	for _, test := range []struct {
		name     string
		pool     *ServicePool
		excluded *ReflectionProxy
		want     *ReflectionProxy
	}{
		{"ready-preferred", idleSelectionTestPool(idle, ready), nil, ready},
		{"idle-first-request", idleSelectionTestPool(notReady, idle), nil, idle},
		{"ready-alternate", idleSelectionTestPool(idle, ready), idle, ready},
		{"no-ready-alternate", idleSelectionTestPool(idle, ready), ready, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.pool.GetForMethod("rpc", test.excluded); got != test.want {
				t.Fatal("unexpected candidate")
			}
			if allocs := testing.AllocsPerRun(1000, func() { _ = test.pool.GetForMethod("rpc", test.excluded) }); allocs != 0 {
				t.Fatalf("method selection allocations=%v, want 0", allocs)
			}
		})
	}
}
