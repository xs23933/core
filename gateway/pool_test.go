package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

func TestServicePoolSharesSchemaAndUsesStableRoundRobin(t *testing.T) {
	var mu sync.Mutex
	var schemaBuilds int
	var connections []*grpc.ClientConn
	closed := make(map[string]int)
	connector := func(_ context.Context, _ *core.Core, _, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
		mu.Lock()
		defer mu.Unlock()
		if schema == nil {
			schemaBuilds++
			schema = &ReflectionSchema{methods: map[string]*MethodDescriptor{}}
		}
		conn := &grpc.ClientConn{}
		connections = append(connections, conn)
		return &ReflectionProxy{
			conn:   conn,
			schema: schema,
			addr:   addr,
			ready:  func() bool { return true },
			closeFn: func() error {
				mu.Lock()
				closed[addr]++
				mu.Unlock()
				return nil
			},
		}, nil
	}

	pool := newServicePoolWithConnector(nil, "billing", connector)
	pool.SetDesiredInstance("instance-b", "addr-b")
	proxyB, changed, err := pool.AddOrUpdateInstanceContext(context.Background(), "instance-b", "addr-b")
	if err != nil || !changed {
		t.Fatalf("connect B changed=%v err=%v", changed, err)
	}
	pool.SetDesiredInstance("instance-a", "addr-a")
	proxyA, changed, err := pool.AddOrUpdateInstanceContext(context.Background(), "instance-a", "addr-a")
	if err != nil || !changed {
		t.Fatalf("connect A changed=%v err=%v", changed, err)
	}

	if schemaBuilds != 1 {
		t.Fatalf("schema builds = %d, want 1", schemaBuilds)
	}
	if len(connections) != 2 || connections[0] == connections[1] {
		t.Fatalf("connections = %#v, want two distinct connections", connections)
	}
	if proxyA.schema == nil || proxyA.schema != proxyB.schema || pool.Schema() != proxyA.schema {
		t.Fatalf("schema pointers A=%p B=%p pool=%p, want one shared schema", proxyA.schema, proxyB.schema, pool.Schema())
	}

	for index, want := range []*ReflectionProxy{proxyA, proxyB, proxyA} {
		if got := pool.Get(); got != want {
			t.Fatalf("selection %d = %p (%s), want %p (%s)", index, got, got.addr, want, want.addr)
		}
	}

	pool.RemoveInstance("instance-a")
	for range 3 {
		if got := pool.Get(); got != proxyB {
			t.Fatalf("selection after removing A = %p, want B %p", got, proxyB)
		}
	}
	if pool.Schema() == nil {
		t.Fatal("schema cleared while B is still installed")
	}
	pool.RemoveInstance("instance-b")
	if pool.Schema() != nil {
		t.Fatalf("schema after final removal = %p, want nil", pool.Schema())
	}
	if closed["addr-a"] != 1 || closed["addr-b"] != 1 {
		t.Fatalf("close counts = %#v, want each connection closed once", closed)
	}
}

func TestServicePoolGetExceptReturnsReadyAlternate(t *testing.T) {
	schema := &ReflectionSchema{methods: map[string]*MethodDescriptor{}}
	connector := func(_ context.Context, _ *core.Core, _, addr string, _ *ReflectionSchema) (*ReflectionProxy, error) {
		return &ReflectionProxy{
			conn:    &grpc.ClientConn{},
			schema:  schema,
			addr:    addr,
			ready:   func() bool { return true },
			closeFn: func() error { return nil },
		}, nil
	}
	pool := newServicePoolWithConnector(nil, "billing", connector)
	first, _, err := pool.AddOrUpdateInstance("a", "addr-a")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := pool.AddOrUpdateInstance("b", "addr-b")
	if err != nil {
		t.Fatal(err)
	}

	if got := pool.GetExcept(first); got != second {
		t.Fatalf("alternate = %p, want second proxy %p", got, second)
	}
	if got := pool.GetExcept(second); got != first {
		t.Fatalf("alternate = %p, want first proxy %p", got, first)
	}

	pool.RemoveInstance("b")
	if got := pool.GetExcept(first); got != nil {
		t.Fatalf("alternate with one instance = %p, want nil", got)
	}
}

