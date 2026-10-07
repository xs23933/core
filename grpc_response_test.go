package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

type cookieHealthServer struct {
	healthpb.UnimplementedHealthServer
	write func(context.Context) error
}

func (s *cookieHealthServer) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	if err := s.write(ctx); err != nil {
		return nil, err
	}
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func TestGrpcSetCookieMatchesBaseCtx(t *testing.T) {
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tt := range []struct {
		name, path   string
		args         []any
		wantDomain   string
		wantSecure   bool
		wantHTTPOnly bool
	}{
		{name: "defaults", wantDomain: "default.example"},
		{name: "flags", path: "/auth", args: []any{"HTTPONLY", true, "explicit.example"}, wantDomain: "explicit.example", wantSecure: true, wantHTTPOnly: true},
		{name: "last option wins", args: []any{true, false, "one.example", "two.example", 123}, wantDomain: "two.example"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := New(Options{"domain": "default.example"})
			client := startBufconnHealthClient(t, app.GetGRPCServer(), &cookieHealthServer{write: func(ctx context.Context) error {
				return GrpcSetCookie(ctx, "session", "a+b /中文", exp, tt.path, tt.args...)
			}})
			// A second Core must not change this server's default domain.
			New(Options{"domain": "other.example"})
			var headers metadata.MD
			if _, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{}, grpc.Header(&headers)); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			c := app.AcquireCtx(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			c.SetCookie("session", "a+b /中文", exp, tt.path, tt.args...)
			app.ReleaseCtx(c)
			if got, want := headers.Get("core-http-set-cookie-bin"), rec.Header().Values("Set-Cookie"); !reflect.DeepEqual(got, want) {
				t.Fatalf("grpc cookies=%v, HTTP cookies=%v", got, want)
			}
			cookies := rec.Result().Cookies()
			wantPath := tt.path
			if wantPath == "" {
				wantPath = "/"
			}
			if len(cookies) != 1 {
				t.Fatalf("cookies=%v", cookies)
			}
			cookie := cookies[0]
			if cookie.Value != "a%2Bb+%2F%E4%B8%AD%E6%96%87" || cookie.Path != wantPath || cookie.Domain != tt.wantDomain || cookie.Secure != tt.wantSecure || cookie.HttpOnly != tt.wantHTTPOnly || cookie.SameSite != http.SameSiteLaxMode || !cookie.Expires.Equal(exp) {
				t.Fatalf("cookie=%+v", cookie)
			}
		})
	}
}

func (s *cookieHealthServer) Watch(_ *healthpb.HealthCheckRequest, stream grpc.ServerStreamingServer[healthpb.HealthCheckResponse]) error {
	if err := s.write(stream.Context()); err != nil {
		return err
	}
	return stream.Send(&healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING})
}

func TestGrpcCookieStreamAndRemovalDefaults(t *testing.T) {
	app := New(Options{"domain": "stream.example"})
	client := startBufconnHealthClient(t, app.GetGRPCServer(), &cookieHealthServer{write: func(ctx context.Context) error { return GrpcRemoveCookie(ctx, "session", "") }})
	New(Options{"domain": "other.example"})
	stream, err := client.Watch(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	md, err := stream.Header()
	if err != nil {
		t.Fatal(err)
	}
	values := md.Get("core-http-set-cookie-bin")
	cookies := (&http.Response{Header: http.Header{"Set-Cookie": values}}).Cookies()
	if len(cookies) != 1 || cookies[0].Domain != "stream.example" || cookies[0].Path != "" || !cookies[0].Expires.Before(time.Now()) {
		t.Fatalf("cookies=%v", cookies)
	}
}

func TestGrpcCookieHeadersAndRemoval(t *testing.T) {
	app := New(Options{"domain": "default.example"})
	client := startBufconnHealthClient(t, app.GetGRPCServer(), &cookieHealthServer{write: func(ctx context.Context) error {
		if err := GrpcSetHeader(ctx, "X-Label", "中文", "second"); err != nil {
			return err
		}
		if err := GrpcCookie(ctx, &http.Cookie{Name: "__Host-session", Value: "token", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 3600}); err != nil {
			return err
		}
		return GrpcRemoveCookie(ctx, "refresh", "/auth", "explicit.example")
	}})
	var headers metadata.MD
	if _, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{}, grpc.Header(&headers)); err != nil {
		t.Fatal(err)
	}
	if got := headers.Get("core-http-x-label-bin"); !reflect.DeepEqual(got, []string{"中文", "second"}) {
		t.Fatalf("headers=%v", got)
	}
	values := headers.Get("core-http-set-cookie-bin")
	if len(values) != 2 {
		t.Fatalf("cookies=%v", values)
	}
	if values[0] != "__Host-session=token; Path=/; Max-Age=3600; HttpOnly; Secure; SameSite=Strict" {
		t.Fatalf("full cookie=%q", values[0])
	}
	cookies := (&http.Response{Header: http.Header{"Set-Cookie": values}}).Cookies()
	if cookies[1].Domain != "explicit.example" || cookies[1].Path != "/auth" || !cookies[1].Expires.Before(time.Now()) {
		t.Fatalf("removal=%v", cookies[1])
	}
}

func TestGrpcResponseHelpersRejectInvalidInput(t *testing.T) {
	for _, key := range []string{"", "bad name", "X-Test!", "X-Test+", "Content-Type", "Content-Length", "Content-Encoding", "Connection", "Trailer", "Grpc-Status", "Transfer-Encoding"} {
		t.Run(key, func(t *testing.T) {
			client := startBufconnHealthClient(t, New().GetGRPCServer(), &cookieHealthServer{write: func(ctx context.Context) error {
				if err := GrpcSetHeader(ctx, key, "value"); err == nil {
					return http.ErrNoCookie
				}
				return nil
			}})
			var headers metadata.MD
			if _, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{}, grpc.Header(&headers)); err != nil {
				t.Fatalf("invalid header accepted: %v", err)
			}
			if len(headers.Get("core-http-"+key+"-bin")) != 0 {
				t.Fatalf("invalid header published: %v", headers)
			}
		})
	}
	client := startBufconnHealthClient(t, New().GetGRPCServer(), &cookieHealthServer{write: func(ctx context.Context) error {
		for _, c := range []*http.Cookie{nil, {Name: "bad name", Value: "token"}, {Name: "ok", Value: "bad\r\n"}, {Name: "ok", Domain: "bad/domain"}} {
			if err := GrpcCookie(ctx, c); err == nil {
				return http.ErrNoCookie
			}
		}
		if err := GrpcSetHeader(ctx, "X-Test", "good", "bad\r\nInjected: yes"); err == nil {
			return http.ErrNoCookie
		}
		return nil
	}})
	var headers metadata.MD
	if _, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{}, grpc.Header(&headers)); err != nil {
		t.Fatal(err)
	}
	if len(headers.Get("core-http-set-cookie-bin")) != 0 || len(headers.Get("core-http-x-test-bin")) != 0 {
		t.Fatalf("partial invalid metadata=%v", headers)
	}
	if err := GrpcSetCookie(context.Background(), "session", "token", time.Time{}, ""); err == nil {
		t.Fatal("missing server context accepted")
	}
}
