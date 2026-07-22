package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	core "github.com/xs23933/core/v3"
	gatewayroute "github.com/xs23933/core/v3/gateway/route"
	mvccpb "go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
)

func TestRouteWatchAppliesDirectEventAfterInitialSnapshot(t *testing.T) {
	initial := minimalRouteStateDefinition("initial", gatewayroute.SourceManual, "", "initial", "/api/initial")
	event := minimalRouteStateDefinition("event", gatewayroute.SourceManual, "", "event", "/api/event")
	source := &minimalFakeRouteWatchSource{
		loads: []routeSnapshotRevision{{
			Snapshot: newRouteSnapshot(map[string]*routeSourceValue{"/gateway/routes/initial": {Definition: initial}}),
			Revision: 10,
		}},
		cycles: [][]routeWatchResponse{{{
			Revision: 11,
			Events: []routeEvent{{
				StorageKey: "/gateway/routes/event",
				Value:      &routeSourceValue{Definition: event},
			}},
		}}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := emptyRouteSnapshot()
	runRouteWatchLoopFrom(ctx, source, nil, func(snapshot routeSnapshot) {
		current = snapshot
	}, func(events []routeEvent) {
		current = current.withRouteBatch(events)
		cancel()
	}, func(context.Context, int) bool { return true })

	if source.loadCount() != 1 {
		t.Fatalf("Load calls = %d, want one initial full snapshot", source.loadCount())
	}
	if revisions := source.watchRevisions(); !equalMinimalInt64s(revisions, []int64{11}) {
		t.Fatalf("Watch revisions = %v, want [11]", revisions)
	}
	if _, exists := current.sources["/gateway/routes/initial"]; !exists {
		t.Fatalf("initial source missing: %#v", current.sources)
	}
	if _, exists := current.sources["/gateway/routes/event"]; !exists {
		t.Fatalf("direct watch event not applied: %#v", current.sources)
	}
}

func TestRouteWatchResnapshotsAfterErrorCloseOrCompaction(t *testing.T) {
	tests := []struct {
		name      string
		responses []routeWatchResponse
	}{
		{name: "closed stream"},
		{name: "watch error", responses: []routeWatchResponse{{Err: errors.New("watch failed")}}},
		{
			name: "compacted stream",
			responses: []routeWatchResponse{
				{Err: rpctypes.ErrCompacted},
				{Revision: 12, Events: []routeEvent{{StorageKey: "/gateway/routes/stale", Value: &routeSourceValue{Definition: minimalRouteStateDefinition("stale", gatewayroute.SourceManual, "", "stale", "/api/stale")}}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldKey := "/gateway/routes/old"
			refreshedKey := "/gateway/routes/refreshed"
			eventKey := "/gateway/routes/event"
			source := &minimalFakeRouteWatchSource{
				loads: []routeSnapshotRevision{
					{
						Snapshot: newRouteSnapshot(map[string]*routeSourceValue{oldKey: {Definition: minimalRouteStateDefinition("old", gatewayroute.SourceManual, "", "old", "/api/old")}}),
						Revision: 10,
					},
					{
						Snapshot: newRouteSnapshot(map[string]*routeSourceValue{refreshedKey: {Definition: minimalRouteStateDefinition("refreshed", gatewayroute.SourceManual, "", "refreshed", "/api/refreshed")}}),
						Revision: 20,
					},
				},
				cycles: [][]routeWatchResponse{
					tt.responses,
					{
						{
							Revision: 21,
							Events: []routeEvent{{
								StorageKey: eventKey,
								Value: &routeSourceValue{
									Definition: minimalRouteStateDefinition("event", gatewayroute.SourceManual, "", "event", "/api/event"),
								},
							}},
						},
					},
				},
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			current := emptyRouteSnapshot()
			var installed []routeSnapshot

			runRouteWatchLoopFrom(ctx, source, nil, func(snapshot routeSnapshot) {
				current = snapshot
				installed = append(installed, snapshot)
			}, func(events []routeEvent) {
				current = current.withRouteBatch(events)
				cancel()
			}, func(context.Context, int) bool { return true })

			if source.loadCount() != 2 || len(installed) != 2 {
				t.Fatalf("loads=%d installed=%d, want initial and refreshed snapshots", source.loadCount(), len(installed))
			}
			if _, exists := installed[1].sources[oldKey]; exists {
				t.Fatalf("resnapshot retained stale source %s", oldKey)
			}
			if _, exists := installed[1].sources[refreshedKey]; !exists {
				t.Fatalf("refreshed source missing: %#v", installed[1].sources)
			}
			if _, exists := current.sources[eventKey]; !exists {
				t.Fatalf("revision 21 event was not applied: %#v", current.sources)
			}
			if _, exists := current.sources["/gateway/routes/stale"]; exists {
				t.Fatalf("event after broken stream was applied: %#v", current.sources)
			}
			if revisions := source.watchRevisions(); !equalMinimalInt64s(revisions, []int64{11, 21}) {
				t.Fatalf("Watch revisions = %v, want [11 21]", revisions)
			}
		})
	}
}

func TestRouteWatchRecoveryPreservesRuntimeAutoGRPCSources(t *testing.T) {
	staleKey := "/gateway/routes/stale"
	refreshedKey := "/gateway/routes/refreshed"
	eventKey := "/gateway/routes/event"
	runtimeKey := "runtime:auto-grpc/users/runtime"
	initial := routeSnapshotRevision{
		Snapshot: newRouteSnapshot(map[string]*routeSourceValue{
			staleKey: {Definition: minimalRouteStateGRPCDefinition("stale", gatewayroute.SourceManual, "", "stale", "/api/stale/:id", "/stale.Service/Get")},
		}),
		Revision: 10,
	}
	refreshed := routeSnapshotRevision{
		Snapshot: newRouteSnapshot(map[string]*routeSourceValue{
			refreshedKey: {Definition: minimalRouteStateGRPCDefinition("refreshed", gatewayroute.SourceManual, "", "refreshed", "/api/refreshed/:id", "/refreshed.Service/Get")},
		}),
		Revision: 20,
	}
	source := &minimalFakeRouteWatchSource{
		loads: []routeSnapshotRevision{refreshed},
		cycles: [][]routeWatchResponse{
			nil,
			{{
				Revision: 21,
				Events: []routeEvent{{
					StorageKey: eventKey,
					Value: &routeSourceValue{Definition: minimalRouteStateGRPCDefinition(
						"event", gatewayroute.SourceManual, "", "event", "/api/event/:id", "/event.Service/Get",
					)},
				}},
			}},
		},
	}

	app := core.New()
	gw := &EtcdGateway{app: app, prefix: "/gateway/routes/", connPool: NewConnectionPool()}
	gw.storeRouteSnapshot(emptyRouteSnapshot())
	gw.replaceEtcdRouteSnapshot(initial.Snapshot)
	gw.applyRouteBatch([]routeEvent{{
		StorageKey: runtimeKey,
		Value: &routeSourceValue{Definition: minimalRouteStateGRPCDefinition(
			"runtime", gatewayroute.SourceAutoGRPC, "users", "users", "/api/runtime/:id", "/users.Service/Get",
		)},
	}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runRouteWatchLoopFrom(
		ctx,
		source,
		&initial,
		func(snapshot routeSnapshot) { gw.replaceEtcdRouteSnapshot(snapshot) },
		func(events []routeEvent) {
			gw.applyRouteBatch(events)
			cancel()
		},
		func(context.Context, int) bool { return true },
	)

	snapshot := gw.loadRouteSnapshot()
	if _, exists := snapshot.sources[runtimeKey]; !exists {
		t.Fatalf("runtime auto-gRPC source was lost: %#v", snapshot.sources)
	}
	if _, exists := snapshot.sources[staleKey]; exists {
		t.Fatalf("stale etcd source survived refreshed snapshot: %#v", snapshot.sources)
	}
	for _, key := range []string{refreshedKey, eventKey} {
		if _, exists := snapshot.sources[key]; !exists {
			t.Fatalf("expected refreshed source %s missing: %#v", key, snapshot.sources)
		}
	}
}

func TestRouteWatchBackoffIncreasesUntilHealthyProgress(t *testing.T) {
	source := &brokenWatchBackoffSource{cycles: [][]routeWatchResponse{
		nil,
		{{Err: errors.New("watch failed")}},
		nil,
		{{
			Revision: 4,
			Events: []routeEvent{{
				StorageKey: "/gateway/routes/progress",
				Value: &routeSourceValue{Definition: minimalRouteStateGRPCDefinition(
					"progress", gatewayroute.SourceManual, "", "progress", "/api/progress/:id", "/progress.Service/Get",
				)},
			}},
		}},
	}}
	var waits []int
	applied := 0
	runRouteWatchLoopFrom(
		context.Background(),
		source,
		nil,
		func(routeSnapshot) {},
		func([]routeEvent) { applied++ },
		func(_ context.Context, failures int) bool {
			waits = append(waits, failures)
			return len(waits) < 4
		},
	)

	if want := []int{0, 1, 2, 0}; !equalMinimalInts(waits, want) {
		t.Fatalf("wait failure indices = %v, want %v", waits, want)
	}
	if applied != 1 {
		t.Fatalf("healthy watch responses applied = %d, want 1", applied)
	}
}

func TestRouteWatchEtcdSourceUsesOneRoutePrefix(t *testing.T) {
	client, _ := startGatewayRouteStoreEtcd(t)
	publisher := gatewayroute.Publisher{Store: clientRouteStore{client: client}, RoutePrefix: "/gateway/routes/"}
	manual := minimalRouteStateDefinition("manual", gatewayroute.SourceManual, "", "manual", "/api/manual")
	automatic := minimalRouteStateDefinition("automatic", gatewayroute.SourceAutoHTTP, "users", "users", "/api/users")
	if err := publisher.PublishManual(context.Background(), manual); err != nil {
		t.Fatal(err)
	}
	if err := publisher.SyncAutomaticHTTP(context.Background(), "users", []*gatewayroute.Definition{automatic}); err != nil {
		t.Fatal(err)
	}

	source := etcdRouteWatchSource{
		client:      client,
		routePrefix: "/gateway/routes/",
		launch:      func(task func()) { go task() },
	}
	loaded, err := source.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision <= 0 || len(loaded.Snapshot.sources) != 2 {
		t.Fatalf("loaded revision=%d sources=%d, want one prefix snapshot with two source values", loaded.Revision, len(loaded.Snapshot.sources))
	}
	if active := loaded.Snapshot.activeSlots[automatic.Slot]; active == nil || active.ServiceName != "users" {
		t.Fatalf("automatic catalog active route = %#v", active)
	}

	watchCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	responses := source.Watch(watchCtx, nextRouteWatchRevision(loaded.Revision))
	legacy := minimalRouteStateDefinition("legacy", gatewayroute.SourceManual, "", "legacy", "/api/legacy")
	legacyValue, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(context.Background(), "/gateway/routes/legacy", string(legacyValue)); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-responses:
		if response.Err != nil || len(response.Events) != 1 || response.Events[0].StorageKey != "/gateway/routes/legacy" {
			t.Fatalf("watch response = %#v", response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for single route watch event")
	}
}

func TestRouteWatchRejectsCatalogOwnerMismatch(t *testing.T) {
	definition := minimalRouteStateDefinition("automatic", gatewayroute.SourceAutoHTTP, "users", "users", "/api/users")
	value, err := json.Marshal(gatewayroute.Catalog{ServiceName: "users", Routes: []*gatewayroute.Definition{definition}})
	if err != nil {
		t.Fatal(err)
	}
	wrongKey := "/gateway/routes/auto_http/" + gatewayroute.OwnerID("other")
	if _, err := decodeWatchedRouteSource("/gateway/routes/", wrongKey, value); err == nil {
		t.Fatal("catalog owner mismatch error = nil")
	}
}

type minimalFakeRouteWatchSource struct {
	mu        sync.Mutex
	loads     []routeSnapshotRevision
	loadIndex int
	cycles    [][]routeWatchResponse
	watches   []int64
}

type brokenWatchBackoffSource struct {
	loads  int
	watch  int
	cycles [][]routeWatchResponse
}

func (source *brokenWatchBackoffSource) Load(context.Context) (routeSnapshotRevision, error) {
	source.loads++
	return routeSnapshotRevision{Snapshot: emptyRouteSnapshot(), Revision: int64(source.loads)}, nil
}

func (source *brokenWatchBackoffSource) Watch(context.Context, int64) <-chan routeWatchResponse {
	responses := source.cycles[source.watch]
	source.watch++
	result := make(chan routeWatchResponse, len(responses))
	for _, response := range responses {
		result <- response
	}
	close(result)
	return result
}

func (source *minimalFakeRouteWatchSource) Load(context.Context) (routeSnapshotRevision, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	loaded := source.loads[source.loadIndex]
	source.loadIndex++
	return loaded, nil
}

func (source *minimalFakeRouteWatchSource) Watch(ctx context.Context, revision int64) <-chan routeWatchResponse {
	source.mu.Lock()
	cycle := len(source.watches)
	source.watches = append(source.watches, revision)
	responses := source.cycles[cycle]
	source.mu.Unlock()

	result := make(chan routeWatchResponse, len(responses))
	for _, response := range responses {
		result <- response
	}
	if responses == nil {
		close(result)
		return result
	}
	go func() {
		<-ctx.Done()
		close(result)
	}()
	return result
}

func (source *minimalFakeRouteWatchSource) loadCount() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.loadIndex
}

func (source *minimalFakeRouteWatchSource) watchRevisions() []int64 {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([]int64(nil), source.watches...)
}

func minimalRouteWatchKV(key string, value []byte) *mvccpb.KeyValue {
	return &mvccpb.KeyValue{Key: []byte(key), Value: value}
}

func equalMinimalInt64s(left, right []int64) bool {
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

func equalMinimalInts(left, right []int) bool {
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
