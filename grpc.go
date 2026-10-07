// grpc.go
package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xs23933/core/v3/etcd"
	"github.com/xs23933/core/v3/reuseport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

var (
	// ErrGRPCServerAlreadyInitialized 表示 gRPC server 已创建，不能再改变 transport 或 interceptor。
	ErrGRPCServerAlreadyInitialized = errors.New("grpc server already initialized")
	// ErrGRPCServerAlreadyConfigured 表示同一个 Core 实例已经完成一次 gRPC server 配置。
	ErrGRPCServerAlreadyConfigured = errors.New("grpc server already configured")
	// ErrGRPCTLSSharedAddress 表示 transport credentials 不能在 HTTP 共端口 ServeHTTP 路径上生效。
	ErrGRPCTLSSharedAddress = errors.New("grpc transport credentials require a dedicated grpc address")
	// ErrEtcdDiscoveryConfigurationChanged protects clients bound to an existing Discovery.
	ErrEtcdDiscoveryConfigurationChanged = errors.New("core: etcd discovery configuration change requires a new Core instance")

	newCoreEtcdRegistry      = etcd.NewRegistry
	newCoreEtcdDiscovery     = etcd.NewDiscovery
	registerCoreEtcdRegistry = func(registry *etcd.Registry) error {
		return registry.Register()
	}
	deregisterCoreEtcdRegistry = func(registry *etcd.Registry) error {
		return registry.Deregister()
	}
	closeCoreEtcdDiscovery = func(discovery *etcd.Discovery) error {
		return discovery.Close()
	}
)

// GRPCServerConfig 定义 Core 创建 gRPC server 前可注入的通用 transport 与 interceptor。
// 调用方拥有证书身份和授权策略；Core 只负责 server 生命周期和 interceptor chain。
type GRPCServerConfig struct {
	TransportCredentials credentials.TransportCredentials
	UnaryInterceptors    []grpc.UnaryServerInterceptor
	StreamInterceptors   []grpc.StreamServerInterceptor
}

// ConfigureGRPCServer 在 gRPC server 初始化前配置 transport 和 interceptor。
// 每个 Core 实例只允许配置一次，避免后续调用静默覆盖安全策略。
func (app *Core) ConfigureGRPCServer(config GRPCServerConfig) error {
	app.mutex.Lock()
	defer app.mutex.Unlock()

	if app.grpcServer != nil {
		return ErrGRPCServerAlreadyInitialized
	}
	if app.grpcConfig != nil {
		return ErrGRPCServerAlreadyConfigured
	}
	config.UnaryInterceptors = append([]grpc.UnaryServerInterceptor(nil), config.UnaryInterceptors...)
	config.StreamInterceptors = append([]grpc.StreamServerInterceptor(nil), config.StreamInterceptors...)
	app.grpcConfig = &config
	return nil
}

// EnableGRPC 启用 gRPC 支持
func (app *Core) EnableGRPC(addr ...string) *Core {
	app.grpcEnabled = true
	if len(addr) > 0 {
		app.grpcAddr = addr[0]
	} else if app.grpcAddr == "" {
		app.grpcAddr = app.addr
	}
	Info("gRPC enabled, listening on %s", app.grpcAddr)
	return app
}

// GetGRPCServer 获取 gRPC 服务器实例（用于注册服务）
func (app *Core) GetGRPCServer() *grpc.Server {
	app.mutex.Lock()
	defer app.mutex.Unlock()
	return app.getOrCreateGRPCServer()
}

func (app *Core) getOrCreateGRPCServer() *grpc.Server {
	if app.grpcServer == nil {
		app.grpcServer = app.newGRPCServer()
		app.grpcEnabled = true
	}
	if app.grpcAddr == "" {
		app.grpcAddr = app.addr
	}
	return app.grpcServer
}

func (app *Core) newGRPCServer() *grpc.Server {
	unaryInterceptors := []grpc.UnaryServerInterceptor{app.grpcResponseContext, errorWrapInterceptor}
	streamInterceptors := []grpc.StreamServerInterceptor{app.grpcStreamResponseContext}
	serverOptions := make([]grpc.ServerOption, 0, 3)
	if app.grpcConfig != nil {
		unaryInterceptors = append(unaryInterceptors, app.grpcConfig.UnaryInterceptors...)
		if len(app.grpcConfig.StreamInterceptors) > 0 {
			streamInterceptors = append(streamInterceptors, app.grpcConfig.StreamInterceptors...)
		}
		if app.grpcConfig.TransportCredentials != nil {
			serverOptions = append(serverOptions, grpc.Creds(app.grpcConfig.TransportCredentials))
		}
	}
	serverOptions = append(serverOptions, grpc.ChainUnaryInterceptor(unaryInterceptors...), grpc.ChainStreamInterceptor(streamInterceptors...))
	return grpc.NewServer(serverOptions...)
}

// RegisterGRPCService 注册 gRPC 服务
func (app *Core) RegisterGRPCService(registerFunc func(*grpc.Server)) {
	registerFunc(app.GetGRPCServer())
}

