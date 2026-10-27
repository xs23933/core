package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestServicePoolWithdrawalDrainsActiveRPC(t *testing.T) {
	for _, action := range []string{"delete", "not-ready", "generation", "address"} {
		t.Run(action, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			server := grpc.NewServer()
			healthpb.RegisterHealthServer(server, &responseMetadataServer{write: func(ctx context.Context, _ *healthpb.HealthCheckRequest) error {
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}})
			old := metadataTestProxy(t, server)
			connections := newConnectionPoolWithConnector(func(_ context.Context, _ *core.Core, _, addr string, _ *ReflectionSchema) (*ReflectionProxy, error) {
				if addr == old.addr {
					return old, nil
				}
				return lifecycleTestProxy(addr, old.schema, nil), nil
			})
			pool := connections.GetOrCreate(nil, "drain")
			t.Cleanup(connections.Close)
			if _, _, err := pool.AddOrUpdateInstance("a", old.addr); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				body, err := old.Invoke(ctx, "/grpc.health.v1.Health/Check", []byte(`{}`))
				if err == nil && !strings.Contains(string(body), "SERVING") {
					err = status.Error(codes.Internal, "missing response")
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("RPC did not enter the real server")
			}
			switch action {
			case "delete":
				pool.RemoveInstance("a")
			case "not-ready":
				pool.MarkInstanceUndesired("a")
				pool.RemoveInstanceIfUndesired("a")
			case "generation":
				pool.SetDesiredGeneration("a", old.addr, "next")
			case "address":
				if _, _, err := pool.AddOrUpdateInstance("a", "next-address"); err != nil {
					t.Fatal(err)
				}
			}
			if pool.GetForMethod("/grpc.health.v1.Health/Check", nil) == old || pool.Get() == old {
				t.Fatal("withdrawn connection remains selectable for new business")
			}
			select {
			case err := <-done:
				t.Fatalf("withdrawal interrupted an unfinished RPC: %v", err)
			case <-time.After(80 * time.Millisecond):
			}
			if _, err := old.Invoke(ctx, "/grpc.health.v1.Health/Check", []byte(`{}`)); status.Code(err) != codes.Unavailable {
				t.Fatalf("stale proxy admitted new RPC: %v", err)
			}
			releaseOnce.Do(func() { close(release) })
			if err := <-done; err != nil {
				t.Fatalf("in-flight RPC did not finish: %v", err)
			}
			eventuallyGateway(t, func() bool { return old.conn.GetState() == connectivity.Shutdown })
			if action == "delete" || action == "not-ready" {
				eventuallyGateway(t, func() bool { return connections.Get("drain") == nil })
			}
		})
	}
}

func TestServicePoolCloseIncludesRetiringConnections(t *testing.T) {
	entered := make(chan struct{})
	server := grpc.NewServer()
	healthpb.RegisterHealthServer(server, &responseMetadataServer{write: func(ctx context.Context, _ *healthpb.HealthCheckRequest) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}})
	proxy := metadataTestProxy(t, server)
	pool := newServicePoolWithConnector(nil, "drain", func(context.Context, *core.Core, string, string, *ReflectionSchema) (*ReflectionProxy, error) {
		return proxy, nil
	})
	if _, _, err := pool.AddOrUpdateInstance("a", proxy.addr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := proxy.Invoke(ctx, "/grpc.health.v1.Health/Check", []byte(`{}`)); done <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("RPC did not start")
	}
	pool.RemoveInstance("a")
	if pool.markClosedIfRemovable() {
		t.Fatal("retiring connection lost its pool owner")
	}
	first := proxy.Drain()
	if next := proxy.Drain(); first != next {
		t.Fatal("repeated drain did not share completion")
	}
	pool.Close()
	pool.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("explicit final close did not stop the connection")
		}
	case <-ctx.Done():
		t.Fatal("final pool close lost the retiring connection")
	}
	select {
	case <-first:
	default:
		t.Fatal("final close did not complete drain notification")
	}
}

func TestReflectionProxyDrainWaitsForEveryActiveRPC(t *testing.T) {
	entered := make(chan struct{}, 2)
	releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var once [2]sync.Once
	finish := func(index int) { once[index].Do(func() { close(releases[index]) }) }
	t.Cleanup(func() { finish(0); finish(1) })
	server := grpc.NewServer()
	healthpb.RegisterHealthServer(server, &responseMetadataServer{write: func(ctx context.Context, req *healthpb.HealthCheckRequest) error {
		index := 0
		if req.Service == "second" {
			index = 1
		}
		entered <- struct{}{}
		select {
		case <-releases[index]:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	proxy := metadataTestProxy(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := []chan error{make(chan error, 1), make(chan error, 1)}
	for index, body := range []string{`{}`, `{"service":"second"}`} {
		go func() {
			_, err := proxy.Invoke(ctx, "/grpc.health.v1.Health/Check", []byte(body))
			results[index] <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("both RPCs did not start")
		}
	}
	drained := proxy.Drain()
	finish(0)
	if err := <-results[0]; err != nil {
		t.Fatalf("first active RPC failed: %v", err)
	}
	select {
	case <-drained:
		t.Fatal("drain closed while another RPC was still active")
	default:
	}
	finish(1)
	if err := <-results[1]; err != nil {
		t.Fatalf("last active RPC failed: %v", err)
	}
	select {
	case <-drained:
	case <-ctx.Done():
		t.Fatal("completed RPCs did not close the connection")
	}
}

func TestHTTPInstanceRemovalPreservesActiveRequest(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			_, _ = w.Write([]byte("finished"))
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(finish)
	app := core.New()
	gw := &EtcdGateway{app: app, connPool: NewConnectionPool()}
	if err := gw.addHTTPInstance("drain-http", "a", upstream.URL); err != nil {
		t.Fatal(err)
	}
	gw.registerRoute(&Route{Protocol: RouteProtocolHTTP, Method: http.MethodPost, Path: "/drain", ServiceName: "drain-http", Enabled: true})
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { app.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/drain", nil)); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP request did not start")
	}
	gw.removeHTTPInstance("drain-http", "a")
	newResponse := httptest.NewRecorder()
	app.ServeHTTP(newResponse, httptest.NewRequest(http.MethodPost, "/drain", nil))
	if newResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("new HTTP request accepted after withdrawal: %d", newResponse.Code)
	}
	finish()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("active HTTP request did not finish")
	}
	if response.Code != http.StatusOK || response.Body.String() != "finished" {
		t.Fatalf("active HTTP request interrupted: %d %s", response.Code, response.Body.String())
	}
}
