package core

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xs23933/core/v3/workload"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestManagedWorkloadDirectServeBeforeInitRejectsPlaintext(t *testing.T) {
	app := New(Options{"workload": Options{"enabled": true}})
	server := app.GetGRPCServer()
	healthpb.RegisterHealthServer(server, health.NewServer())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err == nil {
		t.Fatal("uninitialized managed server accepted plaintext via direct Serve")
	}
}

func TestManagedWorkloadLateInitializationResultIsClosed(t *testing.T) {
	opts, dir := managedTestIssuer(t)
	gateway := New(opts)
	if err := gateway.EnableWorkloadIdentity(); err != nil {
		t.Fatal(err)
	}
	defer gateway.ShutdownWithTimeout(time.Second)
	issuer := gateway.workload.issuer
	var app *Core
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Service   string `json:"service"`
			CSRPEM    string `json:"csr_pem"`
			RequestID string `json:"request_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		cert, err := issuer.IssueLocal(req.Service, []byte(req.CSRPEM), req.RequestID)
		if err != nil {
			http.Error(w, "issuance failed", 400)
			return
		}
		// 在身份申请完成与 Core 提交之间模拟 shutdown 标记，不依赖全局 hook。
		app.mutex.Lock()
		app.shutdownStarted = true
		app.mutex.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"certificate_pem": string(cert)})
	}))
	defer service.Close()
	roots := append(issuer.RootPEM(), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: service.Certificate().Raw})...)
	rootFile := filepath.Join(dir, "late-roots.pem")
	if err := os.WriteFile(rootFile, roots, 0600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "late-backend")
	conf := workload.Config{ServiceName: "backend", TrustDomain: "test.local", IssuerURL: service.URL, TrustBundleFile: rootFile, BootstrapTokenFile: filepath.Join(dir, "token"), StateDir: stateDir}
	app = New(Options{"workload": Options{"enabled": true, "service_name": conf.ServiceName, "trust_domain": conf.TrustDomain, "issuer_url": conf.IssuerURL, "trust_bundle_file": rootFile, "bootstrap_token_file": conf.BootstrapTokenFile, "state_dir": stateDir}})
	if err := app.EnableWorkloadIdentity(); err == nil || !strings.Contains(err.Error(), "shutdown began") {
		t.Fatalf("late init error=%v", err)
	}
	if app.workload.identity != nil {
		t.Fatal("late identity published during shutdown")
	}
	reopened, err := workload.Open(context.Background(), conf)
	if err != nil {
		t.Fatalf("late identity lock not released: %v", err)
	}
	defer reopened.Close()
}
