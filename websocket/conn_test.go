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
