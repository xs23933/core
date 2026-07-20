// Package security 提供安全 HTTP 响应头中间件，设置 HSTS、CSP、X-Content-Type-Options 等。
//
// 使用方式:
//
//	app.Use(security.New(security.DefaultConfig))
//
// 零开销 — 仅在首次响应时注入一组静态 header。
package security

import "github.com/xs23933/core/v3"

// Config 安全头配置
type Config struct {
	// HSTS 设置 Strict-Transport-Security，如 "max-age=31536000; includeSubDomains"。空字符串表示不设置。
	HSTS string
	// XFrameOptions 设置 X-Frame-Options，如 "DENY"、"SAMEORIGIN"。空字符串表示不设置。
	XFrameOptions string
	// ContentTypeOptions 设置 X-Content-Type-Options，通常 "nosniff"。空字符串表示不设置。
	ContentTypeOptions string
	// XSSProtection 设置 X-XSS-Protection，如 "1; mode=block"。空字符串表示不设置。
	XSSProtection string
	// ReferrerPolicy 设置 Referrer-Policy，如 "strict-origin-when-cross-origin"。
	ReferrerPolicy string
	// PermissionsPolicy 设置 Permissions-Policy，如 "camera=(), microphone=()"。
	PermissionsPolicy string
	// Skip 可选跳过函数
	Skip func(c core.Ctx) bool
}

// DefaultConfig 生产环境推荐的安全头配置。
var DefaultConfig = Config{
	HSTS:               "max-age=31536000; includeSubDomains",
	XFrameOptions:      "DENY",
	ContentTypeOptions: "nosniff",
	XSSProtection:      "1; mode=block",
	ReferrerPolicy:     "strict-origin-when-cross-origin",
}

// StrictConfig 比 DefaultConfig 更严格，增加 PermissionsPolicy。
var StrictConfig = Config{
	HSTS:               "max-age=63072000; includeSubDomains; preload",
	XFrameOptions:      "DENY",
	ContentTypeOptions: "nosniff",
	XSSProtection:      "0",
	ReferrerPolicy:     "no-referrer",
	PermissionsPolicy:  "camera=(), microphone=(), geolocation=()",
}

// New 返回安全 HTTP 头中间件。
func New(conf ...Config) core.HandlerFunc {
	cfg := DefaultConfig
	if len(conf) > 0 {
		merged := conf[0]
		if merged.HSTS != "" {
			cfg.HSTS = merged.HSTS
		}
		if merged.XFrameOptions != "" {
			cfg.XFrameOptions = merged.XFrameOptions
		}
		if merged.ContentTypeOptions != "" {
			cfg.ContentTypeOptions = merged.ContentTypeOptions
		}
		if merged.XSSProtection != "" {
			cfg.XSSProtection = merged.XSSProtection
		}
		if merged.ReferrerPolicy != "" {
			cfg.ReferrerPolicy = merged.ReferrerPolicy
		}
		if merged.PermissionsPolicy != "" {
			cfg.PermissionsPolicy = merged.PermissionsPolicy
		}
		if merged.Skip != nil {
			cfg.Skip = merged.Skip
		}
	}

	return func(c core.Ctx) error {
		if cfg.Skip != nil && cfg.Skip(c) {
			return c.Next()
		}

		if cfg.HSTS != "" {
			c.SetHeader("Strict-Transport-Security", cfg.HSTS)
		}
		if cfg.XFrameOptions != "" {
			c.SetHeader("X-Frame-Options", cfg.XFrameOptions)
		}
		if cfg.ContentTypeOptions != "" {
			c.SetHeader("X-Content-Type-Options", cfg.ContentTypeOptions)
		}
		if cfg.XSSProtection != "" {
			c.SetHeader("X-XSS-Protection", cfg.XSSProtection)
		}
		if cfg.ReferrerPolicy != "" {
			c.SetHeader("Referrer-Policy", cfg.ReferrerPolicy)
		}
		if cfg.PermissionsPolicy != "" {
			c.SetHeader("Permissions-Policy", cfg.PermissionsPolicy)
		}

		return c.Next()
	}
}
