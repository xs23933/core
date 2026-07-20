package ratelimit

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/xs23933/core/v3"
)

type Config struct {
	Max        int64
	Window     time.Duration
	Algorithm  Algorithm
	Prefix     string
	StatusCode int
	Message    string
	KeyFunc    func(c core.Ctx) string
	Redis      *core.RedisClient
}

type Algorithm string

const (
	FixedWindow   Algorithm = "fixed_window"
	SlidingWindow Algorithm = "sliding_window"
)

// numShards 分片数 — 每个 shard 持有独立的 mutex，减少锁竞争
const numShards = 16

var DefaultConfig = Config{
	Max:        100,
	Window:     time.Minute,
	Algorithm:  FixedWindow,
	Prefix:     "core:ratelimit:",
	StatusCode: core.StatusTooManyRequests,
	Message:    "rate limit exceeded",
}

// shard 速率限制分片，持有独立锁和状态
type shard struct {
	mu      sync.Mutex
	fixed   map[string]*entry
	sliding map[string][]time.Time
	lastGC  time.Time
}

type Limiter struct {
	cfg    Config
	now    func() time.Time
	seq    atomic.Uint64
	shards [numShards]*shard
}

type entry struct {
	count   int64
	expires time.Time
}

// shardKey 对 key 进行哈希，返回对应的 shard 索引
func shardKey(key string) int {
	// FNV-1a 风格哈希，快速且分布均匀
	h := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return int(h % numShards)
}

func New(conf ...Config) core.HandlerFunc {
	cfg := DefaultConfig
	if len(conf) > 0 {
		merged := conf[0]
		if merged.Max > 0 {
			cfg.Max = merged.Max
		}
		if merged.Window > 0 {
			cfg.Window = merged.Window
		}
		if merged.Algorithm != "" {
			cfg.Algorithm = merged.Algorithm
		}
		if merged.Prefix != "" {
			cfg.Prefix = merged.Prefix
		}
		if merged.StatusCode > 0 {
			cfg.StatusCode = merged.StatusCode
		}
		if merged.Message != "" {
			cfg.Message = merged.Message
		}
		if merged.KeyFunc != nil {
			cfg.KeyFunc = merged.KeyFunc
		}
		cfg.Redis = merged.Redis
	}
	limiter := &Limiter{
		cfg: cfg,
		now: time.Now,
	}
	for i := 0; i < numShards; i++ {
		limiter.shards[i] = &shard{
			fixed:   make(map[string]*entry),
			sliding: make(map[string][]time.Time),
		}
	}
	return limiter.Middleware()
}

