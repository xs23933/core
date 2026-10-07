package core

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

func Test_Slice(t *testing.T) {
	want := []string{"GET", "POST", "HEAD", "PUT", "DELETE", "OPTIONS", "CONNECT", "TRACE", "PATCH"}
	if got := Methods[:len(Methods)-2]; !reflect.DeepEqual(got, want) {
		t.Fatalf("methods = %v, want %v", got, want)
	}
}

func Test_reomoteIP(t *testing.T) {
	core := New()

	ctx := core.AcquireCtx(nil, &http.Request{
		URL: &url.URL{
			Path: "/",
		},
		RemoteAddr: "173.245.48.1:12345", // Cloudflare 回源 IP 段，使 RemoteIP 采信转发头
		Header: http.Header{
			"X-Real-Ip":        []string{"192.168.1.2"},
			"X-Forwarded-For":  []string{"127.0.0.1"},
			"CF-Connecting-IP": []string{"192.168.1.2"}, // <- 这么传我就收不到 我的程序应该怎样去适应
		},
	})

	if got := ctx.GetHeader("cf-connecting-ip"); got != "192.168.1.2" {
		t.Fatalf("lowercase cf header = %q, want %q", got, "192.168.1.2")
	}
	if got := ctx.GetHeader("CF-Connecting-IP"); got != "192.168.1.2" {
		t.Fatalf("canonical cf header = %q, want %q", got, "192.168.1.2")
	}
	if got := ctx.GetHeader("X-Forwarded-For"); got != "127.0.0.1" {
		t.Fatalf("forwarded header = %q, want %q", got, "127.0.0.1")
	}

	if got := ctx.RemoteIP(); !got.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("remote ip = %v, want %v", got, "127.0.0.1")
	}
}

func TestRemoteIPFallbacks(t *testing.T) {
	if got := RemoteIP(http.Header{}, "127.0.0.1:8080"); !got.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("remote addr with port = %v, want %v", got, "127.0.0.1")
	}
	if got := RemoteIP(http.Header{}, "127.0.0.1"); !got.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("remote addr without port = %v, want %v", got, "127.0.0.1")
	}
	headers := http.Header{"X-Forwarded-For": []string{"bad-ip, 10.0.0.2"}}
	if got := RemoteIP(headers, "127.0.0.1:8080"); !got.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("invalid forwarded header should fall back to remote addr, got %v", got)
	}
}

func TestRemoteIPPackageTrustedProxy(t *testing.T) {
	tests := []struct {
		name string
		addr string
		xff  string
		want string
	}{
		{"public IPv4", "203.0.113.10:8080", "8.8.8.8", "203.0.113.10"},
		{"public IPv6", "[2001:db8::1]:8080", "8.8.8.8", "2001:db8::1"},
		{"private IPv4", " 10.1.2.3:8080 ", " 8.8.8.8 , 10.0.0.2", "8.8.8.8"},
		{"private IPv6", "[fd12::1]:8080", "8.8.8.8", "8.8.8.8"},
		{"loopback IPv4", "127.0.0.1:8080", "8.8.8.8", "8.8.8.8"},
		{"loopback IPv6", "::1", "8.8.8.8", "8.8.8.8"},
		{"Cloudflare", "173.245.48.1:8080", "8.8.8.8", "8.8.8.8"},
		{"missing header", "10.1.2.3", "", "10.1.2.3"},
		{"invalid first IP", "10.1.2.3:8080", "invalid, 8.8.8.8", "10.1.2.3"},
		{"invalid peer", "invalid", "8.8.8.8", ""},
		{"missing peer", "", "8.8.8.8", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{"X-Forwarded-For": []string{tt.xff}}
			got := RemoteIP(headers, tt.addr)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("remote ip = %v, want nil", got)
				}
				return
			}
			if !got.Equal(net.ParseIP(tt.want)) {
				t.Fatalf("remote ip = %v, want %s", got, tt.want)
			}
		})
	}
}

func TestExtractClientInfoTrustedProxy(t *testing.T) {
	for _, tt := range []struct {
		peer string
		want string
	}{
		{"203.0.113.10", "203.0.113.10"},
		{"10.1.2.3", "8.8.8.8"},
		{"", ""},
	} {
		t.Run(tt.peer, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				"x-forwarded-for", "8.8.8.8", "user-agent", "test-agent",
			))
			if tt.peer != "" {
				ctx = peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(tt.peer), Port: 8080}})
			}
			got := ExtractClientInfo(ctx)
			if got.IP != tt.want || got.UA != "test-agent" {
				t.Fatalf("client info = %+v, want IP %q and UA test-agent", got, tt.want)
			}
		})
	}
}

