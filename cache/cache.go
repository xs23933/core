package cache

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"reflect"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/xs23933/core/v3"
	"golang.org/x/sync/singleflight"
)

var ErrNotFound = errors.New("cache: not found")

var (
	defaultMu    sync.RWMutex
	defaultStore Store
)

type Store interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value any, ttl ...time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

type Loader func(context.Context) (any, error)
type TypedLoader[T any] func(context.Context) (T, error)

type Cache struct {
	store Store
	opts  options
	group singleflight.Group
}

type options struct {
	prefix   string
	ttl      time.Duration
	emptyTTL time.Duration
	jitter   time.Duration
	cacheNil bool
	notFound func(error) bool
}

type Option func(*options)

func New(store Store, opts ...Option) *Cache {
	cfg := buildOptions(opts...)
	return &Cache{
		store: store,
		opts:  cfg,
	}
}

func Default(opts ...Option) *Cache {
	store := DefaultStore()
	if store == nil {
		if rdb := core.RConn(); rdb != nil {
			store = rdb
		}
	}
	return New(store, opts...)
}

func SetDefaultStore(store Store) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultStore = store
}

func DefaultStore() Store {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultStore
}

func Get[T any](ctx context.Context, key string, loader TypedLoader[T]) (T, error) {
	return Load(Default(), ctx, key, loader)
}

func Load[T any](c *Cache, ctx context.Context, key string, loader TypedLoader[T]) (T, error) {
	var value T
	err := c.Take(ctx, key, &value, func(ctx context.Context) (any, error) {
		return loader(ctx)
	})
	return value, err
}

func buildOptions(opts ...Option) options {
	cfg := options{
		ttl:      time.Minute,
		emptyTTL: 10 * time.Second,
		notFound: func(err error) bool {
			return errors.Is(err, ErrNotFound)
		},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

func Prefix(prefix string) Option {
	return func(opts *options) {
		opts.prefix = prefix
	}
}

func TTL(ttl time.Duration) Option {
	return func(opts *options) {
		opts.ttl = ttl
	}
}

func EmptyTTL(ttl time.Duration) Option {
	return func(opts *options) {
		opts.emptyTTL = ttl
	}
}

func Jitter(jitter time.Duration) Option {
	return func(opts *options) {
		opts.jitter = jitter
	}
}

func CacheNil(enabled bool) Option {
	return func(opts *options) {
		opts.cacheNil = enabled
	}
}

func NotFound(fn func(error) bool) Option {
	return func(opts *options) {
		if fn != nil {
			opts.notFound = fn
		}
	}
}

func (c *Cache) Take(ctx context.Context, key string, out any, loader Loader) error {
	if c == nil || c.store == nil {
		value, err := loader(ctx)
		if err != nil {
			return err
		}
		return assign(out, value)
	}

	fullKey := c.key(key)
	if value, ok, err := c.get(ctx, fullKey); err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
	} else if ok {
		return assign(out, value)
	}

	loaded, err, _ := c.group.Do(fullKey, func() (any, error) {
		if value, ok, err := c.get(ctx, fullKey); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, err
			}
		} else if ok {
			return value, nil
		}

		value, err := loader(ctx)
		if err != nil {
			if c.opts.cacheNil && c.opts.notFound(err) {
				_ = c.set(ctx, fullKey, entry{Empty: true}, c.opts.emptyTTL)
			}
			return nil, err
		}

		data, err := sonic.Marshal(value)
		if err != nil {
			return nil, err
		}
		_ = c.set(ctx, fullKey, entry{Data: data}, c.ttl())
		return value, nil
	})
	if err != nil {
		return err
	}
	return assign(out, loaded)
}

func (c *Cache) Delete(ctx context.Context, keys ...string) error {
	if c == nil || c.store == nil {
		return nil
	}
	fullKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		fullKeys = append(fullKeys, c.key(key))
	}
	return c.store.Delete(ctx, fullKeys...)
}

func (c *Cache) get(ctx context.Context, key string) (json.RawMessage, bool, error) {
	raw, err := c.store.Get(ctx, key)
	if err != nil || raw == "" {
		return nil, false, err
	}

	var ent entry
	if err := sonic.UnmarshalString(raw, &ent); err != nil {
		return nil, false, err
	}
	if ent.Empty {
		return nil, false, ErrNotFound
	}
	return ent.Data, true, nil
}

func (c *Cache) set(ctx context.Context, key string, ent entry, ttl time.Duration) error {
	body, err := sonic.Marshal(ent)
	if err != nil {
		return err
	}
	return c.store.Set(ctx, key, body, ttl)
}

func (c *Cache) key(key string) string {
	return c.opts.prefix + key
}

func (c *Cache) ttl() time.Duration {
	ttl := c.opts.ttl
	if c.opts.jitter <= 0 {
		return ttl
	}
	return ttl + time.Duration(rand.Int64N(int64(c.opts.jitter)+1))
}

type entry struct {
	Empty bool            `json:"empty,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

func assign(out any, value any) error {
	if out == nil {
		return errors.New("cache: out must not be nil")
	}
	if raw, ok := value.(json.RawMessage); ok {
		return sonic.Unmarshal(raw, out)
	}
	if raw, ok := value.([]byte); ok {
		return sonic.Unmarshal(raw, out)
	}

	dst := reflect.ValueOf(out)
	if dst.Kind() != reflect.Pointer || dst.IsNil() {
		return errors.New("cache: out must be a non-nil pointer")
	}

	src := reflect.ValueOf(value)
	if !src.IsValid() {
		dst.Elem().Set(reflect.Zero(dst.Elem().Type()))
		return nil
	}
	if src.Type().AssignableTo(dst.Elem().Type()) {
		dst.Elem().Set(src)
		return nil
	}
	if src.Kind() == reflect.Pointer && !src.IsNil() && src.Elem().Type().AssignableTo(dst.Elem().Type()) {
		dst.Elem().Set(src.Elem())
		return nil
	}
	return errors.New("cache: loader value cannot assign to out")
}
