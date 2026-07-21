package websocket

import (
	"testing"
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
