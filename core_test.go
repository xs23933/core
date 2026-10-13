package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	core := New(Options{"trusted_proxies": []string{"127.0.0.1"}})

	ctx := core.AcquireCtx(nil, &http.Request{
		URL: &url.URL{
			Path: "/",
		},
		RemoteAddr: "127.0.0.1:12345", // 显式信任本机反向代理
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
	for _, addr := range []string{"203.0.113.10", "2001:db8::1", "10.1.2.3", "fd12::1", "127.0.0.1", "::1", "173.245.48.1"} {
		t.Run(addr, func(t *testing.T) {
			headers := http.Header{"X-Forwarded-For": {"8.8.8.8"}}
			want := net.ParseIP(addr)
			if want.IsPrivate() || want.IsLoopback() {
				want = net.ParseIP("8.8.8.8")
			}
			if got := RemoteIP(headers, net.JoinHostPort(addr, "8080")); !got.Equal(want) {
				t.Fatalf("remote ip = %v, want %v", got, want)
			}
		})
	}
	for _, addr := range []string{"", "invalid"} {
		if got := RemoteIP(http.Header{"X-Forwarded-For": {"8.8.8.8"}}, addr); got != nil {
			t.Fatalf("invalid peer = %v, want nil", got)
		}
	}
}

func TestExtractClientInfoTrustedProxy(t *testing.T) {
	for _, addr := range []string{"203.0.113.10", "10.1.2.3", "127.0.0.1", ""} {
		t.Run(addr, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				"x-forwarded-for", "8.8.8.8", "user-agent", "test-agent",
			))
			if addr != "" {
				ctx = peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(addr), Port: 8080}})
			}
			got := ExtractClientInfo(ctx)
			want := addr
			if got.IP != want || got.UA != "test-agent" {
				t.Fatalf("client info = %+v, want IP %q and UA test-agent", got, want)
			}
		})
	}
}

func TestRemoteIPTrustedProxyChain(t *testing.T) {
	app := New(Options{"trusted_proxies": []string{"127.0.0.1", "10.0.0.2/32", "::1", "fd12::2/128", "198.51.100.0/24"}})
	tests := []struct {
		name string
		addr string
		xff  []string
		want string
	}{
		{"nginx append spoofed prefix", "127.0.0.1", []string{"6.6.6.6, 203.0.113.10"}, "203.0.113.10"},
		{"caddy overwrite", "127.0.0.1", []string{"203.0.113.10"}, "203.0.113.10"},
		{"multiple trusted proxies", "127.0.0.1", []string{"6.6.6.6, 203.0.113.10, 10.0.0.2"}, "203.0.113.10"},
		{"untrusted intermediate proxy", "127.0.0.1", []string{"6.6.6.6, 192.0.2.2, 10.0.0.2"}, "192.0.2.2"},
		{"unconfigured private hop", "127.0.0.1", []string{"6.6.6.6, 10.0.0.3"}, "10.0.0.3"},
		{"ipv6 chain", "::1", []string{"6.6.6.6, 2001:db8::10, fd12::2"}, "2001:db8::10"},
		{"mapped ipv4 peer", "::ffff:127.0.0.1", []string{"203.0.113.10"}, "203.0.113.10"},
		{"duplicate header appended origin", "127.0.0.1", []string{"6.6.6.6", "203.0.113.10"}, "203.0.113.10"},
		{"duplicate header trusted chain", "127.0.0.1", []string{"6.6.6.6", "203.0.113.10, 10.0.0.2"}, "203.0.113.10"},
		{"invalid rightmost", "127.0.0.1", []string{"8.8.8.8, invalid"}, "127.0.0.1"},
		{"invalid traversed hop", "127.0.0.1", []string{"8.8.8.8, invalid, 10.0.0.2"}, "127.0.0.1"},
		{"empty traversed hop", "127.0.0.1", []string{"8.8.8.8, , 10.0.0.2"}, "127.0.0.1"},
		{"port in header", "127.0.0.1", []string{"203.0.113.10:80"}, "127.0.0.1"},
		{"invalid prefix outside trust boundary", "127.0.0.1", []string{"invalid, 203.0.113.10"}, "203.0.113.10"},
		{"whitespace", "127.0.0.1", []string{" 203.0.113.10 , 10.0.0.2 "}, "203.0.113.10"},
		{"all trusted", "127.0.0.1", []string{"10.0.0.2, 127.0.0.1"}, "10.0.0.2"},
		{"no XFF real IP fallback", "127.0.0.1", nil, "9.9.9.9"},
		{"empty XFF", "127.0.0.1", []string{""}, "127.0.0.1"},
		{"untrusted peer", "203.0.113.10", []string{"6.6.6.6"}, "203.0.113.10"},
		{"cloudflare not built in", "173.245.48.1", []string{"6.6.6.6"}, "173.245.48.1"},
		{"unconfigured private peer", "10.0.0.3", []string{"6.6.6.6"}, "10.0.0.3"},
		{"configured public peer", "198.51.100.2", []string{"6.6.6.6, 203.0.113.10"}, "203.0.113.10"},
		{"configured public hop", "127.0.0.1", []string{"6.6.6.6, 203.0.113.10, 198.51.100.2"}, "203.0.113.10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{
				"X-Forwarded-For": tt.xff,
				"X-Real-Ip":       {"9.9.9.9"}, "Cf-Connecting-Ip": {"9.9.9.9"},
				"Forwarded": {"for=9.9.9.9"}, "True-Client-Ip": {"9.9.9.9"},
			}
			ctx := app.AcquireCtx(nil, &http.Request{URL: &url.URL{}, RemoteAddr: net.JoinHostPort(tt.addr, "8080"), Header: h})
			defer app.ReleaseCtx(ctx)
			if got := ctx.RemoteIP(); !got.Equal(net.ParseIP(tt.want)) {
				t.Fatalf("remote ip = %v, want %s", got, tt.want)
			}
		})
	}
}

