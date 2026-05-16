package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type user struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type order struct {
	ID     string `json:"id"`
	Amount int    `json:"amount"`
}

func TestTakeLoadsAndCachesMiss(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	c := New(store, Prefix("user:"), TTL(time.Minute))

	var loads atomic.Int64
	var got user
	err := c.Take(ctx, "1", &got, func(ctx context.Context) (any, error) {
		loads.Add(1)
		return user{ID: "1", Name: "tom"}, nil
	})
	if err != nil {
		t.Fatalf("first get error: %v", err)
	}
	if got.Name != "tom" {
		t.Fatalf("first get = %+v, want tom", got)
	}

	var cached user
	err = c.Take(ctx, "1", &cached, func(ctx context.Context) (any, error) {
		loads.Add(1)
		return user{ID: "1", Name: "jerry"}, nil
	})
	if err != nil {
		t.Fatalf("second get error: %v", err)
	}
	if cached.Name != "tom" {
		t.Fatalf("second get = %+v, want cached tom", cached)
	}
	if loads.Load() != 1 {
		t.Fatalf("loads = %d, want 1", loads.Load())
	}
	if store.ttl("user:1") != time.Minute {
		t.Fatalf("ttl = %s, want 1m", store.ttl("user:1"))
	}
}

func TestCacheInstanceHandlesDifferentTypes(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	c := New(store, TTL(time.Minute))

	var u user
	if err := c.Take(ctx, "user:1", &u, func(ctx context.Context) (any, error) {
		return user{ID: "1", Name: "tom"}, nil
	}); err != nil {
		t.Fatalf("take user error: %v", err)
	}

	var o order
	if err := c.Take(ctx, "order:1", &o, func(ctx context.Context) (any, error) {
		return order{ID: "1", Amount: 99}, nil
	}); err != nil {
		t.Fatalf("take order error: %v", err)
	}

	if u.Name != "tom" || o.Amount != 99 {
		t.Fatalf("typed values = %+v %+v, want tom/99", u, o)
	}
}

func TestLoadUsesNonGenericCacheInstance(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	c := New(store, Prefix("user:"), TTL(time.Minute))

	got, err := Load[user](c, ctx, "1", func(ctx context.Context) (user, error) {
		return user{ID: "1", Name: "tom"}, nil
	})
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	if got.Name != "tom" {
		t.Fatalf("got = %+v, want tom", got)
	}
}

func TestTakeAssignsPointerLoaderValue(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	c := New(store, Prefix("user:"), TTL(time.Minute))

	var got user
	if err := c.Take(ctx, "1", &got, func(ctx context.Context) (any, error) {
		return &user{ID: "1", Name: "tom"}, nil
	}); err != nil {
		t.Fatalf("take error: %v", err)
	}
	if got.Name != "tom" {
		t.Fatalf("got = %+v, want tom", got)
	}
}

func TestTakeCoalescesConcurrentMisses(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	c := New(store, Prefix("user:"), TTL(time.Minute))

	var loads atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var got user
			err := c.Take(ctx, "1", &got, func(ctx context.Context) (any, error) {
				loads.Add(1)
				time.Sleep(10 * time.Millisecond)
				return user{ID: "1", Name: "tom"}, nil
			})
			if err != nil {
				errs <- err
				return
			}
			if got.Name != "tom" {
				errs <- errors.New("unexpected user")
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loads = %d, want 1", loads.Load())
	}
}

func TestTakeCachesNotFound(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	c := New(
		store,
		Prefix("user:"),
		TTL(time.Minute),
		EmptyTTL(10*time.Second),
		CacheNil(true),
	)

	var loads atomic.Int64
	var got user
	err := c.Take(ctx, "missing", &got, func(ctx context.Context) (any, error) {
		loads.Add(1)
		return user{}, ErrNotFound
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("first err = %v, want ErrNotFound", err)
	}

	err = c.Take(ctx, "missing", &got, func(ctx context.Context) (any, error) {
		loads.Add(1)
		return user{ID: "missing", Name: "should-not-load"}, nil
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("second err = %v, want cached ErrNotFound", err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loads = %d, want 1", loads.Load())
	}
	if store.ttl("user:missing") != 10*time.Second {
		t.Fatalf("empty ttl = %s, want 10s", store.ttl("user:missing"))
	}
}

func TestDeleteUsesPrefix(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	c := New(store, Prefix("user:"), TTL(time.Minute))

	var got user
	if err := c.Take(ctx, "1", &got, func(ctx context.Context) (any, error) {
		return user{ID: "1", Name: "tom"}, nil
	}); err != nil {
		t.Fatalf("get error: %v", err)
	}
	if err := c.Delete(ctx, "1"); err != nil {
		t.Fatalf("delete error: %v", err)
	}
	if store.exists("user:1") {
		t.Fatalf("key user:1 still exists")
	}
}

func TestPackageGetUsesDefaultStore(t *testing.T) {
	ctx := context.Background()
	old := DefaultStore()
	store := newMemoryStore()
	SetDefaultStore(store)
	defer SetDefaultStore(old)

	var loads atomic.Int64
	got, err := Get(ctx, "user:1", func(ctx context.Context) (user, error) {
		loads.Add(1)
		return user{ID: "1", Name: "tom"}, nil
	})
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if got.Name != "tom" {
		t.Fatalf("got = %+v, want tom", got)
	}
	if loads.Load() != 1 {
		t.Fatalf("loads = %d, want 1", loads.Load())
	}
}

func TestNilStoreFallsBackToLoader(t *testing.T) {
	ctx := context.Background()
	c := New(nil)

	var got user
	err := c.Take(ctx, "1", &got, func(ctx context.Context) (any, error) {
		return user{ID: "1", Name: "tom"}, nil
	})
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if got.Name != "tom" {
		t.Fatalf("got = %+v, want tom", got)
	}
}

type memoryStore struct {
	mu   sync.Mutex
	data map[string]string
	ttls map[string]time.Duration
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		data: make(map[string]string),
		ttls: make(map[string]time.Duration),
	}
}

func (s *memoryStore) Get(ctx context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key], nil
}

func (s *memoryStore) Set(ctx context.Context, key string, value any, ttl ...time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = string(value.([]byte))
	if len(ttl) > 0 {
		s.ttls[key] = ttl[0]
	}
	return nil
}

func (s *memoryStore) Delete(ctx context.Context, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.data, key)
		delete(s.ttls, key)
	}
	return nil
}

func (s *memoryStore) exists(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[key]
	return ok
}

func (s *memoryStore) ttl(key string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ttls[key]
}
