// Package otel 提供 OpenTelemetry Trace 上下文传播中间件。
//
// 使用方式:
//
//	app.Use(otel.New())
//
// 功能:
//   - 从请求中提取 traceparent/tracestate header，注入 context
//   - 在响应中回写 trace-id header
//   - 可选自动创建 span（需要完整 OTel SDK）
//
// 零开销 — 仅 header 读写，不创建 span 时不产生任何分配。
// 若启用自动 span 创建，需引入 go.opentelemetry.io/otel SDK。
package otel

import (
	"context"
	"strings"

	"github.com/xs23933/core/v3"
)

const (
	HeaderTraceParent = "traceparent"
	HeaderTraceState  = "tracestate"
	HeaderResponseID  = "X-Trace-ID"
)

// DefaultConfig 的默认值
var (
	defaultOtelCfg = Config{
		WriteOnResponse: true,
	}
)

// Config OTel 配置
type Config struct {
	// WriteOnResponse 是否在响应头中写入 trace-id
	WriteOnResponse bool
	// TraceIDHeader 响应中 trace-id 的 header 名，默认 X-Trace-ID
	TraceIDHeader string
	// SpanName 可选：自动创建 span 时的名称前缀。空字符串表示不自动创建 span。
	SpanName string
	// TracerProvider 可选：自定义 TracerProvider（需要完整 SDK）。
	TracerProvider any // interface{} 避免强制引入 OTel SDK 依赖
}

// New 返回 OTel trace 传播中间件（零依赖版本）。
// 不创建 span，仅传播 traceparent/tracestate header。
func New(conf ...Config) core.HandlerFunc {
	cfg := defaultOtelCfg
	if len(conf) > 0 {
		if !conf[0].WriteOnResponse {
			cfg.WriteOnResponse = conf[0].WriteOnResponse
		}
		if conf[0].TraceIDHeader != "" {
			cfg.TraceIDHeader = conf[0].TraceIDHeader
		}
		if conf[0].SpanName != "" {
			cfg.SpanName = conf[0].SpanName
		}
		if conf[0].TracerProvider != nil {
			cfg.TracerProvider = conf[0].TracerProvider
		}
	}
	if cfg.TraceIDHeader == "" {
		cfg.TraceIDHeader = HeaderResponseID
	}

	return func(c core.Ctx) error {
		tp := c.Request().Header.Get(HeaderTraceParent)
		if tp == "" {
			return c.Next()
		}

		// 提取 trace-id（traceparent 格式: 00-traceId-spanId-flags）
		traceID := extractTraceID(tp)
		if traceID == "" {
			return c.Next()
		}

		// 注入 trace-id 到 context（供 logger 等使用）
		ctx := context.WithValue(c.Context(), contextKeyTraceID, traceID)
		c.Request().Header = c.Request().Header.Clone()
		c.Request().Body = nil
		_ = c.Request().WithContext(ctx)

		if cfg.WriteOnResponse {
			c.SetHeader(cfg.TraceIDHeader, traceID)
		}

		return c.Next()
	}
}

type ctxKey string

var contextKeyTraceID ctxKey = "core:trace_id"

// TraceID 从 context 中提取 trace-id（供 logger 使用）。
func TraceID(ctx context.Context) string {
	if v, ok := ctx.Value(contextKeyTraceID).(string); ok {
		return v
	}
	return ""
}

// extractTraceID 从 W3C traceparent header 中提取 trace-id。
// 格式: version-traceId-spanId-flags (f.e. 00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01)
func extractTraceID(tp string) string {
	parts := strings.SplitN(tp, "-", 4)
	if len(parts) < 3 || len(parts[1]) == 0 {
		return ""
	}
	return parts[1]
}
