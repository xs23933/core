package core

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestGRPCConfiguredReceiveLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		want  codes.Code
	}{
		{"default", 0, codes.ResourceExhausted}, {"configured", 8 << 20, codes.OK}, {"bounded", 1 << 20, codes.ResourceExhausted}, {"negative", -1, codes.ResourceExhausted}, {"above_max", 65 << 20, codes.ResourceExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := New(Options{"grpc": Options{"max_recv_message_bytes": tc.limit}})
			client := startBufconnHealthClient(t, app.GetGRPCServer(), &recordingHealthServer{})
			_, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{Service: strings.Repeat("x", 5<<20)})
			if status.Code(err) != tc.want {
				t.Fatalf("传输结果=%v，期望%s", err, tc.want)
			}
		})
	}
}

func TestGRPCReceiveLimitConfigurationBounds(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  int
	}{
		{nil, 4 << 20}, {0, 4 << 20}, {-1, 4 << 20}, {65 << 20, 4 << 20}, {"invalid", 4 << 20}, {1, 1}, {64 << 20, 64 << 20},
	} {
		app := New(Options{"grpc": Options{"max_recv_message_bytes": tc.value}})
		if got := app.GRPCMaxRecvMessageBytes(); got != tc.want {
			t.Fatalf("配置%v得到%d，期望%d", tc.value, got, tc.want)
		}
	}
	var app *Core
	if app.GRPCMaxRecvMessageBytes() != 4<<20 {
		t.Fatal("空应用未保留默认限制")
	}
}
