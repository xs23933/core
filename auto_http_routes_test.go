package core

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreetcd "github.com/xs23933/core/v3/etcd"
	gatewayroute "github.com/xs23933/core/v3/gateway/route"
	"go.etcd.io/etcd/server/v3/embed"
	"google.golang.org/grpc/credentials"
)

type automaticHTTPRouteCatalogHandler struct {
	Handler
}

func (h *automaticHTTPRouteCatalogHandler) Init() {
	h.Prefix("/api/v1/users")
}

func (h *automaticHTTPRouteCatalogHandler) GetProfile(c Ctx) error {
	return c.SendStatus(http.StatusNoContent)
}

type automaticHTTPRouteAllHandler struct {
	Handler
}

func (h *automaticHTTPRouteAllHandler) Init() {
	h.Prefix("/api/all")
}

func (h *automaticHTTPRouteAllHandler) AllReady(c Ctx) error {
	return c.SendStatus(http.StatusNoContent)
}

func TestAutomaticHTTPRouteCatalogIncludesOnlyAutomaticHandlers(t *testing.T) {
	app := New(Options{"debug": false})
	app.GET("/manual", func(c Ctx) error { return nil })
	app.AddHandle([]string{http.MethodGet}, "/gateway", nil, func(c Ctx) error { return nil })
	app.addHandler(new(automaticHTTPRouteCatalogHandler))

	want := []automaticHTTPRoute{{
		Method:  http.MethodGet,
		Path:    "/api/v1/users/profile",
		Handler: "core.automaticHTTPRouteCatalogHandler.GetProfile",
	}}
	if !reflect.DeepEqual(app.automaticHTTPRoutes, want) {
		t.Fatalf("automatic HTTP route catalog = %#v, want %#v", app.automaticHTTPRoutes, want)
	}
}

func TestAutomaticHTTPRouteCatalogExpandsAllAndDeduplicates(t *testing.T) {
	app := New(Options{"debug": false})
	app.addHandler(new(automaticHTTPRouteAllHandler))
	app.addHandler(new(automaticHTTPRouteAllHandler))

	wantMethods := Methods[:METHOD_ALL]
	if len(app.automaticHTTPRoutes) != len(wantMethods) {
		t.Fatalf("automatic HTTP routes = %#v, want one route for each method %#v", app.automaticHTTPRoutes, wantMethods)
	}
	for index, method := range wantMethods {
		got := app.automaticHTTPRoutes[index]
		if got.Method != method || got.Path != "/api/all/ready" {
			t.Fatalf("automatic HTTP route %d = %#v, want %s /api/all/ready", index, got, method)
		}
	}
}

func TestAutomaticHTTPRouteCatalogConcurrentRecording(t *testing.T) {
	app := New(Options{"debug": false})
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				app.recordAutomaticHTTPRoute(automaticHTTPRoute{
					Method:  http.MethodGet,
					Path:    "/api/concurrent",
					Handler: "concurrent.Get",
				})
				_ = app.automaticHTTPRouteCatalog()
			}
		}()
	}
	workers.Wait()

	routes := app.automaticHTTPRouteCatalog()
	if len(routes) != 1 {
		t.Fatalf("concurrent automatic HTTP routes = %#v, want one deduplicated route", routes)
	}
}

func TestNormalizeAutomaticHTTPRoutePrefixesTreatsSlashOnlyAsRoot(t *testing.T) {
	got := normalizeAutomaticHTTPRoutePrefixes([]string{"//", "///", " / "})
	want := []string{"/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized prefixes = %#v, want %#v", got, want)
	}
}

func TestAutomaticHTTPRouteRegistryTupleSnapshotIsConsistent(t *testing.T) {
	app := New(Options{"debug": false})
	registries := []*coreetcd.Registry{new(coreetcd.Registry), new(coreetcd.Registry)}
	discoveries := []*coreetcd.Discovery{new(coreetcd.Discovery), new(coreetcd.Discovery)}
	app.mutex.Lock()
	app.etcdRegistry = registries[0]
	app.EtcdDiscovery = discoveries[0]
	app.etcdRegistryServiceName = "service-a"
	app.etcdRegistryNamespace = "namespace-a"
	app.mutex.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := 0; index < 1000; index++ {
			value := index % 2
			app.mutex.Lock()
			app.etcdRegistry = registries[value]
			app.EtcdDiscovery = discoveries[value]
			app.etcdRegistryServiceName = []string{"service-a", "service-b"}[value]
			app.etcdRegistryNamespace = []string{"namespace-a", "namespace-b"}[value]
			app.mutex.Unlock()
		}
	}()

	for index := 0; index < 1000; index++ {
		registry, discovery, serviceName, namespace := app.automaticHTTPRouteRegistryTuple()
		if registry == registries[0] {
			if discovery != discoveries[0] || serviceName != "service-a" || namespace != "namespace-a" {
				t.Fatalf("mixed registry tuple A: %p %p %q %q", registry, discovery, serviceName, namespace)
			}
			continue
		}
		if registry != registries[1] || discovery != discoveries[1] || serviceName != "service-b" || namespace != "namespace-b" {
			t.Fatalf("mixed registry tuple B: %p %p %q %q", registry, discovery, serviceName, namespace)
		}
	}
	<-done
}