func TestServicePoolAddressUpdateReusesSchemaAndClosesOnlyOldConnection(t *testing.T) {
	var mu sync.Mutex
	var schemaBuilds int
	closed := make(map[string]int)
	connector := func(_ context.Context, _ *core.Core, _, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
		if schema == nil {
			schemaBuilds++
			schema = &ReflectionSchema{methods: map[string]*MethodDescriptor{}}
		}
		return &ReflectionProxy{
			conn:   &grpc.ClientConn{},
			schema: schema,
			addr:   addr,
			ready:  func() bool { return true },
			closeFn: func() error {
				mu.Lock()
				closed[addr]++
				mu.Unlock()
				return nil
			},
		}, nil
	}
	pool := newServicePoolWithConnector(nil, "billing", connector)
	pool.SetDesiredInstance("billing-1", "addr-old")
	oldProxy, _, err := pool.AddOrUpdateInstanceContext(context.Background(), "billing-1", "addr-old")
	if err != nil {
		t.Fatal(err)
	}
	pool.SetDesiredInstance("billing-1", "addr-new")
	newProxy, changed, err := pool.AddOrUpdateInstanceContext(context.Background(), "billing-1", "addr-new")
	if err != nil || !changed {
		t.Fatalf("update changed=%v err=%v", changed, err)
	}

	if oldProxy == newProxy || oldProxy.schema != newProxy.schema || schemaBuilds != 1 {
		t.Fatalf("old=%p new=%p schema builds=%d schemas=%p/%p", oldProxy, newProxy, schemaBuilds, oldProxy.schema, newProxy.schema)
	}
	if closed["addr-old"] != 1 || closed["addr-new"] != 0 {
		t.Fatalf("close counts after update = %#v, want only old connection closed", closed)
	}
	if got := pool.Get(); got != newProxy {
		t.Fatalf("pool.Get() = %p, want updated proxy %p", got, newProxy)
	}
}