func TestRemoteIPTrustedProxy(t *testing.T) {
	app := New()

	// 不可信代理（直连）伪造转发头 → 返回 TCP 对端 RemoteAddr
	h := http.Header{}
	h.Set("CF-Connecting-IP", "8.8.8.8")
	h.Set("X-Forwarded-For", "6.6.6.6, 10.0.0.2")
	h.Set("X-Real-IP", "9.9.9.9")
	ctx := app.AcquireCtx(nil, &http.Request{
		URL:        &url.URL{},
		RemoteAddr: "203.0.113.10:12345",
		Header:     h,
	})
	if got := ctx.RemoteIP(); !got.Equal(net.ParseIP("203.0.113.10")) {
		t.Fatalf("untrusted proxy = %v, want 203.0.113.10", got)
	}

	// Cloudflare 回源 IP 可信 → 采信 X-Forwarded-For
	cf := http.Header{}
	cf.Set("X-Forwarded-For", "8.8.8.8")
	ctx = app.AcquireCtx(nil, &http.Request{
		URL:        &url.URL{},
		RemoteAddr: "173.245.48.1:12345",
		Header:     cf,
	})
	if got := ctx.RemoteIP(); !got.Equal(net.ParseIP("8.8.8.8")) {
		t.Fatalf("cloudflare proxy = %v, want 8.8.8.8", got)
	}
}

func TestRemoteIPTrustedProxiesConfig(t *testing.T) {
	app := New(Options{"trusted_proxies": []string{"203.0.113.0/24"}})

	h := http.Header{}
	h.Set("X-Forwarded-For", "6.6.6.6, 10.0.0.2")
	ctx := app.AcquireCtx(nil, &http.Request{
		URL:        &url.URL{},
		RemoteAddr: "203.0.113.10:12345",
		Header:     h,
	})
	if got := ctx.RemoteIP(); !got.Equal(net.ParseIP("6.6.6.6")) {
		t.Fatalf("trusted_proxies config = %v, want 6.6.6.6", got)
	}
}

func TestRemoteIPBuiltInTrustedProxies(t *testing.T) {
	configs := []struct {
		name string
		opts Options
	}{
		{"default", nil},
		{"empty", Options{"trusted_proxies": []string{}}},
		{"custom", Options{"trusted_proxies": []string{"203.0.113.0/24"}}},
	}
	peers := []struct {
		ip      string
		trusted bool
	}{
		{"10.1.2.3", true},
		{"172.16.0.1", true},
		{"172.31.255.254", true},
		{"192.168.1.2", true},
		{"fc00::1", true},
		{"fd12::1", true},
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"::1", true},
		{"173.245.48.1", true},
		{"172.15.255.254", false},
		{"172.32.0.1", false},
		{"198.51.100.1", false},
		{"2001:db8::1", false},
		{"169.254.1.2", false},
		{"fe80::1", false},
	}
	for _, config := range configs {
		t.Run(config.name, func(t *testing.T) {
			app := New(config.opts)
			for _, peer := range peers {
				t.Run(peer.ip, func(t *testing.T) {
					ctx := app.AcquireCtx(nil, &http.Request{
						URL:        &url.URL{},
						RemoteAddr: net.JoinHostPort(peer.ip, "12345"),
						Header:     http.Header{"X-Forwarded-For": []string{"8.8.8.8, 10.0.0.2"}},
					})
					defer app.ReleaseCtx(ctx)
					want := net.ParseIP(peer.ip)
					if peer.trusted {
						want = net.ParseIP("8.8.8.8")
					}
					if got := ctx.RemoteIP(); !got.Equal(want) {
						t.Fatalf("remote ip = %v, want %v", got, want)
					}
				})
			}
		})
	}
}

func TestDefaultHealthRoute(t *testing.T) {
	app := New()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestBusinessHealthRouteOverridesDefault(t *testing.T) {
	app := New()
	app.GET("/health", func(c Ctx) error {
		return c.SendString("business health")
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "business health" {
		t.Fatalf("status/body = %d/%q, want 200/business health", rec.Code, rec.Body.String())
	}
}

func TestNewWithoutNSQConfigDoesNotInitializeProducer(t *testing.T) {
	CloseNSQ()

	New(Options{"debug": false})

	if NProducer() != nil {
		t.Fatal("nsq producer initialized without nsq config")
	}
}

func TestOnDemandCreatesCertificateCacheDirectory(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "certmagic-cache")
	app := New()

	if err := app.onDemand("admin@example.com", cacheDir); err != nil {
		t.Fatalf("onDemand setup: %v", err)
	}

	info, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("certificate cache directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("certificate cache path is not a directory")
	}
}

func TestEventHubCloseClosesRegisteredClients(t *testing.T) {
	hub := NewEventHub(time.Millisecond)
	ch := make(chan EventData, 1)
	hub.Register(ch, "client-1")

	hub.Close()

	_, ok := <-ch
	if ok {
		t.Fatal("registered client channel should be closed")
	}
	if clients := hub.loadClients(); len(clients) != 0 {
		t.Fatalf("clients after close = %d, want 0", len(clients))
	}
	if named := hub.loadNamedClients(); len(named) != 0 {
		t.Fatalf("named clients after close = %d, want 0", len(named))
	}
	hub.Broadcast(EventData{Event: "closed", Data: "ignored"})
	hub.SendTo("client-1", EventData{Event: "closed", Data: "ignored"})
}
