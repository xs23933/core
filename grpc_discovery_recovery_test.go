package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xs23933/core/v3/etcd"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestGRPCDiscoveryConnectionIsolationAndRegistryReuse(t *testing.T) {
	endpoint := startAutomaticHTTPRouteEtcd(t)
	address := startClientPlaintextTestServer(t)
	newApp := func(namespace string) *Core {
		app := New(Options{"debug": false})
		if err := app.EnableEtcdDiscovery(&etcd.Options{Namespace: namespace, Endpoints: []string{endpoint}, DialTimeout: 3 * time.Second}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(app.shutdown)
		return app
	}
	appA, appB := newApp("client-a"), newApp("client-b")
	discovery := appB.EtcdDiscovery
	if err := appB.EnableEtcdDiscovery(&etcd.Options{Namespace: "client-b", Endpoints: []string{endpoint}, DialTimeout: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := appB.EnableEtcdRegistry(&etcd.Options{Namespace: "client-b", Endpoints: []string{endpoint}, DialTimeout: 3 * time.Second, ServiceName: "caller", ServiceID: "self", ServiceAddr: address, TTL: 5}); err != nil {
		t.Fatal(err)
	}
	if appB.EtcdDiscovery != discovery {
		t.Fatal("registry retired existing client discovery")
	}
	if err := appB.EnableEtcdDiscovery(&etcd.Options{Namespace: "different", Endpoints: []string{endpoint}, DialTimeout: 3 * time.Second}); !errors.Is(err, ErrEtcdDiscoveryConfigurationChanged) {
		t.Fatalf("changed configuration=%v", err)
	}
	connA, err := appA.GrpcClient("backend")
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()
	connB, err := appB.GrpcClient("backend")
	if err != nil {
		t.Fatal(err)
	}
	defer connB.Close()
	connA.Connect()
	connB.Connect()
	register := func(namespace string) *etcd.Registry {
		r, err := etcd.NewRegistry(&etcd.Options{Namespace: namespace, Endpoints: []string{endpoint}, DialTimeout: 3 * time.Second, ServiceName: "backend", ServiceID: "fixed", ServiceAddr: address, TTL: 5})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Register(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Deregister() })
		return r
	}
	register("client-b")
	check := func(conn *grpc.ClientConn) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true)); err != nil {
			t.Fatal(err)
		}
	}
	check(connB)
	appA.shutdown()
	register("client-b") // fixed-key replacement remains discoverable.
	check(connB)
	// namespace A has no backend; it must not inherit namespace B's addresses.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := healthpb.NewHealthClient(connA).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true)); err == nil {
		t.Fatal("client A used another Core's discovery")
	}
}
