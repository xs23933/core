// Package bodylimit 限制请求体大小，防止恶意大 Body 导致 OOM。
//
// 使用方式:
//
//	app.Use(bodylimit.New(bodylimit.Config{MaxBytes: 10 << 20})) // 10MB
//
// 超过限制时返回 HTTP 413 Request Entity Too Large。
// 该中间件零开销 — 仅在超过 MaxBytes 时才触发错误响应。
package bodylimit

import (
	"fmt"
	"net/http"

	"github.com/xs23933/core/v3"
)

// Config 请求体大小限制配置
type Config struct {
	// MaxBytes 最大请求体字节数。0 表示不限制。
	MaxBytes int64
	// Skip 可选跳过函数，返回 true 时跳过该请求的大小检查
	Skip func(c core.Ctx) bool
}

var DefaultConfig = Config{
	MaxBytes: 10 << 20, // 10MB
}

// New 返回请求体大小限制中间件。
func New(conf ...Config) core.HandlerFunc {
	cfg := DefaultConfig
	if len(conf) > 0 {
		if conf[0].MaxBytes > 0 {
			cfg.MaxBytes = conf[0].MaxBytes
		}
		if conf[0].Skip != nil {
			cfg.Skip = conf[0].Skip
		}
	}

	return func(c core.Ctx) error {
		if cfg.Skip != nil && cfg.Skip(c) {
			return c.Next()
		}

		maxSize := cfg.MaxBytes
		if maxSize <= 0 {
			return c.Next()
		}

		cl := c.Request().Header.Get("Content-Length")
		if cl != "" {
			var size int64
			if _, err := fmt.Sscanf(cl, "%d", &size); err == nil && size > maxSize {
				return c.SendStatus(http.StatusRequestEntityTooLarge,
					"request body too large")
			}
		}

		c.Request().Body = http.MaxBytesReader(nil, c.Request().Body, maxSize)
		return c.Next()
	}
}
