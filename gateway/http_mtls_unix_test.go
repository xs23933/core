package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/gorilla/websocket"
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/workload"
)

func TestManagedHTTPProxyOverUnixMTLS(t *testing.T) {
	base, err := os.MkdirTemp("", "proxy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	policy := filepath.Join(base, "policy.json")
	if err := os.WriteFile(policy, []byte(`{"services":{"backend":{"token_sha256":"0000000000000000000000000000000000000000000000000000000000000001"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	issuer, err := workload.OpenIssuer(workload.IssuerConfig{StateDir: filepath.Join(base, "issuer"), TrustDomain: "proxy.test", ListenAddress: "127.0.0.1:0", AdvertiseURL: "https://127.0.0.1:9444", PolicyFile: policy, PinFile: filepath.Join(base, "root.pin")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := issuer.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	apps := map[string]*core.Core{}
	for _, name := range []string{"gateway", "backend"} {
		state := filepath.Join(base, name)
		roots := filepath.Join(base, "issuer", "root.pem")
		id, err := workload.OpenLocal(context.Background(), workload.Config{ServiceName: name, TrustDomain: "proxy.test", StateDir: state, TrustBundleFile: roots}, issuer)
		if err != nil {
			t.Fatal(err)
		}
		if err = id.Close(); err != nil {
			t.Fatal(err)
		}
		app := core.New(core.Options{"workload": core.Options{"enabled": true, "service_name": name, "trust_domain": "proxy.test", "state_dir": state, "trust_bundle_file": roots}})
		if err = app.EnableWorkloadIdentity(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := app.ShutdownWithTimeout(time.Second); err != nil {
				t.Error(err)
			}
		})
		apps[name] = app
	}
	backendTLS, err := apps["backend"].WorkloadServerTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(base, "backend.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	websocketClosed := make(chan struct{})
	release := make(chan struct{}, 1)
	defer close(release)
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.PeerCertificates[0].URIs) != 1 || r.TLS.PeerCertificates[0].URIs[0].String() != "spiffe://proxy.test/gateway" {
			t.Error("missing gateway workload identity")
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, "data: second\n\n")
			w.(http.Flusher).Flush()
			return
		}
		if r.URL.Path == "/ws" {
			defer close(websocketClosed)
			if r.Host != "gateway.invalid" || r.URL.RawQuery != "tag=b&tag=a" || r.Header.Get("Origin") != "https://allowed.test" || r.Header.Get("X-Origin-Proof") != "fixture" || r.Header.Get("Upgrade") != "websocket" || r.Header.Get("Sec-WebSocket-Protocol") != "proxy.v1" {
				t.Error("proxy changed websocket handshake context")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			upgrader := wire.Upgrader{Subprotocols: []string{"proxy.v1"}, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "https://allowed.test" }}
			conn, err := upgrader.Upgrade(w, r, http.Header{"X-Backend-Proof": []string{"upgraded"}})
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Error(err)
				return
			}
			if err := conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Error(err)
				return
			}
			if err := conn.WriteMessage(wire.TextMessage, []byte("backend push")); err != nil {
				t.Error(err)
				return
			}
			for {
				kind, payload, err := conn.ReadMessage()
				if err != nil {
					if !wire.IsCloseError(err, wire.CloseNormalClosure) {
						t.Errorf("backend websocket close: %v", err)
					}
					return
				}
				if err := conn.WriteMessage(kind, payload); err != nil {
					t.Error(err)
					return
				}
			}
		}
		if r.Host != "original.example" || r.URL.RawQuery != "tag=b&tag=a" || r.Header.Get("X-Origin-Proof") != "fixture" {
			t.Error("proxy changed origin context")
			w.WriteHeader(400)
			return
		}
		_, _ = io.WriteString(w, "managed backend")
	})}
	go backend.Serve(tls.NewListener(ln, backendTLS))
	t.Cleanup(func() { _ = backend.Close() })
	gw := &EtcdGateway{app: apps["gateway"], connPool: NewConnectionPool()}
	t.Cleanup(func() {
		if err := gw.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, target := range []string{"backend", "wrong"} {
		config, err := apps["gateway"].WorkloadClientTLSConfig(target)
		if err != nil {
			t.Fatal(err)
		}
		transport, err := gw.managedHTTPClient(target, config)
		if err != nil {
			t.Fatal(err)
		}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}
		if err = gw.addHTTPInstance(target, target+"-one", "https://backend.invalid"); err != nil {
			t.Fatal(err)
		}
		path := "/" + target
		gw.registerRoute(&Route{Protocol: RouteProtocolHTTP, Method: http.MethodGet, Path: path, ServiceName: target, Enabled: true})
		req := httptest.NewRequest(http.MethodGet, "http://original.example"+path+"?tag=b&tag=a", nil)
		req.Header.Set("X-Origin-Proof", "fixture")
		response := httptest.NewRecorder()
		apps["gateway"].ServeHTTP(response, req)
		if target == "backend" && (response.Code != 200 || response.Body.String() != "managed backend") {
			t.Fatalf("managed proxy: %d %s", response.Code, response.Body.String())
		}
		if target == "wrong" && (response.Code != 502 || calls.Load() != 1) {
			t.Fatalf("wrong identity reached business handler: %d calls=%d", response.Code, calls.Load())
		}
	}
	gw.registerRoute(&Route{Protocol: RouteProtocolHTTP, Method: http.MethodGet, Path: "/events", ServiceName: "backend", Enabled: true})
	frontSocket := filepath.Join(base, "gateway.sock")
	frontListener, err := net.Listen("unix", frontSocket)
	if err != nil {
		t.Fatal(err)
	}
	front := &http.Server{Handler: apps["gateway"]}
	go front.Serve(frontListener)
	t.Cleanup(func() { _ = front.Close() })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", frontSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get("http://gateway.invalid/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	if err != nil || first != "data: first\n" {
		t.Fatalf("first flushed event: %q %v", first, err)
	}
	// The backend is still blocked; receiving this event proves streaming, not buffered completion.
	release <- struct{}{}
	rest, err := io.ReadAll(reader)
	if err != nil || string(rest) != "\ndata: second\n\n" {
		t.Fatalf("remaining events: %q %v", rest, err)
	}
	gw.registerRoute(&Route{Protocol: RouteProtocolHTTP, Method: http.MethodGet, Path: "/ws", ServiceName: "backend", Enabled: true})
	dialer := wire.Dialer{NetDialContext: transport.DialContext, HandshakeTimeout: 3 * time.Second, Subprotocols: []string{"proxy.v1"}}
	// A websocket handshake must fail before reaching the wrong TLS workload.
	before := calls.Load()
	bad, rejected, err := dialer.Dial("ws://gateway.invalid/wrong", nil)
	if bad != nil {
		_ = bad.Close()
		t.Fatal("wrong workload accepted websocket")
	}
	if rejected != nil {
		defer rejected.Body.Close()
	}
	if err == nil || rejected == nil || rejected.StatusCode != http.StatusBadGateway || calls.Load() != before {
		t.Fatalf("wrong websocket identity: response=%v err=%v calls=%d", rejected, err, calls.Load())
	}
	conn, upgraded, err := dialer.Dial("ws://gateway.invalid/ws?tag=b&tag=a", http.Header{"Origin": []string{"https://allowed.test"}, "X-Origin-Proof": []string{"fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	t.Cleanup(func() {
		select {
		case <-websocketClosed:
		case <-time.After(3 * time.Second):
			t.Error("backend websocket handler did not exit during cleanup")
		}
	})
	if upgraded.StatusCode != http.StatusSwitchingProtocols || upgraded.Header.Get("Upgrade") != "websocket" || upgraded.Header.Get("X-Backend-Proof") != "upgraded" || conn.Subprotocol() != "proxy.v1" {
		t.Fatalf("websocket upgrade response: %v protocol=%q", upgraded, conn.Subprotocol())
	}
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := conn.ReadMessage()
	if err != nil || kind != wire.TextMessage || string(payload) != "backend push" {
		t.Fatalf("backend push: kind=%d payload=%q err=%v", kind, payload, err)
	}
	for _, message := range []struct {
		kind int
		body []byte
	}{{wire.TextMessage, []byte("client text")}, {wire.BinaryMessage, []byte{0, 1, 255, 128}}} {
		if err := conn.WriteMessage(message.kind, message.body); err != nil {
			t.Fatal(err)
		}
		kind, payload, err := conn.ReadMessage()
		if err != nil || kind != message.kind || !bytes.Equal(payload, message.body) {
			t.Fatalf("websocket echo: kind=%d payload=%q err=%v", kind, payload, err)
		}
	}
	if err := conn.WriteControl(wire.CloseMessage, wire.FormatCloseMessage(wire.CloseNormalClosure, "done"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); !wire.IsCloseError(err, wire.CloseNormalClosure) {
		t.Fatalf("client websocket close: %v", err)
	}
	select {
	case <-websocketClosed:
	case <-time.After(3 * time.Second):
		t.Fatal("backend websocket handler did not exit after close")
	}
}
