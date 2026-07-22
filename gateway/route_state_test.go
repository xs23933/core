package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/xs23933/core/v3"
	gatewayroute "github.com/xs23933/core/v3/gateway/route"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func TestRouteStateMinimalManualAndAutomaticSources(t *testing.T) {
	manual := minimalRouteStateDefinition("manual", gatewayroute.SourceManual, "", "manual-service", "/api/manual/:id")
	automatic := minimalRouteStateDefinition("automatic", gatewayroute.SourceAutoHTTP, "auto-service", "auto-service", "/api/automatic/:id")
	automaticKey := minimalAutomaticCatalogKey("/gateway/routes/", "auto-service")

	snapshot := newRouteSnapshot(map[string]*routeSourceValue{
		"/gateway/routes/manual/" + manual.Slot: {Definition: manual},
		automaticKey: {
			Catalog: &gatewayroute.Catalog{ServiceName: "auto-service", Routes: []*gatewayroute.Definition{automatic}},
		},
	})

	if len(snapshot.sources) != 2 {
		t.Fatalf("sources = %d, want exact manual and catalog values", len(snapshot.sources))
	}
	if active := snapshot.activeSlots[manual.Slot]; active == nil || active.ID != manual.ID {
		t.Fatalf("manual active route = %#v, want %s", active, manual.ID)
	}
	if active := snapshot.activeSlots[automatic.Slot]; active == nil || active.ID != automatic.ID {
		t.Fatalf("automatic active route = %#v, want %s", active, automatic.ID)
	}
	if records := snapshot.recordsByID[automatic.ID]; len(records) != 1 || records[0].StorageKey != automaticKey {
		t.Fatalf("automatic ID lookup = %#v, want catalog storage key", records)
	}
}

func TestRouteStateMinimalManualOverridesAutomatic(t *testing.T) {
	manual := minimalRouteStateDefinition("manual", gatewayroute.SourceManual, "", "manual-service", "/api/users/:id")
	automatic := minimalRouteStateDefinition("automatic", gatewayroute.SourceAutoHTTP, "auto-service", "auto-service", "/api/users/:id")

	snapshot := newRouteSnapshot(map[string]*routeSourceValue{
		"/gateway/routes/manual/" + manual.Slot: {Definition: manual},
		minimalAutomaticCatalogKey("/gateway/routes/", "auto-service"): {
			Catalog: &gatewayroute.Catalog{ServiceName: "auto-service", Routes: []*gatewayroute.Definition{automatic}},
		},
	})

	if active := snapshot.activeSlots[manual.Slot]; active == nil || active.ID != manual.ID {
		t.Fatalf("active route = %#v, want manual override", active)
	}
	if _, exists := snapshot.conflicts[manual.Slot]; exists {
		t.Fatalf("manual override reported a conflict: %#v", snapshot.conflicts[manual.Slot])
	}
}

func TestRouteStateMinimalIdenticalDuplicatesAreIdempotent(t *testing.T) {
	const prefix = "/gateway/routes/"
	first := minimalRouteStateDefinition("first-id", gatewayroute.SourceAutoHTTP, "legacy-owner", "users", "/api/users/:id")
	second := cloneMinimalRouteStateDefinition(first)
	second.ID = "second-id"
	second.Owner = "manual-owner"
	second.Description = "mutable metadata must not affect dispatch identity"
	firstValue, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondValue, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	legacySource, err := decodeWatchedRouteSource(prefix, prefix+first.ID, firstValue)
	if err != nil {
		t.Fatal(err)
	}
	manualKey := prefix + string(gatewayroute.SourceManual) + "/" + first.Slot
	manualSource, err := decodeWatchedRouteSource(prefix, manualKey, secondValue)
	if err != nil {
		t.Fatal(err)
	}
	for storageKey, source := range map[string]*routeSourceValue{
		prefix + first.ID: legacySource,
		manualKey:         manualSource,
	} {
		if source.Definition.Source != gatewayroute.SourceManual || source.Definition.Owner != "" {
			t.Fatalf("decoded %s precedence metadata = source:%q owner:%q", storageKey, source.Definition.Source, source.Definition.Owner)
		}
	}

	snapshot := newRouteSnapshot(map[string]*routeSourceValue{
		prefix + first.ID: legacySource,
		manualKey:         manualSource,
	})

	if active := snapshot.activeSlots[first.Slot]; active == nil || active.ServiceName != "users" {
		t.Fatalf("identical duplicate active route = %#v", active)
	}
	if _, exists := snapshot.conflicts[first.Slot]; exists {
		t.Fatalf("identical duplicates reported a conflict: %#v", snapshot.conflicts[first.Slot])
	}
}