func TestAutomaticHTTPRouteSync(t *testing.T) {
	t.Run("disabled publishes nothing and requires no discovery", func(t *testing.T) {
		app := New(Options{"debug": false})
		app.automaticHTTPRoutes = []automaticHTTPRoute{{Method: http.MethodGet, Path: "/api/users"}}
		if err := app.syncAutomaticHTTPRoutes(context.Background()); err != nil {
			t.Fatalf("sync disabled automatic HTTP routes: %v", err)
		}
	})

	t.Run("enabled requires at least one include prefix", func(t *testing.T) {
		app := newAutomaticHTTPRouteSyncApp(nil, nil)
		err := app.syncAutomaticHTTPRoutes(context.Background())
		if err == nil || !strings.Contains(err.Error(), "include") {
			t.Fatalf("sync error = %v, want missing include-prefix error", err)
		}
	})

	t.Run("configuration strings do not replace registry identity", func(t *testing.T) {
		app := newAutomaticHTTPRouteSyncApp([]string{"/api"}, nil)
		app.Conf["etcd"] = map[string]any{
			"namespace":    "sync",
			"service_name": "configured-only",
		}
		err := app.syncAutomaticHTTPRoutes(context.Background())
		if err == nil || !strings.Contains(err.Error(), "registry") {
			t.Fatalf("sync error = %v, want missing registry identity error", err)
		}
	})

	discovery := startAutomaticHTTPRouteDiscovery(t, "sync")

	t.Run("enabled requires discovery", func(t *testing.T) {
		app := newAutomaticHTTPRouteSyncApp([]string{"/api"}, nil)
		app.etcdRegistry = &coreetcd.Registry{}
		app.etcdRegistryServiceName = "users"
		app.etcdRegistryNamespace = "sync"
		err := app.syncAutomaticHTTPRoutes(context.Background())
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "discovery") {
			t.Fatalf("sync error = %v, want missing discovery error", err)
		}
	})

	t.Run("filters on segment boundaries and publishes final handler paths", func(t *testing.T) {
		app := newAutomaticHTTPRouteSyncApp([]string{"/api"}, []string{"/api/internal"})
		setAutomaticHTTPRouteRegistry(app, discovery, "sync", "users")
		app.automaticHTTPRoutes = []automaticHTTPRoute{
			{Method: http.MethodGet, Path: "/api/users/:id", Handler: "users.GetById"},
			{Method: http.MethodPost, Path: "/api/internal/reindex", Handler: "users.PostReindex"},
			{Method: http.MethodDelete, Path: "/apix/users/:id", Handler: "users.DeleteById"},
		}

		if err := app.syncAutomaticHTTPRoutes(context.Background()); err != nil {
			t.Fatalf("sync automatic HTTP routes: %v", err)
		}
		key := "/sync/gateway/routes/auto_http/" + gatewayroute.OwnerID("users")
		raw, err := discovery.GetString(key)
		if err != nil {
			t.Fatal(err)
		}
		var catalog gatewayroute.Catalog
		if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
			t.Fatal(err)
		}
		if catalog.ServiceName != "users" || len(catalog.Routes) != 1 {
			t.Fatalf("published catalog = %#v", catalog)
		}
		definition := catalog.Routes[0]
		if definition.Method != http.MethodGet || definition.Path != "/api/users/:id" || definition.UpstreamPath != definition.Path {
			t.Fatalf("published method/paths = %s %q -> %q", definition.Method, definition.Path, definition.UpstreamPath)
		}
		if definition.Source != gatewayroute.SourceAutoHTTP || definition.Owner != "users" || definition.ServiceName != "users" {
			t.Fatalf("published identity = source %q owner %q service %q", definition.Source, definition.Owner, definition.ServiceName)
		}
		if definition.Protocol != gatewayroute.ProtocolHTTP || !definition.Enabled || definition.Description != "users.GetById" {
			t.Fatalf("published route metadata = %#v", definition)
		}
	})

	t.Run("empty selection replaces the catalog with an empty route set", func(t *testing.T) {
		app := newAutomaticHTTPRouteSyncApp([]string{"/retire"}, nil)
		setAutomaticHTTPRouteRegistry(app, discovery, "sync", "retire")
		app.automaticHTTPRoutes = []automaticHTTPRoute{{Method: http.MethodGet, Path: "/retire/users", Handler: "users.Get"}}
		if err := app.syncAutomaticHTTPRoutes(context.Background()); err != nil {
			t.Fatal(err)
		}

		app.automaticHTTPRoutes = []automaticHTTPRoute{{Method: http.MethodGet, Path: "/other/users", Handler: "users.Get"}}
		if err := app.syncAutomaticHTTPRoutes(context.Background()); err != nil {
			t.Fatal(err)
		}
		key := "/sync/gateway/routes/auto_http/" + gatewayroute.OwnerID("retire")
		raw, err := discovery.GetString(key)
		if err != nil {
			t.Fatal(err)
		}
		var catalog gatewayroute.Catalog
		if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
			t.Fatal(err)
		}
		if catalog.ServiceName != "retire" || len(catalog.Routes) != 0 {
			t.Fatalf("replacement catalog = %#v, want empty retire catalog", catalog)
		}
	})
}