func TestServicePoolConnectsDifferentIDsConcurrentlyAfterSchemaExists(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	connector := func(ctx context.Context, _ *core.Core, _, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
		if schema == nil {
			schema = &ReflectionSchema{methods: map[string]*MethodDescriptor{}}
			return &ReflectionProxy{conn: &grpc.ClientConn{}, schema: schema, addr: addr, ready: func() bool { return true }, closeFn: func() error { return nil }}, nil
		}
		started <- addr
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		return &ReflectionProxy{conn: &grpc.ClientConn{}, schema: schema, addr: addr, ready: func() bool { return true }, closeFn: func() error { return nil }}, nil
	}
	pool := newServicePoolWithConnector(nil, "billing", connector)
	pool.SetDesiredInstance("instance-a", "addr-a")
	if _, _, err := pool.AddOrUpdateInstanceContext(context.Background(), "instance-a", "addr-a"); err != nil {
		t.Fatal(err)
	}
	pool.SetDesiredInstance("instance-b", "addr-b")
	pool.SetDesiredInstance("instance-c", "addr-c")
	errs := make(chan error, 2)
	for _, instance := range []struct{ id, addr string }{{"instance-b", "addr-b"}, {"instance-c", "addr-c"}} {
		instance := instance
		go func() {
			_, _, err := pool.AddOrUpdateInstanceContext(context.Background(), instance.id, instance.addr)
			errs <- err
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("different instance dials were serialized after schema publication")
		}
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestServicePoolConcurrentInitialIDsBuildOneSchema(t *testing.T) {
	callersReady := make(chan struct{}, 2)
	start := make(chan struct{})
	buildStarted := make(chan struct{})
	finishBuild := make(chan struct{})
	var schemaBuilds atomic.Int32
	var connects atomic.Int32
	connector := func(ctx context.Context, _ *core.Core, _, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
		connects.Add(1)
		if schema == nil {
			if schemaBuilds.Add(1) == 1 {
				close(buildStarted)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-finishBuild:
			}
			schema = &ReflectionSchema{methods: map[string]*MethodDescriptor{}}
		}
		return &ReflectionProxy{conn: &grpc.ClientConn{}, schema: schema, addr: addr, ready: func() bool { return true }, closeFn: func() error { return nil }}, nil
	}
	pool := newServicePoolWithConnector(nil, "billing", connector)
	pool.SetDesiredInstance("instance-a", "addr-a")
	pool.SetDesiredInstance("instance-b", "addr-b")

	type connected struct {
		proxy *ReflectionProxy
		err   error
	}
	results := make(chan connected, 2)
	for _, instance := range []struct{ id, addr string }{{"instance-a", "addr-a"}, {"instance-b", "addr-b"}} {
		instance := instance
		go func() {
			callersReady <- struct{}{}
			<-start
			proxy, _, err := pool.AddOrUpdateInstanceContext(context.Background(), instance.id, instance.addr)
			results <- connected{proxy: proxy, err: err}
		}()
	}
	for range 2 {
		<-callersReady
	}
	close(start)
	select {
	case <-buildStarted:
	case <-time.After(time.Second):
		t.Fatal("initial schema build did not start")
	}
	for range 100 {
		runtime.Gosched()
	}
	if got := schemaBuilds.Load(); got != 1 {
		t.Fatalf("concurrent initial schema builds before release = %d, want 1", got)
	}
	close(finishBuild)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("initial connect errors = %v, %v", first.err, second.err)
	}
	if first.proxy == second.proxy || first.proxy.schema == nil || first.proxy.schema != second.proxy.schema {
		t.Fatalf("proxies=%p/%p schemas=%p/%p, want distinct proxies sharing one schema", first.proxy, second.proxy, first.proxy.schema, second.proxy.schema)
	}
	if schemaBuilds.Load() != 1 || connects.Load() != 2 {
		t.Fatalf("schema builds=%d connects=%d, want 1/2", schemaBuilds.Load(), connects.Load())
	}
}

func TestConnectionPoolDoesNotRemovePoolWithPendingDesiredInstance(t *testing.T) {
	bStarted := make(chan struct{})
	bRelease := make(chan struct{})
	connector := func(ctx context.Context, _ *core.Core, _, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
		if schema == nil {
			schema = &ReflectionSchema{methods: map[string]*MethodDescriptor{}}
		}
		if addr == "addr-b" {
			close(bStarted)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-bRelease:
			}
		}
		return &ReflectionProxy{conn: &grpc.ClientConn{}, schema: schema, addr: addr, ready: func() bool { return true }, closeFn: func() error { return nil }}, nil
	}
	connectionPool := newConnectionPoolWithConnector(connector)
	pool := connectionPool.GetOrCreate(nil, "billing")
	pool.SetDesiredInstance("instance-a", "addr-a")
	if _, _, err := pool.AddOrUpdateInstanceContext(context.Background(), "instance-a", "addr-a"); err != nil {
		t.Fatal(err)
	}
	pool.SetDesiredInstance("instance-b", "addr-b")
	bDone := make(chan error, 1)
	go func() {
		_, _, err := pool.AddOrUpdateInstanceContext(context.Background(), "instance-b", "addr-b")
		bDone <- err
	}()
	<-bStarted

	pool.RemoveInstance("instance-a")
	if removed := connectionPool.RemoveIfEmpty("billing", pool); removed {
		t.Fatal("pool with pending desired instance B was removed")
	}
	if got := connectionPool.Get("billing"); got != pool {
		t.Fatalf("connection pool entry = %p, want original pool %p", got, pool)
	}
	close(bRelease)
	if err := <-bDone; err != nil {
		t.Fatal(err)
	}
	if addr, ok := pool.InstanceAddress("instance-b"); !ok || addr != "addr-b" {
		t.Fatalf("pending B installation = %q/%v, want addr-b/true", addr, ok)
	}
}

