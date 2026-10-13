package core

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

func TestGRPCCtxRequestAndOutgoingIsolation(t *testing.T) {
	incoming := metadata.Pairs("x-request-id", "request", "x-label", "one", "x-label", "two", "authorization", "secret")
	base, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	base = metadata.NewIncomingContext(base, incoming)
	base = metadata.NewOutgoingContext(base, metadata.Pairs("existing", "keep", "x-request-id", "old"))
	c := NewGRPCCtx(base)
	if c.StdContext() != base {
		t.Fatal("original context replaced")
	}
	if c.GetHeader("X-REQUEST-ID") != "request" {
		t.Fatal("incoming header missing")
	}
	values := c.HeaderValues("X-LABEL")
	values[0] = "changed"
	copy := c.RequestMetadata()
	copy.Set("x-request-id", "changed")
	copy["x-label"][0] = "changed"
	if c.GetHeader("x-request-id") != "request" || !reflect.DeepEqual(c.HeaderValues("x-label"), []string{"one", "two"}) {
		t.Fatal("incoming metadata mutated through copies")
	}
	supplied := metadata.Pairs("x-request-id", "selected", "x-label", "a", "x-label", "b")
	out := c.OutgoingContext(supplied)
	supplied["x-label"][0] = "mutated"
	supplied.Set("x-request-id", "mutated")
	md, _ := metadata.FromOutgoingContext(out)
	if md.Get("existing")[0] != "keep" || !reflect.DeepEqual(md.Get("x-request-id"), []string{"selected"}) || !reflect.DeepEqual(md.Get("x-label"), []string{"a", "b"}) {
		t.Fatalf("outgoing=%v", md)
	}
	if len(md.Get("authorization")) != 0 {
		t.Fatal("incoming authorization forwarded implicitly")
	}
	original, _ := metadata.FromOutgoingContext(base)
	if !reflect.DeepEqual(original.Get("x-request-id"), []string{"old"}) {
		t.Fatal("original outgoing metadata mutated")
	}
	wantDeadline, _ := base.Deadline()
	gotDeadline, _ := out.Deadline()
	if wantDeadline != gotDeadline {
		t.Fatal("deadline changed")
	}
	cancel()
	if !errors.Is(out.Err(), context.Canceled) {
		t.Fatalf("cancellation lost: %v", out.Err())
	}
}

func TestGRPCCtxCookieErrorsAndNoServer(t *testing.T) {
	for _, tt := range []struct {
		name    string
		cookies []string
		want    string
		err     bool
	}{
		{"session", []string{"other=x", "session=a%2Bb+%2F%E4%B8%AD%E6%96%87"}, "a+b /中文", false},
		{"missing", []string{"other=x"}, "", true},
		{"session", []string{"session=%ZZ"}, "", true},
	} {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{"cookie": tt.cookies})
		got, err := NewGRPCCtx(ctx).Cookie(tt.name)
		if got != tt.want || (err != nil) != tt.err {
			t.Fatalf("cookie=%q/%v, want %q error=%v", got, err, tt.want, tt.err)
		}
	}
	c := NewGRPCCtx(nil)
	if c.GetHeader("x") != "" || c.ClientIP() != "" {
		t.Fatal("unexpected client IP")
	}
	if _, err := c.Cookie("missing"); !errors.Is(err, http.ErrNoCookie) {
		t.Fatalf("missing cookie error=%v", err)
	}
	if err := c.SetHeader("X-Test", "value"); err == nil {
		t.Fatal("response write without server accepted")
	}
	if err := c.SetHeader("bad name", "value"); err == nil {
		t.Fatal("invalid response name accepted")
	}
	if err := c.WriteCookie(nil); err == nil {
		t.Fatal("nil response cookie accepted")
	}
}