type automaticHTTPRouteStartupHandler struct {
	Handler
	started chan struct{}
	starts  atomic.Int32
	stops   atomic.Int32
}

type automaticHTTPRouteFactoryLifecycle struct {
	ready        chan struct{}
	starts       atomic.Int32
	startedStops atomic.Int32
	wrongStops   atomic.Int32
}

type automaticHTTPRouteFactoryHandler struct {
	Handler
	lifecycle *automaticHTTPRouteFactoryLifecycle
	started   atomic.Bool
}

func (h *automaticHTTPRouteFactoryHandler) Init() {
	<-h.lifecycle.ready
	h.Prefix("/factory")
}

func (h *automaticHTTPRouteFactoryHandler) Start(app *Core) error {
	h.started.Store(true)
	if h.lifecycle.starts.Add(1) == 1 {
		close(h.lifecycle.ready)
	}
	<-app.Ctx.Done()
	return nil
}

func (h *automaticHTTPRouteFactoryHandler) Stop(*Core) error {
	if h.started.Load() {
		h.lifecycle.startedStops.Add(1)
	} else {
		h.lifecycle.wrongStops.Add(1)
	}
	return nil
}

func (h *automaticHTTPRouteFactoryHandler) GetReady(c Ctx) error {
	return c.SendStatus(http.StatusNoContent)
}

func (h *automaticHTTPRouteStartupHandler) Init() {
	if h.started != nil {
		<-h.started
	}
	h.Prefix("/startup")
}

func (h *automaticHTTPRouteStartupHandler) GetReady(c Ctx) error {
	return c.SendStatus(http.StatusNoContent)
}

func (h *automaticHTTPRouteStartupHandler) Start(app *Core) error {
	if h.starts.Add(1) == 1 && h.started != nil {
		close(h.started)
	}
	<-app.Ctx.Done()
	return nil
}

func (h *automaticHTTPRouteStartupHandler) Stop(*Core) error {
	h.stops.Add(1)
	return nil
}

func TestAutomaticHTTPRouteStartupSyncRunsAfterModuleLoading(t *testing.T) {
	discovery := startAutomaticHTTPRouteDiscovery(t, "startup")
	app := newAutomaticHTTPRouteSyncApp([]string{"/startup"}, nil)
	setAutomaticHTTPRouteRegistry(app, discovery, "startup", "startup-service")
	handler := &automaticHTTPRouteStartupHandler{started: make(chan struct{})}
	registerAutomaticHTTPRouteTestModule(t, app, handler)
	listener := newCloseTrackingListener(t)
	t.Cleanup(func() {
		_ = listener.Close()
		app.shutdown()
	})

	if err := app.prepareServe(listener); err != nil {
		t.Fatalf("prepareServe: %v", err)
	}
	key := "/startup/gateway/routes/auto_http/" + gatewayroute.OwnerID("startup-service")
	raw, err := discovery.GetString(key)
	if err != nil {
		t.Fatal(err)
	}
	var catalog gatewayroute.Catalog
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.ServiceName != "startup-service" || len(catalog.Routes) != 1 {
		t.Fatalf("startup-published catalog = %#v", catalog)
	}
	if catalog.Routes[0].Path != "/startup/ready" {
		t.Fatalf("startup-published path = %q, want /startup/ready", catalog.Routes[0].Path)
	}
	if handler.starts.Load() != 1 {
		t.Fatalf("module starts = %d, want 1", handler.starts.Load())
	}
}

