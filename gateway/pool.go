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

// ServicePool owns the connection-bearing proxies and the one immutable
// reflection schema for a logical service. The request hot path reads a sorted
// copy-on-write snapshot without taking locks or allocating.
type ServicePool struct {
	name           string
	state          atomic.Value // servicePoolState, copy-on-write
	idx            atomic.Uint64
	mu             sync.Mutex
	app            *core.Core
	connector      reflectionProxyConnector
	desired        map[string]string
	schema         *ReflectionSchema
	schemaBuilding bool
	schemaWait     chan struct{}
	closed         bool
}

type servicePoolState struct {
	byID map[string]*ReflectionProxy
	all  []*ReflectionProxy
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
		name:      name,
		app:       app,
		connector: connector,
		desired:   make(map[string]string),
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
	return servicePoolState{byID: byID, all: all}
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
	if len(p.desired) == 0 && len(p.loadState().byID) == 0 && !p.schemaBuilding {
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

// AddOrUpdateInstanceContext builds at most one schema per service and keeps
// candidate construction off the request hot path. Connections for a service
// are serialized only during connect/update; Get remains lock-free.
func (p *ServicePool) AddOrUpdateInstanceContext(ctx context.Context, instanceID, addr string) (*ReflectionProxy, bool, error) {
	if p == nil {
		return nil, false, errors.New("gateway: service pool is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	p.mu.Lock()
	if p.closed || p.desired[instanceID] != addr {
		p.mu.Unlock()
		return nil, false, errServiceInstanceNotDesired
	}
	if old := p.loadState().byID[instanceID]; old != nil && old.addr == addr && old.isReady() {
		p.mu.Unlock()
		return old, false, nil
	}
	p.mu.Unlock()

	proxy, err := p.connectCandidate(ctx, instanceID, addr)
	if err != nil {
		return nil, false, err
	}

	p.mu.Lock()
	if p.closed || p.desired[instanceID] != addr {
		p.mu.Unlock()
		_ = proxy.Close()
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		return nil, false, errServiceInstanceNotDesired
	}
	state := p.loadState()
	if old := state.byID[instanceID]; old != nil && old.addr == addr && old.isReady() {
		p.mu.Unlock()
		_ = proxy.Close()
		return old, false, nil
	}
	if p.schema == nil {
		p.schema = proxy.schema
	} else if proxy.schema != p.schema {
		p.mu.Unlock()
		_ = proxy.Close()
		return nil, false, errors.New("gateway: connector returned a different reflection schema")
	}

	nextByID := copyServicePoolByID(state, 1)
	old := nextByID[instanceID]
	nextByID[instanceID] = proxy
	p.storeState(sortedServicePoolState(nextByID))
	p.mu.Unlock()

	if old != nil {
		_ = old.Close()
	}
	return proxy, true, nil
}

// connectCandidate lets exactly one initial connection build the shared
// schema. Once published, different instance IDs dial concurrently using that
// immutable schema; no service-wide dial lock remains on the steady path.
func (p *ServicePool) connectCandidate(ctx context.Context, instanceID, addr string) (*ReflectionProxy, error) {
	for {
		p.mu.Lock()
		if p.closed || p.desired[instanceID] != addr {
			p.mu.Unlock()
			return nil, errServiceInstanceNotDesired
		}
		if schema := p.schema; schema != nil {
			p.mu.Unlock()
			proxy, err := p.connector(ctx, p.app, p.name, addr, schema)
			if err != nil {
				return nil, err
			}
			if proxy == nil || proxy.schema != schema {
				if proxy != nil {
					_ = proxy.Close()
				}
				return nil, errors.New("gateway: connector returned a proxy with a different reflection schema")
			}
			return proxy, nil
		}
		if p.schemaBuilding {
			wait := p.schemaWait
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-wait:
				continue
			}
		}

		p.schemaBuilding = true
		p.schemaWait = make(chan struct{})
		wait := p.schemaWait
		p.mu.Unlock()

		proxy, err := p.connector(ctx, p.app, p.name, addr, nil)
		if err == nil && (proxy == nil || proxy.schema == nil) {
			if proxy != nil {
				_ = proxy.Close()
			}
			proxy = nil
			err = errors.New("gateway: connector returned a proxy without reflection schema")
		}

		p.mu.Lock()
		p.schemaBuilding = false
		if err == nil && !p.closed && len(p.desired) > 0 {
			p.schema = proxy.schema
		}
		close(wait)
		p.schemaWait = nil
		desired := !p.closed && p.desired[instanceID] == addr
		published := p.schema != nil
		p.mu.Unlock()

		if err != nil {
			return nil, err
		}
		if !desired || !published {
			_ = proxy.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, errServiceInstanceNotDesired
		}
		return proxy, nil
	}
}

func (p *ServicePool) removeInstance(instanceID string, clearDesired bool) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	if clearDesired {
		delete(p.desired, instanceID)
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
	delete(nextByID, instanceID)
	p.storeState(sortedServicePoolState(nextByID))
	if len(nextByID) == 0 && len(p.desired) == 0 {
		p.schema = nil
	}
	p.mu.Unlock()
	_ = proxy.Close()
	return true
}

// RemoveInstance removes the desired marker and the installed connection.
func (p *ServicePool) RemoveInstance(instanceID string) {
	p.removeInstance(instanceID, true)
}

// RemoveInstanceIfUndesired makes delayed delete tasks safe when a newer PUT
// for the same service ID has already arrived.
func (p *ServicePool) RemoveInstanceIfUndesired(instanceID string) bool {
	return p.removeInstance(instanceID, false)
}

func (p *ReflectionProxy) isReady() bool {
	if p == nil {
		return false
	}
	if p.ready != nil {
		return p.ready()
	}
	return p.conn != nil && p.conn.GetState() == connectivity.Ready
}

// Get returns a proxy using deterministic round-robin over the pre-sorted
// snapshot. It performs no sorting, reflection, dialing, or allocation.
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
	return all[int(start%uint64(n))]
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
	p.storeState(servicePoolState{byID: make(map[string]*ReflectionProxy)})
	p.desired = make(map[string]string)
	p.schema = nil
	p.mu.Unlock()
	for _, proxy := range state.byID {
		_ = proxy.Close()
	}
}

func (p *ServicePool) markClosedIfRemovable() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(p.loadState().byID) != 0 || len(p.desired) != 0 {
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
