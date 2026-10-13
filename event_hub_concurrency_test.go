package core

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestEventHubOldConnectionCannotUnregisterReplacement(t *testing.T) {
	hub := NewEventHub(time.Hour)
	defer hub.Close()
	old := make(chan EventData, 1)
	current := make(chan EventData, 1)
	hub.Register(old, "user")
	hub.Register(current, "user")
	hub.UnRegister(old, "user")
	hub.SendTo("user", EventData{Event: "current"})
	select {
	case data, ok := <-current:
		if !ok || data.Event != "current" {
			t.Fatalf("replacement lost: %+v closed=%v", data, !ok)
		}
	default:
		t.Fatal("replacement no longer registered")
	}
	if _, ok := <-old; ok {
		t.Fatal("superseded channel remains open")
	}
}

func TestEventHubChannelAliasesShareLifecycle(t *testing.T) {
	hub := NewEventHub(time.Hour)
	defer hub.Close()
	shared := make(chan EventData, 2)
	replacement := make(chan EventData, 1)
	hub.Register(shared)
	hub.Register(shared, "first")
	hub.Register(shared, "second")
	hub.Register(shared, "second")
	hub.Register(replacement, "first")
	hub.UnRegister(shared, "first")
	hub.UnRegister(shared, "second")
	hub.UnRegister(shared)
	if _, ok := <-shared; ok {
		t.Fatal("replaced shared channel remains open")
	}
	if _, ok := hub.loadNamedClients()["second"]; ok {
		t.Fatal("closed channel alias retained")
	}
	hub.SendTo("second", EventData{Event: "stale"})
	hub.SendTo("first", EventData{Event: "current"})
	select {
	case data := <-replacement:
		if data.Event != "current" {
			t.Fatalf("replacement data=%+v", data)
		}
	default:
		t.Fatal("replacement missing")
	}
	hub.Close()
}

func TestEventHubUnregisterRemovesAllChannelAliases(t *testing.T) {
	hub := NewEventHub(time.Hour)
	defer hub.Close()
	ch := make(chan EventData, 1)
	hub.Register(ch, "a")
	hub.Register(ch, "b")
	hub.UnRegister(ch)
	if len(hub.loadClients()) != 0 || len(hub.loadNamedClients()) != 0 {
		t.Fatal("alias retained after channel unregistration")
	}
	hub.SendTo("a", EventData{})
	hub.SendTo("b", EventData{})
	hub.Close()
}

func TestEventHubConcurrentSendReplaceUnregisterClose(t *testing.T) {
	for round := 0; round < 10; round++ {
		hub := NewEventHub(time.Microsecond)
		start := make(chan struct{})
		var workers sync.WaitGroup
		for worker := 0; worker < 4; worker++ {
			workers.Add(1)
			go func(worker int) {
				defer workers.Done()
				<-start
				for n := 0; n < 250; n++ {
					id := fmt.Sprintf("client-%d", n%8)
					ch := make(chan EventData, 2)
					hub.Register(ch, id)
					hub.SendTo(id, EventData{Event: "direct"})
					hub.UnRegister(ch, id)
				}
			}(worker)
		}
		for worker := 0; worker < 4; worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				for n := 0; n < 1000; n++ {
					data := EventData{Event: "broadcast"}
					hub.broadcast(data)
					hub.Broadcast(data)
					hub.SendTo(fmt.Sprintf("client-%d", n%8), data)
				}
			}()
		}
		workers.Add(1)
		go func() { defer workers.Done(); <-start; time.Sleep(time.Millisecond); hub.Close(); hub.Close() }()
		close(start)
		workers.Wait()
		hub.Close()
		if len(hub.loadClients()) != 0 || len(hub.loadNamedClients()) != 0 {
			t.Fatal("closed hub retained clients")
		}
	}
}

func TestEventHubSendRemainsNonblocking(t *testing.T) {
	hub := NewEventHub(time.Hour)
	defer hub.Close()
	ch := make(chan EventData)
	hub.Register(ch, "slow")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := 0; n < 2000; n++ {
			hub.SendTo("slow", EventData{})
			hub.broadcast(EventData{})
			hub.Broadcast(EventData{})
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow receiver blocked hub send")
	}
}
