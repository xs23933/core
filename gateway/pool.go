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
	instances map[string]*ReflectionProxy // instanceID -> proxy
	idx       uint64                      // atomic round-robin index
	mu        sync.RWMutex
	app       *core.Core
}

// NewServicePool 创建服务连接池
func NewServicePool(app *core.Core, name string) *ServicePool {
	return &ServicePool{
		name:      name,
		instances: make(map[string]*ReflectionProxy),
		app:       app,
	}
}

// AddOrUpdateInstance 添加或更新一个实例连接
// 返回 changed=true 表示是新增或重建了 proxy
func (p *ServicePool) AddOrUpdateInstance(instanceID, addr string) (*ReflectionProxy, bool, error) {
	// 快速路径：已有同 ID 同地址且连接健康，直接跳过
	p.mu.RLock()
	if old, ok := p.instances[instanceID]; ok && old.addr == addr {
		if old.conn != nil && old.conn.GetState() == connectivity.Ready {
			p.mu.RUnlock()
			return old, false, nil
		}
	}
	p.mu.RUnlock()

	// 慢路径：先创建新 proxy（不持锁，避免阻塞 Get）
	proxy, err := NewReflectionProxy(p.app, addr)
	if err != nil {
		return nil, false, err
	}

	// 加锁替换
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.instances == nil {
		p.instances = make(map[string]*ReflectionProxy)
	}

	// double check：可能另一个 goroutine 已抢先完成
	if old, ok := p.instances[instanceID]; ok && old.addr == addr {
		if old.conn != nil && old.conn.GetState() == connectivity.Ready {
			proxy.Close() // 丢弃新创建的
			return old, false, nil
		}
	}

	if old, ok := p.instances[instanceID]; ok {
		old.Close()
	}

	p.instances[instanceID] = proxy
	return proxy, true, nil
}

// RemoveInstance 移除一个实例连接
func (p *ServicePool) RemoveInstance(instanceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if proxy, ok := p.instances[instanceID]; ok {
		proxy.Close()
		delete(p.instances, instanceID)
	}
}

// Get 获取一个可用的 proxy（round-robin + 健康检查）
func (p *ServicePool) Get() *ReflectionProxy {
	p.mu.RLock()
	defer p.mu.RUnlock()

	n := len(p.instances)
	if n == 0 {
		return nil
	}

	// 收集所有 proxy 到 slice 用于 round-robin
	all := make([]*ReflectionProxy, 0, n)
	for _, proxy := range p.instances {
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
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.instances)
}

// Close 关闭所有连接
func (p *ServicePool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, proxy := range p.instances {
		proxy.Close()
	}
	p.instances = make(map[string]*ReflectionProxy)
}

// ConnectionPool 连接池管理器
type ConnectionPool struct {
	pools   map[string]*ServicePool
	poolsMu sync.RWMutex
}

// NewConnectionPool 创建连接池管理器
func NewConnectionPool() *ConnectionPool {
	return &ConnectionPool{
		pools: make(map[string]*ServicePool),
	}
}

// GetOrCreate 获取或创建服务连接池
func (cp *ConnectionPool) GetOrCreate(app *core.Core, serviceName string) *ServicePool {
	cp.poolsMu.RLock()
	if pool, ok := cp.pools[serviceName]; ok {
		cp.poolsMu.RUnlock()
		return pool
	}
	cp.poolsMu.RUnlock()

	pool := NewServicePool(app, serviceName)

	cp.poolsMu.Lock()
	defer cp.poolsMu.Unlock()

	// double check
	if existing, ok := cp.pools[serviceName]; ok {
		return existing
	}

	cp.pools[serviceName] = pool
	return pool
}

// Get 获取服务连接池
func (cp *ConnectionPool) Get(serviceName string) *ServicePool {
	cp.poolsMu.RLock()
	defer cp.poolsMu.RUnlock()
	return cp.pools[serviceName]
}

// Remove 移除服务连接池
func (cp *ConnectionPool) Remove(serviceName string) {
	cp.poolsMu.Lock()
	pool, ok := cp.pools[serviceName]
	if ok {
		delete(cp.pools, serviceName)
	}
	cp.poolsMu.Unlock()

	if pool != nil {
		pool.Close()
	}
}

// Close 关闭所有连接池
func (cp *ConnectionPool) Close() {
	cp.poolsMu.Lock()
	pools := make(map[string]*ServicePool, len(cp.pools))
	for name, pool := range cp.pools {
		pools[name] = pool
	}
	cp.pools = make(map[string]*ServicePool)
	cp.poolsMu.Unlock()

	for _, pool := range pools {
		pool.Close()
	}
}
