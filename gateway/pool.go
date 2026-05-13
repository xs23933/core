package gateway

import (
	"sync"
	"sync/atomic"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc/connectivity"
)

// ServicePool 服务连接池（多个连接 round-robin）
type ServicePool struct {
	name    string
	addrs   []string
	size    int
	proxies []*ReflectionProxy
	idx     uint64 // atomic round-robin index
	mu      sync.RWMutex
	app     *core.Core
}

// NewServicePool 创建服务连接池
func NewServicePool(app *core.Core, name string, addrs []string, size int) *ServicePool {
	if size <= 0 {
		size = 3 // default 3 connections per service
	}
	if len(addrs) == 0 {
		return nil
	}

	pool := &ServicePool{
		name:    name,
		addrs:   addrs,
		size:    size,
		proxies: make([]*ReflectionProxy, 0, size),
		app:     app,
	}

	// 预热连接，每个地址创建 size 个连接
	for i := 0; i < size; i++ {
		addr := addrs[i%len(addrs)]
		proxy, err := NewReflectionProxy(app, addr)
		if err != nil {
			continue
		}
		pool.proxies = append(pool.proxies, proxy)
	}

	if len(pool.proxies) == 0 {
		return nil
	}

	return pool
}

// Get 获取一个可用的 proxy（round-robin + 健康检查）
func (p *ServicePool) Get() *ReflectionProxy {
	p.mu.RLock()
	defer p.mu.RUnlock()

	n := len(p.proxies)
	if n == 0 {
		return nil
	}

	// round-robin
	start := atomic.AddUint64(&p.idx, 1) - 1
	for i := 0; i < n; i++ {
		idx := int((start + uint64(i)) % uint64(n))
		proxy := p.proxies[idx]
		if proxy != nil && proxy.conn != nil && proxy.conn.GetState() == connectivity.Ready {
			return proxy
		}
	}

	// 没有健康的，返回第一个
	return p.proxies[0]
}

// MarkFailed 标记某个 proxy 失败，会尝试重建
func (p *ServicePool) MarkFailed(proxy *ReflectionProxy) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, pxy := range p.proxies {
		if pxy == proxy {
			// 关闭旧连接
			pxy.Close()

			// 用下一个地址重建
			if len(p.addrs) > 1 {
				newAddr := p.addrs[(i+1)%len(p.addrs)]
				if newProxy, err := NewReflectionProxy(p.app, newAddr); err == nil {
					p.proxies[i] = newProxy
					return
				}
			}

			// 无法重建，移除
			p.proxies = append(p.proxies[:i], p.proxies[i+1:]...)
			return
		}
	}
}

// Size 返回连接池大小
func (p *ServicePool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.proxies)
}

// Close 关闭所有连接
func (p *ServicePool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, proxy := range p.proxies {
		proxy.Close()
	}
	p.proxies = nil
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
func (cp *ConnectionPool) GetOrCreate(app *core.Core, serviceName string, addrs []string, size int) *ServicePool {
	// 快速路径：已有池且有可用连接
	cp.poolsMu.RLock()
	if pool, ok := cp.pools[serviceName]; ok {
		cp.poolsMu.RUnlock()
		if pool.Size() > 0 {
			return pool
		}
		// 有池但无连接，删除后重建
		cp.poolsMu.Lock()
		delete(cp.pools, serviceName)
		cp.poolsMu.Unlock()
	} else {
		cp.poolsMu.RUnlock()
	}

	// 创建新池
	pool := NewServicePool(app, serviceName, addrs, size)
	if pool == nil {
		return nil
	}

	cp.poolsMu.Lock()
	defer cp.poolsMu.Unlock()
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
	cp.pools = pools
	cp.poolsMu.Unlock()

	for _, pool := range pools {
		pool.Close()
	}
}
