package core

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestConfigureGRPCServerLifecycle(t *testing.T) {
	app := New()
	unary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(ctx, req)
	}
	stream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, ss)
	}
	config := GRPCServerConfig{
		UnaryInterceptors:  []grpc.UnaryServerInterceptor{unary},
		StreamInterceptors: []grpc.StreamServerInterceptor{stream},
	}
	if err := app.ConfigureGRPCServer(config); err != nil {
		t.Fatalf("configure gRPC server: %v", err)
	}
	config.UnaryInterceptors[0] = nil
	config.StreamInterceptors[0] = nil
	if app.grpcConfig.UnaryInterceptors[0] == nil || app.grpcConfig.StreamInterceptors[0] == nil {
		t.Fatal("ConfigureGRPCServer retained caller-owned interceptor slices")
	}
	if err := app.ConfigureGRPCServer(GRPCServerConfig{}); !errors.Is(err, ErrGRPCServerAlreadyConfigured) {
		t.Fatalf("second configure error = %v, want %v", err, ErrGRPCServerAlreadyConfigured)
	}

	empty := New()
	if err := empty.ConfigureGRPCServer(GRPCServerConfig{}); err != nil {
		t.Fatalf("empty configure: %v", err)
	}

	late := New()
	_ = late.GetGRPCServer()
	if err := late.ConfigureGRPCServer(GRPCServerConfig{}); !errors.Is(err, ErrGRPCServerAlreadyInitialized) {
		t.Fatalf("late configure error = %v, want %v", err, ErrGRPCServerAlreadyInitialized)
	}
}

func TestGRPCInterceptorChains(t *testing.T) {
	t.Run("unary order", func(t *testing.T) {
		calls := make([]string, 0, 3)
		app := New()
		if err := app.ConfigureGRPCServer(GRPCServerConfig{UnaryInterceptors: []grpc.UnaryServerInterceptor{
			recordingUnaryInterceptor("unary-1", &calls),
			recordingUnaryInterceptor("unary-2", &calls),
		}}); err != nil {
			t.Fatalf("configure gRPC server: %v", err)
		}
		client := startBufconnHealthClient(t, app.GetGRPCServer(), &recordingHealthServer{calls: &calls})
		if _, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
			t.Fatalf("health check: %v", err)
		}
		if want := []string{"unary-1", "unary-2", "unary-handler"}; !reflect.DeepEqual(calls, want) {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	})

	t.Run("stream order", func(t *testing.T) {
		calls := make([]string, 0, 3)
		app := New()
		if err := app.ConfigureGRPCServer(GRPCServerConfig{StreamInterceptors: []grpc.StreamServerInterceptor{
			recordingStreamInterceptor("stream-1", &calls),
			recordingStreamInterceptor("stream-2", &calls),
		}}); err != nil {
			t.Fatalf("configure gRPC server: %v", err)
		}
		client := startBufconnHealthClient(t, app.GetGRPCServer(), &recordingHealthServer{calls: &calls})
		watch, err := client.Watch(context.Background(), &healthpb.HealthCheckRequest{})
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
		if _, err := watch.Recv(); err != nil {
			t.Fatalf("receive health status: %v", err)
		}
		if want := []string{"stream-1", "stream-2", "stream-handler"}; !reflect.DeepEqual(calls, want) {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	})

	t.Run("core error wrapper remains active", func(t *testing.T) {
		app := New()
		if err := app.ConfigureGRPCServer(GRPCServerConfig{}); err != nil {
			t.Fatalf("configure gRPC server: %v", err)
		}
		client := startBufconnHealthClient(t, app.GetGRPCServer(), &recordingHealthServer{unaryErr: errors.New("boom")})
		_, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
		if status.Code(err) != codes.Internal {
			t.Fatalf("health check code = %s, err = %v", status.Code(err), err)
		}
	})
}

type recordingHealthServer struct {
	healthpb.UnimplementedHealthServer
	calls    *[]string
	unaryErr error
}

func (server *recordingHealthServer) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	if server.calls != nil {
		*server.calls = append(*server.calls, "unary-handler")
	}
	if server.unaryErr != nil {
		return nil, server.unaryErr
	}
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func (server *recordingHealthServer) Watch(_ *healthpb.HealthCheckRequest, stream grpc.ServerStreamingServer[healthpb.HealthCheckResponse]) error {
	if server.calls != nil {
		*server.calls = append(*server.calls, "stream-handler")
	}
	return stream.Send(&healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING})
}

func recordingUnaryInterceptor(name string, calls *[]string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		*calls = append(*calls, name)
		return handler(ctx, req)
	}
}

func recordingStreamInterceptor(name string, calls *[]string) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		*calls = append(*calls, name)
		return handler(srv, stream)
	}
}

func startBufconnHealthClient(t *testing.T, server *grpc.Server, healthServer healthpb.HealthServer, options ...grpc.DialOption) healthpb.HealthClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	healthpb.RegisterHealthServer(server, healthServer)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	dialOptions := []grpc.DialOption{
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	dialOptions = append(dialOptions, options...)
	conn, err := grpc.NewClient("passthrough:///bufnet", dialOptions...)
	if err != nil {
		t.Fatalf("create gRPC client: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return healthpb.NewHealthClient(conn)
}