func TestAutomaticHTTPRoutePublishErrorUnwindsStartedApplication(t *testing.T) {
	app := newAutomaticHTTPRouteSyncApp([]string{"/api"}, nil)
	handler := &automaticHTTPRouteStartupHandler{started: make(chan struct{})}
	registerAutomaticHTTPRouteTestModule(t, app, handler)
	tracked := newCloseTrackingListener(t)
	app.etcdRegistry = new(coreetcd.Registry)
	app.etcdRegistryServiceName = "users"
	app.etcdRegistryNamespace = "startup"
	var registryCleanup, discoveryCleanup atomic.Int32
	app.etcdRegistryCleanup = func() { registryCleanup.Add(1) }
	app.etcdDiscoveryCleanup = func() { discoveryCleanup.Add(1) }

	err := app.Serve(tracked)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "discovery") {
		t.Fatalf("Serve error = %v, want automatic-route discovery error", err)
	}
	assertAutomaticHTTPRouteStartupUnwound(t, app, tracked, handler)
	if registryCleanup.Load() != 1 || discoveryCleanup.Load() != 1 {
		t.Fatalf("etcd cleanup = registry %d discovery %d, want 1/1", registryCleanup.Load(), discoveryCleanup.Load())
	}
	app.shutdown()
	if registryCleanup.Load() != 1 || discoveryCleanup.Load() != 1 || handler.stops.Load() != 1 {
		t.Fatal("startup cleanup is not idempotent")
	}
}

func TestAutomaticHTTPRouteStartupStopsLoadedFactoryInstance(t *testing.T) {
	app := newAutomaticHTTPRouteSyncApp([]string{"/factory"}, nil)
	lifecycle := &automaticHTTPRouteFactoryLifecycle{ready: make(chan struct{})}
	registerAutomaticHTTPRouteFactoryTestModule(t, app, func() Module {
		return &automaticHTTPRouteFactoryHandler{lifecycle: lifecycle}
	})
	tracked := newCloseTrackingListener(t)

	err := app.Serve(tracked)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "registry") {
		t.Fatalf("Serve error = %v, want registry error", err)
	}
	if lifecycle.starts.Load() != 1 || lifecycle.startedStops.Load() != 1 || lifecycle.wrongStops.Load() != 0 {
		t.Fatalf("factory lifecycle = starts %d started-stops %d wrong-stops %d, want 1/1/0",
			lifecycle.starts.Load(), lifecycle.startedStops.Load(), lifecycle.wrongStops.Load())
	}
}

func TestAutomaticHTTPRouteGRPCStartupFailureUnwindsBeforePublication(t *testing.T) {
	discovery := startAutomaticHTTPRouteDiscovery(t, "startup-grpc")
	app := newAutomaticHTTPRouteSyncApp([]string{"/startup"}, nil)
	setAutomaticHTTPRouteRegistry(app, discovery, "startup-grpc", "grpc-service")
	handler := &automaticHTTPRouteStartupHandler{started: make(chan struct{})}
	registerAutomaticHTTPRouteTestModule(t, app, handler)
	if err := app.ConfigureGRPCServer(GRPCServerConfig{
		TransportCredentials: credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13}),
	}); err != nil {
		t.Fatal(err)
	}
	app.EnableGRPC(app.addr)
	app.GetGRPCServer()
	tracked := newCloseTrackingListener(t)

	err := app.Serve(tracked)
	if !errors.Is(err, ErrGRPCTLSSharedAddress) {
		t.Fatalf("Serve error = %v, want %v", err, ErrGRPCTLSSharedAddress)
	}
	assertAutomaticHTTPRouteStartupUnwound(t, app, tracked, handler)
	key := "/startup-grpc/gateway/routes/auto_http/" + gatewayroute.OwnerID("grpc-service")
	if _, err := discovery.GetString(key); err == nil {
		t.Fatal("automatic catalog was published before gRPC startup succeeded")
	}
}