func TestServicePoolMixedTransport(t *testing.T) {
	walletAddress, walletRoots := startGatewayReflectionServer(t, true)
	merchantAddress, _ := startGatewayReflectionServer(t, false)
	app := core.New()
	if err := app.ConfigureGRPCClient("xpay-wallet", core.GRPCClientConfig{
		TransportCredentials: credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    walletRoots,
			ServerName: "wallet.test",
		}),
	}); err != nil {
		t.Fatalf("configure wallet credentials: %v", err)
	}

	merchantProxy, _, err := NewServicePool(app, "xpay-merchant").AddOrUpdateInstance("merchant-1", merchantAddress)
	if err != nil {
		t.Fatalf("connect plaintext merchant: %v", err)
	}
	assertGatewayHealthMethod(t, merchantProxy)

	walletProxy, _, err := NewServicePool(app, "xpay-wallet").AddOrUpdateInstance("wallet-1", walletAddress)
	if err != nil {
		t.Fatalf("connect TLS wallet: %v", err)
	}
	assertGatewayHealthMethod(t, walletProxy)
}

func TestReflectionProxyServiceCredentialsStayIsolated(t *testing.T) {
	walletAddress, walletRoots := startGatewayReflectionServer(t, true)
	app := core.New()
	if err := app.ConfigureGRPCClient("xpay-wallet", core.GRPCClientConfig{
		TransportCredentials: credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    walletRoots,
			ServerName: "wallet.test",
		}),
	}); err != nil {
		t.Fatalf("configure wallet credentials: %v", err)
	}

	walletProxy, _, err := NewServicePool(app, "xpay-wallet").AddOrUpdateInstance("wallet-1", walletAddress)
	if err != nil {
		t.Fatalf("connect TLS wallet: %v", err)
	}
	assertGatewayHealthMethod(t, walletProxy)

	if _, _, err := NewServicePool(app, "xpay-merchant").AddOrUpdateInstance("merchant-1", walletAddress); err == nil {
		t.Fatal("unconfigured plaintext service connected to TLS wallet address")
	}
}

func TestServicePoolWalletTLSFailureDoesNotBreakPlaintextService(t *testing.T) {
	walletAddress, _ := startGatewayReflectionServer(t, true)
	merchantAddress, _ := startGatewayReflectionServer(t, false)
	_, wrongRoots := startGatewayReflectionServer(t, true)
	app := core.New()
	if err := app.ConfigureGRPCClient("xpay-wallet", core.GRPCClientConfig{
		TransportCredentials: credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    wrongRoots,
			ServerName: "wallet.test",
		}),
	}); err != nil {
		t.Fatalf("configure wallet credentials: %v", err)
	}

	if _, _, err := NewServicePool(app, "xpay-wallet").AddOrUpdateInstance("wallet-1", walletAddress); err == nil {
		t.Fatal("wallet connection with wrong CA succeeded")
	}
	merchantProxy, _, err := NewServicePool(app, "xpay-merchant").AddOrUpdateInstance("merchant-1", merchantAddress)
	if err != nil {
		t.Fatalf("plaintext merchant failed after wallet TLS error: %v", err)
	}
	assertGatewayHealthMethod(t, merchantProxy)
}

func assertGatewayHealthMethod(t *testing.T, proxy *ReflectionProxy) {
	t.Helper()
	if _, ok := proxy.Methods()["/grpc.health.v1.Health/Check"]; !ok {
		t.Fatalf("methods = %#v, want health Check", proxy.Methods())
	}
}

func startGatewayReflectionServer(t *testing.T, withTLS bool) (string, *x509.CertPool) {
	t.Helper()
	var options []grpc.ServerOption
	var roots *x509.CertPool
	if withTLS {
		root, rootKey := issueGatewayTestCA(t)
		certificate := issueGatewayTestServerCertificate(t, root, rootKey)
		roots = x509.NewCertPool()
		roots.AddCert(root)
		options = append(options, grpc.Creds(credentials.NewTLS(&tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{certificate},
		})))
	}
	server := grpc.NewServer(options...)
	healthpb.RegisterHealthServer(server, &gatewayTestHealthServer{})
	reflection.Register(server)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String(), roots
}

type gatewayTestHealthServer struct {
	healthpb.UnimplementedHealthServer
}

func issueGatewayTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          gatewayTestSerial(t),
		Subject:               pkix.Name{CommonName: "gateway-test-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	return certificate, key
}

func issueGatewayTestServerCertificate(t *testing.T, root *x509.Certificate, rootKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: gatewayTestSerial(t),
		Subject:      pkix.Name{CommonName: "wallet.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"wallet.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func gatewayTestSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	return serial
}
