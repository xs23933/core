package metrics

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestMetricsConcurrentRecord 验证并发记录 metric 的同步安全性
func TestMetricsConcurrentRecord(t *testing.T) {
	m := &Metrics{
		cfg: Config{
			NormalizePath: true,
		},
	}

	var wg sync.WaitGroup
	const goroutines = 50
	const iterations = 500

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			method := "GET"
			path := "/api/users/" + strconv.Itoa(id%100)
			status := 200 + (id % 10)
			for j := 0; j < iterations; j++ {
				key := metricKey(method, normalizePath(path), status)
				now := time.Now()
				val, _ := m.records.LoadOrStore(key, &recordEntry{})
				entry := val.(*recordEntry)
				entry.storeCreatedAt(now)
				entry.rec.count.Add(1)
				entry.rec.latencyNs.Add(1000)
			}
		}(i)
	}

	wg.Wait()

	count := 0
	m.records.Range(func(k, v any) bool {
		count++
		return true
	})

	t.Logf("Concurrent records: %d unique keys from %d goroutines × %d iterations",
		count, goroutines, iterations)

	output := m.RenderPrometheus()
	if len(output) == 0 {
		t.Error("RenderPrometheus returned empty output")
	}
}

// TestMetricsGCStress 验证 GC 清理和并发读写的安全性
func TestMetricsGCStress(t *testing.T) {
	m := &Metrics{
		cfg: Config{
			NormalizePath: true,
			MaxAge:        100 * time.Millisecond,
			MaxSize:       500,
		},
	}
	go m.startGC()

	var wg sync.WaitGroup
	const goroutines = 20
	const iterations = 200

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				key := metricKey("POST", "/api/items/"+strconv.Itoa(j%50), 200+(j%5))
				now := time.Now()
				val, _ := m.records.LoadOrStore(key, &recordEntry{})
				entry := val.(*recordEntry)
				entry.storeCreatedAt(now)
				entry.rec.count.Add(1)
				entry.rec.latencyNs.Add(1000)

				if j%50 == 0 {
					_ = m.RenderPrometheus()
				}
			}
		}(i)
	}

	wg.Wait()

	time.Sleep(200 * time.Millisecond)

	remaining := 0
	m.records.Range(func(_, _ any) bool {
		remaining++
		return true
	})

	t.Logf("After GC stress: remaining=%d records (from %d goroutines × %d iterations, maxSize=500)",
		remaining, goroutines, iterations)
}

// BenchmarkMetricsRecord 基准测试并发 metric 记录
func BenchmarkMetricsRecord(b *testing.B) {
	m := &Metrics{
		cfg: Config{},
	}

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			key := metricKey("GET", "/api/test", 200)
			val, _ := m.records.LoadOrStore(key, &recordEntry{})
			entry := val.(*recordEntry)
			entry.rec.count.Add(1)
		}
	})
}
