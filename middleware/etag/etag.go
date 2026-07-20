// Package etag 提供基于请求路径/查询参数的 ETag HTTP 缓存中间件。
//
// 使用方式:
//
//	app.Use(etag.New())                     // 全局 ETag
//	app.GET("/api/data", h, etag.New())     // 单路由 ETag
//
// 功能:
//   - 基于请求 URL 生成 ETag
//   - 自动处理 If-None-Match → 304 Not Modified
//   - 零分配 — 仅在缓存命中时返回 304
package etag

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/xs23933/core/v3"
)

// Config ETag 配置
type Config struct {
	// Weak 是否为弱 ETag（前缀 W/）
	Weak bool
	// HeaderName 自定义 ETag 头字段，默认 ETag
	HeaderName string
	// Skip 可选跳过函数，返回 true 时跳过 ETag 处理
	Skip func(c core.Ctx) bool
}

var DefaultConfig = Config{
	Weak:       true,
	HeaderName: "ETag",
}

// New 返回 ETag 中间件。
func New(conf ...Config) core.HandlerFunc {
	cfg := DefaultConfig
	if len(conf) > 0 {
		if conf[0].Weak {
			cfg.Weak = conf[0].Weak
		}
		if conf[0].HeaderName != "" {
			cfg.HeaderName = conf[0].HeaderName
		}
		if conf[0].Skip != nil {
			cfg.Skip = conf[0].Skip
		}
	}

	return func(c core.Ctx) error {
		if cfg.Skip != nil && cfg.Skip(c) {
			return c.Next()
		}

		method := c.Method()
		if method != "GET" && method != "HEAD" {
			return c.Next()
		}

		// 基于请求 URL 生成 ETag
		hash := sha256.Sum256([]byte(c.Request().URL.String()))
		tag := hex.EncodeToString(hash[:])
		if cfg.Weak {
			tag = "W/\"" + tag + "\""
		} else {
			tag = "\"" + tag + "\""
		}

		// 检查 If-None-Match
		ifMatch := c.Request().Header.Get("If-None-Match")
		if ifMatch != "" && matchETag(tag, ifMatch) {
			c.Status(http.StatusNotModified)
			c.SetHeader(cfg.HeaderName, tag)
			return c.SendStatus(http.StatusNotModified)
		}

		// 设置 ETag header 并继续处理
		c.SetHeader(cfg.HeaderName, tag)
		return c.Next()
	}
}

// matchETag 判断 client ETag 列表是否匹配 server ETag。
func matchETag(serverTag, clientHeader string) bool {
	tags := strings.Split(clientHeader, ",")
	for _, t := range tags {
		if strings.TrimSpace(t) == serverTag {
			return true
		}
	}
	return false
}