func TestAutomaticHTTPRouteTLSPreparationFailureUnwindsBeforePublication(t *testing.T) {
	discovery := startAutomaticHTTPRouteDiscovery(t, "startup-tls")
	app := newAutomaticHTTPRouteSyncApp([]string{"/startup"}, nil)
	setAutomaticHTTPRouteRegistry(app, discovery, "startup-tls", "tls-service")
	handler := &automaticHTTPRouteStartupHandler{started: make(chan struct{})}
	registerAutomaticHTTPRouteTestModule(t, app, handler)
	tracked := newCloseTrackingListener(t)
	wantErr := errors.New("prepare tls failed")
	previousPrepareTLS := prepareCoreTLSForServe
	prepareCoreTLSForServe = func(*Core) error { return wantErr }
	t.Cleanup(func() { prepareCoreTLSForServe = previousPrepareTLS })

	err := app.Serve(tracked)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Serve error = %v, want %v", err, wantErr)
	}
	assertAutomaticHTTPRouteStartupUnwound(t, app, tracked, handler)
	key := "/startup-tls/gateway/routes/auto_http/" + gatewayroute.OwnerID("tls-service")
	if _, err := discovery.GetString(key); err == nil {
		t.Fatal("automatic catalog was published before TLS preparation succeeded")
	}
}

func TestAutomaticHTTPRouteHTTPServeErrorUnwindsStartedApplication(t *testing.T) {
	app := New(Options{"debug": false})
	handler := &automaticHTTPRouteStartupHandler{started: make(chan struct{})}
	registerAutomaticHTTPRouteTestModule(t, app, handler)
	tracked := newCloseTrackingListener(t)
	if err := tracked.Listener.Close(); err != nil {
		t.Fatal(err)
	}

	err := app.Serve(tracked)
	if err == nil {
		t.Fatal("Serve error = nil, want closed-listener error")
	}
	assertAutomaticHTTPRouteStartupUnwound(t, app, tracked, handler)
}

func TestAutomaticHTTPRoutePrepareServeRunsOnce(t *testing.T) {
	app := New(Options{"debug": false})
	handler := &automaticHTTPRouteStartupHandler{started: make(chan struct{})}
	registerAutomaticHTTPRouteTestModule(t, app, handler)
	first := newCloseTrackingListener(t)
	second := newCloseTrackingListener(t)
	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
		app.shutdown()
	})
	previousSync := syncCoreAutomaticHTTPRoutesForServe
	var syncCalls atomic.Int32
	syncCoreAutomaticHTTPRoutesForServe = func(*Core, context.Context) error {
		syncCalls.Add(1)
		return nil
	}
	t.Cleanup(func() { syncCoreAutomaticHTTPRoutesForServe = previousSync })

	if err := app.prepareServe(first); err != nil {
		t.Fatalf("first prepareServe: %v", err)
	}
	if err := app.prepareServe(second); err != nil {
		t.Fatalf("second prepareServe: %v", err)
	}
	if syncCalls.Load() != 1 || handler.starts.Load() != 1 {
		t.Fatalf("startup executions = sync %d module starts %d, want 1/1", syncCalls.Load(), handler.starts.Load())
	}
}

func TestEnableEtcdRegistryRetainsEffectiveAutomaticHTTPRouteIdentity(t *testing.T) {
	endpoint := startAutomaticHTTPRouteEtcd(t)
	app := New(Options{
		"debug": false,
		"etcd": map[string]any{
			"namespace":    "configured-namespace",
			"service_name": "configured-service",
		},
	})
	t.Cleanup(func() {
		app.shutdown()
		app.stop()
	})

	err := app.EnableEtcdRegistry(&coreetcd.Options{
		Namespace:   "explicit-namespace",
		Endpoints:   []string{endpoint},
		DialTimeout: 3 * time.Second,
		ServiceName: "explicit-service",
		ServiceAddr: "127.0.0.1:19001",
		ServiceID:   "explicit-instance",
		TTL:         5,
	})
	if err != nil {
		t.Fatalf("EnableEtcdRegistry: %v", err)
	}
	if app.etcdRegistryServiceName != "explicit-service" || app.etcdRegistryNamespace != "explicit-namespace" {
		t.Fatalf("retained registry identity = %q/%q, want explicit-namespace/explicit-service",
			app.etcdRegistryNamespace, app.etcdRegistryServiceName)
	}
}

