/*
* grpc.go

yaml

etcd:

	endpoints:
	  - 192.168.31.5:2379
	username: ""
	password: ""
	service_name: "auth-service"
	service_addr: "192.168.31.2:8080"
	service_id: "auth-service-1"
	ttl: 10
	version: "1.0.0"

package main

import (

	"flag"
	"fmt"
	"os"
	"xauth/service"

	"github.com/xs23933/core/v3"

)

var configFile = flag.String("f", "config.yaml", "Configuration file path")

func main() {

		flag.Parse()
		conf := core.LoadConfigFile(*configFile)

		app := core.New(conf)

		// 启用 etcd 服务注册
		if err := app.EnableEtcdRegistry(nil); err != nil {
			fmt.Printf("Failed to enable etcd registry: %v\n", err)
			os.Exit(1)
		}

		service.NewUserService(app.GetGRPCServer())

		if err := app.Run(); err != nil {
			panic(err)
		}
	}
*/
package core

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/xs23933/core/v3/etcd"
	"github.com/xs23933/core/v3/reuseport"
	"google.golang.org/grpc"
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

	// 在单独的 goroutine 中运行 gRPC 服务器
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
		if app.isGRPCRequest(r) {
			app.grpcServer.ServeHTTP(w, r)
			return
		}

		originalHandler.ServeHTTP(w, r)
	})
}

// isGRPCRequest 判断是否为 gRPC 请求
func (app *Core) isGRPCRequest(r *http.Request) bool {
	// gRPC 请求特征：
	// 1. 必须是 HTTP/2
	// 2. Content-Type 以 application/grpc 开头
	return r.ProtoMajor == 2 &&
		strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc")
}

// shutdownGRPC 优雅关闭 gRPC 服务器
func (app *Core) shutdownGRPC() {
	if app.grpcServer != nil {
		Info("Shutting down gRPC server...")
		app.grpcServer.GracefulStop()
	}
}

/*
	EnableEtcdRegistry 启用 etcd 服务注册

----

yaml

etcd:

	endpoints:
	  - 192.168.31.5:2379
	username: ""
	password: ""
	service_name: "auth-service"
	service_addr: "192.168.31.2:8080"
	service_id: "auth-service-1"
	ttl: 10
	version: "1.0.0"

package main

import (

	"flag"
	"fmt"
	"os"
	"xauth/service"

	"github.com/xs23933/core/v3"

)

var configFile = flag.String("f", "config.yaml", "Configuration file path")

func main() {

		flag.Parse()
		conf := core.LoadConfigFile(*configFile)

		app := core.New(conf)

		// 启用 etcd 服务注册
		if err := app.EnableEtcdRegistry(nil); err != nil {
			fmt.Printf("Failed to enable etcd registry: %v\n", err)
			os.Exit(1)
		}

		service.NewUserService(app.GetGRPCServer())

		if err := app.Run(); err != nil {
			panic(err)
		}
	}
*/
func (app *Core) EnableEtcdRegistry(opts *etcd.Options) error {
	if opts == nil {
		opts = etcd.DefaultOptions()
	}

	// 从配置读取（仅当 opts 未设置时）
	opts.Endpoints = app.Conf.GetStrings("etcd.endpoints", []string{"127.0.0.1:2379"})
	opts.ServiceName = app.Conf.GetString("etcd.service_name", "")
	opts.ServiceAddr = app.Conf.GetString("etcd.service_addr", app.addr)
	opts.ServiceID = app.Conf.GetString("etcd.service_id", "")
	opts.TTL = app.Conf.GetInt64("etcd.ttl", 10)
	opts.Version = app.Conf.GetString("etcd.version", "1.0.0")

	registry, err := etcd.NewRegistry(opts)
	if err != nil {
		return err
	}

	app.etcdRegistry = registry

	// 注册服务
	if err := registry.Register(); err != nil {
		return err
	}

	// 在关闭时注销
	app.OnShutdown(func() {
		registry.Deregister()
	})

	return nil
}

// 集成到 Core
func (app *Core) EnableEtcdDiscovery(opts *etcd.Options) error {
	if opts == nil {
		opts = etcd.DefaultOptions()
	}

	// 从配置读取（仅当 opts 未设置时）
	if len(opts.Endpoints) == 0 {
		opts.Endpoints = app.Conf.GetStrings("etcd.endpoints", []string{"127.0.0.1:2379"})
	}

	discovery, err := etcd.NewDiscovery(opts)
	if err != nil {
		return err
	}

	app.EtcdDiscovery = discovery

	app.OnShutdown(func() {
		if app.EtcdDiscovery != nil {
			app.EtcdDiscovery.Close()
		}
	})
	return nil
}

// OnShutdown 添加关闭钩子
func (app *Core) OnShutdown(fn func()) {
	app.shutdownHooks = append(app.shutdownHooks, fn)
}
