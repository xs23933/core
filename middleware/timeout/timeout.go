// Package timeout 提供请求超时控制中间件。
//
// 使用方式:
//
//	app.Use(timeout.New(timeout.Config{Timeout: 30 * time.Second}))
//
// 超时后，请求 context 被取消，handler 逻辑通过 c.Context().Done() 感知超时。
// 零开销 — context.WithTimeout 仅在超时时触发 cancel。
package timeout

import (
	"context"
	"net/http"
	"time"

	"github.com/xs23933/core/v3"
)

// Config 超时配置
type Config struct {
	// Timeout 请求超时时间。0 表示不设置超时。
	Timeout time.Duration
	// StatusCode 超时后返回的 HTTP 状态码，默认 503
	StatusCode int
	// Message 超时后返回的消息
	Message string
	// Skip 可选跳过函数
	Skip func(c core.Ctx) bool
}

var DefaultConfig = Config{
	Timeout:    30 * time.Second,
	StatusCode: http.StatusServiceUnavailable,
	Message:    "request timeout",
}

// New 返回请求超时中间件。
func New(conf ...Config) core.HandlerFunc {
	cfg := DefaultConfig
	if len(conf) > 0 {
		if conf[0].Timeout > 0 {
			cfg.Timeout = conf[0].Timeout
		}
		if conf[0].StatusCode > 0 {
			cfg.StatusCode = conf[0].StatusCode
		}
		if conf[0].Message != "" {
			cfg.Message = conf[0].Message
		}
		if conf[0].Skip != nil {
			cfg.Skip = conf[0].Skip
		}
	}

	return func(c core.Ctx) error {
		if cfg.Skip != nil && cfg.Skip(c) {
			return c.Next()
		}

		ctx, cancel := context.WithTimeout(c.Context(), cfg.Timeout)
		defer cancel()

		c.Request().Header = c.Request().Header.Clone()
		c.Request().Body = nil // read in handler
		req := c.Request().WithContext(ctx)
		c.Request().Header = req.Header
		c.Request().Body = c.Request().Body

		done := make(chan struct{})
		var handlerErr error

		go func() {
			handlerErr = c.Next()
			close(done)
		}()

		select {
		case <-done:
			return handlerErr
		case <-ctx.Done():
			return c.SendStatus(cfg.StatusCode, cfg.Message)
		}
	}
}
