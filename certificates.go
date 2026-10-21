package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/xs23933/core/v3/certificate"
	"golang.org/x/net/http2"
)

// UseCertificateManager 将动态证书接入已有 HTTPS listener，并托管可选的 HTTP-01 listener。
// 必须在 Run/Serve 前调用；httpAddress 为空时由部署层转发挑战到 manager.HTTPHandler。
// 成功后由 Core 在关闭时释放 manager；证书状态和域名归属仍由调用方管理。
func (app *Core) UseCertificateManager(manager *certificate.Manager, httpAddress string) error {
	if manager == nil {
		return errors.New("certificate manager required")
	}
	if app.dynamicCertificateManager != nil || app.certManager != nil || app.certMagicEnabled || app.certMagicConfig.Email != "" || app.TLSConfig != nil {
		return errors.New("certificate manager conflicts with existing TLS configuration")
	}
	var ln net.Listener
	if httpAddress != "" {
		var err error
		ln, err = net.Listen("tcp", httpAddress)
		if err != nil {
			return fmt.Errorf("certificate HTTP listener: %w", err)
		}
	}
	app.dynamicCertificateManager = manager
	app.TLSConfig = manager.TLSConfig()
	app.OnShutdown(func() {
		if err := manager.Close(); err != nil {
			Erro("certificate manager shutdown failed: %v", err)
		}
	})
	if ln != nil {
		server := &http.Server{Handler: manager.HTTPHandler(nil), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute}
		app.dynamicCertificateHTTP = server
		app.OnShutdown(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(ctx); err != nil {
				Erro("certificate HTTP shutdown failed: %v", err)
			}
		})
		go func() {
			if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				Erro("certificate HTTP server failed: %v", err)
			}
		}()
	}
	return nil
}
func (app *Core) prepareDynamicCertificateTLS() error {
	return http2.ConfigureServer(app.Server, &http2.Server{})
}
