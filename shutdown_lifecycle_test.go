package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

type lifecycleBlockingHealth struct {
	healthpb.UnimplementedHealthServer
	started chan struct{}
	release chan struct{}
}

func (s *lifecycleBlockingHealth) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	close(s.started)
	<-s.release
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func startLifecycleBlockingRPC(t *testing.T, app *Core) (*lifecycleBlockingHealth, <-chan struct{}) {
	t.Helper()
	service := &lifecycleBlockingHealth{started: make(chan struct{}), release: make(chan struct{})}
	app.RegisterGRPCService(func(g *grpc.Server) { healthpb.RegisterHealthServer(g, service) })
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(func() { _ = listener.Close() })
	go func() { _ = app.GetGRPCServer().Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///lifecycle", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		_, _ = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	}()
	select {
	case <-service.started:
	case <-ctx.Done():
		t.Fatal("RPC did not start")
	}
	return service, callDone
}

func TestShutdownDeadlineBoundsGRPCAndRepeats(t *testing.T) {
	app := New(Options{"debug": false})
	service, callDone := startLifecycleBlockingRPC(t, app)
	started := time.Now()
	err := app.ShutdownWithTimeout(40 * time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown exceeded deadline: %v", elapsed)
	}
	close(service.release)
	<-callDone
	select {
	case <-app.shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish")
	}
	if err := app.ShutdownWithTimeout(time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("repeated shutdown lost original result: %v", err)
	}
}

func TestShutdownWithdrawsBeforeDrainAndClosesDependenciesAfterRPC(t *testing.T) {
	app := New(Options{"debug": false})
	service, _ := startLifecycleBlockingRPC(t, app)
	withdrawn, cleaned := make(chan struct{}), make(chan struct{})
	app.etcdRegistryCleanup = func() { close(withdrawn) }
	app.OnShutdown(func() { close(cleaned) })
	result := make(chan error, 1)
	go func() { result <- app.ShutdownWithTimeout(time.Second) }()
	select {
	case <-withdrawn:
	case <-time.After(time.Second):
		t.Fatal("service not withdrawn")
	}
	select {
	case <-cleaned:
		t.Fatal("dependency closed before active RPC drained")
	default:
	}
	close(service.release)
	if err := <-result; err != nil {
		t.Fatalf("graceful shutdown: %v", err)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("dependency cleanup missing")
	}
}

func TestShutdownDeadlineBoundsBlockedLegacyHook(t *testing.T) {
	app := New(Options{"debug": false})
	started, release := make(chan struct{}), make(chan struct{})
	app.OnShutdown(func() { close(started); <-release })
	result := make(chan error, 1)
	go func() { result <- app.ShutdownWithTimeout(40 * time.Millisecond) }()
	<-started
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked hook result: %v", err)
	}
	close(release)
	select {
	case <-app.shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("released hook cleanup missing")
	}
}

func TestShutdownDeadlineClosesHTTPStream(t *testing.T) {
	app := New(Options{"debug": false})
	started, stopped := make(chan struct{}), make(chan struct{})
	app.GET("/events", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	app.Server.Handler = app
	server := httptest.NewUnstartedServer(app)
	server.Config = app.Server
	server.Start()
	t.Cleanup(server.Close)
	response, err := server.Client().Get(server.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	<-started
	if err := app.ShutdownWithTimeout(40 * time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream shutdown: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("HTTP stream still active")
	}
}

func TestStartupPublishesReadyAfterRouteCatalog(t *testing.T) {
	oldTLS, oldRoutes, oldReady := prepareCoreTLSForServe, syncCoreAutomaticHTTPRoutesForServe, markCoreEtcdRegistryReadyForServe
	t.Cleanup(func() {
		prepareCoreTLSForServe, syncCoreAutomaticHTTPRoutesForServe, markCoreEtcdRegistryReadyForServe = oldTLS, oldRoutes, oldReady
	})
	var stage atomic.Int32
	prepareCoreTLSForServe = func(*Core) error { stage.Store(1); return nil }
	syncCoreAutomaticHTTPRoutesForServe = func(*Core, context.Context) error {
		if stage.Load() != 1 {
			t.Fatal("TLS preparation order")
		}
		stage.Store(2)
		return nil
	}
	markCoreEtcdRegistryReadyForServe = func(*Core, context.Context) error {
		if stage.Load() != 2 {
			t.Fatal("ready published before route catalog")
		}
		stage.Store(3)
		return nil
	}
	app := New(Options{"debug": false})
	app.modName = "lifecycle-ready-test"
	if err := app.prepareServeOnce(); err != nil {
		t.Fatal(err)
	}
	app.shutdown()
	if stage.Load() != 3 {
		t.Fatal("ready not published")
	}
	stage.Store(0)
	syncCoreAutomaticHTTPRoutesForServe = func(*Core, context.Context) error { return errors.New("catalog publication failed") }
	app = New(Options{"debug": false})
	app.modName = "lifecycle-ready-test"
	if err := app.prepareServeOnce(); err == nil {
		t.Fatal("startup accepted failed catalog")
	}
	app.shutdown()
	if stage.Load() != 1 {
		t.Fatal("ready published after failed startup")
	}
}

func TestShutdownLaterDeadlineDoesNotInterruptOriginalDrain(t *testing.T) {
	app := New(Options{"debug": false})
	service, callDone := startLifecycleBlockingRPC(t, app)
	withdrawn := make(chan struct{})
	app.etcdRegistryCleanup = func() { close(withdrawn) }
	result := make(chan error, 1)
	go func() { result <- app.ShutdownWithTimeout(time.Second) }()
	<-withdrawn
	if err := app.ShutdownWithTimeout(20 * time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("later deadline: %v", err)
	}
	select {
	case <-callDone:
		t.Fatal("later deadline interrupted original RPC drain")
	default:
	}
	close(service.release)
	if err := <-result; err != nil {
		t.Fatalf("original drain: %v", err)
	}
	if err := app.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatalf("repeat: %v", err)
	}
}

func TestShutdownClosesHTTP3OnSuccessfulDrain(t *testing.T) {
	app := New(Options{"debug": false})
	tlsServer := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(tlsServer.Close)
	app.h3 = &http3.Server{TLSConfig: tlsServer.TLS}
	t.Cleanup(func() { _ = app.h3.Close() })
	if err := app.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	result := make(chan error, 1)
	go func() { result <- app.h3.Serve(conn) }()
	select {
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("HTTP3 serve after shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP3 accepted listener after successful shutdown")
	}
}
