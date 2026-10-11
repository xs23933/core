package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestGatewayGRPCRequestTimeout(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		timeout, parentTimeout time.Duration
		want                   int
	}{
		{name: "configured deadline cancels slow RPC", timeout: 20 * time.Millisecond, want: http.StatusGatewayTimeout},
		{name: "longer budget permits slow RPC", timeout: time.Second, want: http.StatusOK},
		{name: "shorter parent still cancels", timeout: time.Second, parentTimeout: 20 * time.Millisecond, want: http.StatusGatewayTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := grpc.NewServer()
			healthpb.RegisterHealthServer(srv, &responseMetadataServer{write: func(ctx context.Context, _ *healthpb.HealthCheckRequest) error {
				timer := time.NewTimer(100 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}})
			proxy := metadataTestProxy(t, srv)
			app := core.New()
			gw := &EtcdGateway{app: app, config: &Config{GRPCRequestTimeout: tc.timeout}, connPool: NewConnectionPool()}
			pool := gw.connPool.GetOrCreate(app, "metadata")
			pool.storeState(servicePoolState{byID: map[string]*ReflectionProxy{"one": proxy}, all: []*ReflectionProxy{proxy}, methods: map[string][]*ReflectionProxy{"/grpc.health.v1.Health/Check": {proxy}}})
			gw.registerRoute(&Route{Method: http.MethodPost, Path: "/check", ServiceName: "metadata", GRPCMethod: "/grpc.health.v1.Health/Check"})
			req := httptest.NewRequest(http.MethodPost, "/check", strings.NewReader(`{}`))
			if tc.parentTimeout > 0 {
				ctx, cancel := context.WithTimeout(req.Context(), tc.parentTimeout)
				defer cancel()
				req = req.WithContext(ctx)
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestGatewayGRPCRequestTimeoutDefaults(t *testing.T) {
	for _, value := range []time.Duration{0, -time.Second, time.Minute} {
		config := &Config{GRPCRequestTimeout: value}
		applyGatewayConfigDefaults(config)
		want := value
		if want <= 0 {
			want = 10 * time.Second
		}
		gw := &EtcdGateway{config: config}
		if got := gw.grpcRequestTimeout(); got != want {
			t.Fatalf("timeout=%s want=%s", got, want)
		}
	}
	var gw *EtcdGateway
	if gw.grpcRequestTimeout() != 10*time.Second {
		t.Fatal("nil gateway must retain default")
	}
}