func TestGRPCCtxRequestResponseAndDownstreamRPC(t *testing.T) {
	downstreamApp := New()
	t.Cleanup(downstreamApp.stop)
	received := make(chan metadata.MD, 1)
	downstream := startBufconnHealthClient(t, downstreamApp.GetGRPCServer(), &cookieHealthServer{write: func(ctx context.Context) error {
		md, _ := metadata.FromIncomingContext(ctx)
		received <- md
		return nil
	}})
	app := New(Options{"domain": "service.example"})
	t.Cleanup(app.stop)
	client := startBufconnHealthClient(t, app.GetGRPCServer(), &cookieHealthServer{write: func(ctx context.Context) error {
		c := NewGRPCCtx(ctx)
		value, err := c.Cookie("session")
		if err != nil {
			return err
		}
		if value != "a+b /中文" {
			return errors.New("request cookie was not decoded")
		}
		if !reflect.DeepEqual(c.HeaderValues("x-label"), []string{"one", "two"}) {
			return errors.New("request values lost")
		}
		if err := c.SetHeader("X-Label", "response-one", "response-two"); err != nil {
			return err
		}
		if err := c.SetCookie("session", value, time.Time{}, "", "httponly", true); err != nil {
			return err
		}
		if err := c.WriteCookie(&http.Cookie{Name: "explicit", Value: "literal", Path: "/", HttpOnly: true}); err != nil {
			return err
		}
		if err := c.RemoveCookie("refresh", "/auth"); err != nil {
			return err
		}
		if c.GetHeader("x-label") != "one" {
			return errors.New("response write changed request")
		}
		_, err = downstream.Check(c.OutgoingContext(metadata.Pairs("x-request-id", c.GetHeader("x-request-id"))), &healthpb.HealthCheckRequest{})
		return err
	}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", "other=x", "cookie", "session=a%2Bb+%2F%E4%B8%AD%E6%96%87", "authorization", "secret", "core-client-ip", "6.6.6.6", "x-request-id", "req-1", "x-label", "one", "x-label", "two"))
	var headers metadata.MD
	if _, err := client.Check(ctx, &healthpb.HealthCheckRequest{}, grpc.Header(&headers)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(headers.Get("core-http-x-label-bin"), []string{"response-one", "response-two"}) {
		t.Fatalf("response headers=%v", headers)
	}
	cookies := (&http.Response{Header: http.Header{"Set-Cookie": headers.Get("core-http-set-cookie-bin")}}).Cookies()
	if len(cookies) != 3 || cookies[0].Domain != "service.example" || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[1].Domain != "" || cookies[2].Path != "/auth" || !cookies[2].Expires.Before(time.Now()) {
		t.Fatalf("response cookies=%v", cookies)
	}
	md := <-received
	if !reflect.DeepEqual(md.Get("x-request-id"), []string{"req-1"}) {
		t.Fatalf("downstream metadata=%v", md)
	}
	for _, key := range []string{"authorization", "cookie", GatewayClientIPMetadata, "core-http-x-label-bin", "core-http-set-cookie-bin"} {
		if len(md.Get(key)) != 0 {
			t.Fatalf("implicitly forwarded %s: %v", key, md)
		}
	}
}

type grpcCtxStreamServer struct {
	healthpb.UnimplementedHealthServer
}

func (s *grpcCtxStreamServer) Watch(_ *healthpb.HealthCheckRequest, stream healthpb.Health_WatchServer) error {
	c := NewGRPCCtx(stream.Context())
	value, err := c.Cookie("session")
	if err != nil {
		return err
	}
	if err := c.SetHeader("X-Client-IP", c.ClientIP()); err != nil {
		return err
	}
	if err := c.SetCookie("session", value, time.Time{}, "", "httponly"); err != nil {
		return err
	}
	return stream.Send(&healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING})
}
func TestGRPCCtxStream(t *testing.T) {
	app := New(Options{"domain": "stream.example"})
	t.Cleanup(app.stop)
	if err := app.ConfigureGRPCServer(GRPCServerConfig{TrustGateway: func(ctx context.Context) bool {
		p, ok := peer.FromContext(ctx)
		return ok && p.Addr != nil && p.Addr.String() == "bufconn"
	}}); err != nil {
		t.Fatal(err)
	}
	client := startBufconnHealthClient(t, app.GetGRPCServer(), &grpcCtxStreamServer{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(GatewayClientIPMetadata, "203.0.113.10", "cookie", "session=stream%2Bvalue"))
	stream, err := client.Watch(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	headers, err := stream.Header()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(headers.Get("core-http-x-client-ip-bin"), []string{"203.0.113.10"}) {
		t.Fatalf("stream headers=%v", headers)
	}
	cookies := (&http.Response{Header: http.Header{"Set-Cookie": headers.Get("core-http-set-cookie-bin")}}).Cookies()
	if len(cookies) != 1 || cookies[0].Value != "stream%2Bvalue" || cookies[0].Domain != "stream.example" || !cookies[0].HttpOnly {
		t.Fatalf("stream cookies=%v", cookies)
	}
}
