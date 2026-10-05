package core

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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

	if got := ctx.RemoteIP(); !got.Equal(net.ParseIP("192.168.1.2")) {
		t.Fatalf("remote ip = %v, want %v", got, "192.168.1.2")
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

	// Cloudflare 回源 IP 可信 → 采信 CF-Connecting-IP
	cf := http.Header{}
	cf.Set("CF-Connecting-IP", "8.8.8.8")
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
	app := New(Options{"trusted_proxies": []string{"10.0.0.0/8"}})

	h := http.Header{}
	h.Set("X-Forwarded-For", "6.6.6.6, 10.0.0.2")
	ctx := app.AcquireCtx(nil, &http.Request{
		URL:        &url.URL{},
		RemoteAddr: "10.1.2.3:12345",
		Header:     h,
	})
	if got := ctx.RemoteIP(); !got.Equal(net.ParseIP("6.6.6.6")) {
		t.Fatalf("trusted_proxies config = %v, want 6.6.6.6", got)
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
