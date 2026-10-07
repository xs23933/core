package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type responseMetadataServer struct {
	healthpb.UnimplementedHealthServer
	write func(context.Context, *healthpb.HealthCheckRequest) error
}

func (s *responseMetadataServer) Check(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	if s.write != nil {
		if err := s.write(ctx, req); err != nil {
			return nil, err
		}
		return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
	}
	md := metadata.Pairs(
		"core-http-set-cookie-bin", "session=token; Path=/; HttpOnly; Secure; SameSite=Lax",
		"core-http-set-cookie-bin", "refresh=second; Path=/; HttpOnly",
		"core-http-x-request-id-bin", "request-1",
		"core-http-x-label-bin", "中文",
		"core-http-cache-control-bin", "no-store",
		"core-http-content-type-bin", "text/html",
		"core-http-content-length-bin", "999",
		"core-http-content-encoding-bin", "gzip",
		"core-http-connection-bin", "close",
		"core-http-grpc-status-bin", "0",
		"core-http-x-injected-bin", "bad\r\nInjected: yes",
		"core-http-x-test!-bin", "unsupported",
		"x-internal", "private",
	)
	if req.Service == "error" {
		md.Set("core-http-set-cookie-bin", "session=; Path=/; Max-Age=0")
	}
	if err := grpc.SetHeader(ctx, md); err != nil {
		return nil, err
	}
	grpc.SetTrailer(ctx, metadata.Pairs("core-http-x-trailer-bin", "must-not-bridge"))
	if req.Service == "error" {
		return nil, status.Error(codes.Unauthenticated, "expired")
	}
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func TestGatewayCoreCookieHelpers(t *testing.T) {
	serviceApp := core.New(core.Options{"domain": "service.example"})
	healthpb.RegisterHealthServer(serviceApp.GetGRPCServer(), &responseMetadataServer{write: func(ctx context.Context, req *healthpb.HealthCheckRequest) error {
		if err := core.GrpcSetHeader(ctx, "X-Received-Cookie", core.GrpcHeader(ctx, "cookie")); err != nil {
			return err
		}
		if req.Service == "clear" {
			if err := core.GrpcRemoveCookie(ctx, "session", "/"); err != nil {
				return err
			}
			return status.Error(codes.Unauthenticated, "expired")
		}
		return core.GrpcSetCookie(ctx, "session", req.Service, time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC), "", "httponly", true)
	}})
	proxy := metadataTestProxy(t, serviceApp.GetGRPCServer())
	app := metadataTestGateway(t, proxy)
	for _, value := range []string{"first", "clear", "second"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/check", strings.NewReader(`{"service":"`+value+`"}`))
		req.AddCookie(&http.Cookie{Name: "old", Value: "incoming"})
		app.ServeHTTP(rec, req)
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Domain != "service.example" || cookies[0].Path != "/" {
			t.Fatalf("cookies=%v", cookies)
		}
		if rec.Header().Get("X-Received-Cookie") != "old=incoming" {
			t.Fatalf("incoming cookie not forwarded: %v", rec.Header())
		}
		if value == "clear" {
			if rec.Code != http.StatusUnauthorized || cookies[0].Value != "" || !cookies[0].Expires.Before(time.Now()) {
				t.Fatalf("clear response: status=%d cookie=%v", rec.Code, cookies[0])
			}
		} else if rec.Code != http.StatusOK || cookies[0].Value != value || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
			t.Fatalf("login response: status=%d cookie=%v", rec.Code, cookies[0])
		}
	}
	// The original public JSON-only Invoke API remains available.
	body, err := proxy.Invoke(context.Background(), "/grpc.health.v1.Health/Check", []byte(`{"service":"native"}`))
	if err != nil || !strings.Contains(string(body), "SERVING") {
		t.Fatalf("Invoke: body=%s err=%v", body, err)
	}
}

