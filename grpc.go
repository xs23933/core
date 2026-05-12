// grpc.go
package core

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/xs23933/core/v3/etcd"
	"github.com/xs23933/core/v3/reuseport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

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
	if app.grpcServer == nil {
		app.grpcServer = grpc.NewServer()
		app.grpcEnabled = true
	}
	if app.grpcAddr == "" {
		app.grpcAddr = app.addr
	}
	reflection.Register(app.grpcServer)
	return app.grpcServer
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

	registry, err := etcd.NewRegistry(opts)
	if err != nil {
		return err
	}

	app.etcdRegistry = registry

	if err := registry.Register(); err != nil {
		return err
	}

	app.OnShutdown(func() {
		registry.Deregister()
	})

	return nil
}

// EnableEtcdDiscovery 启用 etcd 服务发现
func (app *Core) EnableEtcdDiscovery(opts *etcd.Options) error {
	if opts == nil {
		opts = etcd.DefaultOptions()
	}

	if len(opts.Endpoints) == 0 {
		opts.Endpoints = app.Conf.GetStrings("etcd.endpoints", []string{"127.0.0.1:2379"})
	}

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

// OnShutdown 添加关闭钩子
func (app *Core) OnShutdown(fn func()) {
	app.shutdownHooks = append(app.shutdownHooks, fn)
}