type closeTrackingListener struct {
	net.Listener
	closed atomic.Bool
}

func newCloseTrackingListener(t *testing.T) *closeTrackingListener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return &closeTrackingListener{Listener: listener}
}

func (l *closeTrackingListener) Close() error {
	l.closed.Store(true)
	return l.Listener.Close()
}

func assertAutomaticHTTPRouteStartupUnwound(
	t *testing.T,
	app *Core,
	listener *closeTrackingListener,
	handler *automaticHTTPRouteStartupHandler,
) {
	t.Helper()
	if !listener.closed.Load() {
		t.Fatal("startup failure did not close listener")
	}
	select {
	case <-app.Ctx.Done():
	default:
		t.Fatal("startup failure did not cancel application context")
	}
	if handler.starts.Load() != 1 || handler.stops.Load() != 1 {
		t.Fatalf("module lifecycle = starts %d stops %d, want 1/1", handler.starts.Load(), handler.stops.Load())
	}
}

func registerAutomaticHTTPRouteTestModule(t *testing.T, app *Core, instance Module) {
	t.Helper()
	app.modName = "task5startup"
	id := app.modName + ".handler"
	modulesMu.Lock()
	modules[id] = ModuleInfo{ID: id, Instance: func() Module { return instance }}
	modulesMu.Unlock()
	t.Cleanup(func() {
		app.stop()
		modulesMu.Lock()
		delete(modules, id)
		modulesMu.Unlock()
	})
}

func registerAutomaticHTTPRouteFactoryTestModule(t *testing.T, app *Core, factory func() Module) {
	t.Helper()
	app.modName = "task4factory"
	id := app.modName + ".handler"
	modulesMu.Lock()
	modules[id] = ModuleInfo{ID: id, Instance: factory}
	modulesMu.Unlock()
	t.Cleanup(func() {
		app.stop()
		modulesMu.Lock()
		delete(modules, id)
		modulesMu.Unlock()
	})
}

func newAutomaticHTTPRouteSyncApp(include, exclude []string) *Core {
	return New(Options{
		"debug": false,
		"gateway": map[string]any{
			"auto_http_routes": map[string]any{
				"enabled":          true,
				"include_prefixes": include,
				"exclude_prefixes": exclude,
			},
		},
	})
}

func setAutomaticHTTPRouteRegistry(app *Core, discovery *coreetcd.Discovery, namespace, serviceName string) {
	app.etcdRegistry = &coreetcd.Registry{}
	app.EtcdDiscovery = discovery
	app.etcdRegistryNamespace = namespace
	app.etcdRegistryServiceName = serviceName
}

func startAutomaticHTTPRouteDiscovery(t *testing.T, namespace string) *coreetcd.Discovery {
	t.Helper()
	endpoint := startAutomaticHTTPRouteEtcd(t)
	discovery, err := coreetcd.NewDiscovery(&coreetcd.Options{
		Namespace:   namespace,
		Endpoints:   []string{endpoint},
		DialTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("create etcd discovery: %v", err)
	}
	t.Cleanup(func() { _ = discovery.Close() })
	return discovery
}

func startAutomaticHTTPRouteEtcd(t *testing.T) string {
	t.Helper()
	config := embed.NewConfig()
	config.Dir = t.TempDir()
	config.LogLevel = "error"
	clientURL := reserveAutomaticHTTPRouteURL(t)
	peerURL := reserveAutomaticHTTPRouteURL(t)
	config.ListenClientUrls = []url.URL{clientURL}
	config.AdvertiseClientUrls = []url.URL{clientURL}
	config.ListenPeerUrls = []url.URL{peerURL}
	config.AdvertisePeerUrls = []url.URL{peerURL}
	config.InitialCluster = fmt.Sprintf("%s=%s", config.Name, peerURL.String())

	server, err := embed.StartEtcd(config)
	if err != nil {
		t.Fatalf("start embedded etcd: %v", err)
	}
	t.Cleanup(server.Close)
	select {
	case <-server.Server.ReadyNotify():
	case <-time.After(10 * time.Second):
		server.Server.Stop()
		t.Fatal("embedded etcd did not become ready")
	}

	endpoint := "http://" + server.Clients[0].Addr().String()
	return endpoint
}

func reserveAutomaticHTTPRouteURL(t *testing.T) url.URL {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve etcd address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close reserved etcd address: %v", err)
	}
	return url.URL{Scheme: "http", Host: address}
}
