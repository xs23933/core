package route

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

type memoryStore struct {
	values map[string][]byte
	puts   []string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{values: make(map[string][]byte)}
}

func (s *memoryStore) Put(_ context.Context, key string, value []byte) error {
	s.puts = append(s.puts, key)
	s.values[key] = bytes.Clone(value)
	return nil
}

func automaticHTTPDefinition(path string) *Definition {
	return &Definition{
		Protocol:    ProtocolHTTP,
		Method:      "GET",
		Path:        path,
		ServiceName: "users",
		Enabled:     true,
	}
}

func TestPublisherWritesManualDefinitionsBySlot(t *testing.T) {
	store := newMemoryStore()
	publisher := Publisher{Store: store, RoutePrefix: "/gateway/routes/"}
	definition := automaticHTTPDefinition("/api/users/:id")
	slot, err := SlotID(definition.Method, definition.Path)
	if err != nil {
		t.Fatal(err)
	}

	if err := publisher.PublishManual(context.Background(), definition); err != nil {
		t.Fatal(err)
	}
	wantKey := "/gateway/routes/manual/" + slot
	if len(store.puts) != 1 || store.puts[0] != wantKey {
		t.Fatalf("put keys = %v, want [%s]", store.puts, wantKey)
	}
}

func TestPublisherSyncAutomaticHTTPReplacesOneCatalogValue(t *testing.T) {
	store := newMemoryStore()
	publisher := Publisher{Store: store, RoutePrefix: "/gateway/routes/"}
	syncer, ok := any(publisher).(interface {
		SyncAutomaticHTTP(context.Context, string, []*Definition) error
	})
	if !ok {
		t.Fatal("Publisher does not implement SyncAutomaticHTTP")
	}

	first := automaticHTTPDefinition("/api/users/:id")
	second := automaticHTTPDefinition("/api/users")
	if err := syncer.SyncAutomaticHTTP(context.Background(), "users", []*Definition{first, second}); err != nil {
		t.Fatal(err)
	}
	key := "/gateway/routes/auto_http/" + OwnerID("users")
	if len(store.values) != 1 || len(store.puts) != 1 || store.puts[0] != key {
		t.Fatalf("first sync values=%v puts=%v, want one put to %s", store.values, store.puts, key)
	}

	var catalog Catalog
	if err := json.Unmarshal(store.values[key], &catalog); err != nil {
		t.Fatalf("decode first catalog: %v", err)
	}
	if catalog.ServiceName != "users" || len(catalog.Routes) != 2 {
		t.Fatalf("first catalog = %#v", catalog)
	}
	for _, definition := range catalog.Routes {
		if definition.Source != SourceAutoHTTP || definition.Owner != "users" || definition.ServiceName != "users" {
			t.Fatalf("automatic route metadata = %#v", definition)
		}
	}
	if catalog.Routes[0].Slot > catalog.Routes[1].Slot {
		t.Fatalf("catalog routes are not sorted by slot: %s > %s", catalog.Routes[0].Slot, catalog.Routes[1].Slot)
	}

	if err := syncer.SyncAutomaticHTTP(context.Background(), "users", []*Definition{second}); err != nil {
		t.Fatal(err)
	}
	if len(store.values) != 1 || len(store.puts) != 2 || store.puts[1] != key {
		t.Fatalf("second sync values=%v puts=%v, want replacement at %s", store.values, store.puts, key)
	}
	if err := json.Unmarshal(store.values[key], &catalog); err != nil {
		t.Fatalf("decode replacement catalog: %v", err)
	}
	if len(catalog.Routes) != 1 || catalog.Routes[0].Path != second.Path {
		t.Fatalf("replacement retained stale routes: %#v", catalog.Routes)
	}
}

func TestPublisherValidatesBatchesBeforeWriting(t *testing.T) {
	store := newMemoryStore()
	publisher := Publisher{Store: store, RoutePrefix: "/gateway/routes/"}
	invalid := automaticHTTPDefinition("not-absolute")

	if err := publisher.PublishManual(context.Background(), automaticHTTPDefinition("/valid"), invalid); err == nil {
		t.Fatal("PublishManual() error = nil, want validation failure")
	}
	if len(store.puts) != 0 {
		t.Fatalf("invalid manual batch wrote keys: %v", store.puts)
	}

	syncer, ok := any(publisher).(interface {
		SyncAutomaticHTTP(context.Context, string, []*Definition) error
	})
	if !ok {
		return
	}
	if err := syncer.SyncAutomaticHTTP(context.Background(), "users", []*Definition{automaticHTTPDefinition("/valid"), invalid}); err == nil {
		t.Fatal("SyncAutomaticHTTP() error = nil, want validation failure")
	}
	if len(store.puts) != 0 {
		t.Fatalf("invalid automatic batch wrote keys: %v", store.puts)
	}
}
