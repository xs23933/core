package core

import (
	"context"
	"testing"
	"time"

	"github.com/xs23933/core/v3/etcd"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestGRPCDiscoverySkipsUnreadyInstance(t *testing.T) {
	endpoint := startAutomaticHTTPRouteEtcd(t)
	address := startClientPlaintextTestServer(t)
	app := New(Options{"debug": false})
	if err := app.EnableEtcdDiscovery(&etcd.Options{Endpoints: []string{endpoint}, DialTimeout: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.shutdown)
	ready := false
	r, err := etcd.NewRegistry(&etcd.Options{Endpoints: []string{endpoint}, DialTimeout: 3 * time.Second, ServiceName: "backend", ServiceID: "ready", ServiceAddr: address, TTL: 5, RegistrationReady: &ready})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Register(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Deregister() })
	conn, err := app.GrpcClient("backend")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
	cancel()
	if err == nil {
		t.Fatal("unready instance entered load balancing")
	}
	if err := r.SetReady(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("ready instance unavailable: %v", err)
	}
}
