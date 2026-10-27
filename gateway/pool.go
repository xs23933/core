package gateway

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"

	core "github.com/xs23933/core/v3"
	"google.golang.org/grpc/connectivity"
)

var errServiceInstanceNotDesired = errors.New("gateway: service instance is no longer desired")

type reflectionProxyConnector func(
	context.Context,
	*core.Core,
	string,
	string,
	*ReflectionSchema,
) (*ReflectionProxy, error)

func defaultReflectionProxyConnector(ctx context.Context, app *core.Core, serviceName, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
	return newReflectionProxyForServiceContext(ctx, app, serviceName, addr, schema)
}

// ServicePool 管理代理连接，仅在各进程反射协议指纹一致后共享不可变编解码器。
// 请求热路径读取已排序的写时复制快照，不加锁或分配内存。
type ServicePool struct {
	name        string
	state       atomic.Value // servicePoolState，写时复制
	idx         atomic.Uint64
	mu          sync.Mutex
	app         *core.Core
	connector   reflectionProxyConnector
	desired     map[string]string
	generations map[string]string
	schema      *ReflectionSchema
	closed      bool
	retiring    map[*ReflectionProxy]struct{}
	onRetired   func()
}

type servicePoolState struct {
	byID    map[string]*ReflectionProxy
	all     []*ReflectionProxy
	methods map[string][]*ReflectionProxy
}

// NewServicePool creates a service connection pool.
func NewServicePool(app *core.Core, name string) *ServicePool {
	return newServicePoolWithConnector(app, name, defaultReflectionProxyConnector)
}

func newServicePoolWithConnector(app *core.Core, name string, connector reflectionProxyConnector) *ServicePool {
	if connector == nil {
		connector = defaultReflectionProxyConnector
	}
	p := &ServicePool{
		name:        name,
		app:         app,
		connector:   connector,
		desired:     make(map[string]string),
		generations: make(map[string]string),
		retiring:    make(map[*ReflectionProxy]struct{}),
	}
	p.storeState(servicePoolState{byID: make(map[string]*ReflectionProxy)})
	return p
}

func (p *ServicePool) loadState() servicePoolState {
	if p != nil {
		if state, ok := p.state.Load().(servicePoolState); ok && state.byID != nil {
			return state
		}
	}
	return servicePoolState{byID: make(map[string]*ReflectionProxy)}
}

func (p *ServicePool) storeState(state servicePoolState) {
	p.state.Store(state)
}

func sortedServicePoolState(byID map[string]*ReflectionProxy) servicePoolState {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	all := make([]*ReflectionProxy, 0, len(ids))
	for _, id := range ids {
		all = append(all, byID[id])
	}
	methods := make(map[string][]*ReflectionProxy)
	contracts := make(map[string]string)
	conflicts := make(map[string]bool)
	for _, proxy := range all {
		for name, desc := range proxy.schema.methods {
			if contract, seen := contracts[name]; seen && contract != desc.Contract {
				conflicts[name] = true
			}
			contracts[name] = desc.Contract
			methods[name] = append(methods[name], proxy)
		}
	}
	for name := range conflicts {
		methods[name] = nil
	}
	return servicePoolState{byID: byID, all: all, methods: methods}
}

func copyServicePoolByID(state servicePoolState, extra int) map[string]*ReflectionProxy {
	next := make(map[string]*ReflectionProxy, len(state.byID)+extra)
	for id, proxy := range state.byID {
		next[id] = proxy
	}
	return next
}

// SetDesiredInstance must happen before a connect is launched. A candidate is
// installed only if this exact ID/address is still desired when it completes.
func (p *ServicePool) SetDesiredInstance(instanceID, addr string) bool {
	if p == nil || instanceID == "" || addr == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.desired[instanceID] = addr
	return true
}

func (p *ServicePool) DesiredAddress(instanceID string) (string, bool) {
	if p == nil {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	addr, ok := p.desired[instanceID]
	return addr, ok && !p.closed
}

func (p *ServicePool) MarkInstanceUndesired(instanceID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.desired, instanceID)
	delete(p.generations, instanceID)
	if len(p.desired) == 0 && len(p.loadState().byID) == 0 {
		p.schema = nil
	}
	p.mu.Unlock()
}

func (p *ServicePool) Schema() *ReflectionSchema {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.schema
}

// AddOrUpdateInstance preserves the historical API for direct users. Gateway
// lifecycle code uses AddOrUpdateInstanceContext with its owned context.
func (p *ServicePool) AddOrUpdateInstance(instanceID, addr string) (*ReflectionProxy, bool, error) {
	p.SetDesiredInstance(instanceID, addr)
	return p.AddOrUpdateInstanceContext(context.Background(), instanceID, addr)
}

