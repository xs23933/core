package core

import (
	"sync"
	"testing"
	"time"
)

// TestConnConcurrentReads 验证 Conn() 在并发读场景下不会触发 race condition
func TestConnConcurrentReads(t *testing.T) {
	// 先初始化 conns（模拟 NewModel 调用）
	dbMu.Lock()
	conns["test_stress"] = nil // 模拟数据库连接存在
	conns["default"] = nil
	dbMu.Unlock()

	var wg sync.WaitGroup
	const goroutines = 100
	const iterations = 1000

	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				db := Conn("test_stress")
				_ = db     // 不 panic 即可
				_ = Conn() // 默认连接
				_ = DBType("test_stress")
				_ = DBType()
			}
		}()
	}

	wg.Wait()
	close(errCh)

	// 清理
	dbMu.Lock()
	delete(conns, "test_stress")
	dbMu.Unlock()

	t.Logf("Completed %d concurrent reads across %d goroutines without crash",
		goroutines*iterations, goroutines)
}

// TestModelMapConcurrentWriteRead 验证并发写（模拟 NewModel）和读（Conn）同时进行时的安全性
func TestModelMapConcurrentWriteRead(t *testing.T) {
	var wg sync.WaitGroup
	const goroutines = 20
	const iterations = 1000

	// 先放入初始值
	dbMu.Lock()
	conns["shared"] = nil
	dbsType["shared"] = "mysql"
	dbMu.Unlock()

	done := make(chan struct{})

	// 读者
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					_ = Conn("shared")
					_ = DBType("shared")
				}
			}
		}()
	}

	// 写者（模拟多数据库配置写入）
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				dbMu.Lock()
				conns["shared"] = nil
				dbsType["shared"] = "mysql"
				dbMu.Unlock()
			}
		}(i)
	}

	// 给写者一点时间运行
	time.Sleep(50 * time.Millisecond)
	close(done)
	wg.Wait()

	t.Logf("Concurrent read/write on conns/dbsType maps completed without fatal crash")
}