func TestRouteStateMinimalDistinctSamePrecedenceFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]*routeSourceValue
	}{
		{
			name: "manual",
			values: func() map[string]*routeSourceValue {
				first := minimalRouteStateDefinition("manual-a", gatewayroute.SourceManual, "", "users-a", "/api/users/:id")
				second := minimalRouteStateDefinition("manual-b", gatewayroute.SourceManual, "", "users-b", "/api/users/:userID")
				return map[string]*routeSourceValue{
					"/gateway/routes/legacy-a":              {Definition: first},
					"/gateway/routes/manual/" + second.Slot: {Definition: second},
				}
			}(),
		},
		{
			name: "automatic",
			values: func() map[string]*routeSourceValue {
				prefix := "/gateway/routes/"
				values := make(map[string]*routeSourceValue, 2)
				for _, serviceName := range []string{"users-a", "users-b"} {
					definition := minimalRouteStateDefinition("auto-"+serviceName, gatewayroute.SourceAutoHTTP, serviceName, serviceName, "/api/users/:id")
					encoded, err := json.Marshal(&gatewayroute.Catalog{
						ServiceName: serviceName,
						Routes:      []*gatewayroute.Definition{definition},
					})
					if err != nil {
						t.Fatal(err)
					}
					storageKey := minimalAutomaticCatalogKey(prefix, serviceName)
					decoded, err := decodeWatchedRouteSource(prefix, storageKey, encoded)
					if err != nil {
						t.Fatalf("decode automatic catalog %s: %v", serviceName, err)
					}
					values[storageKey] = decoded
				}
				return values
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := newRouteSnapshot(tt.values)
			slot := minimalRouteStateSlot(t, "/api/users/:id")
			if active := snapshot.activeSlots[slot]; active != nil {
				t.Fatalf("conflicting route selected: %#v", active)
			}
			if conflict, exists := snapshot.conflicts[slot]; !exists || len(conflict.Records) != 2 {
				t.Fatalf("conflict = %#v, %v; want two records", conflict, exists)
			}
		})
	}
}

func TestRouteStateMinimalMalformedCatalogInvalidatesPreviousValue(t *testing.T) {
	definition := minimalRouteStateDefinition("automatic", gatewayroute.SourceAutoHTTP, "users", "users", "/api/users/:id")
	storageKey := minimalAutomaticCatalogKey("/gateway/routes/", "users")
	snapshot := newRouteSnapshot(map[string]*routeSourceValue{
		storageKey: {Catalog: &gatewayroute.Catalog{ServiceName: "users", Routes: []*gatewayroute.Definition{definition}}},
	})

	events := decodeRouteWatchEvents("/gateway/routes/", []*clientv3.Event{{
		Type: clientv3.EventTypePut,
		Kv:   minimalRouteWatchKV(storageKey, []byte(`{"service_name":"users","routes":[{"protocol":"grpc"}]}`)),
	}})
	next := snapshot.withRouteBatch(events)
	if _, exists := next.sources[storageKey]; exists {
		t.Fatalf("malformed catalog retained previous source value: %#v", next.sources[storageKey])
	}
	if active := next.activeSlots[definition.Slot]; active != nil {
		t.Fatalf("malformed catalog retained active route: %#v", active)
	}
}

func TestRouteStateMinimalCatalogReplacementRemovesStaleRoutes(t *testing.T) {
	oldRoute := minimalRouteStateDefinition("old", gatewayroute.SourceAutoHTTP, "users", "users", "/api/old")
	newRoute := minimalRouteStateDefinition("new", gatewayroute.SourceAutoHTTP, "users", "users", "/api/new")
	storageKey := minimalAutomaticCatalogKey("/gateway/routes/", "users")
	snapshot := newRouteSnapshot(map[string]*routeSourceValue{
		storageKey: {Catalog: &gatewayroute.Catalog{ServiceName: "users", Routes: []*gatewayroute.Definition{oldRoute}}},
	})

	next := snapshot.withRouteBatch([]routeEvent{{
		StorageKey: storageKey,
		Value: &routeSourceValue{
			Catalog: &gatewayroute.Catalog{ServiceName: "users", Routes: []*gatewayroute.Definition{newRoute}},
		},
	}})
	if active := next.activeSlots[oldRoute.Slot]; active != nil {
		t.Fatalf("catalog replacement retained stale route: %#v", active)
	}
	if active := next.activeSlots[newRoute.Slot]; active == nil || active.ID != newRoute.ID {
		t.Fatalf("catalog replacement active route = %#v, want new", active)
	}
}