func (l *Limiter) Middleware() core.HandlerFunc {
	return func(c core.Ctx) error {
		key := l.buildKey(c)

		var current int64
		var resetIn time.Duration
		var err error
		if l.cfg.Redis != nil {
			current, resetIn, err = l.allowRedis(c.Context(), key)
		} else if l.cfg.Algorithm == SlidingWindow {
			current, resetIn = l.allowMemorySliding(key)
		} else {
			current, resetIn = l.allowMemoryFixed(key)
		}
		if err != nil {
			core.Warn("ratelimit backend error: %v", err)
			return c.Next()
		}

		remaining := l.cfg.Max - current
		if remaining < 0 {
			remaining = 0
		}
		resetAt := time.Now().Add(resetIn).Unix()
		c.SetHeader("X-RateLimit-Limit", strconv.FormatInt(l.cfg.Max, 10))
		c.SetHeader("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
		c.SetHeader("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))

		if current > l.cfg.Max {
			c.SetHeader("Retry-After", strconv.FormatInt(int64(resetIn.Seconds()), 10))
			return c.SendStatus(l.cfg.StatusCode, l.cfg.Message)
		}
		return c.Next()
	}
}

func (l *Limiter) buildKey(c core.Ctx) string {
	if l.cfg.KeyFunc != nil {
		return l.cfg.Prefix + l.cfg.KeyFunc(c)
	}
	return fmt.Sprintf("%s%s:%s:%s", l.cfg.Prefix, c.Method(), c.Path(), c.RemoteIP().String())
}

func (l *Limiter) allowMemoryFixed(key string) (current int64, resetIn time.Duration) {
	now := l.now()
	sh := l.shards[shardKey(key)]

	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.fixed[key]
	if !ok || now.After(e.expires) {
		e = &entry{
			count:   1,
			expires: now.Add(l.cfg.Window),
		}
		sh.fixed[key] = e
		l.gcShardLocked(sh, now)
		return e.count, l.cfg.Window
	}

	e.count++
	resetIn = e.expires.Sub(now)
	if resetIn < 0 {
		resetIn = 0
	}
	return e.count, resetIn
}

func (l *Limiter) allowMemorySliding(key string) (current int64, resetIn time.Duration) {
	now := l.now()
	cutoff := now.Add(-l.cfg.Window)
	sh := l.shards[shardKey(key)]

	sh.mu.Lock()
	defer sh.mu.Unlock()

	hits := sh.sliding[key]
	firstValid := 0
	for firstValid < len(hits) && !hits[firstValid].After(cutoff) {
		firstValid++
	}
	if firstValid > 0 {
		copy(hits, hits[firstValid:])
		hits = hits[:len(hits)-firstValid]
	}

	if int64(len(hits)) >= l.cfg.Max {
		resetIn = max(hits[0].Add(l.cfg.Window).Sub(now), 0)
		sh.sliding[key] = hits
		return int64(len(hits)) + 1, resetIn
	}

	hits = append(hits, now)
	sh.sliding[key] = hits
	l.gcShardLocked(sh, now)
	if len(hits) == 0 {
		return 0, l.cfg.Window
	}
	resetIn = max(hits[0].Add(l.cfg.Window).Sub(now), 0)
	return int64(len(hits)), resetIn
}

// gcShardLocked 清理 shard 中已过期的条目。调用者必须持有 sh.mu。
// 与原始版本不同：1) 仅清理当前 shard（1/16 条目） 2) 有 GC 间隔限流（>= 30s）
func (l *Limiter) gcShardLocked(sh *shard, now time.Time) {
	const gcInterval = 30 * time.Second
	const gcThreshold = 64

	if len(sh.fixed) < gcThreshold && len(sh.sliding) < gcThreshold {
		return
	}
	if now.Before(sh.lastGC.Add(gcInterval)) {
		return
	}
	sh.lastGC = now

	// 批量清理 fixed 过期条目
	for k, v := range sh.fixed {
		if now.After(v.expires) {
			delete(sh.fixed, k)
		}
	}
	// 批量清理 sliding 过期条目
	if len(sh.sliding) > 0 {
		cutoff := now.Add(-l.cfg.Window)
		for k, hits := range sh.sliding {
			if len(hits) == 0 || !hits[len(hits)-1].After(cutoff) {
				delete(sh.sliding, k)
			}
		}
	}
}

func (l *Limiter) allowRedis(ctx context.Context, key string) (current int64, resetIn time.Duration, err error) {
	if l.cfg.Algorithm == SlidingWindow {
		return l.allowRedisSliding(ctx, key)
	}
	return l.allowRedisFixed(ctx, key)
}

func (l *Limiter) allowRedisFixed(ctx context.Context, key string) (current int64, resetIn time.Duration, err error) {
	const script = `
local current = redis.call("INCR", KEYS[1])
if current == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
local ttl = redis.call("PTTL", KEYS[1])
return {current, ttl}
`
	raw, err := l.cfg.Redis.Eval(ctx, script, []string{key}, int64(l.cfg.Window/time.Millisecond))
	if err != nil {
		return 0, 0, err
	}

	arr, ok := raw.([]any)
	if !ok || len(arr) != 2 {
		return 0, 0, fmt.Errorf("unexpected redis result: %T", raw)
	}
	cur, err := toInt64(arr[0])
	if err != nil {
		return 0, 0, err
	}
	ttlMs, err := toInt64(arr[1])
	if err != nil {
		return 0, 0, err
	}
	if ttlMs < 0 {
		ttlMs = int64(l.cfg.Window / time.Millisecond)
	}
	return cur, time.Duration(ttlMs) * time.Millisecond, nil
}

func (l *Limiter) allowRedisSliding(ctx context.Context, key string) (current int64, resetIn time.Duration, err error) {
	const script = `
redis.call("ZREMRANGEBYSCORE", KEYS[1], 0, ARGV[1] - ARGV[2])
local current = redis.call("ZCARD", KEYS[1])
if current < tonumber(ARGV[3]) then
  redis.call("ZADD", KEYS[1], ARGV[1], ARGV[4])
  current = current + 1
else
  current = current + 1
end
redis.call("PEXPIRE", KEYS[1], ARGV[2])
local first = redis.call("ZRANGE", KEYS[1], 0, 0, "WITHSCORES")
local ttl = ARGV[2]
if first[2] then
  ttl = tonumber(first[2]) + tonumber(ARGV[2]) - tonumber(ARGV[1])
end
if ttl < 0 then
  ttl = 0
end
return {current, ttl}
`
	now := l.now()
	nowMs := now.UnixMilli()
	member := fmt.Sprintf("%d:%d", now.UnixNano(), l.seq.Add(1))
	raw, err := l.cfg.Redis.Eval(ctx, script, []string{key},
		nowMs,
		int64(l.cfg.Window/time.Millisecond),
		l.cfg.Max,
		member,
	)
	if err != nil {
		return 0, 0, err
	}

	arr, ok := raw.([]any)
	if !ok || len(arr) != 2 {
		return 0, 0, fmt.Errorf("unexpected redis result: %T", raw)
	}
	cur, err := toInt64(arr[0])
	if err != nil {
		return 0, 0, err
	}
	ttlMs, err := toInt64(arr[1])
	if err != nil {
		return 0, 0, err
	}
	return cur, time.Duration(ttlMs) * time.Millisecond, nil
}

func toInt64(v any) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case int:
		return int64(t), nil
	case string:
		return strconv.ParseInt(t, 10, 64)
	default:
		return 0, fmt.Errorf("unsupported number type %T", v)
	}
}

// RedisNil reports whether the backend error is redis.Nil.
func RedisNil(err error) bool {
	return err == redis.Nil
}