// SetDesiredGeneration 立即停止旧进程的新调用准入，并保留连接至在途调用完成。
func (p *ServicePool) SetDesiredGeneration(instanceID, addr, generation string) bool {
	if p == nil || instanceID == "" || addr == "" {
		return false
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	p.desired[instanceID] = addr
	p.generations[instanceID] = generation
	state := p.loadState()
	old := state.byID[instanceID]
	var done <-chan struct{}
	var idle bool
	if old != nil && (old.addr != addr || old.generation != generation) {
		done, idle = p.retireProxyLocked(old)
		next := copyServicePoolByID(state, 0)
		delete(next, instanceID)
		p.storeState(sortedServicePoolState(next))
	} else {
		old = nil
	}
	p.mu.Unlock()
	if old != nil {
		p.finishRetirement(old, done, idle)
	}
	return true
}

// 每个候选连接必须独立反射所属进程，之后才能共享编解码器。
func (p *ServicePool) AddOrUpdateInstanceContext(ctx context.Context, instanceID, addr string) (*ReflectionProxy, bool, error) {
	if p == nil {
		return nil, false, errors.New("gateway: service pool is nil")
	}
	p.mu.Lock()
	generation := p.generations[instanceID]
	p.mu.Unlock()
	return p.addOrUpdateGeneration(ctx, instanceID, addr, generation)
}
func (p *ServicePool) addOrUpdateGeneration(ctx context.Context, instanceID, addr, generation string) (*ReflectionProxy, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	p.mu.Lock()
	if p.closed || p.desired[instanceID] != addr || p.generations[instanceID] != generation {
		p.mu.Unlock()
		return nil, false, errServiceInstanceNotDesired
	}
	if old := p.loadState().byID[instanceID]; old != nil && old.addr == addr && old.generation == generation && old.isReady() {
		p.mu.Unlock()
		return old, false, nil
	}
	p.mu.Unlock()
	proxy, err := p.connector(ctx, p.app, p.name, addr, nil)
	if err != nil {
		return nil, false, err
	}
	if proxy == nil || proxy.schema == nil {
		if proxy != nil {
			_ = proxy.Close()
		}
		return nil, false, errors.New("gateway: connector returned a proxy without reflection schema")
	}
	proxy.generation = generation
	p.mu.Lock()
	if p.closed || ctx.Err() != nil || p.desired[instanceID] != addr || p.generations[instanceID] != generation {
		p.mu.Unlock()
		_ = proxy.Close()
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		return nil, false, errServiceInstanceNotDesired
	}
	state := p.loadState()
	if old := state.byID[instanceID]; old != nil && old.addr == addr && old.generation == generation && old.isReady() {
		p.mu.Unlock()
		_ = proxy.Close()
		return old, false, nil
	}
	fingerprint := proxy.schema.fingerprint
	if fingerprint == "" {
		fingerprint = reflectionFingerprint(proxy.schema.methods)
	}
	for _, existing := range state.all {
		other := existing.schema.fingerprint
		if other == "" {
			other = reflectionFingerprint(existing.schema.methods)
		}
		if other == fingerprint {
			proxy.schema = existing.schema
			break
		}
	}
	next := copyServicePoolByID(state, 1)
	old := next[instanceID]
	var done <-chan struct{}
	var idle bool
	if old != nil {
		done, idle = p.retireProxyLocked(old)
	}
	next[instanceID] = proxy
	p.storeState(sortedServicePoolState(next))
	p.schema = proxy.schema
	p.mu.Unlock()
	if old != nil {
		p.finishRetirement(old, done, idle)
	}
	return proxy, true, nil
}

// Methods returns the union of installed process schemas. Conflicting codecs
// retain a route but have no eligible upstream until the rolling overlap ends.
func (p *ServicePool) Methods() map[string]*MethodDescriptor {
	result := make(map[string]*MethodDescriptor)
	for _, proxy := range p.loadState().all {
		for name, desc := range proxy.schema.methods {
			result[name] = desc
		}
	}
	return result
}
func (p *ServicePool) GetForMethod(method string, excluded *ReflectionProxy) *ReflectionProxy {
	all := p.loadState().methods[method]
	n := len(all)
	if n == 0 {
		return nil
	}
	start := p.idx.Add(1) - 1
	var idle *ReflectionProxy
	for offset := 0; offset < n; offset++ {
		proxy := all[(int(start)+offset)%n]
		if proxy != excluded && proxy.isReady() {
			return proxy
		}
		// 首个 RPC 可唤醒空闲连接，此处不拨号或反射；备用重试仍只使用 Ready 连接。
		// 必须先遵循就绪钩子，避免读取可能用于测试的连接替身。
		if excluded == nil && idle == nil && proxy != nil && !proxy.draining.Load() && proxy.ready == nil && proxy.conn != nil && proxy.conn.GetState() == connectivity.Idle {
			idle = proxy
		}
	}
	return idle
}
func (p *ServicePool) removeInstance(instanceID string, clearDesired bool) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	if clearDesired {
		delete(p.desired, instanceID)
		delete(p.generations, instanceID)
	} else if _, desired := p.desired[instanceID]; desired {
		p.mu.Unlock()
		return false
	}
	state := p.loadState()
	proxy := state.byID[instanceID]
	if proxy == nil {
		if len(state.byID) == 0 && len(p.desired) == 0 {
			p.schema = nil
		}
		p.mu.Unlock()
		return false
	}
	nextByID := copyServicePoolByID(state, 0)
	done, idle := p.retireProxyLocked(proxy)
	delete(nextByID, instanceID)
	p.storeState(sortedServicePoolState(nextByID))
	p.schema = nil
	for _, remaining := range nextByID {
		p.schema = remaining.schema
		break
	}
	p.mu.Unlock()
	p.finishRetirement(proxy, done, idle)
	return true
}

