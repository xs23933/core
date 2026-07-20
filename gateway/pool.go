package gateway

import (
	"sync"
	"sync/atomic"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc/connectivity"
)

// ServicePool 服务连接池（按 instanceID 管理多个实例连接）
type ServicePool struct {
	name  string
	state atomic.Value // servicePoolState, copy-on-write
	idx   uint64       // atomic round-robin index
	mu    sync.Mutex
	app   *core.Core
}

type servicePoolState struct {
	byID map[string]*ReflectionProxy
	all  []*ReflectionProxy
}

// NewServicePool 创建服务连接池
func NewServicePool(app *core.Core, name string) *ServicePool {
	p := &ServicePool{
		name: name,
		app:  app,
	}
	p.storeState(servicePoolState{byID: make(map[string]*ReflectionProxy)})
	return p
}

func (p *ServicePool) loadState() servicePoolState {
	if state, ok := p.state.Load().(servicePoolState); ok && state.byID != nil {
		return state
	}
	return servicePoolState{byID: make(map[string]*ReflectionProxy)}
}

func (p *ServicePool) storeState(state servicePoolState) {
	p.state.Store(state)
}

func copyServicePoolState(state servicePoolState, extra int) servicePoolState {
	nextByID := make(map[string]*ReflectionProxy, len(state.byID)+extra)
	for id, proxy := range state.byID {
		nextByID[id] = proxy
	}
	nextAll := make([]*ReflectionProxy, 0, len(nextByID))
	for _, proxy := range nextByID {
		nextAll = append(nextAll, proxy)
	}
	return servicePoolState{byID: nextByID, all: nextAll}
}

// AddOrUpdateInstance 添加或更新一个实例连接
// 返回 changed=true 表示是新增或重建了 proxy
func (p *ServicePool) AddOrUpdateInstance(instanceID, addr string) (*ReflectionProxy, bool, error) {
	// 快速路径：已有同 ID 同地址且连接健康，直接跳过
	if old, ok := p.loadState().byID[instanceID]; ok && old.addr == addr {
		if old.conn != nil && old.conn.GetState() == connectivity.Ready {
			return old, false, nil
		}
	}

	// 慢路径：先创建新 proxy（不持锁，避免阻塞 Get）
	proxy, err := newReflectionProxyForService(p.app, p.name, addr)
	if err != nil {
		return nil, false, err
	}

	// 加锁替换
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.loadState()

	// double check：可能另一个 goroutine 已抢先完成
	if old, ok := state.byID[instanceID]; ok && old.addr == addr {
		if old.conn != nil && old.conn.GetState() == connectivity.Ready {
			proxy.Close() // 丢弃新创建的
			return old, false, nil
		}
	}

	next := copyServicePoolState(state, 1)
	old := next.byID[instanceID]
	next.byID[instanceID] = proxy
	next.all = append(next.all[:0], next.byID[instanceID])
	for id, existing := range next.byID {
		if id != instanceID {
			next.all = append(next.all, existing)
		}
	}
	p.storeState(next)
	if old != nil {
		old.Close()
	}
	return proxy, true, nil
}

// RemoveInstance 移除一个实例连接
func (p *ServicePool) RemoveInstance(instanceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.loadState()
	proxy, ok := state.byID[instanceID]
	if !ok {
		return
	}

	next := copyServicePoolState(state, 0)
	delete(next.byID, instanceID)
	next.all = next.all[:0]
	for _, existing := range next.byID {
		next.all = append(next.all, existing)
	}
	p.storeState(next)
	proxy.Close()
}

// Get 获取一个可用的 proxy（round-robin + 健康检查）
func (p *ServicePool) Get() *ReflectionProxy {
	all := p.loadState().all
	n := len(all)
	if n == 0 {
		return nil
	}

	// round-robin
	start := atomic.AddUint64(&p.idx, 1) - 1
	for i := 0; i < n; i++ {
		proxy := all[(int(start)+i)%n]
		if proxy != nil && proxy.conn != nil && proxy.conn.GetState() == connectivity.Ready {
			return proxy
		}
	}

	// 没有健康连接，返回第一个
	return all[0]
}

// Size 返回实例数量
func (p *ServicePool) Size() int {
	return len(p.loadState().byID)
}

// Close 关闭所有连接
func (p *ServicePool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.loadState()
	p.storeState(servicePoolState{byID: make(map[string]*ReflectionProxy)})
	for _, proxy := range state.byID {
		proxy.Close()
	}
}

// ConnectionPool 连接池管理器
type ConnectionPool struct {
	pools   atomic.Value // map[string]*ServicePool, copy-on-write
	poolsMu sync.Mutex
}

// NewConnectionPool 创建连接池管理器
func NewConnectionPool() *ConnectionPool {
	cp := &ConnectionPool{}
	cp.storePools(make(map[string]*ServicePool))
	return cp
}

func (cp *ConnectionPool) loadPools() map[string]*ServicePool {
	if pools, ok := cp.pools.Load().(map[string]*ServicePool); ok && pools != nil {
		return pools
	}
	return nil
}

func (cp *ConnectionPool) storePools(pools map[string]*ServicePool) {
	cp.pools.Store(pools)
}

// GetOrCreate 获取或创建服务连接池
func (cp *ConnectionPool) GetOrCreate(app *core.Core, serviceName string) *ServicePool {
	if pool, ok := cp.loadPools()[serviceName]; ok {
		return pool
	}

	pool := NewServicePool(app, serviceName)

	cp.poolsMu.Lock()
	defer cp.poolsMu.Unlock()

	// double check
	pools := cp.loadPools()
	if existing, ok := pools[serviceName]; ok {
		return existing
	}

	next := make(map[string]*ServicePool, len(pools)+1)
	for name, existing := range pools {
		next[name] = existing
	}
	next[serviceName] = pool
	cp.storePools(next)
	return pool
}

// Get 获取服务连接池
func (cp *ConnectionPool) Get(serviceName string) *ServicePool {
	return cp.loadPools()[serviceName]
}

// Remove 移除服务连接池
func (cp *ConnectionPool) Remove(serviceName string) {
	cp.poolsMu.Lock()
	pools := cp.loadPools()
	pool := pools[serviceName]
	if pool != nil {
		next := make(map[string]*ServicePool, len(pools)-1)
		for name, existing := range pools {
			if name != serviceName {
				next[name] = existing
			}
		}
		cp.storePools(next)
	}
	cp.poolsMu.Unlock()

	if pool != nil {
		pool.Close()
	}
}

// Close 关闭所有连接池
func (cp *ConnectionPool) Close() {
	cp.poolsMu.Lock()
	pools := cp.loadPools()
	cp.storePools(make(map[string]*ServicePool))
	cp.poolsMu.Unlock()

	for _, pool := range pools {
		pool.Close()
	}
}
