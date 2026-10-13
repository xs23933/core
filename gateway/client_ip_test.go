package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xs23933/core/v3"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

func TestGatewayClientIPOverridesSpoofedMetadata(t *testing.T) {
	for _, tt := range []struct{ name, remote, xff, real, want string }{
		{"public ignores forged XFF", "203.0.113.10:5000", "6.6.6.6", "7.7.7.7", "203.0.113.10"},
		{"trusted proxy chain", "10.0.0.2:5000", "6.6.6.6, 203.0.113.10", "7.7.7.7", "203.0.113.10"},
		{"real IP fallback", "10.0.0.2:5000", "", "203.0.113.10", "203.0.113.10"},
		{"invalid connection", "invalid", "6.6.6.6", "7.7.7.7", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := core.New()
			if err := service.ConfigureGRPCServer(core.GRPCServerConfig{TrustGateway: func(ctx context.Context) bool {
				p, ok := peer.FromContext(ctx)
				return ok && p.Addr != nil && p.Addr.String() == "bufconn"
			}}); err != nil {
				t.Fatal(err)
			}
			captured := make(chan string, 1)
			healthpb.RegisterHealthServer(service.GetGRPCServer(), &responseMetadataServer{write: func(ctx context.Context, _ *healthpb.HealthCheckRequest) error {
				md, _ := metadata.FromIncomingContext(ctx)
				values := md.Get(core.GatewayClientIPMetadata)
				if tt.want == "" {
					if len(values) != 0 {
						t.Errorf("forged value retained: %v", values)
					}
				} else if len(values) != 1 || values[0] != tt.want {
					t.Errorf("metadata=%v, want %s", values, tt.want)
				}
				captured <- core.ExtractClientInfo(ctx).IP
				return nil
			}})
			proxy := metadataTestProxy(t, service.GetGRPCServer())
			app := core.New(core.Options{"trusted_proxies": []string{"10.0.0.2/32"}})
			app.Use(func(c core.Ctx) error { c.Set("CORE-CLIENT-IP", "8.8.8.8"); return c.Next() })
			gw := &EtcdGateway{app: app, connPool: NewConnectionPool()}
			pool := gw.connPool.GetOrCreate(app, "metadata")
			pool.storeState(servicePoolState{byID: map[string]*ReflectionProxy{"one": proxy}, all: []*ReflectionProxy{proxy}, methods: map[string][]*ReflectionProxy{"/grpc.health.v1.Health/Check": {proxy}}})
			gw.registerRoute(&Route{Method: http.MethodPost, Path: "/check", ServiceName: "metadata", GRPCMethod: "/grpc.health.v1.Health/Check"})
			req := httptest.NewRequest(http.MethodPost, "/check", strings.NewReader(`{}`))
			req.RemoteAddr = tt.remote
			req.Header.Add("Core-Client-IP", "4.4.4.4")
			req.Header.Add("Core-Client-IP", "5.5.5.5")
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			req.Header.Set("X-Real-IP", tt.real)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := <-captured; got != tt.want {
				t.Fatalf("ExtractClientInfo=%s, want %s", got, tt.want)
			}
		})
	}
}
