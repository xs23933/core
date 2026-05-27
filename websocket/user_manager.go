package websocket

import (
	"hash/fnv"
	"sync"
)

const userManagerShardCount = 32

type userShard struct {
	mu    sync.RWMutex
	conns map[string]map[*Conn]struct{}
}

// UserManager manages websocket connections grouped by user ID.
type UserManager struct {
	shards [userManagerShardCount]*userShard

	connMu    sync.RWMutex
	connUsers map[*Conn]string
}

var DefaultUserManager = NewUserManager()

func NewUserManager() *UserManager {
	m := &UserManager{
		connUsers: make(map[*Conn]string),
	}
	for i := range m.shards {
		m.shards[i] = &userShard{
			conns: make(map[string]map[*Conn]struct{}),
		}
	}
	return m
}

func (m *UserManager) Add(userID string, c *Conn) bool {
	if m == nil || userID == "" || c == nil {
		return false
	}

	if oldUserID, ok := m.userIDByConn(c); ok && oldUserID != userID {
		m.removeFromUser(oldUserID, c)
	}

	shard := m.shard(userID)
	shard.mu.Lock()
	if shard.conns[userID] == nil {
		shard.conns[userID] = make(map[*Conn]struct{})
	}
	shard.conns[userID][c] = struct{}{}
	shard.mu.Unlock()

	m.connMu.Lock()
	m.connUsers[c] = userID
	m.connMu.Unlock()

	return true
}

func (m *UserManager) Remove(c *Conn) bool {
	if m == nil || c == nil {
		return false
	}

	m.connMu.Lock()
	userID, ok := m.connUsers[c]
	if ok {
		delete(m.connUsers, c)
	}
	m.connMu.Unlock()

	if !ok {
		return false
	}
	m.removeFromUser(userID, c)
	return true
}

func (m *UserManager) SendToUser(userID string, data []byte) bool {
	conns := m.Connections(userID)
	if len(conns) == 0 {
		return false
	}
	for _, c := range conns {
		c.Send(data)
	}
	return true
}

func (m *UserManager) SendToUsers(userIDs []string, data []byte) bool {
	conns := make(map[*Conn]struct{})
	for _, userID := range userIDs {
		for _, c := range m.Connections(userID) {
			conns[c] = struct{}{}
		}
	}
	if len(conns) == 0 {
		return false
	}
	for c := range conns {
		c.Send(data)
	}
	return true
}

func (m *UserManager) Online(userID string) bool {
	return m.Count(userID) > 0
}

func (m *UserManager) Count(userID string) int {
	if m == nil || userID == "" {
		return 0
	}
	shard := m.shard(userID)
	shard.mu.RLock()
	defer shard.mu.RUnlock()
	return len(shard.conns[userID])
}

func (m *UserManager) Connections(userID string) []*Conn {
	if m == nil || userID == "" {
		return nil
	}
	shard := m.shard(userID)
	shard.mu.RLock()
	defer shard.mu.RUnlock()

	conns := shard.conns[userID]
	if len(conns) == 0 {
		return nil
	}
	out := make([]*Conn, 0, len(conns))
	for c := range conns {
		out = append(out, c)
	}
	return out
}

func (m *UserManager) userIDByConn(c *Conn) (string, bool) {
	m.connMu.RLock()
	defer m.connMu.RUnlock()
	userID, ok := m.connUsers[c]
	return userID, ok
}

func (m *UserManager) removeFromUser(userID string, c *Conn) {
	shard := m.shard(userID)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	conns := shard.conns[userID]
	if len(conns) == 0 {
		return
	}
	delete(conns, c)
	if len(conns) == 0 {
		delete(shard.conns, userID)
	}
}

func (m *UserManager) shard(userID string) *userShard {
	return m.shards[hashUserID(userID)%userManagerShardCount]
}

func hashUserID(userID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(userID))
	return h.Sum32()
}
