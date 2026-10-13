package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xs23933/core/v3/workload"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func managedTestIssuer(t *testing.T) (Options, string) {
	t.Helper()
	dir := t.TempDir()
	token := "test-bootstrap-token-with-more-than-thirty-two-bytes"
	sum := sha256.Sum256([]byte(token))
	policy, _ := json.Marshal(workload.EnrollmentPolicy{Services: map[string]workload.EnrollmentBinding{"backend": {TokenSHA256: hex.EncodeToString(sum[:])}}})
	if err := os.WriteFile(filepath.Join(dir, "policy.json"), policy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		"workload":        Options{"enabled": true, "service_name": "gateway", "trust_domain": "test.local", "state_dir": filepath.Join(dir, "gateway"), "issuer_url": "https://127.0.0.1:9444", "trust_bundle_file": filepath.Join(dir, "issuer", "root.pem")},
		"workload_issuer": Options{"enabled": true, "state_dir": filepath.Join(dir, "issuer"), "listen": "127.0.0.1:0", "advertise_url": "https://127.0.0.1:9444", "policy_file": filepath.Join(dir, "policy.json"), "pin_file": filepath.Join(dir, "root.pin")},
	}
	return opts, dir
}

func TestManagedWorkloadCoreTransport(t *testing.T) {
	opts, dir := managedTestIssuer(t)
	app := New(opts)
	if err := app.EnableWorkloadIdentity(); err != nil {
		t.Fatal(err)
	}
	defer app.ShutdownWithTimeout(time.Second)
	issuer := app.workload.issuer
	backend, err := workload.OpenLocal(context.Background(), workload.Config{ServiceName: "backend", TrustDomain: "test.local", StateDir: filepath.Join(dir, "backend"), TrustBundleFile: filepath.Join(dir, "issuer", "root.pem")}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(backend.ServerCredentials()))
	healthpb.RegisterHealthServer(server, health.NewServer())
	go server.Serve(listener)
	defer server.Stop()
	conn, err := app.GrpcClientAt("backend", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}
	wrong, err := app.GrpcClientAt("other", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	badctx, badcancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer badcancel()
	if _, err := healthpb.NewHealthClient(wrong).Check(badctx, &healthpb.HealthCheckRequest{}); err == nil {
		t.Fatal("wrong service identity accepted")
	}
	if err := app.ConfigureGRPCClient("backend", GRPCClientConfig{TransportCredentials: backend.ClientCredentials("backend")}); err == nil {
		t.Fatal("explicit managed transport accepted")
	}
}

func TestManagedWorkloadFailClosedBeforeInitialization(t *testing.T) {
	app := New(Options{"workload": Options{"enabled": true}})
	if _, err := app.GrpcClientAt("backend", "127.0.0.1:1"); err == nil {
		t.Fatal("uninitialized client accepted")
	}
	if err := app.ConfigureGRPCServer(GRPCServerConfig{}); err != nil {
		t.Fatal(err)
	}
	app.RegisterGRPCService(func(*grpc.Server) {})
	if err := app.startGRPCServer(); err == nil {
		t.Fatal("uninitialized server accepted")
	}
	if err := app.EnableWorkloadIdentity(); err == nil {
		t.Fatal("late identity initialization accepted")
	}
}

func TestManagedWorkloadPreservesInterceptorsAndRejectsSharedPort(t *testing.T) {
	opts, _ := managedTestIssuer(t)
	app := New(opts)
	if err := app.EnableWorkloadIdentity(); err != nil {
		t.Fatal(err)
	}
	defer app.ShutdownWithTimeout(time.Second)
	if err := app.ConfigureGRPCServer(GRPCServerConfig{UnaryInterceptors: []grpc.UnaryServerInterceptor{func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		return h(ctx, req)
	}}}); err != nil {
		t.Fatal(err)
	}
	app.RegisterGRPCService(func(s *grpc.Server) { healthpb.RegisterHealthServer(s, health.NewServer()) })
	if err := app.startGRPCServer(); err != ErrGRPCTLSSharedAddress {
		t.Fatalf("shared port: %v", err)
	}
}
