package core

import (
	"sync"
	"testing"
)

// TestNewErrorConcurrent 验证 NewError 并发安全 — 不会再 panic
func TestNewErrorConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	const goroutines = 50
	const iterations = 1000

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// 正常场景
				_ = NewError(500, "internal error")
				_ = NewError(404, "not found")
				_ = NewError(400)

				// 非法类型 — 不应 panic（修复前会 panic）
				_ = NewError(500, 12345)
				_ = NewError(400, struct{ Name string }{Name: "test"})
				_ = NewError(401, 123, "extra", "args")
			}
		}(i)
	}

	wg.Wait()
	t.Logf("NewError survived %d concurrent calls (%d goroutines × %d iterations)",
		goroutines*iterations, goroutines, iterations)
}

// BenchmarkNewErrorNormal 基准测试修复后的 NewError 性能
func BenchmarkNewErrorNormal(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewError(500, "error message: test")
	}
}

func BenchmarkNewErrorNoArgs(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewError(404)
	}
}

func BenchmarkNewErrorConcurrent(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = NewError(500, "error message")
		}
	})
}