// retireProxyLocked 在快照更新前关闭旧实例准入，防止旧快照持有者开始新调用。
func (p *ServicePool) retireProxyLocked(proxy *ReflectionProxy) (<-chan struct{}, bool) {
	if p.retiring == nil {
		p.retiring = make(map[*ReflectionProxy]struct{})
	}
	p.retiring[proxy] = struct{}{}
	return proxy.startDrain()
}

// finishRetirement 保留在途连接的所有权，确保最终 Close 也能清理尚未完成的排空。
func (p *ServicePool) finishRetirement(proxy *ReflectionProxy, done <-chan struct{}, idle bool) {
	if idle {
		_ = proxy.Close()
	}
	forget := func() {
		p.mu.Lock()
		delete(p.retiring, proxy)
		onRetired := p.onRetired
		p.mu.Unlock()
		if onRetired != nil {
			onRetired()
		}
	}
	select {
	case <-done:
		forget()
	default:
		go func() { <-done; forget() }()
	}
}

// RemoveInstance 删除期望标记并摘除已安装实例，在途调用结束后关闭连接。
func (p *ServicePool) RemoveInstance(instanceID string) {
	p.removeInstance(instanceID, true)
}

// RemoveInstanceIfUndesired makes delayed delete tasks safe when a newer PUT
// for the same service ID has already arrived.
func (p *ServicePool) RemoveInstanceIfUndesired(instanceID string) bool {
	return p.removeInstance(instanceID, false)
}

func (p *ReflectionProxy) isReady() bool {
	if p == nil || p.draining.Load() {
		return false
	}
	if p.ready != nil {
		return p.ready()
	}
	return p.conn != nil && p.conn.GetState() == connectivity.Ready
}

// Get 在预排序快照中确定性轮询，不执行排序、反射、拨号或内存分配。
func (p *ServicePool) Get() *ReflectionProxy {
	all := p.loadState().all
	n := len(all)
	if n == 0 {
		return nil
	}

	start := p.idx.Add(1) - 1
	for offset := 0; offset < n; offset++ {
		proxy := all[(int(start)+offset)%n]
		if proxy.isReady() {
			return proxy
		}
	}
	for offset := 0; offset < n; offset++ {
		proxy := all[(int(start)+offset)%n]
		if !proxy.draining.Load() {
			return proxy
		}
	}
	return nil
}

// GetExcept returns a ready proxy other than excluded. It is used for one
// alternate-instance retry and never falls back to an unready connection.
func (p *ServicePool) GetExcept(excluded *ReflectionProxy) *ReflectionProxy {
	all := p.loadState().all
	n := len(all)
	if n < 2 {
		return nil
	}

	start := p.idx.Add(1) - 1
	for offset := 0; offset < n; offset++ {
		proxy := all[(int(start)+offset)%n]
		if proxy != excluded && proxy.isReady() {
			return proxy
		}
	}
	return nil
}

func (p *ServicePool) Size() int {
	return len(p.loadState().byID)
}

func (p *ServicePool) InstanceAddress(instanceID string) (string, bool) {
	proxy := p.loadState().byID[instanceID]
	if proxy == nil {
		return "", false
	}
	return proxy.addr, true
}

