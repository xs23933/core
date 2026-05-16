package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xs23933/core/v3"
)

func TestMemoryRateLimit(t *testing.T) {
	app := core.New()
	app.Use(New(Config{
		Max:    2,
		Window: 2 * time.Second,
		KeyFunc: func(c core.Ctx) string {
			return "u:1"
		},
	}))
	app.GET("/v1/users", func(c core.Ctx) error {
		return c.SendString("ok")
	})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d status = %d, want 200", i+1, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("X-RateLimit-Limit") != "2" {
		t.Fatalf("limit header = %q, want 2", rec.Header().Get("X-RateLimit-Limit"))
	}
	if rec.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("remaining header = %q, want 0", rec.Header().Get("X-RateLimit-Remaining"))
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatalf("retry-after header must exist")
	}
}

func TestMemorySlidingWindow(t *testing.T) {
	now := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	limiter := &Limiter{
		cfg: Config{
			Max:       2,
			Window:    time.Second,
			Algorithm: SlidingWindow,
		},
		now:     func() time.Time { return now },
		fixed:   make(map[string]*entry),
		sliding: make(map[string][]time.Time),
	}

	current, _ := limiter.allowMemorySliding("user:1")
	if current != 1 {
		t.Fatalf("first current = %d, want 1", current)
	}
	current, _ = limiter.allowMemorySliding("user:1")
	if current != 2 {
		t.Fatalf("second current = %d, want 2", current)
	}
	current, resetIn := limiter.allowMemorySliding("user:1")
	if current != 3 {
		t.Fatalf("third current = %d, want 3", current)
	}
	if resetIn <= 0 {
		t.Fatalf("resetIn = %s, want positive", resetIn)
	}

	now = now.Add(time.Second + time.Millisecond)
	current, _ = limiter.allowMemorySliding("user:1")
	if current != 1 {
		t.Fatalf("after window current = %d, want 1", current)
	}
}

func TestMemoryRateLimitConcurrent(t *testing.T) {
	app := core.New()
	var passed atomic.Int64
	app.Use(New(Config{
		Max:       10,
		Window:    time.Second,
		Algorithm: SlidingWindow,
		KeyFunc: func(c core.Ctx) string {
			return "u:concurrent"
		},
	}))
	app.GET("/v1/users", func(c core.Ctx) error {
		passed.Add(1)
		return c.SendString("ok")
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK && rec.Code != http.StatusTooManyRequests {
				t.Errorf("status = %d, want 200 or 429", rec.Code)
			}
		}()
	}
	wg.Wait()

	if got := passed.Load(); got != 10 {
		t.Fatalf("passed = %d, want 10", got)
	}
}

func BenchmarkMemoryFixedWindow(b *testing.B) {
	benchmarkLimiterMemory(b, FixedWindow)
}

func BenchmarkMemorySlidingWindow(b *testing.B) {
	benchmarkLimiterMemory(b, SlidingWindow)
}

func benchmarkLimiterMemory(b *testing.B, algorithm Algorithm) {
	limiter := &Limiter{
		cfg: Config{
			Max:       int64(b.N) + 1,
			Window:    time.Minute,
			Algorithm: algorithm,
		},
		now:     time.Now,
		fixed:   make(map[string]*entry),
		sliding: make(map[string][]time.Time),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var current int64
		if algorithm == SlidingWindow {
			current, _ = limiter.allowMemorySliding("bench")
		} else {
			current, _ = limiter.allowMemoryFixed("bench")
		}
		if current > int64(b.N)+1 {
			b.Fatalf("current = %d, want <= %d", current, b.N+1)
		}
	}
}
