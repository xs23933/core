package core

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestManagedHTTPOnlyRegistryDoesNotCreateGRPC 验证出站身份和 HTTP 注册不会开启入站 gRPC。
func TestManagedHTTPOnlyRegistryDoesNotCreateGRPC(t *testing.T) {
	endpoint := startAutomaticHTTPRouteEtcd(t)
	opts, directory := managedTestIssuer(t)
	opts["listen"] = "127.0.0.1:0"
	opts["etcd"] = Options{"endpoints": []string{endpoint}, "namespace": "http-only", "service_name": "pages", "service_addr": "127.0.0.1:9006", "http_addr": "http://127.0.0.1:9006"}
	app := New(opts)
	if err := app.EnableWorkloadIdentity(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ShutdownWithTimeout(time.Second) })
	static := filepath.Join(directory, "static")
	if err := os.Mkdir(static, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(static, "index.html"), []byte("static ready"), 0600); err != nil {
		t.Fatal(err)
	}
	app.StaticFS("/site", http.Dir(static))
	if err := app.EnableEtcdRegistry(nil); err != nil {
		t.Fatal(err)
	}
	if app.grpcServer != nil || app.grpcEnabled || app.grpcReflectionRegistered {
		t.Fatal("HTTP-only registration implicitly enabled gRPC reflection/listener")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = app.prepareServe(listener); err != nil {
		t.Fatalf("managed HTTP-only startup: %v", err)
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/site/", nil))
	if response.Code != 200 || response.Body.String() != "static ready" {
		t.Fatalf("static route response=%d", response.Code)
	}
}
