package websocket

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"
)

type Shard struct {
	conns atomic.Value // map[*Conn]struct{}, copy-on-write
	mu    sync.Mutex
}

type Manager struct {
	shards [32]*Shard
}

var DefaultManager = NewManager()

func NewManager() *Manager {
	m := &Manager{}
	for i := 0; i < 32; i++ {
		m.shards[i] = newShard()
	}
	return m
}

func newShard() *Shard {
	s := &Shard{}
	s.store(make(map[*Conn]struct{}))
	return s
}

func (s *Shard) load() map[*Conn]struct{} {
	if conns, ok := s.conns.Load().(map[*Conn]struct{}); ok && conns != nil {
		return conns
	}
	return nil
}

func (s *Shard) store(conns map[*Conn]struct{}) {
	s.conns.Store(conns)
}

func (m *Manager) Add(c *Conn) {
	idx := rand.IntN(32)
	s := m.shards[idx]

	s.mu.Lock()
	conns := s.load()
	next := make(map[*Conn]struct{}, len(conns)+1)
	for conn := range conns {
		next[conn] = struct{}{}
	}
	next[c] = struct{}{}
	s.store(next)
	s.mu.Unlock()

	c.shard = s
}

func (m *Manager) Remove(c *Conn) {
	s := c.shard
	if s == nil {
		return
	}

	s.mu.Lock()
	conns := s.load()
	if _, ok := conns[c]; ok {
		next := make(map[*Conn]struct{}, len(conns)-1)
		for conn := range conns {
			if conn != c {
				next[conn] = struct{}{}
			}
		}
		s.store(next)
	}
	s.mu.Unlock()
}

func (m *Manager) Broadcast(msg []byte) {
	m.BroadcastWithType(TextMessage, msg)
}

func (m *Manager) BroadcastWithType(messageType MessageType, msg []byte) {
	for i := 0; i < 32; i++ {
		go func(s *Shard) {
			for c := range s.load() {
				c.SendWithType(messageType, msg)
			}
		}(m.shards[i])
	}
}