func TestRemoteIPDefaultTrust(t *testing.T) {
	for _, opts := range []Options{nil, {"trusted_proxies": []string{}}} {
		app := New(opts)
		for _, addr := range []string{"127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "fd12::1", "173.245.48.1", "203.0.113.10"} {
			ctx := app.AcquireCtx(nil, &http.Request{URL: &url.URL{}, RemoteAddr: net.JoinHostPort(addr, "8080"), Header: http.Header{"X-Forwarded-For": {"6.6.6.6"}}})
			got := ctx.RemoteIP()
			app.ReleaseCtx(ctx)
			want := net.ParseIP(addr)
			if want.IsPrivate() || want.IsLoopback() {
				want = net.ParseIP("6.6.6.6")
			}
			if !got.Equal(want) {
				t.Fatalf("opts %v peer %s: got %v, want %v", opts, addr, got, want)
			}
		}
	}
}

func TestRemoteIPRealIPFallback(t *testing.T) {
	for _, tt := range []struct {
		name string
		addr string
		xff  []string
		real []string
		want string
	}{
		{"IPv4", "127.0.0.1", nil, []string{" 203.0.113.10 "}, "203.0.113.10"},
		{"IPv6", "::1", nil, []string{"2001:db8::10"}, "2001:db8::10"},
		{"private real IP", "127.0.0.1", nil, []string{"10.1.2.3"}, "10.1.2.3"},
		{"missing", "127.0.0.1", nil, nil, "127.0.0.1"},
		{"empty", "127.0.0.1", nil, []string{""}, "127.0.0.1"},
		{"invalid", "127.0.0.1", nil, []string{"invalid"}, "127.0.0.1"},
		{"port rejected", "127.0.0.1", nil, []string{"203.0.113.10:80"}, "127.0.0.1"},
		{"list rejected", "127.0.0.1", nil, []string{"8.8.8.8, 9.9.9.9"}, "127.0.0.1"},
		{"duplicates rejected", "127.0.0.1", nil, []string{"8.8.8.8", "9.9.9.9"}, "127.0.0.1"},
		{"XFF wins", "127.0.0.1", []string{"203.0.113.10"}, []string{"9.9.9.9"}, "203.0.113.10"},
		{"empty XFF blocks fallback", "127.0.0.1", []string{""}, []string{"9.9.9.9"}, "127.0.0.1"},
		{"blank XFF blocks fallback", "127.0.0.1", []string{" "}, []string{"9.9.9.9"}, "127.0.0.1"},
		{"invalid XFF blocks fallback", "127.0.0.1", []string{"invalid"}, []string{"9.9.9.9"}, "127.0.0.1"},
		{"public peer ignores real IP", "203.0.113.10", nil, []string{"9.9.9.9"}, "203.0.113.10"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{"X-Forwarded-For": tt.xff, "X-Real-Ip": tt.real}
			if got := RemoteIP(h, net.JoinHostPort(tt.addr, "8080")); !got.Equal(net.ParseIP(tt.want)) {
				t.Fatalf("got %v, want %s", got, tt.want)
			}
		})
	}
}

