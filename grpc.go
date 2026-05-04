// grpc.go
package core

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/xs23933/core/v3/reuseport"
	"google.golang.org/grpc"
)

// EnableGRPC 启用 gRPC 支持
func (app *Core) EnableGRPC(addr ...string) *Core {
	app.grpcEnabled = true
	if len(addr) > 0 {
		app.grpcAddr = addr[0]
	} else if app.grpcAddr == "" {
		app.grpcAddr = ":50051"
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

// shutdownGRPC 优雅关闭 gRPC 服务器
func (app *Core) shutdownGRPC() {
	if app.grpcServer != nil {
		Info("Shutting down gRPC server...")
		app.grpcServer.GracefulStop()
	}
}
