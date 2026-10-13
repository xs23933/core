package core

import (
	"context"
	"net"
	"testing"
	"time"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

func TestExtractClientInfoGatewayTrust(t *testing.T) {
	for _, tt := range []struct {
		name, peer                   string
		values                       []string
		configured, trusted, managed bool
		want                         string
	}{
		{"trusted IPv4", "10.0.0.2", []string{"203.0.113.10"}, true, true, true, "203.0.113.10"},
		{"trusted IPv6", "2001:db8::2", []string{"2001:db8::10"}, true, true, true, "2001:db8::10"},
		{"untrusted private caller", "10.0.0.3", []string{"6.6.6.6"}, true, false, true, "10.0.0.3"},
		{"untrusted loopback", "127.0.0.1", []string{"6.6.6.6"}, true, false, true, "127.0.0.1"},
		{"default private IPv4", "10.0.0.2", []string{"6.6.6.6"}, false, false, true, "6.6.6.6"},
		{"default private IPv6", "fd12::2", []string{"2001:db8::10"}, false, false, true, "2001:db8::10"},
		{"default loopback IPv4", "127.0.0.1", []string{"6.6.6.6"}, false, false, true, "6.6.6.6"},
		{"default loopback IPv6", "::1", []string{"2001:db8::10"}, false, false, true, "2001:db8::10"},
		{"default public denied", "203.0.113.2", []string{"6.6.6.6"}, false, false, true, "203.0.113.2"},
		{"default link local denied", "169.254.1.2", []string{"6.6.6.6"}, false, false, true, "169.254.1.2"},
		{"default malformed field", "10.0.0.2", []string{"invalid"}, false, false, true, "10.0.0.2"},
		{"default duplicates", "10.0.0.2", []string{"6.6.6.6", "7.7.7.7"}, false, false, true, "10.0.0.2"},
		{"unmanaged context", "10.0.0.2", []string{"6.6.6.6"}, true, true, false, "10.0.0.2"},
		{"missing peer", "", []string{"6.6.6.6"}, true, true, true, ""},
		{"missing field", "10.0.0.2", nil, true, true, true, "10.0.0.2"},
		{"empty field", "10.0.0.2", []string{""}, true, true, true, "10.0.0.2"},
		{"invalid field", "10.0.0.2", []string{"invalid"}, true, true, true, "10.0.0.2"},
		{"port rejected", "10.0.0.2", []string{"203.0.113.10:80"}, true, true, true, "10.0.0.2"},
		{"list rejected", "10.0.0.2", []string{"6.6.6.6, 7.7.7.7"}, true, true, true, "10.0.0.2"},
		{"duplicates rejected", "10.0.0.2", []string{"6.6.6.6", "7.7.7.7"}, true, true, true, "10.0.0.2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := New()
			t.Cleanup(app.stop)
			if tt.configured {
				if err := app.ConfigureGRPCServer(GRPCServerConfig{TrustGateway: func(ctx context.Context) bool {
					p, ok := peer.FromContext(ctx)
					return tt.trusted && ok && p.Addr != nil && p.Addr.String() == net.JoinHostPort(tt.peer, "9001")
				}}); err != nil {
					t.Fatal(err)
				}
			}
			if !tt.configured && tt.managed {
				if err := app.ConfigureGRPCServer(GRPCServerConfig{}); err != nil {
					t.Fatal(err)
				}
			}
			md := metadata.Pairs("x-forwarded-for", "9.9.9.9", "x-real-ip", "8.8.8.8", "user-agent", "test-agent")
			if tt.values != nil {
				md.Set(GatewayClientIPMetadata, tt.values...)
			}
			ctx := metadata.NewIncomingContext(context.Background(), md)
			if tt.peer != "" {
				ctx = peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(tt.peer), Port: 9001}})
			}
			check := func(ctx context.Context) {
				got := ExtractClientInfo(ctx)
				if ip := NewGRPCCtx(ctx).ClientIP(); ip != tt.want {
					t.Fatalf("GRPCCtx.ClientIP()=%s, want %s", ip, tt.want)
				}
				if got.IP != tt.want || got.UA != "test-agent" {
					t.Fatalf("got %+v, want IP=%s", got, tt.want)
				}
			}
			if tt.managed {
				_, err := app.grpcResponseContext(ctx, nil, nil, func(ctx context.Context, _ any) (any, error) { check(ctx); return nil, nil })
				if err != nil {
					t.Fatal(err)
				}
			} else {
				check(ctx)
			}
		})
	}
}

func TestExtractClientInfoWithoutGRPCConfigUsesPrivateFallback(t *testing.T) {
	app := New()
	t.Cleanup(app.stop)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(GatewayClientIPMetadata, "203.0.113.10"))
	ctx = peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.2"), Port: 9001}})
	_, err := app.grpcResponseContext(ctx, nil, nil, func(ctx context.Context, _ any) (any, error) {
		if got := ExtractClientInfo(ctx).IP; got != "203.0.113.10" {
			t.Fatalf("IP=%s, want fallback to private peer trust", got)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type clientIPHealthServer struct {
	healthpb.UnimplementedHealthServer
	infos chan *ClientInfo
}

func (s *clientIPHealthServer) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	s.infos <- ExtractClientInfo(ctx)
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}
func (s *clientIPHealthServer) Watch(_ *healthpb.HealthCheckRequest, stream healthpb.Health_WatchServer) error {
	s.infos <- ExtractClientInfo(stream.Context())
	return stream.Send(&healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING})
}
func TestGRPCGatewayClientIPUnaryAndStream(t *testing.T) {
	for _, trust := range []bool{false, true} {
		name := "untrusted"
		if trust {
			name = "trusted"
		}
		t.Run(name, func(t *testing.T) {
			app := New()
			t.Cleanup(app.stop)
			if err := app.ConfigureGRPCServer(GRPCServerConfig{TrustGateway: func(ctx context.Context) bool {
				p, ok := peer.FromContext(ctx)
				return trust && ok && p.Addr != nil && p.Addr.String() == "bufconn"
			}}); err != nil {
				t.Fatal(err)
			}
			srv := &clientIPHealthServer{infos: make(chan *ClientInfo, 2)}
			client := startBufconnHealthClient(t, app.GetGRPCServer(), srv)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
			defer cancel()
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(GatewayClientIPMetadata, "203.0.113.10", "x-forwarded-for", "6.6.6.6"))
			if _, err := client.Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
				t.Fatal(err)
			}
			stream, err := client.Watch(ctx, &healthpb.HealthCheckRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Recv(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				info := <-srv.infos
				want := ""
				if trust {
					want = "203.0.113.10"
				}
				if info.IP != want {
					t.Fatalf("IP=%s, want %s", info.IP, want)
				}
			}
		})
	}
}
