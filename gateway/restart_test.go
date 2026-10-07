package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestGatewayDeadlineRecoversWithoutAnotherEvent(t *testing.T) {
	var calls atomic.Int32
	gw := newLifecycleTestGateway(func(_ context.Context, _ *core.Core, _, addr string, _ *ReflectionSchema) (*ReflectionProxy, error) {
		if calls.Add(1) == 1 {
			return nil, context.DeadlineExceeded
		}
		return lifecycleTestProxy(addr, &ReflectionSchema{methods: map[string]*MethodDescriptor{}}, nil), nil
	})
	defer gw.Close()
	gw.reconcileServiceSnapshot(serviceSnapshot{"demo/a": {ServiceName: "demo", InstanceID: "a", GRPCAddr: "addr"}})
	eventuallyGateway(t, func() bool { return gw.connPool.Get("demo").Size() == 1 })
	if calls.Load() != 2 {
		t.Fatalf("attempts=%d, want 2", calls.Load())
	}
}
func TestGatewayCircuitExpiryRecoversWithoutAnotherEvent(t *testing.T) {
	var calls atomic.Int32
	gw := newLifecycleTestGateway(func(_ context.Context, _ *core.Core, _, addr string, _ *ReflectionSchema) (*ReflectionProxy, error) {
		calls.Add(1)
		return lifecycleTestProxy(addr, &ReflectionSchema{methods: map[string]*MethodDescriptor{}}, nil), nil
	})
	defer gw.Close()
	gw.circuitStates.Store("demo", &CircuitBreaker{state: circuitOpen, lastFailTime: time.Now().Add(-circuitCooldown + 200*time.Millisecond), failCount: 5})
	gw.reconcileServiceSnapshot(serviceSnapshot{"demo/a": {ServiceName: "demo", InstanceID: "a", GRPCAddr: "addr"}})
	eventuallyGateway(t, func() bool { return gw.connPool.Get("demo").Size() == 1 })
	if calls.Load() != 1 {
		t.Fatalf("connector attempts=%d, want 1", calls.Load())
	}
}
func TestGatewaySameAddressGenerationFencesLateCandidate(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls, closed atomic.Int32
	gw := newLifecycleTestGateway(func(_ context.Context, _ *core.Core, _, addr string, _ *ReflectionSchema) (*ReflectionProxy, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return lifecycleTestProxy(addr, &ReflectionSchema{methods: map[string]*MethodDescriptor{}}, func() { closed.Add(1) }), nil
		}
		return lifecycleTestProxy(addr, &ReflectionSchema{methods: map[string]*MethodDescriptor{}}, nil), nil
	})
	defer gw.Close()
	instance := registeredServiceInstance{ServiceName: "demo", InstanceID: "a", GRPCAddr: "same", Generation: "old"}
	gw.reconcileServiceSnapshot(serviceSnapshot{"demo/a": instance})
	<-started
	instance.Generation = "new"
	gw.reconcileServiceSnapshot(serviceSnapshot{"demo/a": instance})
	eventuallyGateway(t, func() bool { return gw.connPool.Get("demo").Size() == 1 })
	close(release)
	gw.taskWG.Wait()
	proxy := gw.connPool.Get("demo").Get()
	if proxy.generation != instanceGenerationToken(instance) || closed.Load() != 1 {
		t.Fatalf("generation=%s closes=%d", proxy.generation, closed.Load())
	}
}
func TestGatewayDeleteCancelsOngoingReflection(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	gw := newLifecycleTestGateway(func(ctx context.Context, _ *core.Core, _, _ string, _ *ReflectionSchema) (*ReflectionProxy, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	})
	defer gw.Close()
	gw.reconcileServiceSnapshot(serviceSnapshot{"demo/a": {ServiceName: "demo", InstanceID: "a", GRPCAddr: "addr"}})
	<-started
	gw.reconcileServiceSnapshot(serviceSnapshot{})
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("delete did not cancel reflection")
	}
}

