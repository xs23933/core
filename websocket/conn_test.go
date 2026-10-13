package websocket

import (
	"sync"
	"testing"
	"time"
)

func TestSendAfterCloseDoesNotQueueMessage(t *testing.T) {
	c := &Conn{
		send:      make(chan *message, 32),
		batchMsgs: make([]*message, 0),
	}

	c.Close()
	c.Send([]byte("after-close"))

	if got := len(c.send); got != 0 {
		t.Fatalf("queued messages after close = %d, want 0", got)
	}
}

func TestPutBufferDropsOversizedBuffers(t *testing.T) {
	oversized := make([]byte, 0, maxPooledBufferCap+1)

	putBuffer(&oversized)

	got := bufferPool.Get()
	defer putBuffer(got)
	if cap(*got) > maxPooledBufferCap {
		t.Fatalf("pooled buffer cap = %d, want <= %d", cap(*got), maxPooledBufferCap)
	}
}

func TestBatchQueueFullPreservesAlreadyQueuedPayload(t *testing.T) {
	c := &Conn{send: make(chan *message, 1)}
	c.EnableBatch(BatchConfig{MaxSize: 2, MaxDelay: time.Hour})
	c.SendBatchWithType(TextMessage, []byte("first-owned-by-send-queue"))
	c.SendBatchWithType(BinaryMessage, []byte("second-dropped-on-overflow"))
	if !c.closed.Load() {
		t.Fatal("queue overflow did not close connection")
	}
	// 首条消息已成功入队，Close 不能把其缓冲区作为剩余批量再次归还并清空。
	msg := <-c.send
	defer putBuffer(msg.data)
	if got := string(*msg.data); got != "first-owned-by-send-queue" {
		t.Fatalf("queued payload corrupted after batch overflow: %q", got)
	}
	if len(c.batchMsgs) != 0 {
		t.Fatal("closed connection retained batch ownership")
	}
}

func TestBatchOverflowFlushAndCloseDoNotDeadlock(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		c := &Conn{send: make(chan *message, 1)}
		c.EnableBatch(BatchConfig{MaxSize: 2, MaxDelay: time.Microsecond})
		var workers sync.WaitGroup
		for sender := 0; sender < 4; sender++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for message := 0; message < 10; message++ {
					c.SendBatchWithType(TextMessage, []byte("payload"))
				}
			}()
		}
		workers.Add(1)
		go func() { defer workers.Done(); c.Close() }()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("batch flush and Close deadlocked")
		}
		for len(c.send) > 0 {
			msg := <-c.send
			putBuffer(msg.data)
		}
	}
}
