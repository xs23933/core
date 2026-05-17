package core

import (
	"net"
	"net/http"
	"net/url"
	"reflect"
	"testing"
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

func TestNewWithoutNSQConfigDoesNotInitializeProducer(t *testing.T) {
	CloseNSQ()

	New(Options{"debug": false})

	if NProducer() != nil {
		t.Fatal("nsq producer initialized without nsq config")
	}
}
