package websocket

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConnCloseDoneChannel 验证 Close 关闭 done channel 后 writeLoop 快速退出
func TestConnCloseDoneChannel(t *testing.T) {
	c := newConn(nil)
	c.conn = nil // 跳过真正的 websocket conn

	// 启动 writeLoop（会因为 c.conn 为 nil 而退出，但先验证 done channel 存在）
	doneClosed := false

	c.Close()

	// 验证 done channel 已关闭
	select {
	case <-c.done:
		doneClosed = true
	default:
	}

	if !doneClosed {
		t.Error("done channel was not closed after Close()")
	}
}

// TestConnCloseNoDoubleClose 验证重复 Close 不会导致 panic
func TestConnCloseNoDoubleClose(t *testing.T) {
	c := newConn(nil)
	c.conn = nil

	// 多次调用 Close 不应 panic
	for i := 0; i < 10; i++ {
		c.Close()
	}
}

// TestConnConcurrentSendAndClose 验证并发 Send 和 Close 不会 race
func TestConnConcurrentSendAndClose(t *testing.T) {
	c := newConn(nil)
	c.conn = nil

	var wg sync.WaitGroup
	const senders = 20
	const iterations = 500

	var sendDone atomic.Int32

	// 发送者
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				c.Send([]byte("concurrent stress test message"))
				if c.closed.Load() {
					return
				}
			}
			sendDone.Add(1)
		}()
	}

	// 等待一小段时间让 senders 开始
	time.Sleep(10 * time.Millisecond)

	// 关闭
	c.Close()

	wg.Wait()

	// 排空 send channel
	drained := 0
	for {
		select {
		case <-c.send:
			drained++
		default:
			goto done
		}
	}
done:

	t.Logf("Concurrent send/close: drained=%d messages, senders_completed=%d",
		drained, sendDone.Load())
}

// TestConnMetaConcurrent 验证 SetMeta / GetMeta 并发安全
func TestConnMetaConcurrent(t *testing.T) {
	c := newConn(nil)
	c.conn = nil

	var wg sync.WaitGroup
	const goroutines = 50
	const iterations = 500

	// 写者
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				c.SetMeta(j)
			}
		}(i)
	}

	// 读者（包括泛型和非泛型）
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = c.GetMeta()
				_, _ = GetMeta[int](c)
			}
		}()
	}

	wg.Wait()
	t.Logf("Concurrent SetMeta/GetMeta across %d goroutines completed without race", goroutines)
}

// TestConnBatchConfigConcurrent 验证 EnableBatch / SendBatchWithType 并发安全
func TestConnBatchConfigConcurrent(t *testing.T) {
	c := newConn(nil)
	c.conn = nil

	c.EnableBatch(BatchConfig{
		Enabled:    true,
		MaxSize:    5,
		MaxDelay:   10 * time.Millisecond,
		InitialCap: 10,
	})

	var wg sync.WaitGroup
	const goroutines = 10
	const iterations = 50

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				c.SendBatchWithType(TextMessage, []byte("batch-stress"))
				if c.closed.Load() {
					return
				}
			}
		}(i)
	}

	time.Sleep(100 * time.Millisecond)
	c.Close()
	wg.Wait()

	t.Log("Concurrent batch sending completed without race")
}
