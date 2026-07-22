package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/etcd"
	"github.com/xs23933/core/v3/gateway/route"
	clientv3 "go.etcd.io/etcd/client/v3"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Route = route.Definition
type RouteProtocol = route.Protocol

const (
	RouteProtocolGRPC = route.ProtocolGRPC
	RouteProtocolHTTP = route.ProtocolHTTP
)

// CircuitBreaker 熔断器
type CircuitBreaker struct {
	mu           sync.Mutex
	lastFailTime time.Time
	state        int // 0: closed, 1: open, 2: half-open
	failCount    int
}

const (
	circuitClosed   = 0
	circuitOpen     = 1
	circuitHalfOpen = 2
)

const (
	circuitFailThreshold = 5                // 连续失败 5 次，熔断
	circuitCooldown      = 30 * time.Second // 熔断后 30s 尝试恢复
)

// serviceInstance 用于解析 etcd 中注册的服务实例信息
type serviceInstance struct {
	Addr     string            `json:"addr"`
	ID       string            `json:"id"`
	Version  string            `json:"version,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type registeredServiceInstance struct {
	ServiceName string
	InstanceID  string
	GRPCAddr    string
	HTTPAddr    string
}

type serviceSnapshot map[string]registeredServiceInstance

type serviceSnapshotRevision struct {
	Snapshot serviceSnapshot
	Revision int64
}

type serviceWatchEvent struct {
	Key      string
	Instance registeredServiceInstance
	Deleted  bool
}

type serviceWatchResponse struct {
	Revision int64
	Events   []serviceWatchEvent
	Err      error
}

type serviceWatchSource interface {
	Load(context.Context) (serviceSnapshotRevision, error)
	Watch(context.Context, int64) <-chan serviceWatchResponse
}

func (s *serviceInstance) HTTPAddr() string {
	if s == nil || s.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(s.Metadata["http_addr"])
}

// EtcdGateway etcd 网关
type EtcdGateway struct {
	app         *core.Core
	etcdCli     *clientv3.Client
	prefix      string
	serviceRoot string
	config      *Config

	mu         sync.Mutex
	routeState atomic.Value // routeSnapshot, copy-on-write
	// grpcServiceRoutes is indexed by logical service_name. Its schema pointer
	// prevents redundant route registration for additional service IDs.
	grpcServiceRoutes  map[string]map[string]bool
	grpcServiceSchemas map[string]*ReflectionSchema

	watchCtx       context.Context
	watchCancel    context.CancelFunc
	lifecycleMu    sync.Mutex
	taskWG         sync.WaitGroup
	closed         bool
	closeOnce      sync.Once
	closeErr       error
	ownsEtcdClient bool

	circuitStates       sync.Map // map[string]*CircuitBreaker
	connectInFlight     sync.Map // map[string]struct{}
	connectGroup        singleflight.Group
	connectWaitObserver func(string)

	desiredMu       sync.Mutex
	desiredServices atomic.Value // serviceSnapshot, copy-on-write

	httpMu        sync.Mutex
	httpInstances atomic.Value // map[string]*httpServiceInstances
	httpIndexMu   sync.Mutex
	httpIndexes   atomic.Value // map[string]*atomic.Uint64

	// 连接池
	connPool *ConnectionPool
}

type Config struct {
	EtcdEndpoints       []string      `yaml:"etcd_endpoints"`
	EtcdDialTimeout     time.Duration `yaml:"etcd_dial_timeout"`
	Namespace           string        `yaml:"namespace"`
	RoutePrefix         string        `yaml:"route_prefix"`
	HTTPAddr            string        `yaml:"http_addr"`
	GRPCServiceExcludes []string      `yaml:"grpc_service_excludes"`
	MaxRequestBodyBytes int64         `yaml:"max_request_body_bytes"`
}

const defaultMaxRequestBodyBytes int64 = 8 << 20

var errRequestBodyTooLarge = errors.New("request body too large")

var newGatewayEtcdClient = clientv3.New

type etcdServiceWatchSource struct {
	client      *clientv3.Client
	serviceRoot string
	launch      func(func()) bool
}

func (source etcdServiceWatchSource) Load(ctx context.Context) (serviceSnapshotRevision, error) {
	if source.client == nil {
		return serviceSnapshotRevision{}, errors.New("gateway: service watch etcd client is nil")
	}
	loadCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := source.client.Get(loadCtx, source.serviceRoot, clientv3.WithPrefix())
	if err != nil {
		return serviceSnapshotRevision{}, err
	}
	snapshot := make(serviceSnapshot)
	for _, value := range response.Kvs {
		instance, ok := decodeServiceSnapshotValue(source.serviceRoot, string(value.Key), value.Value)
		if !ok {
			continue
		}
		snapshot[connectInstanceKey(instance.ServiceName, instance.InstanceID)] = instance
	}
	return serviceSnapshotRevision{Snapshot: snapshot, Revision: response.Header.GetRevision()}, nil
}

func (source etcdServiceWatchSource) Watch(ctx context.Context, revision int64) <-chan serviceWatchResponse {
	responses := make(chan serviceWatchResponse)
	if source.client == nil || source.launch == nil {
		close(responses)
		return responses
	}
	options := []clientv3.OpOption{clientv3.WithPrefix()}
	if revision > 0 {
		options = append(options, clientv3.WithRev(revision))
	}
	watch := source.client.Watch(ctx, source.serviceRoot, options...)
	forward := func() {
		defer close(responses)
		for {
			select {
			case <-ctx.Done():
				return
			case response, ok := <-watch:
				if !ok {
					return
				}
				converted := serviceWatchResponse{Revision: response.Header.GetRevision(), Err: response.Err()}
				if converted.Err == nil {
					converted.Events = decodeServiceWatchEvents(source.serviceRoot, response.Events)
				}
				select {
				case <-ctx.Done():
					return
				case responses <- converted:
				}
				if converted.Err != nil {
					return
				}
			}
		}
	}
	if !source.launch(forward) {
		close(responses)
	}
	return responses
}

func decodeServiceWatchEvents(serviceRoot string, events []*clientv3.Event) []serviceWatchEvent {
	result := make([]serviceWatchEvent, 0, len(events))
	for _, event := range events {
		if event == nil || event.Kv == nil {
			continue
		}
		key := string(event.Kv.Key)
		serviceName, instanceID, ok := parseServiceKey(key, serviceRoot)
		if !ok {
			core.Warn("[Gateway] ignore invalid service key from etcd watch: %s", key)
			continue
		}
		item := serviceWatchEvent{
			Key:      connectInstanceKey(serviceName, instanceID),
			Deleted:  event.Type == clientv3.EventTypeDelete,
			Instance: registeredServiceInstance{ServiceName: serviceName, InstanceID: instanceID},
		}
		if !item.Deleted {
			instance, valid := decodeServiceSnapshotValue(serviceRoot, key, event.Kv.Value)
			if !valid {
				// An invalid replacement must invalidate a previously usable value
				// for the same authoritative etcd key.
				item.Deleted = true
			} else {
				item.Instance = instance
			}
		}
		result = append(result, item)
	}
	return result
}

func decodeServiceSnapshotValue(serviceRoot, key string, value []byte) (registeredServiceInstance, bool) {
	serviceName, instanceID, ok := parseServiceKey(key, serviceRoot)
	if !ok {
		return registeredServiceInstance{}, false
	}
	info, err := parseServiceInstance(value)
	if err != nil {
		core.Warn("[Gateway] ignore invalid service instance %s: %v", key, err)
		return registeredServiceInstance{}, false
	}
	return registeredServiceInstance{
		ServiceName: serviceName,
		InstanceID:  instanceID,
		GRPCAddr:    strings.TrimSpace(info.Addr),
		HTTPAddr:    info.HTTPAddr(),
	}, true
}

func cloneServiceSnapshot(snapshot serviceSnapshot) serviceSnapshot {
	result := make(serviceSnapshot, len(snapshot))
	for key, instance := range snapshot {
		result[key] = instance
	}
	return result
}

func applyServiceWatchEvents(snapshot serviceSnapshot, events []serviceWatchEvent) serviceSnapshot {
	next := cloneServiceSnapshot(snapshot)
	for _, event := range events {
		if event.Key == "" {
			continue
		}
		if event.Deleted {
			delete(next, event.Key)
		} else {
			next[event.Key] = event.Instance
		}
	}
	return next
}

func nextServiceWatchRevision(revision int64) int64 {
	if revision <= 0 {
		return 0
	}
	return revision + 1
}

func runServiceWatchLoop(ctx context.Context, source serviceWatchSource, reconcile func(serviceSnapshot), wait func(context.Context, int) bool) {
	runServiceWatchLoopFrom(ctx, source, nil, reconcile, wait)
}

func runServiceWatchLoopFrom(ctx context.Context, source serviceWatchSource, initial *serviceSnapshotRevision, reconcile func(serviceSnapshot), wait func(context.Context, int) bool) {
	failures := 0
	for ctx.Err() == nil {
		var loaded serviceSnapshotRevision
		if initial != nil {
			loaded = *initial
			initial = nil
		} else {
			var err error
			loaded, err = source.Load(ctx)
			if err != nil {
				core.Warn("[Gateway] refresh service snapshot failed: %v", err)
				if !wait(ctx, failures) {
					return
				}
				failures++
				continue
			}
		}
		current := cloneServiceSnapshot(loaded.Snapshot)
		reconcile(current)
		if ctx.Err() != nil {
			return
		}

		watchCtx, cancelWatch := context.WithCancel(ctx)
		responses := source.Watch(watchCtx, nextServiceWatchRevision(loaded.Revision))
		broken := false
		for !broken {
			select {
			case <-ctx.Done():
				cancelWatch()
				return
			case response, ok := <-responses:
				if !ok || response.Err != nil {
					broken = true
					continue
				}
				current = applyServiceWatchEvents(current, response.Events)
				reconcile(current)
				failures = 0
			}
		}
		cancelWatch()
		if !wait(ctx, failures) {
			return
		}
		failures++
	}
}

func (gw *EtcdGateway) initializeLifecycle() {
	if gw == nil {
		return
	}
	gw.lifecycleMu.Lock()
	if gw.watchCtx == nil {
		gw.watchCtx, gw.watchCancel = context.WithCancel(context.Background())
	}
	gw.lifecycleMu.Unlock()
	if gw.connPool == nil {
		gw.connPool = NewConnectionPool()
	}
	if gw.grpcServiceRoutes == nil {
		gw.grpcServiceRoutes = make(map[string]map[string]bool)
	}
	if gw.grpcServiceSchemas == nil {
		gw.grpcServiceSchemas = make(map[string]*ReflectionSchema)
	}
	if gw.loadDesiredServiceSnapshot() == nil {
		gw.setDesiredServiceSnapshot(make(serviceSnapshot))
	}
}

func (gw *EtcdGateway) launchTask(task func()) bool {
	if gw == nil || task == nil {
		return false
	}
	gw.initializeLifecycle()
	gw.lifecycleMu.Lock()
	if gw.closed || gw.watchCtx.Err() != nil {
		gw.lifecycleMu.Unlock()
		return false
	}
	gw.taskWG.Add(1)
	gw.lifecycleMu.Unlock()
	go func() {
		defer gw.taskWG.Done()
		task()
	}()
	return true
}

func (gw *EtcdGateway) loadDesiredServiceSnapshot() serviceSnapshot {
	if gw == nil {
		return nil
	}
	if snapshot, ok := gw.desiredServices.Load().(serviceSnapshot); ok {
		return snapshot
	}
	return nil
}

func (gw *EtcdGateway) setDesiredServiceSnapshot(snapshot serviceSnapshot) {
	gw.desiredServices.Store(cloneServiceSnapshot(snapshot))
}

func gatewayServiceRoot(namespace string) string {
	return etcd.NamespacePrefix(namespace, "services")
}

func defaultRoutePrefix(namespace string) string {
	return etcd.NamespacePrefix(namespace, "gateway/routes")
}

func applyGatewayPrefixDefaults(config *Config) {
	if config.RoutePrefix == "" {
		config.RoutePrefix = defaultRoutePrefix(config.Namespace)
	}
}

func applyGatewayConfigDefaults(config *Config) {
	applyGatewayPrefixDefaults(config)
	if config.MaxRequestBodyBytes <= 0 {
		config.MaxRequestBodyBytes = defaultMaxRequestBodyBytes
	}
}

func grpcServiceExcluded(config *Config, service string) bool {
	if config == nil {
		return false
	}
	for _, excluded := range config.GRPCServiceExcludes {
		if strings.TrimSpace(excluded) == service {
			return true
		}
	}
	return false
}

// circuitBreakerCheck 检查熔断器状态，返回 true 表示可以尝试连接
func (gw *EtcdGateway) circuitBreakerCheck(serviceName string) bool {
	val, ok := gw.circuitStates.Load(serviceName)
	if !ok {
		return true
	}
	cb := val.(*CircuitBreaker)
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == circuitClosed {
		return true
	}

	if cb.state == circuitOpen {
		if time.Since(cb.lastFailTime) > circuitCooldown {
			cb.state = circuitHalfOpen
			core.D("[Gateway] circuit breaker for %s entering half-open state", serviceName)
			return true
		}
		return false
	}

	return true
}

func (gw *EtcdGateway) circuitBreakerRecordSuccess(serviceName string) {
	val, ok := gw.circuitStates.Load(serviceName)
	if !ok {
		return
	}
	cb := val.(*CircuitBreaker)
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == circuitHalfOpen || cb.state == circuitOpen {
		core.D("[Gateway] circuit breaker for %s closed (recovered)", serviceName)
	}
	cb.state = circuitClosed
	cb.failCount = 0
}

func (gw *EtcdGateway) circuitBreakerRecordFailure(serviceName string) {
	val, _ := gw.circuitStates.LoadOrStore(serviceName, &CircuitBreaker{})
	cb := val.(*CircuitBreaker)
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failCount++
	cb.lastFailTime = time.Now()

	if cb.state == circuitHalfOpen {
		cb.state = circuitOpen
		cb.failCount = circuitFailThreshold
		core.D("[Gateway] circuit breaker for %s reopened after half-open failure", serviceName)
		return
	}

	if cb.failCount >= circuitFailThreshold && cb.state == circuitClosed {
		cb.state = circuitOpen
		core.D("[Gateway] circuit breaker for %s opened (tripped after %d failures)", serviceName, cb.failCount)
	}
}

// grpcStatusToHTTP 将 gRPC status code 映射为 HTTP 状态码
//
// 如果错误码大于999，则返回200状态码 由客户端判断code 信息处理错误
func grpcStatusToHTTP(err error) int {
	st, ok := status.FromError(err)
	if !ok {
		return core.StatusInternalServerError
	}

	if st.Code() > 999 {
		return core.StatusOK
	}

	switch st.Code() {
	case codes.InvalidArgument:
		return core.StatusBadRequest
	case codes.NotFound:
		return core.StatusNotFound
	case codes.AlreadyExists:
		return core.StatusConflict
	case codes.PermissionDenied:
		return core.StatusForbidden
	case codes.Unauthenticated:
		return core.StatusUnauthorized
	case codes.ResourceExhausted:
		return core.StatusTooManyRequests
	case codes.Unavailable:
		return core.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		return core.StatusGatewayTimeout
	case codes.Canceled:
		return 499 // client disconnected
	default:
		return core.StatusInternalServerError
	}
}

// grpcErrorToResponse 将 gRPC 错误转为前端友好的 JSON 响应
func grpcErrorToResponse(err error) core.Map {
	st, ok := status.FromError(err)
	if !ok {
		return core.Map{"code": 500, "msg": "internal server error"}
	}

	code := int(st.Code())
	msg := st.Message()
	if msg == "" {
		msg = "internal server error"
	}
	return core.Map{"code": code, "msg": msg}
}

func NewEtcdGateway(app *core.Core, conf ...*Config) (*EtcdGateway, error) {
	var config *Config
	if len(conf) > 0 {
		config = conf[0]
	}
	if config == nil {
		config = &Config{}
		config.EtcdEndpoints = app.Conf.GetStrings("etcd.endpoints")
		config.EtcdDialTimeout = time.Duration(app.Conf.GetInt64("etcd.dial_timeout", 5)) * time.Second
		config.Namespace = app.Conf.GetString("etcd.namespace", "")
		config.GRPCServiceExcludes = app.Conf.GetStrings("gateway.grpc_service_excludes")
		config.MaxRequestBodyBytes = app.Conf.GetInt64("gateway.max_request_body_bytes", defaultMaxRequestBodyBytes)
	}
	applyGatewayConfigDefaults(config)
	if config.EtcdDialTimeout == 0 {
		config.EtcdDialTimeout = 5 * time.Second
	}

	cli, err := newGatewayEtcdClient(clientv3.Config{
		Endpoints:   config.EtcdEndpoints,
		DialTimeout: config.EtcdDialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create etcd client failed: %w", err)
	}
	clientOwned := true
	defer func() {
		if clientOwned {
			_ = cli.Close()
		}
	}()
	gw := &EtcdGateway{
		app:                app,
		etcdCli:            cli,
		prefix:             config.RoutePrefix,
		serviceRoot:        gatewayServiceRoot(config.Namespace),
		config:             config,
		grpcServiceRoutes:  make(map[string]map[string]bool),
		grpcServiceSchemas: make(map[string]*ReflectionSchema),
		connPool:           NewConnectionPool(),
		ownsEtcdClient:     true,
	}
	gw.initializeLifecycle()
	gw.storeRouteSnapshot(emptyRouteSnapshot())
	gw.storeHTTPInstances(make(map[string]*httpServiceInstances))
	gw.storeHTTPIndexes(make(map[string]*atomic.Uint64))

	routeSource := etcdRouteWatchSource{
		client:      cli,
		routePrefix: config.RoutePrefix,
		launch:      func(task func()) { gw.launchTask(task) },
	}
	var initialRouteSnapshot *routeSnapshotRevision
	loadedRoutes, err := routeSource.Load(gw.watchCtx)
	if err != nil {
		core.D("[Gateway] load route snapshot failed: %v", err)
	} else {
		gw.replaceEtcdRouteSnapshot(loadedRoutes.Snapshot)
		initialRouteSnapshot = &loadedRoutes
	}

	gw.launchTask(func() {
		runRouteWatchLoopFrom(
			gw.watchCtx,
			routeSource,
			initialRouteSnapshot,
			func(snapshot routeSnapshot) { gw.replaceEtcdRouteSnapshot(snapshot) },
			func(events []routeEvent) { gw.applyRouteBatch(events) },
			waitRouteWatchBackoff,
		)
	})

	serviceSource := etcdServiceWatchSource{
		client:      cli,
		serviceRoot: gw.serviceRoot,
		launch:      gw.launchTask,
	}
	var initialServiceSnapshot *serviceSnapshotRevision
	loadedServices, err := serviceSource.Load(gw.watchCtx)
	if err != nil {
		core.D("[Gateway] load service snapshot failed: %v", err)
	} else {
		gw.reconcileServiceSnapshot(loadedServices.Snapshot)
		initialServiceSnapshot = &loadedServices
	}
	gw.launchTask(func() {
		runServiceWatchLoopFrom(
			gw.watchCtx,
			serviceSource,
			initialServiceSnapshot,
			gw.reconcileServiceSnapshot,
			waitRouteWatchBackoff,
		)
	})

	gw.setupAdminAPI()

	app.OnShutdown(func() { gw.Close() })

	core.D("[Gateway] ✅ Etcd Gateway started")
	gw.ownsEtcdClient = true
	clientOwned = false
	return gw, nil
}

// parseServiceKey 解析 etcd key，提取 serviceName 和 instanceID
// key 格式: /services/{serviceName}/{instanceID}
func parseServiceKey(key, serviceRoot string) (serviceName, instanceID string, ok bool) {
	if !strings.HasPrefix(key, serviceRoot) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(key, serviceRoot), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// parseServiceInstance 从 etcd value 解析服务实例信息
func parseServiceInstance(data []byte) (*serviceInstance, error) {
	var info serviceInstance
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	if info.Addr == "" {
		return nil, fmt.Errorf("empty addr")
	}
	// 如果 value 中没有 ID，从 key 中提取
	return &info, nil
}

// discoverAndConnectServices 启动时发现并连接所有服务
func (gw *EtcdGateway) discoverAndConnectServices() {
	source := etcdServiceWatchSource{client: gw.etcdCli, serviceRoot: gw.serviceRoot, launch: gw.launchTask}
	gw.launchTask(func() {
		runServiceWatchLoop(gw.watchCtx, source, gw.reconcileServiceSnapshot, waitRouteWatchBackoff)
	})
}

// connectInstance 连接单个服务实例（watch PUT 触发），带重试
func (gw *EtcdGateway) connectInstance(serviceName, instanceID, addr string) (*ReflectionProxy, bool, error) {
	key := connectInstanceKey(serviceName, instanceID)
	for {
		desired, ok := gw.loadDesiredServiceSnapshot()[key]
		if !ok || desired.GRPCAddr == "" {
			return nil, false, errServiceInstanceNotDesired
		}
		targetAddr := desired.GRPCAddr
		if gw.connectWaitObserver != nil {
			gw.connectWaitObserver(key)
		}
		value, err, _ := gw.connectGroup.Do(key, func() (any, error) {
			gw.connectInFlight.Store(key, struct{}{})
			defer gw.connectInFlight.Delete(key)
			proxy, changed, connectErr := gw.connectInstanceOnce(serviceName, instanceID, targetAddr)
			return connectResult{proxy: proxy, changed: changed, addr: targetAddr}, connectErr
		})
		result, _ := value.(connectResult)
		if err != nil {
			latest, stillDesired := gw.loadDesiredServiceSnapshot()[key]
			if stillDesired && latest.GRPCAddr != "" && latest.GRPCAddr != result.addr && gw.watchCtx.Err() == nil {
				continue
			}
			return nil, false, err
		}
		latest, stillDesired := gw.loadDesiredServiceSnapshot()[key]
		if stillDesired && latest.GRPCAddr != result.addr && gw.watchCtx.Err() == nil {
			continue
		}
		return result.proxy, result.changed, nil
	}
}

type connectResult struct {
	proxy   *ReflectionProxy
	changed bool
	addr    string
}

func (gw *EtcdGateway) connectInstanceOnce(serviceName, instanceID, addr string) (*ReflectionProxy, bool, error) {
	if !gw.circuitBreakerCheck(serviceName) {
		return nil, false, errors.New("gateway: service circuit breaker is open")
	}

	pool := gw.connPool.GetOrCreate(gw.app, serviceName)
	if desiredAddr, desired := pool.DesiredAddress(instanceID); !desired || desiredAddr != addr {
		return nil, false, errServiceInstanceNotDesired
	}

	// 尝试连接，带重试（服务可能 etcd 注册了但 gRPC 还没启动）
	var proxy *ReflectionProxy
	var changed bool
	var err error

	for attempt := range gatewayConnectRetryAttempts {
		if attempt > 0 {
			timer := time.NewTimer(gatewayConnectRetryDelay(attempt))
			select {
			case <-gw.watchCtx.Done():
				timer.Stop()
				return nil, false, gw.watchCtx.Err()
			case <-timer.C:
			}
		}
		proxy, changed, err = pool.AddOrUpdateInstanceContext(gw.watchCtx, instanceID, addr)
		if err == nil {
			break
		}
		if errors.Is(err, errServiceInstanceNotDesired) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, err
		}
	}

	if err != nil {
		core.D("[Gateway] connect instance %s/%s at %s failed: %v", serviceName, instanceID, addr, err)
		gw.circuitBreakerRecordFailure(serviceName)
		return nil, false, err
	}

	gw.circuitBreakerRecordSuccess(serviceName)

	if changed {
		core.D("[Gateway] ✅ instance %s/%s connected at %s", serviceName, instanceID, addr)
		gw.autoRegisterRoutes(serviceName, proxy)
	}
	return proxy, changed, nil
}

const gatewayConnectRetryAttempts = 30

func gatewayConnectRetryDelay(attempt int) time.Duration {
	delay := time.Duration(attempt) * 500 * time.Millisecond
	if delay > 5*time.Second {
		return 5 * time.Second
	}
	return delay
}

func connectInstanceKey(serviceName, instanceID string) string {
	return serviceName + "/" + instanceID
}

// handleServiceDelete preserves etcd watch order for HTTP targets while the
// potentially slow gRPC connection cleanup remains asynchronous.
func (gw *EtcdGateway) handleServiceDelete(serviceName, instanceID string) {
	gw.removeHTTPInstance(serviceName, instanceID)
	if pool := gw.connPool.Get(serviceName); pool != nil {
		pool.MarkInstanceUndesired(instanceID)
	}
	gw.launchTask(func() { gw.removeGRPCInstance(serviceName, instanceID) })
}

func (gw *EtcdGateway) removeGRPCInstance(serviceName, instanceID string) {
	pool := gw.connPool.Get(serviceName)
	if pool == nil {
		core.Warn("[Gateway] instance %s/%s removed from etcd but proxy pool is missing", serviceName, instanceID)
		return
	}

	pool.RemoveInstanceIfUndesired(instanceID)
	core.D("[Gateway] instance %s/%s removed", serviceName, instanceID)

	// 如果没有实例了，移除整个池
	if gw.connPool.RemoveIfEmpty(serviceName, pool) {
		gw.removeAutoRegisteredRoutes(serviceName)
		core.Warn("[Gateway] service %s fully disconnected after removing instance %s", serviceName, instanceID)
	}
}

// watchEtcdServices 监听 etcd 中的服务变化，watch channel 意外关闭时自动重试
func (gw *EtcdGateway) watchEtcdServices(startRev int64) {
	_ = startRev // Recovery always begins with a full authoritative snapshot.
	source := etcdServiceWatchSource{client: gw.etcdCli, serviceRoot: gw.serviceRoot, launch: gw.launchTask}
	runServiceWatchLoop(gw.watchCtx, source, gw.reconcileServiceSnapshot, waitRouteWatchBackoff)
}

func (gw *EtcdGateway) reconcileServiceSnapshot(snapshot serviceSnapshot) {
	if gw == nil {
		return
	}
	gw.initializeLifecycle()
	next := cloneServiceSnapshot(snapshot)

	gw.desiredMu.Lock()
	previous := cloneServiceSnapshot(gw.loadDesiredServiceSnapshot())
	gw.setDesiredServiceSnapshot(next)
	gw.desiredMu.Unlock()

	// Publish every desired marker before any asynchronous removal. This keeps a
	// pending B alive when A is deleted from the same logical service.
	for _, instance := range next {
		gw.connPool.SetDesiredInstance(gw.app, instance.ServiceName, instance.InstanceID, instance.GRPCAddr)
	}

	// HTTP reconciliation is synchronous, so a delete followed immediately by a
	// PUT cannot be undone by delayed gRPC cleanup.
	for serviceName, instances := range gw.loadHTTPInstances() {
		for instanceID := range instances.byID {
			if _, desired := next[connectInstanceKey(serviceName, instanceID)]; !desired {
				gw.removeHTTPInstance(serviceName, instanceID)
			}
		}
	}
	for _, instance := range next {
		if err := gw.addHTTPInstance(instance.ServiceName, instance.InstanceID, instance.HTTPAddr); err != nil {
			core.Warn("[Gateway] remove invalid HTTP instance %s/%s: %v", instance.ServiceName, instance.InstanceID, err)
		}
	}

	for key, old := range previous {
		if _, desired := next[key]; desired {
			continue
		}
		if pool := gw.connPool.Get(old.ServiceName); pool != nil {
			pool.MarkInstanceUndesired(old.InstanceID)
		}
		serviceName, instanceID := old.ServiceName, old.InstanceID
		gw.launchTask(func() { gw.removeGRPCInstance(serviceName, instanceID) })
	}

	for _, instance := range next {
		pool := gw.connPool.Get(instance.ServiceName)
		if installedAddr, installed := pool.InstanceAddress(instance.InstanceID); installed && installedAddr == instance.GRPCAddr {
			continue
		}
		serviceName, instanceID, addr := instance.ServiceName, instance.InstanceID, instance.GRPCAddr
		gw.launchTask(func() { _, _, _ = gw.connectInstance(serviceName, instanceID, addr) })
	}
}

func (gw *EtcdGateway) addDesiredServiceInstance(instance registeredServiceInstance) {
	gw.desiredMu.Lock()
	next := cloneServiceSnapshot(gw.loadDesiredServiceSnapshot())
	next[connectInstanceKey(instance.ServiceName, instance.InstanceID)] = instance
	gw.setDesiredServiceSnapshot(next)
	gw.desiredMu.Unlock()
	gw.connPool.SetDesiredInstance(gw.app, instance.ServiceName, instance.InstanceID, instance.GRPCAddr)
}

// autoRegisterRoutes 自动为 gRPC 方法注册 HTTP 路由，同时清理已删除方法的旧路由
func (gw *EtcdGateway) autoRegisterRoutes(serviceName string, proxy *ReflectionProxy) {
	if proxy == nil || proxy.schema == nil {
		return
	}
	methods := proxy.Methods()

	gw.mu.Lock()
	defer gw.mu.Unlock()
	if gw.grpcServiceSchemas == nil {
		gw.grpcServiceSchemas = make(map[string]*ReflectionSchema)
	}
	if gw.grpcServiceRoutes == nil {
		gw.grpcServiceRoutes = make(map[string]map[string]bool)
	}
	if gw.grpcServiceSchemas[serviceName] == proxy.schema {
		return
	}
	events := make([]routeEvent, 0, len(methods))

	newHashes := make(map[string]bool)

	for fullMethod, desc := range methods {
		if grpcServiceExcluded(gw.config, desc.Service) {
			continue
		}
		httpMethod, httpPath := grpcToHTTP(desc.Package, desc.Service, desc.Method)

		serviceParts := strings.Split(desc.Service, ".")
		shortService := serviceParts[len(serviceParts)-1]
		grpcServiceName := strings.ToLower(strings.TrimSuffix(shortService, "Service"))

		routeID := fmt.Sprintf("auto-%s-%s", grpcServiceName, strings.TrimPrefix(fullMethod, "/"))
		routeIDHash := core.SHA256(routeID)

		storageKey := "runtime:auto-grpc/" + serviceName + "/" + routeIDHash

		definition := &Route{
			ID:          routeIDHash,
			Source:      route.SourceAutoGRPC,
			Owner:       serviceName,
			Protocol:    RouteProtocolGRPC,
			Method:      httpMethod,
			Path:        httpPath,
			ServiceName: serviceName,
			GRPCMethod:  fullMethod,
			Description: fmt.Sprintf("auto-registered from %s", serviceName),
			Enabled:     true,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		if err := normalizeAndValidateRoute(definition); err != nil {
			core.Warn("[Gateway] ignore invalid reflected route %s: %v", fullMethod, err)
			continue
		}
		newHashes[storageKey] = true
		events = append(events, routeEvent{StorageKey: storageKey, Value: &routeSourceValue{Definition: definition}})
	}

	for oldStorageKey := range gw.grpcServiceRoutes[serviceName] {
		if !newHashes[oldStorageKey] {
			events = append(events, routeEvent{StorageKey: oldStorageKey, Deleted: true})
		}
	}
	gw.grpcServiceRoutes[serviceName] = newHashes
	gw.grpcServiceSchemas[serviceName] = proxy.schema
	gw.applyRouteBatchLocked(events)
}

func (gw *EtcdGateway) removeAutoRegisteredRoutes(serviceName string) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	old := gw.grpcServiceRoutes[serviceName]
	if len(old) == 0 {
		delete(gw.grpcServiceRoutes, serviceName)
		delete(gw.grpcServiceSchemas, serviceName)
		return
	}
	events := make([]routeEvent, 0, len(old))
	for storageKey := range old {
		events = append(events, routeEvent{StorageKey: storageKey, Deleted: true})
	}
	delete(gw.grpcServiceRoutes, serviceName)
	delete(gw.grpcServiceSchemas, serviceName)
	gw.applyRouteBatchLocked(events)
}

// grpcToHTTP 将 gRPC 方法转换为 HTTP 路由
// e.g. ("v1.auth", "v1.auth.UserService", "PostLogin") -> ("POST", "/v1/auth/user/login")
func grpcToHTTP(pkg, service, method string) (httpMethod, path string) {
	httpMethod = "POST"
	methodName := method
	for _, prefix := range []string{"Post", "Get", "Put", "Delete"} {
		if strings.HasPrefix(method, prefix) {
			httpMethod = strings.ToUpper(prefix)
			methodName = strings.TrimPrefix(method, prefix)
			break
		}
	}

	pkgPath := "/" + strings.ReplaceAll(pkg, ".", "/")

	serviceParts := strings.Split(service, ".")
	shortService := serviceParts[len(serviceParts)-1]
	svcName := strings.ToLower(strings.TrimSuffix(shortService, "Service"))

	methodPath := parseMethodName(methodName)

	return httpMethod, fmt.Sprintf("%s/%s/%s", pkgPath, svcName, methodPath)
}

var re = regexp.MustCompile(`(?i)by/?`)

// parseMethodName 解析方法名，处理 By 关键字
func parseMethodName(methodName string) string {
	base := camelToKebab(methodName)

	result := re.ReplaceAllStringFunc(base, func(match string) string {
		return ":"
	})

	return result
}

// camelToKebab CamelCase -> kebab-case, underscore -> hyphen
func camelToKebab(s string) string {
	var result []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			result = append(result, '-')
			if i+1 < len(s) && 'A' <= s[i+1] && s[i+1] <= 'Z' {
				result = append(result, s[i+1]+32)
				i++
			}
		} else if i > 0 && 'A' <= c && c <= 'Z' {
			result = append(result, '/')
			result = append(result, c+32)
		} else if 'A' <= c && c <= 'Z' {
			result = append(result, c+32)
		} else {
			result = append(result, c)
		}
	}
	return string(result)
}

func nextRouteWatchRevision(loadedRevision int64) int64 {
	if loadedRevision <= 0 {
		return 0
	}
	return loadedRevision + 1
}

func (gw *EtcdGateway) addOrUpdateRoute(route *Route) {
	if err := normalizeAndValidateRoute(route); err != nil {
		core.Warn("[Gateway] ignore invalid route: %v", err)
		return
	}
	if route.ID == "" {
		route.ID = routeID(route)
	}
	storageKey := gw.prefix + route.ID
	if gw.prefix == "" {
		storageKey = route.ID
	}
	gw.applyRouteBatch([]routeEvent{{StorageKey: storageKey, Value: &routeSourceValue{Definition: route}}})
}

func (gw *EtcdGateway) removeRouteByID(routeID string) {
	snapshot := gw.loadRouteSnapshot()
	records := snapshot.recordsByID[routeID]
	if len(records) == 0 {
		return
	}
	events := make([]routeEvent, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if _, exists := seen[record.StorageKey]; exists {
			continue
		}
		seen[record.StorageKey] = struct{}{}
		events = append(events, routeEvent{StorageKey: record.StorageKey, Deleted: true})
	}
	gw.applyRouteBatch(events)
}

func (gw *EtcdGateway) registerRoute(route *Route) {
	var handler core.HandlerFunc
	if route.Protocol == RouteProtocolHTTP {
		handler = gw.createHTTPProxyHandler(route)
	} else {
		handler = gw.createProxyHandler(route)
	}

	route.Path = strings.TrimSuffix(route.Path, "/")
	target := route.GRPCMethod
	if route.Protocol == RouteProtocolHTTP {
		target = route.UpstreamPath
	}
	core.D("Add Route ✅: %s %s -> %s:%s", route.Method, route.Path, route.Protocol, target)
	gw.app.AddHandle([]string{route.Method}, route.Path, nil, handler)
}

func (gw *EtcdGateway) unregisterRoute(route *Route) {
	gw.app.RemoveHandle([]string{strings.ToUpper(route.Method)}, strings.TrimSuffix(route.Path, "/"))
}

func (gw *EtcdGateway) createProxyHandler(route *Route) core.HandlerFunc {
	return func(ctx core.Ctx) error {
		var rawBody []byte
		if ctx.Method() != "GET" {
			var err error
			rawBody, err = readAndResetBody(ctx.Request(), gw.maxRequestBodyBytes())
			if errors.Is(err, errRequestBodyTooLarge) {
				return ctx.Status(http.StatusRequestEntityTooLarge).JSON(core.Map{
					"code": http.StatusRequestEntityTooLarge, "msg": "request body too large",
				})
			}
			if err != nil {
				core.Erro("[Gateway] gRPC proxy read body failed: method=%s path=%s grpc=%s err=%v",
					ctx.Method(), ctx.Path(), route.GRPCMethod, err)
				return ctx.Status(http.StatusBadRequest).JSON(core.Map{
					"code": 400, "msg": "invalid request body",
				})
			}
		}

		proxy := gw.doGetProxy(route.ServiceName)
		if proxy == nil {
			core.Erro("[Gateway] proxy not available for service: %s route=%s %s grpc=%s state={%s}",
				route.ServiceName, ctx.Method(), ctx.Path(), route.GRPCMethod, gw.proxyLookupDebugState(route.ServiceName))
			return ctx.Status(http.StatusServiceUnavailable).JSON(core.Map{
				"code": 503,
				"msg":  fmt.Sprintf("service %s unavailable", route.ServiceName),
			})
		}

		var bodyMap core.Map
		if ctx.Method() != "GET" && len(rawBody) > 0 {
			if err := sonic.Unmarshal(rawBody, &bodyMap); err != nil {
				core.Erro("[Gateway] gRPC proxy bind body failed: method=%s path=%s grpc=%s err=%v",
					ctx.Method(), ctx.Path(), route.GRPCMethod, err)
			}
		}
		reqBody := mergeGRPCRequestBody(ctx.ParamsMaps(), ctx.Request().URL.Query(), bodyMap)
		appendGatewayRequestMetadata(reqBody, ctx.Request().Header, rawBody)

		jsonReq, err := sonic.Marshal(reqBody)
		if err != nil {
			core.Erro("[Gateway] gRPC proxy marshal request failed: method=%s path=%s grpc=%s err=%v",
				ctx.Method(), ctx.Path(), route.GRPCMethod, err)
			return ctx.Status(http.StatusBadRequest).JSON(core.Map{
				"code": 400, "msg": "invalid request body",
			})
		}
		md := metadata.New(nil)
		appendHTTPHeadersToMetadata(md, ctx.Request().Header)

		for k, v := range ctx.Vars() {
			md.Set(k, fmt.Sprintf("%v", v))
		}

		grpcCtx := gatewayOutgoingContext(ctx.Context(), md)

		callCtx, cancel := context.WithTimeout(grpcCtx, 10*time.Second)
		defer cancel()

		jsonResp, err := proxy.Invoke(callCtx, route.GRPCMethod, jsonReq)
		if err != nil {
			if st, ok := status.FromError(err); ok {
				core.Erro("[Gateway] gRPC proxy upstream error: grpc=%s code=%s message=%q",
					route.GRPCMethod, st.Code(), st.Message())
			} else {
				core.Erro("[Gateway] gRPC proxy upstream error: grpc=%s err=%v",
					route.GRPCMethod, err)
			}
			return ctx.Status(grpcStatusToHTTP(err)).JSON(grpcErrorToResponse(err))
		}
		return ctx.Type("json").Send(jsonResp)
	}
}

func gatewayOutgoingContext(parent context.Context, md metadata.MD) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return metadata.NewOutgoingContext(parent, md)
}

func (gw *EtcdGateway) maxRequestBodyBytes() int64 {
	if gw != nil && gw.config != nil && gw.config.MaxRequestBodyBytes > 0 {
		return gw.config.MaxRequestBodyBytes
	}
	return defaultMaxRequestBodyBytes
}

func readAndResetBody(req *http.Request, limit int64) ([]byte, error) {
	if req == nil || req.Body == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultMaxRequestBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errRequestBodyTooLarge
	}
	req.Body = io.NopCloser(bytes.NewBuffer(body))
	return body, nil
}

func buildGRPCRequestBody(params map[string]string, query map[string][]string) core.Map {
	reqBody := make(core.Map, len(params)+len(query))
	for k, v := range params {
		reqBody[k] = v
	}
	for k, v := range query {
		if len(v) > 0 {
			reqBody[k] = v[0]
		}
	}
	return reqBody
}

func mergeGRPCRequestBody(params map[string]string, query map[string][]string, body core.Map) core.Map {
	reqBody := buildGRPCRequestBody(nil, query)
	maps.Copy(reqBody, body)
	for key, value := range params {
		reqBody[key] = value
	}
	return reqBody
}

func appendGatewayRequestMetadata(reqBody core.Map, headers http.Header, rawBody []byte) {
	if len(rawBody) > 0 {
		reqBody["raw_body"] = base64.StdEncoding.EncodeToString(rawBody)
	}
	if len(headers) > 0 {
		reqBody["headers"] = core.HeaderMap(headers)
	}
}

func appendHTTPHeadersToMetadata(md metadata.MD, headers http.Header) {
	for key, values := range headers {
		if key == "" || len(values) == 0 {
			continue
		}
		key = strings.ToLower(key)
		if grpcMetadataHeaderSkipped(key) {
			continue
		}
		md.Append(key, values...)
	}
}

func grpcMetadataHeaderSkipped(key string) bool {
	switch key {
	case "connection", "keep-alive", "proxy-connection", "transfer-encoding", "upgrade",
		"content-length", "content-type", "te", "trailer":
		return true
	default:
		return strings.HasPrefix(key, ":") || strings.HasPrefix(key, "grpc-")
	}
}

func (gw *EtcdGateway) doGetProxy(serviceName string) *ReflectionProxy {
	if gw == nil || gw.connPool == nil {
		return nil
	}
	pool := gw.connPool.Get(serviceName)
	if pool != nil {
		proxy := pool.Get()
		if proxy != nil {
			gw.circuitBreakerRecordSuccess(serviceName)
			return proxy
		}
	}
	if !gw.circuitBreakerCheck(serviceName) {
		core.D("[Gateway] doGetProxy: %s circuit open with no usable pooled connection", serviceName)
		return nil
	}

	// Pool misses use the same lifecycle-owned, singleflight-coalesced connect
	// path as etcd watch reconciliation. Request handling never dials or reflects.
	if gw.app == nil || gw.app.EtcdDiscovery == nil {
		core.Warn("[Gateway] doGetProxy: %s has no proxy pool and EtcdDiscovery is disabled", serviceName)
		return nil
	}
	services := gw.app.EtcdDiscovery.GetServices(serviceName)
	if len(services) == 0 {
		core.Warn("[Gateway] doGetProxy: %s has no proxy pool and EtcdDiscovery returned no instances", serviceName)
		return nil
	}

	for _, s := range services {
		instanceID := s.ID
		if instanceID == "" {
			instanceID = s.Addr // fallback
		}
		instance := registeredServiceInstance{ServiceName: serviceName, InstanceID: instanceID, GRPCAddr: s.Addr}
		gw.addDesiredServiceInstance(instance)
		gw.launchTask(func() { _, _, _ = gw.connectInstance(serviceName, instanceID, s.Addr) })
	}
	return nil
}

func (gw *EtcdGateway) proxyLookupDebugState(serviceName string) string {
	parts := []string{"service=" + serviceName}
	if value, ok := gw.circuitStates.Load(serviceName); ok {
		cb := value.(*CircuitBreaker)
		cb.mu.Lock()
		parts = append(parts, fmt.Sprintf("circuit_state=%d", cb.state))
		parts = append(parts, fmt.Sprintf("circuit_failures=%d", cb.failCount))
		cb.mu.Unlock()
	} else {
		parts = append(parts, "circuit_state=absent")
	}

	if gw.connPool == nil {
		parts = append(parts, "pool=disabled")
	} else if pool := gw.connPool.Get(serviceName); pool == nil {
		parts = append(parts, "pool=missing")
	} else {
		parts = append(parts, fmt.Sprintf("pool_instances=%d", pool.Size()))
	}

	if gw.app == nil || gw.app.EtcdDiscovery == nil {
		parts = append(parts, "discovery=disabled")
	} else {
		parts = append(parts, fmt.Sprintf("discovery_instances=%d", len(gw.app.EtcdDiscovery.GetServices(serviceName))))
	}
	return strings.Join(parts, " ")
}

func (gw *EtcdGateway) setupAdminAPI() {
	gw.app.POST("/admin/gateway/routes", gw.localOnly(gw.createRoute))
	gw.app.PUT("/admin/gateway/routes/:id", gw.localOnly(gw.updateRoute))
	gw.app.DELETE("/admin/gateway/routes/:id", gw.localOnly(gw.deleteRoute))
	gw.app.GET("/admin/gateway/routes", gw.localOnly(gw.listRoutes))
	gw.app.GET("/admin/gateway/routes/:id", gw.localOnly(gw.getRoute))
}

func (gw *EtcdGateway) localOnly(fn func(core.Ctx) error) func(core.Ctx) error {
	return func(ctx core.Ctx) error {
		if !isLoopbackRemoteAddr(ctx.Request().RemoteAddr) {
			return ctx.Status(403).JSON(core.Map{"code": 403, "msg": "forbidden"})
		}
		return fn(ctx)
	}
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (gw *EtcdGateway) createRoute(ctx core.Ctx) error {
	var definition Route
	if err := ctx.Bind(&definition); err != nil {
		return ctx.Status(400).JSON(core.Map{"code": 400, "msg": err.Error()})
	}

	if err := prepareRoute(&definition); err != nil {
		return ctx.Status(400).JSON(core.Map{"code": 400, "msg": err.Error()})
	}
	definition.Source = route.SourceManual
	definition.Owner = ""
	putCtx, cancel := context.WithTimeout(ctx.Context(), 5*time.Second)
	defer cancel()
	if err := gw.routePublisher().PublishManual(putCtx, &definition); err != nil {
		return ctx.Status(500).JSON(core.Map{"code": 500, "msg": err.Error()})
	}

	return ctx.Status(201).JSON(core.Map{"code": 0, "msg": "success", "data": definition})
}

func (gw *EtcdGateway) updateRoute(ctx core.Ctx) error {
	routeID := ctx.Params("id")
	var definition Route
	if err := ctx.Bind(&definition); err != nil {
		return ctx.Status(400).JSON(core.Map{"code": 400, "msg": err.Error()})
	}

	definition.ID = routeID
	if err := normalizeAndValidateRoute(&definition); err != nil {
		return ctx.Status(400).JSON(core.Map{"code": 400, "msg": err.Error()})
	}
	definition.Source = route.SourceManual
	definition.Owner = ""
	definition.UpdatedAt = time.Now()
	snapshot := gw.loadRouteSnapshot()
	if previous, exists := routeRecordByID(snapshot, gw.prefix, routeID); exists && definition.CreatedAt.IsZero() {
		definition.CreatedAt = previous.Definition.CreatedAt
	}
	putCtx, cancel := context.WithTimeout(ctx.Context(), 5*time.Second)
	defer cancel()
	if err := gw.routePublisher().PublishManual(putCtx, &definition); err != nil {
		return ctx.Status(500).JSON(core.Map{"code": 500, "msg": err.Error()})
	}
	newStorageKey := manualRouteStorageKey(gw.prefix, definition.Slot)
	for _, record := range snapshot.recordsByID[routeID] {
		stored := record.Definition
		if record.StorageKey == newStorageKey || stored == nil || (stored.Source != "" && stored.Source != route.SourceManual) {
			continue
		}
		if _, err := gw.etcdCli.Delete(putCtx, record.StorageKey); err != nil {
			return ctx.Status(500).JSON(core.Map{"code": 500, "msg": err.Error()})
		}
	}

	return ctx.JSON(core.Map{"code": 0, "msg": "success"})
}

func (gw *EtcdGateway) deleteRoute(ctx core.Ctx) error {
	routeID := ctx.Params("id")
	deleteCtx, cancel := context.WithTimeout(ctx.Context(), 5*time.Second)
	defer cancel()
	snapshot := gw.loadRouteSnapshot()
	storageKeys := make([]string, 0, len(snapshot.recordsByID[routeID]))
	for _, record := range snapshot.recordsByID[routeID] {
		definition := record.Definition
		if definition != nil && (definition.Source == "" || definition.Source == route.SourceManual) {
			storageKeys = append(storageKeys, record.StorageKey)
		}
	}
	if len(storageKeys) == 0 {
		storageKeys = append(storageKeys, normalizeRouteStoragePrefix(gw.prefix)+routeID)
	}
	for _, storageKey := range storageKeys {
		if _, err := gw.etcdCli.Delete(deleteCtx, storageKey); err != nil {
			return ctx.Status(500).JSON(core.Map{"code": 500, "msg": err.Error()})
		}
	}
	return ctx.ToJSONCode("success")
}

func (gw *EtcdGateway) listRoutes(ctx core.Ctx) error {
	snapshot := gw.loadRouteSnapshot()
	records := snapshot.allRecords()
	routes := make([]routeAdminView, 0, len(records))
	for _, record := range records {
		routes = append(routes, newRouteAdminView(snapshot, record))
	}

	return ctx.JSON(core.Map{"code": 0, "data": routes, "total": len(routes)})
}

func (gw *EtcdGateway) getRoute(ctx core.Ctx) error {
	routeID := ctx.Params("id")
	snapshot := gw.loadRouteSnapshot()
	record, exists := routeRecordByID(snapshot, gw.prefix, routeID)
	if !exists {
		return ctx.Status(404).JSON(core.Map{"code": 404, "msg": "route not found"})
	}
	return ctx.ToJSONCode(newRouteAdminView(snapshot, record))
}

type routeAdminView struct {
	*Route
	StorageKey string         `json:"storage_key"`
	Slot       string         `json:"slot"`
	Source     route.Source   `json:"source"`
	Owner      string         `json:"owner"`
	Active     bool           `json:"active"`
	Conflict   *routeConflict `json:"conflict,omitempty"`
}

func newRouteAdminView(snapshot routeSnapshot, record routeRecord) routeAdminView {
	definition := record.Definition
	source := definition.Source
	if source == "" {
		source = route.SourceManual
	}
	var conflict *routeConflict
	if value, exists := snapshot.conflicts[definition.Slot]; exists {
		copy := value
		conflict = &copy
	}
	return routeAdminView{
		Route:      definition,
		StorageKey: record.StorageKey,
		Slot:       definition.Slot,
		Source:     source,
		Owner:      definition.Owner,
		Active:     snapshot.activeRecordKeys[routeRecordKey(record)],
		Conflict:   conflict,
	}
}

func routeRecordByID(snapshot routeSnapshot, routePrefix, id string) (routeRecord, bool) {
	records := snapshot.recordsByID[id]
	if len(records) == 0 {
		return routeRecord{}, false
	}
	legacyKey := normalizeRouteStoragePrefix(routePrefix) + id
	for _, record := range records {
		if record.StorageKey == legacyKey {
			return record, true
		}
	}
	for _, record := range records {
		if snapshot.activeRecordKeys[routeRecordKey(record)] {
			return record, true
		}
	}
	return records[0], true
}

func (gw *EtcdGateway) routePublisher() route.Publisher {
	return route.Publisher{
		Store:       clientRouteStore{client: gw.etcdCli},
		RoutePrefix: gw.prefix,
	}
}

func manualRouteStorageKey(prefix, slot string) string {
	return normalizeRouteStoragePrefix(prefix) + string(route.SourceManual) + "/" + slot
}

func normalizeRouteStoragePrefix(prefix string) string {
	return strings.TrimRight(strings.TrimSpace(prefix), "/") + "/"
}

func (gw *EtcdGateway) Close() error {
	if gw == nil {
		return nil
	}
	gw.closeOnce.Do(func() {
		gw.lifecycleMu.Lock()
		gw.closed = true
		cancel := gw.watchCancel
		gw.lifecycleMu.Unlock()
		if cancel != nil {
			cancel()
		}

		// Every Gateway watcher, forwarder, retry, connect, and async delete is
		// accounted for before state or owned clients are torn down.
		gw.taskWG.Wait()
		if gw.connPool != nil {
			gw.connPool.Close()
		}

		gw.httpMu.Lock()
		gw.storeHTTPInstances(make(map[string]*httpServiceInstances))
		gw.httpMu.Unlock()
		gw.httpIndexMu.Lock()
		gw.storeHTTPIndexes(make(map[string]*atomic.Uint64))
		gw.httpIndexMu.Unlock()

		gw.desiredMu.Lock()
		gw.setDesiredServiceSnapshot(make(serviceSnapshot))
		gw.desiredMu.Unlock()
		gw.mu.Lock()
		gw.storeRouteSnapshot(emptyRouteSnapshot())
		gw.grpcServiceRoutes = make(map[string]map[string]bool)
		gw.grpcServiceSchemas = make(map[string]*ReflectionSchema)
		gw.mu.Unlock()

		gw.circuitStates.Range(func(key, _ any) bool {
			gw.circuitStates.Delete(key)
			return true
		})
		gw.connectInFlight.Range(func(key, _ any) bool {
			gw.connectInFlight.Delete(key)
			return true
		})
		if gw.ownsEtcdClient && gw.etcdCli != nil {
			gw.closeErr = gw.etcdCli.Close()
		}
	})
	return gw.closeErr
}
