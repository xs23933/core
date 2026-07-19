// grpc.go
package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	unaryInterceptors := []grpc.UnaryServerInterceptor{errorWrapInterceptor}
	serverOptions := make([]grpc.ServerOption, 0, 3)
	if app.grpcConfig != nil {
		unaryInterceptors = append(unaryInterceptors, app.grpcConfig.UnaryInterceptors...)
		if len(app.grpcConfig.StreamInterceptors) > 0 {
			serverOptions = append(serverOptions, grpc.ChainStreamInterceptor(app.grpcConfig.StreamInterceptors...))
		}
		if app.grpcConfig.TransportCredentials != nil {
			serverOptions = append(serverOptions, grpc.Creds(app.grpcConfig.TransportCredentials))
		}
	}
	serverOptions = append(serverOptions, grpc.ChainUnaryInterceptor(unaryInterceptors...))
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
	if app.grpcServer != nil {
		Info("Shutting down gRPC server...")
		app.grpcServer.GracefulStop()
	}
}

// EnableEtcdRegistry 启用 etcd 服务注册
// 当 opts 为 nil 时，从配置文件读取；当 opts 已设置字段时，保留用户值
func (app *Core) EnableEtcdRegistry(opts *etcd.Options) error {
	if opts == nil {
		opts = etcd.DefaultOptions()
	}
	app.applyEtcdRegistryDefaults(opts)

	registry, err := etcd.NewRegistry(opts)
	if err != nil {
		return err
	}

	discoveryOpts := &etcd.Options{
		Namespace:   opts.Namespace,
		Endpoints:   append([]string(nil), opts.Endpoints...),
		Username:    opts.Username,
		Password:    opts.Password,
		DialTimeout: opts.DialTimeout,
	}
	if err := app.EnableEtcdDiscovery(discoveryOpts); err != nil {
		return err
	}

	app.etcdRegistry = registry

	if err := registry.Register(); err != nil {
		return err
	}

	app.OnShutdown(func() {
		registry.Deregister()
	})
	reflection.Register(app.GetGRPCServer())

	return nil
}

func (app *Core) applyEtcdRegistryDefaults(opts *etcd.Options) {
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
}

// EnableEtcdDiscovery 启用 etcd 服务发现
func (app *Core) EnableEtcdDiscovery(opts *etcd.Options) error {
	if opts == nil {
		opts = etcd.DefaultOptions()
	}
	app.applyEtcdDiscoveryDefaults(opts)

	discovery, err := etcd.NewDiscovery(opts)
	if err != nil {
		return err
	}

	app.EtcdDiscovery = discovery

	app.OnShutdown(func() {
		discovery.Close()
	})

	return nil
}

func (app *Core) applyEtcdDiscoveryDefaults(opts *etcd.Options) {
	if opts.Namespace == "" {
		opts.Namespace = app.Conf.GetString("etcd.namespace", "")
	}
	if len(opts.Endpoints) == 0 {
		opts.Endpoints = app.Conf.GetStrings("etcd.endpoints", []string{"127.0.0.1:2379"})
	}
}

// OnShutdown 添加关闭钩子
func (app *Core) OnShutdown(fn func()) {
	app.shutdownHooks = append(app.shutdownHooks, fn)
}
