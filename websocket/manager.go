package websocket

import (
	"math/rand/v2"
	"sync"
)

type Shard struct {
	conns map[*Conn]struct{}
	mu    sync.RWMutex
}

type Manager struct {
	shards [32]*Shard
}

var DefaultManager = NewManager()

func NewManager() *Manager {
	m := &Manager{}
	for i := 0; i < 32; i++ {
		m.shards[i] = &Shard{
			conns: make(map[*Conn]struct{}),
		}
	}
	return m
}

func (m *Manager) Add(c *Conn) {
	idx := rand.IntN(32)
	s := m.shards[idx]

	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()

	c.shard = s
}

func (m *Manager) Remove(c *Conn) {
	s := c.shard
	if s == nil {
		return
	}

	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (m *Manager) Broadcast(msg []byte) {
	m.BroadcastWithType(TextMessage, msg)
}

func (m *Manager) BroadcastWithType(messageType MessageType, msg []byte) {
	for i := 0; i < 32; i++ {
		go func(s *Shard) {
			s.mu.RLock()
			defer s.mu.RUnlock()
			for c := range s.conns {
				c.SendWithType(messageType, msg)
			}
		}(m.shards[i])
	}
}