func TestExtractClientInfoForwardedHeaders(t *testing.T) {
	for _, tt := range []struct {
		name  string
		pairs []string
		want  string
	}{
		{"duplicate XFF ignored", []string{"x-forwarded-for", "6.6.6.6", "x-forwarded-for", "203.0.113.10"}, "127.0.0.1"},
		{"real IP ignored", []string{"x-real-ip", "203.0.113.10"}, "127.0.0.1"},
		{"empty XFF blocks fallback", []string{"x-forwarded-for", "", "x-real-ip", "203.0.113.10"}, "127.0.0.1"},
		{"duplicate real IP", []string{"x-real-ip", "8.8.8.8", "x-real-ip", "9.9.9.9"}, "127.0.0.1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(tt.pairs...))
			ctx = peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}})
			if got := ExtractClientInfo(ctx); got.IP != tt.want {
				t.Fatalf("got %s, want %s", got.IP, tt.want)
			}
		})
	}
}

func TestTrustedProxyConfiguration(t *testing.T) {
	for _, raw := range []any{"127.0.0.1", 42, []string{""}, []string{" "}, []string{"invalid"}, []string{"127.0.0.1/99"}, []string{"10.0.0.2", "invalid"}, []any{"10.0.0.2", 42}} {
		t.Run(fmt.Sprint(raw), func(t *testing.T) {
			if nets, err := parseCIDRs(raw); err == nil || nets != nil {
				t.Fatalf("parseCIDRs(%v) = %v, %v; want nil and error", raw, nets, err)
			}
			defer func() {
				if err := recover(); err == nil || !strings.Contains(fmt.Sprint(err), "trusted_proxies") {
					t.Fatalf("New should reject invalid trusted_proxies, got %v", err)
				}
			}()
			New(Options{"trusted_proxies": raw})
		})
	}
	for _, raw := range []any{[]string{" 203.0.113.10 ", "2001:db8::/32"}, []any{"203.0.113.10", "2001:db8::/32"}} {
		app := New(Options{"trusted_proxies": raw})
		for _, addr := range []string{"203.0.113.10", "2001:db8::10", "203.0.113.11", "10.0.0.2", "127.0.0.1", "::1", "fd12::1"} {
			h := http.Header{"X-Forwarded-For": {"8.8.8.8"}, "X-Real-Ip": {"9.9.9.9"}}
			ctx := app.AcquireCtx(nil, &http.Request{URL: &url.URL{}, RemoteAddr: net.JoinHostPort(addr, "8080"), Header: h})
			got := ctx.RemoteIP()
			app.ReleaseCtx(ctx)
			want := addr
			if addr == "203.0.113.10" || addr == "2001:db8::10" {
				want = "8.8.8.8"
			}
			if !got.Equal(net.ParseIP(want)) {
				t.Fatalf("peer %s: got %v, want %s", addr, got, want)
			}
		}
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