// startGRPCServer 内部方法，启动 gRPC 服务器
func (app *Core) startGRPCServer() error {
	if !app.grpcEnabled || app.grpcServer == nil {
		return nil
	}

	if app.grpcAddr == app.addr {
		// grpc.Server 的 transport credentials 只在 Serve listener 握手路径执行。
		// ServeHTTP 共端口无法提供等价的 gRPC peer.AuthInfo，必须拒绝伪安全配置。
		if app.grpcConfig != nil && app.grpcConfig.TransportCredentials != nil {
			return ErrGRPCTLSSharedAddress
		}
		Info("gRPC sharing port with HTTP on %s", app.addr)
		app.setupSharedHandler()
		return nil
	}

	ln, err := reuseport.Listen(app.networkProto, app.grpcAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on gRPC address %s: %w", app.grpcAddr, err)
	}

	Info("gRPC server listening on %s (protocol: HTTP/2 over TCP)", app.grpcAddr)

	app.eg.Go(func() error {
		if err := app.grpcServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			Erro("gRPC server error: %v", err)
			return err
		}
		return nil
	})

	return nil
}

func (app *Core) setupSharedHandler() {
	originalHandler := app.Handler

	app.Protocols = new(http.Protocols)
	app.Protocols.SetHTTP1(true)
	app.Protocols.SetUnencryptedHTTP2(true)

	app.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			app.grpcServer.ServeHTTP(w, r)
			return
		}
		originalHandler.ServeHTTP(w, r)
	})
}

// errorWrapInterceptor 将非 status.Error 的 error 自动包装为 codes.Internal
func errorWrapInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	resp, err := handler(ctx, req)
	if err != nil {
		if _, ok := status.FromError(err); !ok {
			err = status.Error(codes.Internal, err.Error())
		}
	}
	return resp, err
}

// shutdownGRPC 优雅关闭 gRPC 服务器
func (app *Core) shutdownGRPC() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	app.shutdownGRPCContext(ctx)
}

func (app *Core) shutdownGRPCContext(ctx context.Context) {
	if app.grpcServer == nil {
		return
	}
	Info("Shutting down gRPC server...")
	done := make(chan struct{})
	go func() {
		app.grpcServer.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		app.forceStopGRPC()
	}
}

func (app *Core) forceStopGRPC() {
	app.grpcForceStopOnce.Do(func() {
		if app.grpcServer != nil {
			go app.grpcServer.Stop()
		}
	})
}

// EnableEtcdRegistry 启用 etcd 服务注册
// 当 opts 为 nil 时，从配置文件读取；当 opts 已设置字段时，保留用户值
func (app *Core) EnableEtcdRegistry(opts *etcd.Options) error {
	app.etcdRegistryMu.Lock()
	defer app.etcdRegistryMu.Unlock()
	app.mutex.Lock()
	shutdownStarted := app.shutdownStarted
	app.mutex.Unlock()
	if shutdownStarted {
		return errors.New("core: cannot enable etcd registry after shutdown")
	}
	if opts == nil {
		opts = etcd.DefaultOptions()
	}
	app.applyEtcdRegistryDefaults(opts)

	registry, err := newCoreEtcdRegistry(opts)
	if err != nil {
		return err
	}
	registryCleanup := onceCleanup(func() { _ = deregisterCoreEtcdRegistry(registry) })

	discoveryOpts := &etcd.Options{
		Namespace:   opts.Namespace,
		Endpoints:   append([]string(nil), opts.Endpoints...),
		Username:    opts.Username,
		Password:    opts.Password,
		DialTimeout: opts.DialTimeout,
	}
	app.mutex.Lock()
	discovery := app.EtcdDiscovery
	discoveryCleanup := app.etcdDiscoveryCleanup
	app.mutex.Unlock()
	newDiscovery := discovery == nil
	if !newDiscovery && !discovery.MatchesOptions(discoveryOpts) {
		registryCleanup()
		return ErrEtcdDiscoveryConfigurationChanged
	}
	if newDiscovery {
		discovery, err = newCoreEtcdDiscovery(discoveryOpts)
		if err != nil {
			registryCleanup()
			return err
		}
		discoveryCleanup = onceCleanup(func() { _ = closeCoreEtcdDiscovery(discovery) })
	}
	candidateCleanup := func() {
		if newDiscovery {
			discoveryCleanup()
		}
	}

	if err := registerCoreEtcdRegistry(registry); err != nil {
		candidateCleanup()
		registryCleanup()
		return err
	}

	app.mutex.Lock()
	if app.shutdownStarted {
		app.mutex.Unlock()
		candidateCleanup()
		registryCleanup()
		return errors.New("core: cannot install etcd registry after shutdown")
	}
	// A client may have enabled Discovery while registration was in flight.
	if app.EtcdDiscovery != nil && app.EtcdDiscovery != discovery {
		current := app.EtcdDiscovery
		if !current.MatchesOptions(discoveryOpts) {
			app.mutex.Unlock()
			candidateCleanup()
			registryCleanup()
			return ErrEtcdDiscoveryConfigurationChanged
		}
		candidateCleanup()
		discovery = current
		discoveryCleanup = app.etcdDiscoveryCleanup
	}
	previousRegistryCleanup := app.etcdRegistryCleanup
	app.etcdRegistry = registry
	app.EtcdDiscovery = discovery
	app.etcdRegistryServiceName = opts.ServiceName
	app.etcdRegistryNamespace = opts.Namespace
	app.etcdRegistryCleanup = registryCleanup
	app.etcdDiscoveryCleanup = discoveryCleanup
	registerReflection := !app.grpcReflectionRegistered
	app.grpcReflectionRegistered = true
	app.mutex.Unlock()

	if previousRegistryCleanup != nil {
		previousRegistryCleanup()
	}
	if registerReflection {
		reflection.Register(app.GetGRPCServer())
	}

	return nil
}

