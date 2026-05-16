package gateway

import (
	"sync"
	"sync/atomic"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc/connectivity"
)

// ServicePool 服务连接池（按 instanceID 管理多个实例连接）
type ServicePool struct {
	name      string
	instances atomic.Value // map[string]*ReflectionProxy, copy-on-write
	idx       uint64       // atomic round-robin index
	mu        sync.Mutex
	app       *core.Core
}

// NewServicePool 创建服务连接池
func NewServicePool(app *core.Core, name string) *ServicePool {
	p := &ServicePool{
		name: name,
		app:  app,
	}
	p.storeInstances(make(map[string]*ReflectionProxy))
	return p
}

func (p *ServicePool) loadInstances() map[string]*ReflectionProxy {
	if instances, ok := p.instances.Load().(map[string]*ReflectionProxy); ok && instances != nil {
		return instances
	}
	return nil
}

func (p *ServicePool) storeInstances(instances map[string]*ReflectionProxy) {
	p.instances.Store(instances)
}

// AddOrUpdateInstance 添加或更新一个实例连接
// 返回 changed=true 表示是新增或重建了 proxy
func (p *ServicePool) AddOrUpdateInstance(instanceID, addr string) (*ReflectionProxy, bool, error) {
	// 快速路径：已有同 ID 同地址且连接健康，直接跳过
	if old, ok := p.loadInstances()[instanceID]; ok && old.addr == addr {
		if old.conn != nil && old.conn.GetState() == connectivity.Ready {
			return old, false, nil
		}
	}

	// 慢路径：先创建新 proxy（不持锁，避免阻塞 Get）
	proxy, err := NewReflectionProxy(p.app, addr)
	if err != nil {
		return nil, false, err
	}

	// 加锁替换
	p.mu.Lock()
	defer p.mu.Unlock()

	instances := p.loadInstances()

	// double check：可能另一个 goroutine 已抢先完成
	if old, ok := instances[instanceID]; ok && old.addr == addr {
		if old.conn != nil && old.conn.GetState() == connectivity.Ready {
			proxy.Close() // 丢弃新创建的
			return old, false, nil
		}
	}

	next := make(map[string]*ReflectionProxy, len(instances)+1)
	for id, existing := range instances {
		next[id] = existing
	}
	old := next[instanceID]
	next[instanceID] = proxy
	p.storeInstances(next)
	if old != nil {
		old.Close()
	}
	return proxy, true, nil
}

// RemoveInstance 移除一个实例连接
func (p *ServicePool) RemoveInstance(instanceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	instances := p.loadInstances()
	proxy, ok := instances[instanceID]
	if !ok {
		return
	}

	next := make(map[string]*ReflectionProxy, len(instances)-1)
	for id, existing := range instances {
		if id != instanceID {
			next[id] = existing
		}
	}
	p.storeInstances(next)
	proxy.Close()
}

// Get 获取一个可用的 proxy（round-robin + 健康检查）
func (p *ServicePool) Get() *ReflectionProxy {
	instances := p.loadInstances()
	n := len(instances)
	if n == 0 {
		return nil
	}

	// 收集所有 proxy 到 slice 用于 round-robin
	all := make([]*ReflectionProxy, 0, n)
	for _, proxy := range instances {
		all = append(all, proxy)
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
	return len(p.loadInstances())
}

// Close 关闭所有连接
func (p *ServicePool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	instances := p.loadInstances()
	p.storeInstances(make(map[string]*ReflectionProxy))
	for _, proxy := range instances {
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