func (p *ServicePool) InstanceIDs() []string {
	state := p.loadState()
	ids := make([]string, 0, len(state.byID))
	for id := range state.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (p *ServicePool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	state := p.loadState()
	retiring := p.retiring
	p.retiring = make(map[*ReflectionProxy]struct{})
	p.storeState(servicePoolState{byID: make(map[string]*ReflectionProxy)})
	p.desired = make(map[string]string)
	p.generations = make(map[string]string)
	p.schema = nil
	p.mu.Unlock()
	for _, proxy := range state.byID {
		_ = proxy.Close()
	}
	for proxy := range retiring {
		_ = proxy.Close()
	}
}

func (p *ServicePool) markClosedIfRemovable() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(p.loadState().byID) != 0 || len(p.desired) != 0 || len(p.retiring) != 0 {
		return false
	}
	p.closed = true
	p.schema = nil
	p.storeState(servicePoolState{byID: make(map[string]*ReflectionProxy)})
	return true
}

type ConnectionPool struct {
	pools     atomic.Value // map[string]*ServicePool, copy-on-write
	poolsMu   sync.Mutex
	connector reflectionProxyConnector
}

func NewConnectionPool() *ConnectionPool {
	return newConnectionPoolWithConnector(defaultReflectionProxyConnector)
}

func newConnectionPoolWithConnector(connector reflectionProxyConnector) *ConnectionPool {
	if connector == nil {
		connector = defaultReflectionProxyConnector
	}
	cp := &ConnectionPool{connector: connector}
	cp.storePools(make(map[string]*ServicePool))
	return cp
}

func (cp *ConnectionPool) loadPools() map[string]*ServicePool {
	if cp == nil {
		return nil
	}
	if pools, ok := cp.pools.Load().(map[string]*ServicePool); ok && pools != nil {
		return pools
	}
	return nil
}

func (cp *ConnectionPool) storePools(pools map[string]*ServicePool) {
	cp.pools.Store(pools)
}

func (cp *ConnectionPool) GetOrCreate(app *core.Core, serviceName string) *ServicePool {
	if pool := cp.Get(serviceName); pool != nil {
		return pool
	}
	cp.poolsMu.Lock()
	defer cp.poolsMu.Unlock()
	if pool := cp.loadPools()[serviceName]; pool != nil {
		return pool
	}
	pools := cp.loadPools()
	next := make(map[string]*ServicePool, len(pools)+1)
	for name, existing := range pools {
		next[name] = existing
	}
	pool := newServicePoolWithConnector(app, serviceName, cp.connector)
	pool.onRetired = func() { cp.RemoveIfEmpty(serviceName, pool) }
	next[serviceName] = pool
	cp.storePools(next)
	return pool
}

func (cp *ConnectionPool) SetDesiredInstance(app *core.Core, serviceName, instanceID, addr string) *ServicePool {
	if cp == nil {
		return nil
	}
	cp.poolsMu.Lock()
	defer cp.poolsMu.Unlock()
	pools := cp.loadPools()
	pool := pools[serviceName]
	if pool == nil {
		next := make(map[string]*ServicePool, len(pools)+1)
		for name, existing := range pools {
			next[name] = existing
		}
		pool = newServicePoolWithConnector(app, serviceName, cp.connector)
		pool.onRetired = func() { cp.RemoveIfEmpty(serviceName, pool) }
		next[serviceName] = pool
		cp.storePools(next)
	}
	if !pool.SetDesiredInstance(instanceID, addr) {
		return nil
	}
	return pool
}

func (cp *ConnectionPool) Get(serviceName string) *ServicePool {
	if cp == nil {
		return nil
	}
	return cp.loadPools()[serviceName]
}

func (cp *ConnectionPool) Remove(serviceName string) {
	if cp == nil {
		return
	}
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

func (cp *ConnectionPool) RemoveIfEmpty(serviceName string, expected *ServicePool) bool {
	if cp == nil || expected == nil {
		return false
	}
	cp.poolsMu.Lock()
	pools := cp.loadPools()
	if pools[serviceName] != expected || !expected.markClosedIfRemovable() {
		cp.poolsMu.Unlock()
		return false
	}
	next := make(map[string]*ServicePool, len(pools)-1)
	for name, pool := range pools {
		if name != serviceName {
			next[name] = pool
		}
	}
	cp.storePools(next)
	cp.poolsMu.Unlock()
	return true
}

func (cp *ConnectionPool) ServiceNames() []string {
	pools := cp.loadPools()
	names := make([]string, 0, len(pools))
	for name := range pools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (cp *ConnectionPool) Close() {
	if cp == nil {
		return
	}
	cp.poolsMu.Lock()
	pools := cp.loadPools()
	cp.storePools(make(map[string]*ServicePool))
	cp.poolsMu.Unlock()
	for _, pool := range pools {
		pool.Close()
	}
}