// EtcdRegistryIdentity returns the identity installed by the most recent
// successful EnableEtcdRegistry call. Failed replacements leave this snapshot
// unchanged.
func (app *Core) EtcdRegistryIdentity() (serviceName, namespace string, ok bool) {
	if app == nil {
		return "", "", false
	}
	app.mutex.Lock()
	defer app.mutex.Unlock()
	if app.etcdRegistry == nil || app.EtcdDiscovery == nil {
		return "", "", false
	}
	return app.etcdRegistryServiceName, app.etcdRegistryNamespace, true
}

func (app *Core) applyEtcdRegistryDefaults(opts *etcd.Options) {
	app.applyEtcdDiscoveryDefaults(opts)
	if opts.RegistrationReady == nil {
		ready := false
		opts.RegistrationReady = &ready
	}
	if opts.Namespace == "" {
		opts.Namespace = app.Conf.GetString("etcd.namespace", "")
	}

	// 仅在用户未设置时从配置读取
	if len(opts.Endpoints) == 0 {
		opts.Endpoints = app.Conf.GetStrings("etcd.endpoints", []string{"127.0.0.1:2379"})
	}
	if opts.ServiceName == "" {
		opts.ServiceName = app.Conf.GetString("etcd.service_name", "")
	}
	if opts.ServiceAddr == "" {
		opts.ServiceAddr = app.Conf.GetString("etcd.service_addr", app.addr)
	}
	if opts.ServiceID == "" {
		opts.ServiceID = app.Conf.GetString("etcd.service_id", "")
	}
	if opts.TTL == 0 {
		opts.TTL = app.Conf.GetInt64("etcd.ttl", 10)
	}
	if opts.Version == "" {
		opts.Version = app.Conf.GetString("etcd.version", "1.0.0")
	}
	if httpAddr := strings.TrimSpace(app.Conf.GetString("etcd.http_addr", "")); httpAddr != "" {
		if _, exists := opts.Metadata["http_addr"]; !exists {
			metadata := make(map[string]string, len(opts.Metadata)+1)
			for key, value := range opts.Metadata {
				metadata[key] = value
			}
			metadata["http_addr"] = httpAddr
			opts.Metadata = metadata
		}
	}
}

// EnableEtcdDiscovery 启用 etcd 服务发现
func (app *Core) EnableEtcdDiscovery(opts *etcd.Options) error {
	if opts == nil {
		opts = etcd.DefaultOptions()
	}
	app.applyEtcdDiscoveryDefaults(opts)

	// Serialize discovery initialization so concurrent clients share one owner.
	app.mutex.Lock()
	defer app.mutex.Unlock()
	if app.shutdownStarted {
		return errors.New("core: cannot enable etcd discovery after shutdown")
	}
	if app.EtcdDiscovery != nil {
		if !app.EtcdDiscovery.MatchesOptions(opts) {
			return ErrEtcdDiscoveryConfigurationChanged
		}
		return nil
	}
	discovery, err := newCoreEtcdDiscovery(opts)
	if err != nil {
		return err
	}
	app.EtcdDiscovery = discovery
	app.etcdDiscoveryCleanup = onceCleanup(func() { _ = closeCoreEtcdDiscovery(discovery) })
	return nil
}

func (app *Core) applyEtcdDiscoveryDefaults(opts *etcd.Options) {
	if opts.DialTimeout == 0 {
		opts.DialTimeout = time.Duration(app.Conf.GetInt64("etcd.dialTimeout", 5)) * time.Second
	}
	if opts.Username == "" {
		opts.Username = app.Conf.GetString("etcd.username", "")
	}
	if opts.Password == "" {
		opts.Password = app.Conf.GetString("etcd.password", "")
	}
	if opts.Namespace == "" {
		opts.Namespace = app.Conf.GetString("etcd.namespace", "")
	}
	if len(opts.Endpoints) == 0 {
		opts.Endpoints = app.Conf.GetStrings("etcd.endpoints", []string{"127.0.0.1:2379"})
	}
}

// OnShutdown 添加关闭钩子
func (app *Core) OnShutdown(fn func()) {
	if fn == nil {
		return
	}
	app.mutex.Lock()
	if app.shutdownStarted {
		app.mutex.Unlock()
		fn()
		return
	}
	app.shutdownHooks = append(app.shutdownHooks, fn)
	app.mutex.Unlock()
}

func onceCleanup(fn func()) func() {
	var once sync.Once
	return func() {
		once.Do(fn)
	}
}
