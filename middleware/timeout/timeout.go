// Package timeout 提供请求超时控制中间件。
//
// 使用方式:
//
//	app.Use(timeout.New(timeout.Config{Timeout: 30 * time.Second}))
//
// 超时后，请求 context 被取消，handler 逻辑通过 c.Context().Done() 感知超时。
// 采用协作式取消：等待 handler 返回，避免请求 Ctx 被提前回收。
package timeout

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"time"

	"github.com/xs23933/core/v3"
)

// Config 超时配置
type Config struct {
	// Timeout 请求超时时间。非正值使用默认 30 秒。
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

		// Keep all request context access paths on the same deadline. Body and
		// parsed form state remain owned by this synchronous request chain.
		req := c.Request()
		originalContext := req.Context()
		*req = *req.WithContext(ctx)
		defer func() { *req = *req.WithContext(originalContext) }()
		originalWriter := c.Response()
		c.SetResponseWriter(&deadlineWriter{ResponseWriter: originalWriter, ctx: ctx})
		defer c.SetResponseWriter(originalWriter)

		err := c.Next()
		c.SetResponseWriter(originalWriter)
		if ctx.Err() != nil && !c.Response().Written() {
			// These headers describe the abandoned response, not the timeout body.
			for _, name := range []string{"Content-Length", "Content-Encoding", "Content-Range", "ETag", "Last-Modified", "Content-MD5", "Content-Digest", "Accept-Ranges", "Trailer"} {
				c.Response().Header().Del(name)
			}
			c.SetHeader("Content-Type", "text/plain; charset=utf-8")
			c.SetHeader("Cache-Control", "no-store")
			return c.SendStatus(cfg.StatusCode, cfg.Message)
		}
		return err
	}
}

// Reject writes after cancellation without racing the handler or its writer.
type deadlineWriter struct {
	core.ResponseWriter
	ctx context.Context
}

func (w *deadlineWriter) Write(p []byte) (int, error) {
	if w.ctx.Err() != nil {
		return 0, http.ErrHandlerTimeout
	}
	return w.ResponseWriter.Write(p)
}
func (w *deadlineWriter) WriteString(s string) (int, error) {
	if w.ctx.Err() != nil {
		return 0, http.ErrHandlerTimeout
	}
	return w.ResponseWriter.WriteString(s)
}
func (w *deadlineWriter) WriteHeader(code int) {
	if w.ctx.Err() == nil {
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *deadlineWriter) DoWriteHeader() {
	if w.ctx.Err() == nil {
		w.ResponseWriter.DoWriteHeader()
	}
}
func (w *deadlineWriter) Flush() {
	if w.ctx.Err() == nil {
		w.ResponseWriter.Flush()
	}
}
func (w *deadlineWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.ctx.Err() != nil {
		return nil, nil, http.ErrHandlerTimeout
	}
	return w.ResponseWriter.Hijack()
}