func TestRouteStateMinimalLegacyDirectRecordIsManual(t *testing.T) {
	legacy := minimalRouteStateDefinition("", gatewayroute.SourceAutoHTTP, "ignored", "legacy-service", "/api/legacy")
	value, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	source, err := decodeWatchedRouteSource("/gateway/routes/", "/gateway/routes/legacy-id", value)
	if err != nil {
		t.Fatal(err)
	}
	if source.Definition == nil || source.Definition.ID != "legacy-id" {
		t.Fatalf("legacy source = %#v, want direct definition ID from key", source)
	}
	if source.Definition.Source != gatewayroute.SourceManual || source.Definition.Owner != "" {
		t.Fatalf("legacy precedence metadata = source:%q owner:%q", source.Definition.Source, source.Definition.Owner)
	}
	snapshot := newRouteSnapshot(map[string]*routeSourceValue{"/gateway/routes/legacy-id": source})
	if active := snapshot.activeSlots[source.Definition.Slot]; active == nil || active.ID != "legacy-id" {
		t.Fatalf("legacy active route = %#v", active)
	}
}

func TestRouteStateMinimalEtcdReplacementTransitionsCoreRouteTree(t *testing.T) {
	app := core.New()
	gw := &EtcdGateway{app: app, prefix: "/gateway/routes/", connPool: NewConnectionPool()}
	gw.storeRouteSnapshot(emptyRouteSnapshot())

	oldRoute := minimalRouteStateGRPCDefinition("old", gatewayroute.SourceManual, "", "old-service", "/api/users/:id", "/old.Service/Get")
	staleRoute := minimalRouteStateGRPCDefinition("stale", gatewayroute.SourceManual, "", "stale-service", "/api/stale/:id", "/stale.Service/Get")
	gw.replaceEtcdRouteSnapshot(newRouteSnapshot(map[string]*routeSourceValue{
		"/gateway/routes/old":   {Definition: oldRoute},
		"/gateway/routes/stale": {Definition: staleRoute},
	}))
	assertMinimalRouteStateResponse(t, app, "/api/users/42", http.StatusServiceUnavailable, "service old-service unavailable")
	assertMinimalRouteStateResponse(t, app, "/api/stale/42", http.StatusServiceUnavailable, "service stale-service unavailable")

	newRoute := minimalRouteStateGRPCDefinition("new", gatewayroute.SourceManual, "", "new-service", "/api/users/:userID", "/new.Service/Get")
	gw.replaceEtcdRouteSnapshot(newRouteSnapshot(map[string]*routeSourceValue{
		"/gateway/routes/new": {Definition: newRoute},
	}))
	assertMinimalRouteStateResponse(t, app, "/api/users/42", http.StatusServiceUnavailable, "service new-service unavailable")
	assertMinimalRouteStateResponse(t, app, "/api/stale/42", http.StatusNotFound, "")
}

func minimalRouteStateDefinition(id string, source gatewayroute.Source, owner, service, path string) *gatewayroute.Definition {
	slot, err := gatewayroute.SlotID(http.MethodGet, path)
	if err != nil {
		panic(err)
	}
	return &gatewayroute.Definition{
		ID:          id,
		Slot:        slot,
		Source:      source,
		Owner:       owner,
		Protocol:    gatewayroute.ProtocolHTTP,
		Method:      http.MethodGet,
		Path:        path,
		ServiceName: service,
		Enabled:     true,
	}
}

func minimalRouteStateGRPCDefinition(id string, source gatewayroute.Source, owner, service, path, method string) *gatewayroute.Definition {
	definition := minimalRouteStateDefinition(id, source, owner, service, path)
	definition.Protocol = gatewayroute.ProtocolGRPC
	definition.GRPCMethod = method
	return definition
}

func assertMinimalRouteStateResponse(t *testing.T, app *core.Core, path string, status int, message string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("%s status = %d, want %d; body=%s", path, response.Code, status, response.Body.String())
	}
	if message == "" {
		return
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s response: %v", path, err)
	}
	if body["msg"] != message {
		t.Fatalf("%s msg = %v, want %q", path, body["msg"], message)
	}
	if _, exists := body["message"]; exists {
		t.Fatalf("%s response contains forbidden message field: %s", path, response.Body.String())
	}
}

func minimalAutomaticCatalogKey(prefix, serviceName string) string {
	return normalizeRouteStoragePrefix(prefix) + string(gatewayroute.SourceAutoHTTP) + "/" + gatewayroute.OwnerID(serviceName)
}

func minimalRouteStateSlot(t *testing.T, path string) string {
	t.Helper()
	slot, err := gatewayroute.SlotID(http.MethodGet, path)
	if err != nil {
		t.Fatal(err)
	}
	return slot
}

func cloneMinimalRouteStateDefinition(definition *gatewayroute.Definition) *gatewayroute.Definition {
	copy := *definition
	return &copy
}