// Uses isolated etcd and real reflection servers. The Gateway and watcher stay
// alive throughout slow startup, same-address process replacement and rollout.
func TestGatewayRealReflectionRestartAndRollingProtocols(t *testing.T) {
	client := restartEtcd(t)
	var failed atomic.Int32
	gw := newLifecycleTestGateway(func(ctx context.Context, app *core.Core, service, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
		proxy, err := defaultReflectionProxyConnector(ctx, app, service, addr, schema)
		if err != nil {
			failed.Add(1)
		}
		return proxy, err
	})
	t.Cleanup(func() { _ = gw.Close(); _ = gw.app.ShutdownWithTimeout(time.Second) })
	source := etcdServiceWatchSource{client: client, serviceRoot: "/restart/services/", launch: gw.launchTask}
	gw.launchTask(func() {
		runServiceWatchLoopFrom(gw.watchCtx, source, nil, gw.reconcileServiceSnapshot, func(ctx context.Context, _ int) bool {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(10 * time.Millisecond):
				return true
			}
		})
	})
	address := reserveGatewayURL(t).Host
	put := func(id, generation, addr string, ready bool) {
		value, err := json.Marshal(etcd.ServiceInfo{ID: id, Name: "demo", Addr: addr, Generation: generation, Ready: &ready, Metadata: map[string]string{"http_addr": "http://127.0.0.1:8081"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = client.Put(context.Background(), "/restart/services/demo/"+id, string(value)); err != nil {
			t.Fatal(err)
		}
	}
	put("a", "one", address, false)
	eventuallyGateway(t, func() bool { v, ok := gw.loadDesiredServiceSnapshot()["demo/a"]; return ok && v.NotReady })
	if gw.connPool.Get("demo") != nil || gw.getHTTPInstanceByID("demo", "a") != nil {
		t.Fatal("unready process installed upstream")
	}
	put("a", "one", address, true)
	eventuallyGateway(t, func() bool { return failed.Load() > 0 })
	recoveryStarted := time.Now()
	first := startRestartRPC(t, address, "one", []string{"GetOld"}, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	eventuallyGateway(t, func() bool { return gatewayRestartStatus(gw, "old") == 200 })
	t.Logf("recovery after first failed connection: %s", time.Since(recoveryStarted))
	first.Stop()
	second := startRestartRPC(t, address, "two", []string{"GetNew"}, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	defer second.Stop()
	restartStarted := time.Now()
	put("a", "two", address, true)
	eventuallyGateway(t, func() bool { return gatewayRestartStatus(gw, "new") == 200 && gatewayRestartStatus(gw, "old") == 404 })
	t.Logf("same-address restart route refresh: %s", time.Since(restartStarted))
	otherAddr := reserveGatewayURL(t).Host
	third := startRestartRPC(t, otherAddr, "three", []string{"GetNew", "GetExtra"}, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	defer third.Stop()
	put("b", "three", otherAddr, true)
	eventuallyGateway(t, func() bool { return gatewayRestartStatus(gw, "extra") == 200 })
	for range 10 {
		if status := gatewayRestartStatus(gw, "extra"); status != 200 {
			t.Fatalf("new RPC selected old instance, status=%d", status)
		}
	}
	// Replace one process by an incompatible codec. Overlap fails closed for the
	// affected method, while the method exclusive to b remains accessible.
	second.Stop()
	fourth := startRestartRPC(t, address, "four", []string{"GetNew"}, descriptorpb.FieldDescriptorProto_TYPE_INT64)
	defer fourth.Stop()
	put("a", "four", address, true)
	eventuallyGateway(t, func() bool { return gatewayRestartStatus(gw, "new") == 503 && gatewayRestartStatus(gw, "extra") == 200 })
	if _, err := client.Delete(context.Background(), "/restart/services/demo/a"); err != nil {
		t.Fatal(err)
	}
	eventuallyGateway(t, func() bool { return gatewayRestartStatus(gw, "new") == 200 })
	if _, err := client.Delete(context.Background(), "/restart/services/demo/b"); err != nil {
		t.Fatal(err)
	}
	eventuallyGateway(t, func() bool { return gatewayRestartStatus(gw, "new") == 503 && gatewayRestartStatus(gw, "extra") == 503 })
	movedAddr := reserveGatewayURL(t).Host
	moved := startRestartRPC(t, movedAddr, "five", []string{"GetNew"}, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	defer moved.Stop()
	movedStarted := time.Now()
	put("a", "five", movedAddr, true)
	eventuallyGateway(t, func() bool { return gatewayRestartStatus(gw, "new") == 200 && gatewayRestartStatus(gw, "extra") == 404 })
	t.Logf("changed-address restart after full offline: %s", time.Since(movedStarted))

}
func gatewayRestartStatus(gw *EtcdGateway, method string) int {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/restart/"+method, nil)
	rec := httptest.NewRecorder()
	gw.app.ServeHTTP(rec, req)
	return rec.Code
}
func eventuallyGateway(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("gateway did not converge before deadline")
}
func reserveGatewayURL(t *testing.T) url.URL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err = ln.Close(); err != nil {
		t.Fatal(err)
	}
	return url.URL{Scheme: "http", Host: addr}
}
func restartEtcd(t *testing.T) *clientv3.Client {
	t.Helper()
	cfg := embed.NewConfig()
	cfg.Dir = t.TempDir()
	cfg.LogLevel = "error"
	c, p := reserveGatewayURL(t), reserveGatewayURL(t)
	cfg.ListenClientUrls = []url.URL{c}
	cfg.AdvertiseClientUrls = []url.URL{c}
	cfg.ListenPeerUrls = []url.URL{p}
	cfg.AdvertisePeerUrls = []url.URL{p}
	cfg.InitialCluster = fmt.Sprintf("%s=%s", cfg.Name, p.String())
	server, err := embed.StartEtcd(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	select {
	case <-server.Server.ReadyNotify():
	case <-time.After(10 * time.Second):
		t.Fatal("embedded etcd readiness timed out")
	}
	cli, err := clientv3.New(clientv3.Config{Endpoints: []string{c.String()}, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}
func startRestartRPC(t *testing.T, address, value string, methods []string, fieldType descriptorpb.FieldDescriptorProto_Type) *grpc.Server {
	t.Helper()
	file := &descriptorpb.FileDescriptorProto{Name: proto.String("restart.proto"), Package: proto.String("api.v1"), Syntax: proto.String("proto3"), MessageType: []*descriptorpb.DescriptorProto{
		{Name: proto.String("Request"), Field: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("payload"), Number: proto.Int32(1), Type: &fieldType}}},
		{Name: proto.String("Response"), Field: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("value"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}}},
	}, Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("RestartService")}}}
	for _, method := range methods {
		file.Service[0].Method = append(file.Service[0].Method, &descriptorpb.MethodDescriptorProto{Name: proto.String(method), InputType: proto.String(".api.v1.Request"), OutputType: proto.String(".api.v1.Response")})
	}
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{file}})
	if err != nil {
		t.Fatal(err)
	}
	fd, err := files.FindFileByPath("restart.proto")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	desc := grpc.ServiceDesc{ServiceName: "api.v1.RestartService", HandlerType: (*interface{})(nil), Metadata: "restart.proto"}
	for _, method := range methods {
		method := method
		desc.Methods = append(desc.Methods, grpc.MethodDesc{MethodName: method, Handler: func(_ any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			req := dynamicpb.NewMessage(fd.Messages().ByName("Request"))
			if err := dec(req); err != nil {
				return nil, err
			}
			run := func(context.Context, any) (any, error) {
				resp := dynamicpb.NewMessage(fd.Messages().ByName("Response"))
				if err := protoJSONUnmarshal([]byte(`{"value":"`+value+`"}`), resp, nil); err != nil {
					return nil, err
				}
				return resp, nil
			}
			if interceptor != nil {
				return interceptor(ctx, req, &grpc.UnaryServerInfo{FullMethod: "/api.v1.RestartService/" + method}, run)
			}
			return run(ctx, req)
		}})
	}
	srv.RegisterService(&desc, struct{}{})
	reflectionpb.RegisterServerReflectionServer(srv, reflection.NewServer(reflection.ServerOptions{Services: srv, DescriptorResolver: files}))
	ln, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return srv
}

func TestMethodContractIncludesNestedMessagesAndEnums(t *testing.T) {
	build := func(kind descriptorpb.FieldDescriptorProto_Type, enumValue string) *ReflectionSchema {
		f := &descriptorpb.FileDescriptorProto{Name: proto.String("nested.proto"), Package: proto.String("nested"), Syntax: proto.String("proto3"), EnumType: []*descriptorpb.EnumDescriptorProto{{Name: proto.String("Kind"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String(enumValue), Number: proto.Int32(0)}}}}, MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Request"), Field: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("child"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".nested.Child")}}},
			{Name: proto.String("Child"), Field: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("data"), Number: proto.Int32(1), Type: &kind}, {Name: proto.String("kind"), Number: proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), TypeName: proto.String(".nested.Kind")}}},
		}}
		files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{f}})
		if err != nil {
			t.Fatal(err)
		}
		fd, err := files.FindFileByPath("nested.proto")
		if err != nil {
			t.Fatal(err)
		}
		request := fd.Messages().ByName("Request")
		return &ReflectionSchema{methods: map[string]*MethodDescriptor{"rpc": {Contract: methodContract(request, request)}}}
	}
	base := reflectionFingerprint(build(descriptorpb.FieldDescriptorProto_TYPE_STRING, "ZERO").methods)
	for _, other := range []*ReflectionSchema{build(descriptorpb.FieldDescriptorProto_TYPE_INT64, "ZERO"), build(descriptorpb.FieldDescriptorProto_TYPE_STRING, "NONE")} {
		if strings.EqualFold(base, reflectionFingerprint(other.methods)) {
			t.Fatal("transitive descriptor change did not alter codec fingerprint")
		}
	}
}

func TestGatewayFallbackCannotReplaceWatchGeneration(t *testing.T) {
	gw := newLifecycleTestGateway(nil)
	defer gw.Close()
	current := registeredServiceInstance{ServiceName: "demo", InstanceID: "a", GRPCAddr: "new-address", Generation: "new", NotReady: true}
	gw.setDesiredServiceSnapshot(serviceSnapshot{"demo/a": current})
	gw.addDesiredServiceInstance(registeredServiceInstance{ServiceName: "demo", InstanceID: "a", GRPCAddr: "old-address", Generation: "old"})
	if got := gw.loadDesiredServiceSnapshot()["demo/a"]; got != current {
		t.Fatalf("fallback replaced watch registration: %+v", got)
	}
	if gw.connPool.Get("demo") != nil {
		t.Fatal("fallback installed an upstream for an unready registration")
	}
}
