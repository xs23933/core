package websocket

import (
	"fmt"
	"sync"
	"testing"
)

func testConn() *Conn {
	return &Conn{
		send:      make(chan *message, 32),
		batchMsgs: make([]*message, 0),
	}
}

func readTextMessage(t *testing.T, c *Conn) string {
	t.Helper()
	select {
	case msg := <-c.send:
		defer bufferPool.Put(msg.data)
		return string(*msg.data)
	default:
		t.Fatal("expected websocket message")
		return ""
	}
}

func assertNoMessage(t *testing.T, c *Conn) {
	t.Helper()
	select {
	case msg := <-c.send:
		defer bufferPool.Put(msg.data)
		t.Fatalf("unexpected websocket message: %q", string(*msg.data))
	default:
	}
}

func TestUserManagerSendToUserSendsAllUserConnections(t *testing.T) {
	manager := NewUserManager()
	c1 := testConn()
	c2 := testConn()
	other := testConn()

	if !manager.Add("u1", c1) || !manager.Add("u1", c2) || !manager.Add("u2", other) {
		t.Fatal("add connection failed")
	}

	if got := manager.Count("u1"); got != 2 {
		t.Fatalf("count u1 = %d, want 2", got)
	}
	if !manager.Online("u1") {
		t.Fatal("u1 should be online")
	}

	if !manager.SendToUser("u1", []byte("hello")) {
		t.Fatal("send to u1 failed")
	}

	if got := readTextMessage(t, c1); got != "hello" {
		t.Fatalf("c1 message = %q, want hello", got)
	}
	if got := readTextMessage(t, c2); got != "hello" {
		t.Fatalf("c2 message = %q, want hello", got)
	}
	assertNoMessage(t, other)
}

func TestUserManagerRemoveConnection(t *testing.T) {
	manager := NewUserManager()
	c1 := testConn()
	c2 := testConn()

	manager.Add("u1", c1)
	manager.Add("u1", c2)
	if !manager.Remove(c1) {
		t.Fatal("remove c1 failed")
	}

	if got := manager.Count("u1"); got != 1 {
		t.Fatalf("count after remove = %d, want 1", got)
	}

	manager.SendToUser("u1", []byte("after-remove"))
	assertNoMessage(t, c1)
	if got := readTextMessage(t, c2); got != "after-remove" {
		t.Fatalf("c2 message = %q, want after-remove", got)
	}

	manager.Remove(c2)
	if manager.Online("u1") {
		t.Fatal("u1 should be offline")
	}
}

func TestConnCloseRemovesDefaultUserManagerConnection(t *testing.T) {
	oldDefault := DefaultUserManager
	DefaultUserManager = NewUserManager()
	defer func() {
		DefaultUserManager = oldDefault
	}()

	c := testConn()
	DefaultUserManager.Add("u1", c)
	c.Close()

	if DefaultUserManager.Online("u1") {
		t.Fatal("closed connection should be removed from default user manager")
	}
}

func TestUserManagerSendToUsersDeduplicatesConnections(t *testing.T) {
	manager := NewUserManager()
	c := testConn()

	manager.Add("u1", c)
	manager.Add("u1", c)

	if !manager.SendToUsers([]string{"u1", "u1"}, []byte("once")) {
		t.Fatal("send to users failed")
	}

	if got := readTextMessage(t, c); got != "once" {
		t.Fatalf("message = %q, want once", got)
	}
	assertNoMessage(t, c)
}

func TestUserManagerConcurrentAccess(t *testing.T) {
	manager := NewUserManager()
	var wg sync.WaitGroup

	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			userID := fmt.Sprintf("u%d", i%8)
			conn := testConn()
			manager.Add(userID, conn)
			manager.SendToUser(userID, []byte("msg"))
			manager.Remove(conn)
		}(i)
	}

	wg.Wait()
}
