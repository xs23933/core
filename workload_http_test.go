package core

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/xs23933/core/v3/workload"
)

func TestWorkloadTLSConfigRequiresManagedIdentity(t *testing.T) {
	for _, app := range []*Core{New(), New(Options{"workload": Options{"enabled": true}})} {
		if _, err := app.WorkloadServerTLSConfig(); err == nil {
			t.Fatal("server config without identity")
		}
		if _, err := app.WorkloadClientTLSConfig("backend"); err == nil {
			t.Fatal("client config without identity")
		}
	}
}

func TestWorkloadHTTPOverUnixSocket(t *testing.T) {
	_, dir := managedTestIssuer(t)
	issuer, err := workload.OpenIssuer(workload.IssuerConfig{StateDir: filepath.Join(dir, "issuer"), TrustDomain: "test.local", ListenAddress: "127.0.0.1:0", AdvertiseURL: "https://127.0.0.1:9444", PolicyFile: filepath.Join(dir, "policy.json"), PinFile: filepath.Join(dir, "root.pin")})
	if err != nil {
		t.Fatal(err)
	}
	defer issuer.Close(context.Background())
	apps := make(map[string]*Core)
	for _, service := range []string{"gateway", "backend"} {
		id, err := workload.OpenLocal(context.Background(), workload.Config{ServiceName: service, TrustDomain: "test.local", StateDir: filepath.Join(dir, service), TrustBundleFile: filepath.Join(dir, "issuer", "root.pem")}, issuer)
		if err != nil {
			t.Fatal(err)
		}
		defer id.Close()
		app := New(Options{"workload": Options{"enabled": true}})
		app.workload = &workloadRuntime{identity: id}
		apps[service] = app
	}
	serverTLS, err := apps["backend"].WorkloadServerTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "http.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })}
	go server.Serve(tls.NewListener(listener, serverTLS))
	defer server.Close()
	for _, target := range []string{"backend", "wrong", "no-certificate"} {
		expected := target
		if target == "no-certificate" {
			expected = "backend"
		}
		clientTLS, err := apps["gateway"].WorkloadClientTLSConfig(expected)
		if err != nil {
			t.Fatal(err)
		}
		if target == "no-certificate" {
			clientTLS.GetClientCertificate = nil
		}
		transport := &http.Transport{TLSClientConfig: clientTLS, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		client := &http.Client{Transport: transport, Timeout: time.Second}
		response, err := client.Get("https://workload/status")
		if target == "backend" && (err != nil || response.StatusCode != 204) {
			t.Fatalf("valid client: %v", err)
		}
		if target != "backend" && err == nil {
			t.Fatal("wrong server identity accepted")
		}
		if response != nil {
			response.Body.Close()
		}
		transport.CloseIdleConnections()
	}
}
