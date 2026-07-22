package gateway

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/xs23933/core/v3"
	"google.golang.org/grpc"
)

func TestGatewayLifecycleDuplicateConnectSharesResult(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	joined := make(chan struct{}, 2)
	joinRelease := make(chan struct{})
	var connects atomic.Int32
	connector := func(ctx context.Context, _ *core.Core, _, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
		connects.Add(1)
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		if schema == nil {
			schema = &ReflectionSchema{methods: map[string]*MethodDescriptor{}}
		}
		return lifecycleTestProxy(addr, schema, nil), nil
	}
	gw := newLifecycleTestGateway(connector)
	gw.connectWaitObserver = func(string) {
		joined <- struct{}{}
		<-joinRelease
	}
	gw.setDesiredServiceSnapshot(serviceSnapshot{
		"billing/billing-1": {ServiceName: "billing", InstanceID: "billing-1", GRPCAddr: "addr-1"},
	})
	pool := gw.connPool.GetOrCreate(gw.app, "billing")
	pool.SetDesiredInstance("billing-1", "addr-1")

	type result struct {
		proxy   *ReflectionProxy
		changed bool
		err     error
	}
	results := make(chan result, 2)
	var callers sync.WaitGroup
	callers.Add(2)
	for range 2 {
		go func() {
			defer callers.Done()
			proxy, changed, err := gw.connectInstance("billing", "billing-1", "addr-1")
			results <- result{proxy: proxy, changed: changed, err: err}
		}()
	}
	for range 2 {
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Fatal("connect caller did not join singleflight")
		}
	}
	close(joinRelease)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("connector did not start")
	}
	for range 100 {
		runtime.Gosched()
	}
	close(release)
	callers.Wait()
	close(results)

	var first *ReflectionProxy
	for got := range results {
		if got.err != nil || !got.changed || got.proxy == nil {
			t.Fatalf("shared connect result = %#v", got)
		}
		if first == nil {
			first = got.proxy
		} else if got.proxy != first {
			t.Fatalf("shared proxies = %p and %p, want identical result", first, got.proxy)
		}
	}
	if got := connects.Load(); got != 1 {
		t.Fatalf("connector calls = %d, want 1", got)
	}
	if _, exists := gw.connectInFlight.Load(connectInstanceKey("billing", "billing-1")); exists {
		t.Fatal("singleflight bookkeeping retained completed key")
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayLifecycleAddressUpdateDuringFlightInstallsLatestAddress(t *testing.T) {
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	observers := make(chan struct{}, 8)
	var oldClosed atomic.Int32
	var connects atomic.Int32
	connector := func(_ context.Context, _ *core.Core, _, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
		connects.Add(1)
		if schema == nil {
			schema = &ReflectionSchema{methods: map[string]*MethodDescriptor{}}
		}
		if addr == "addr-old" {
			close(oldStarted)
			<-releaseOld
			return lifecycleTestProxy(addr, schema, func() { oldClosed.Add(1) }), nil
		}
		return lifecycleTestProxy(addr, schema, nil), nil
	}
	gw := newLifecycleTestGateway(connector)
	gw.connectWaitObserver = func(string) { observers <- struct{}{} }
	oldSnapshot := serviceSnapshot{
		"billing/billing-1": {ServiceName: "billing", InstanceID: "billing-1", GRPCAddr: "addr-old"},
	}
	gw.setDesiredServiceSnapshot(oldSnapshot)
	pool := gw.connPool.SetDesiredInstance(gw.app, "billing", "billing-1", "addr-old")
	oldResult := make(chan error, 1)
	go func() {
		proxy, _, err := gw.connectInstance("billing", "billing-1", "addr-old")
		if err == nil && (proxy == nil || proxy.addr != "addr-new") {
			err = errors.New("old caller did not receive latest-address proxy")
		}
		oldResult <- err
	}()
	<-observers
	<-oldStarted

	newSnapshot := serviceSnapshot{
		"billing/billing-1": {ServiceName: "billing", InstanceID: "billing-1", GRPCAddr: "addr-new"},
	}
	gw.setDesiredServiceSnapshot(newSnapshot)
	pool.SetDesiredInstance("billing-1", "addr-new")
	newResult := make(chan error, 1)
	go func() {
		proxy, _, err := gw.connectInstance("billing", "billing-1", "addr-new")
		if err == nil && (proxy == nil || proxy.addr != "addr-new") {
			err = errors.New("new caller did not receive latest-address proxy")
		}
		newResult <- err
	}()
	<-observers
	for range 100 {
		runtime.Gosched()
	}
	close(releaseOld)
	for _, result := range []<-chan error{oldResult, newResult} {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	if addr, ok := pool.InstanceAddress("billing-1"); !ok || addr != "addr-new" {
		t.Fatalf("installed address = %q/%v, want addr-new/true", addr, ok)
	}
	if oldClosed.Load() != 1 {
		t.Fatalf("old candidate close count = %d, want 1", oldClosed.Load())
	}
	if connects.Load() != 2 {
		t.Fatalf("connector calls = %d, want old and new only", connects.Load())
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayLifecycleCloseCancelsBlockedReflectionAndWaits(t *testing.T) {
	started := make(chan struct{})
	exited := make(chan struct{})
	connector := func(ctx context.Context, _ *core.Core, _, _ string, _ *ReflectionSchema) (*ReflectionProxy, error) {
		close(started)
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	}
	gw := newLifecycleTestGateway(connector)
	gw.setDesiredServiceSnapshot(serviceSnapshot{
		"billing/billing-1": {ServiceName: "billing", InstanceID: "billing-1", GRPCAddr: "addr-1"},
	})
	pool := gw.connPool.GetOrCreate(gw.app, "billing")
	pool.SetDesiredInstance("billing-1", "addr-1")
	if !gw.launchTask(func() { _, _, _ = gw.connectInstance("billing", "billing-1", "addr-1") }) {
		t.Fatal("connect task was not launched")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocked reflection did not start")
	}

	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("Close returned before blocked reflection observed cancellation")
	}
}

func TestGatewayLifecycleLateConnectIsClosedAndNeverInstalled(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var closed atomic.Int32
	connector := func(_ context.Context, _ *core.Core, _, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
		close(started)
		<-release // Deliberately model a connector that returns after cancellation.
		if schema == nil {
			schema = &ReflectionSchema{methods: map[string]*MethodDescriptor{}}
		}
		return lifecycleTestProxy(addr, schema, func() { closed.Add(1) }), nil
	}
	gw := newLifecycleTestGateway(connector)
	gw.setDesiredServiceSnapshot(serviceSnapshot{
		"billing/billing-1": {ServiceName: "billing", InstanceID: "billing-1", GRPCAddr: "addr-1"},
	})
	pool := gw.connPool.GetOrCreate(gw.app, "billing")
	pool.SetDesiredInstance("billing-1", "addr-1")
	if !gw.launchTask(func() { _, _, _ = gw.connectInstance("billing", "billing-1", "addr-1") }) {
		t.Fatal("connect task was not launched")
	}
	<-started

	closeDone := make(chan error, 1)
	go func() { closeDone <- gw.Close() }()
	select {
	case <-gw.watchCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel lifecycle context")
	}
	close(release)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}

	if got := closed.Load(); got != 1 {
		t.Fatalf("late candidate close count = %d, want 1", got)
	}
	if pool.Size() != 0 || pool.Schema() != nil || len(gw.connPool.ServiceNames()) != 0 {
		t.Fatalf("late candidate installed: size=%d schema=%p pools=%v", pool.Size(), pool.Schema(), gw.connPool.ServiceNames())
	}
}

func TestGatewayLifecycleCloseIsIdempotentAndClearsState(t *testing.T) {
	gw := newLifecycleTestGateway(nil)
	gw.setDesiredServiceSnapshot(serviceSnapshot{
		"billing/billing-1": {ServiceName: "billing", InstanceID: "billing-1", GRPCAddr: "addr-1"},
	})
	if err := gw.addHTTPInstance("billing", "billing-1", "http://127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	gw.circuitStates.Store("billing", &CircuitBreaker{})
	gw.connectInFlight.Store("billing/billing-1", struct{}{})
	gw.grpcServiceRoutes["billing"] = map[string]bool{"runtime:auto-grpc/billing/x": true}

	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if len(gw.loadHTTPInstances()) != 0 || len(gw.loadHTTPIndexes()) != 0 {
		t.Fatalf("HTTP state retained: instances=%v indexes=%v", gw.loadHTTPInstances(), gw.loadHTTPIndexes())
	}
	if len(gw.loadDesiredServiceSnapshot()) != 0 || len(gw.connPool.ServiceNames()) != 0 {
		t.Fatalf("service state retained: desired=%v pools=%v", gw.loadDesiredServiceSnapshot(), gw.connPool.ServiceNames())
	}
	if len(gw.grpcServiceRoutes) != 0 || len(gw.loadRouteSnapshot().sources) != 0 {
		t.Fatalf("route state retained: grpc=%v snapshot=%v", gw.grpcServiceRoutes, gw.loadRouteSnapshot().sources)
	}
	if _, exists := gw.connectInFlight.Load("billing/billing-1"); exists {
		t.Fatal("in-flight bookkeeping retained after Close")
	}
	retainedCircuit := false
	gw.circuitStates.Range(func(_, _ any) bool { retainedCircuit = true; return false })
	if retainedCircuit {
		t.Fatal("circuit state retained after Close")
	}
	if gw.launchTask(func() {}) {
		t.Fatal("task launched after Close")
	}
}

func TestServiceWatchResnapshotReconcilesMissedDeleteAndUsesKeyID(t *testing.T) {
	initial := serviceSnapshot{
		"billing/key-a": {ServiceName: "billing", InstanceID: "key-a", GRPCAddr: "grpc-a", HTTPAddr: "http://127.0.0.1:8081"},
		"billing/key-b": {ServiceName: "billing", InstanceID: "key-b", GRPCAddr: "grpc-b", HTTPAddr: "http://127.0.0.1:8082"},
	}
	refreshed := serviceSnapshot{
		"billing/key-b": {ServiceName: "billing", InstanceID: "key-b", GRPCAddr: "grpc-b", HTTPAddr: "http://127.0.0.1:8082"},
		"billing/key-c": {ServiceName: "billing", InstanceID: "key-c", GRPCAddr: "grpc-c", HTTPAddr: "http://127.0.0.1:8083"},
	}
	source := &lifecycleFakeServiceWatchSource{
		loads: []serviceSnapshotRevision{
			{Snapshot: initial, Revision: 10},
			{Snapshot: refreshed, Revision: 20},
		},
		cycles: [][]serviceWatchResponse{nil, nil},
	}
	connector := func(_ context.Context, _ *core.Core, _, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
		if schema == nil {
			schema = &ReflectionSchema{methods: map[string]*MethodDescriptor{}}
		}
		return lifecycleTestProxy(addr, schema, nil), nil
	}
	gw := newLifecycleTestGateway(connector)
	ctx, cancel := context.WithCancel(gw.watchCtx)
	defer cancel()
	installed := 0
	runServiceWatchLoopFrom(ctx, source, nil, func(snapshot serviceSnapshot) {
		gw.reconcileServiceSnapshot(snapshot)
		installed++
		if installed == 2 {
			cancel()
		}
	}, func(context.Context, int) bool { return true })
	gw.taskWG.Wait()

	if source.loadCount() != 2 {
		t.Fatalf("snapshot loads = %d, want 2", source.loadCount())
	}
	if got := source.watchRevisions(); !equalLifecycleInt64s(got, []int64{11}) {
		t.Fatalf("watch revisions = %v, want [11]", got)
	}
	pool := gw.connPool.Get("billing")
	if pool == nil || pool.Size() != 2 {
		t.Fatalf("reconciled pool = %#v size=%d, want B and C", pool, poolSize(pool))
	}
	if _, ok := pool.InstanceAddress("key-a"); ok {
		t.Fatal("missed delete key-a survived full resnapshot")
	}
	for id := range map[string]struct{}{"key-b": {}, "key-c": {}} {
		if _, ok := pool.InstanceAddress(id); !ok {
			t.Fatalf("instance %s missing after resnapshot", id)
		}
	}
	if got := gw.getHTTPInstanceByID("billing", "key-a"); got != nil {
		t.Fatalf("stale HTTP target = %#v", got)
	}
	for id := range map[string]struct{}{"key-b": {}, "key-c": {}} {
		if got := gw.getHTTPInstanceByID("billing", id); got == nil {
			t.Fatalf("HTTP instance %s missing after resnapshot", id)
		}
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceWatchResnapshotsAfterCloseOrError(t *testing.T) {
	for _, tt := range []struct {
		name      string
		responses []serviceWatchResponse
	}{
		{name: "closed channel"},
		{name: "watch error", responses: []serviceWatchResponse{{Err: errors.New("watch failed")}}},
		{name: "compacted watch", responses: []serviceWatchResponse{{Err: errors.New("required revision has been compacted")}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := &lifecycleFakeServiceWatchSource{
				loads: []serviceSnapshotRevision{
					{Snapshot: serviceSnapshot{"billing/a": {ServiceName: "billing", InstanceID: "a", GRPCAddr: "a"}}, Revision: 10},
					{Snapshot: serviceSnapshot{"billing/b": {ServiceName: "billing", InstanceID: "b", GRPCAddr: "b"}}, Revision: 20},
				},
				cycles: [][]serviceWatchResponse{tt.responses},
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var installed []serviceSnapshot
			runServiceWatchLoopFrom(ctx, source, nil, func(snapshot serviceSnapshot) {
				installed = append(installed, cloneServiceSnapshot(snapshot))
				if len(installed) == 2 {
					cancel()
				}
			}, func(context.Context, int) bool { return true })
			if len(installed) != 2 || source.loadCount() != 2 {
				t.Fatalf("installed=%d loads=%d, want two full snapshots", len(installed), source.loadCount())
			}
			if _, stale := installed[1]["billing/a"]; stale {
				t.Fatalf("refreshed snapshot retained stale A: %#v", installed[1])
			}
			if _, fresh := installed[1]["billing/b"]; !fresh {
				t.Fatalf("refreshed snapshot missing B: %#v", installed[1])
			}
			if got := source.watchRevisions(); !equalLifecycleInt64s(got, []int64{11}) {
				t.Fatalf("watch revisions = %v, want [11]", got)
			}
		})
	}
}

func TestDecodeServiceSnapshotUsesEtcdKeyAsAuthoritativeID(t *testing.T) {
	instance, ok := decodeServiceSnapshotValue(
		"/core/services/",
		"/core/services/billing/key-id",
		[]byte(`{"addr":"127.0.0.1:9000","id":"payload-id","metadata":{"http_addr":"http://127.0.0.1:8080"}}`),
	)
	if !ok {
		t.Fatal("valid service value was rejected")
	}
	if instance.ServiceName != "billing" || instance.InstanceID != "key-id" {
		t.Fatalf("decoded identity = %s/%s, want billing/key-id", instance.ServiceName, instance.InstanceID)
	}
}

func newLifecycleTestGateway(connector reflectionProxyConnector) *EtcdGateway {
	gw := &EtcdGateway{
		app:               core.New(),
		config:            &Config{},
		grpcServiceRoutes: make(map[string]map[string]bool),
		connPool:          newConnectionPoolWithConnector(connector),
	}
	gw.initializeLifecycle()
	gw.storeRouteSnapshot(emptyRouteSnapshot())
	gw.storeHTTPInstances(make(map[string]*httpServiceInstances))
	gw.storeHTTPIndexes(make(map[string]*atomic.Uint64))
	gw.setDesiredServiceSnapshot(make(serviceSnapshot))
	return gw
}

func lifecycleTestProxy(addr string, schema *ReflectionSchema, onClose func()) *ReflectionProxy {
	return &ReflectionProxy{
		conn:   &grpc.ClientConn{},
		schema: schema,
		addr:   addr,
		ready:  func() bool { return true },
		closeFn: func() error {
			if onClose != nil {
				onClose()
			}
			return nil
		},
	}
}

type lifecycleFakeServiceWatchSource struct {
	mu        sync.Mutex
	loads     []serviceSnapshotRevision
	loadIndex int
	cycles    [][]serviceWatchResponse
	watches   []int64
}

func (source *lifecycleFakeServiceWatchSource) Load(context.Context) (serviceSnapshotRevision, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.loadIndex >= len(source.loads) {
		return serviceSnapshotRevision{}, errors.New("unexpected service snapshot load")
	}
	loaded := source.loads[source.loadIndex]
	source.loadIndex++
	return loaded, nil
}

func (source *lifecycleFakeServiceWatchSource) Watch(_ context.Context, revision int64) <-chan serviceWatchResponse {
	source.mu.Lock()
	cycle := len(source.watches)
	source.watches = append(source.watches, revision)
	responses := source.cycles[cycle]
	source.mu.Unlock()
	result := make(chan serviceWatchResponse, len(responses))
	for _, response := range responses {
		result <- response
	}
	close(result)
	return result
}

func (source *lifecycleFakeServiceWatchSource) loadCount() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.loadIndex
}

func (source *lifecycleFakeServiceWatchSource) watchRevisions() []int64 {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([]int64(nil), source.watches...)
}

func (gw *EtcdGateway) getHTTPInstanceByID(serviceName, instanceID string) *HTTPInstance {
	service := gw.loadHTTPInstances()[serviceName]
	if service == nil {
		return nil
	}
	return service.byID[instanceID]
}

func poolSize(pool *ServicePool) int {
	if pool == nil {
		return 0
	}
	return pool.Size()
}

func equalLifecycleInt64s(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