func TestGatewayResponseMetadataUsesFinalRetry(t *testing.T) {
	makeProxy := func(value string, rpcErr error) *ReflectionProxy {
		srv := grpc.NewServer()
		healthpb.RegisterHealthServer(srv, &responseMetadataServer{write: func(ctx context.Context, _ *healthpb.HealthCheckRequest) error {
			if err := core.GrpcCookie(ctx, &http.Cookie{Name: "session", Value: value, Path: "/"}); err != nil {
				return err
			}
			return rpcErr
		}})
		return metadataTestProxy(t, srv)
	}
	first := makeProxy("failed-attempt", status.Error(codes.Unavailable, "retry"))
	second := makeProxy("final-attempt", nil)
	app := core.New()
	gw := &EtcdGateway{app: app, connPool: NewConnectionPool()}
	pool := gw.connPool.GetOrCreate(app, "metadata")
	pool.storeState(servicePoolState{byID: map[string]*ReflectionProxy{"one": first, "two": second}, all: []*ReflectionProxy{first, second}, methods: map[string][]*ReflectionProxy{"/grpc.health.v1.Health/Check": {first, second}}})
	gw.registerRoute(&Route{Method: http.MethodGet, Path: "/check", ServiceName: "metadata", GRPCMethod: "/grpc.health.v1.Health/Check"})
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/check", nil))
	cookies := rec.Result().Cookies()
	if rec.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Value != "final-attempt" {
		t.Fatalf("retry response: status=%d cookies=%v", rec.Code, cookies)
	}
}

func metadataTestProxy(t *testing.T, srv *grpc.Server) *ReflectionProxy {
	t.Helper()
	reflection.Register(srv)
	ln := bufconn.Listen(1024 * 1024)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Stop(); _ = ln.Close() })
	conn, err := grpc.NewClient("passthrough:///metadata", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return ln.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proxy, err := newReflectionProxyFromConnContext(ctx, core.New(), "metadata", conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	return proxy
}

func metadataTestGateway(t *testing.T, proxy *ReflectionProxy) *core.Core {
	t.Helper()
	app := core.New()
	gw := &EtcdGateway{app: app, connPool: NewConnectionPool()}
	pool := gw.connPool.GetOrCreate(app, "metadata")
	pool.storeState(servicePoolState{byID: map[string]*ReflectionProxy{"one": proxy}, all: []*ReflectionProxy{proxy}, methods: map[string][]*ReflectionProxy{"/grpc.health.v1.Health/Check": {proxy}}})
	gw.registerRoute(&Route{Method: http.MethodPost, Path: "/check", ServiceName: "metadata", GRPCMethod: "/grpc.health.v1.Health/Check"})
	return app
}

func TestGatewayResponseMetadata(t *testing.T) {
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, &responseMetadataServer{})
	app := metadataTestGateway(t, metadataTestProxy(t, srv))
	for _, service := range []string{"ok", "error"} {
		t.Run(service, func(t *testing.T) {
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/check", strings.NewReader(`{"service":"`+service+`"}`)))
			wantStatus, wantCookies := http.StatusOK, 2
			if service == "error" {
				wantStatus, wantCookies = http.StatusUnauthorized, 1
			}
			if rec.Code != wantStatus {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			cookies := rec.Result().Cookies()
			if len(cookies) != wantCookies {
				t.Fatalf("cookies=%v, want %d", cookies, wantCookies)
			}
			if service == "error" && cookies[0].MaxAge != -1 {
				t.Fatalf("cookie not cleared: %v", cookies[0])
			}
			if rec.Header().Get("X-Request-ID") != "request-1" || rec.Header().Get("X-Label") != "中文" || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("headers=%v", rec.Header())
			}
			if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("content type=%q", rec.Header().Get("Content-Type"))
			}
			for _, key := range []string{"Content-Length", "Content-Encoding", "Connection", "Grpc-Status", "X-Injected", "X-Test!", "X-Internal", "X-Trailer"} {
				if rec.Header().Get(key) != "" {
					t.Fatalf("unexpected %s=%q", key, rec.Header().Get(key))
				}
			}
		})
	}
}
