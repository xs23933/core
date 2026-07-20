---
name: core-middleware
description: Core Framework 中间件开发与组合，包含 requestid、cors、metrics、ratelimit、bodylimit、timeout、security、otel、etag、monitor 以及自定义中间件规范
tags: [go, core-framework, middleware, cors, metrics, ratelimit, requestid, bodylimit, timeout, security, otel, etag, monitor]
---

# Core Middleware 技能

## 触发条件

- "写中间件"
- "全局鉴权"
- "路径级中间件"
- "app.Use 路径前缀"
- "加 request id"
- "加 metrics"
- "限流"
- "统一日志/耗时"
- "请求体大小限制" / "安全头" / "超时" / "ETag" / "健康检查" / "OpenTelemetry" / "监控"

## 1. 中间件签名

Core 中间件统一签名：

```go
func(c core.Ctx) error
```

必须在需要放行时 `return c.Next()`。

## 2. 常用组合

```go
import (
    "time"

    "github.com/xs23933/core/v3"
    "github.com/xs23933/core/v3/middleware/cors"
    "github.com/xs23933/core/v3/middleware/metrics"
    "github.com/xs23933/core/v3/middleware/ratelimit"
    "github.com/xs23933/core/v3/middleware/requestid"
)

app := core.New()

app.Use(requestid.New())
app.Use(cors.New())

m, mw := metrics.New()
app.Use(mw)
metrics.Mount(app, "/metrics", m)

app.Use(ratelimit.New(ratelimit.Config{
    Max:    300,
    Window: time.Minute,
}))
```

`cors.New(config...)` 是纯中间件构造器。未匹配显式路由的 OPTIONS 请求会进入全局中间件，由 CORS 处理合法预检；显式 OPTIONS 路由仍优先。精确 Origin 会校验 scheme、host 和显式端口，`*.example.com` 只匹配子域。`AllowHeaders` 为空时回显 `Access-Control-Request-Headers`，而 `AllowOrigins: "*"` 与凭据模式不能组合。旧的 `cors.New(app, ...)` 调用迁移到 `cors.New(...)`；`cors.NewWithApp(app, ...)` 仅用于临时兼容。

## 3. 路径级中间件

路径级中间件直接使用不带通配符的路径前缀：

```go
app.Use("/api", authMiddleware.Handle)
app.Use("/internal", internalAuthMiddleware.Handle)
```

`app.Use("/api", middleware)` 会作用于 `/api` 及其所有子路径。不要写 `app.Use("/api/*", middleware)`；`*` 是路由 catch-all 段，不是中间件前缀标记。

全局中间件、同一路径中间件和嵌套路径中间件的执行顺序规则：

| 类型 | 执行顺序 |
| --- | --- |
| 全局 `app.Use(middleware)` | 后注册的前置执行；按期望顺序反向注册 |
| 同一路径 `app.Use("/api", ...)` | 按注册顺序执行 |
| 嵌套路径 | 父路径先于子路径执行 |
| 路由 middleware 与 handler | 在全局和路径级中间件之后执行 |

```go
// 执行顺序：requestID -> accessLog -> handler。
app.Use(accessLogMiddleware)
app.Use(requestIDMiddleware)

// GET /api/internal/status：auth -> internalAuth -> handler。
app.Use("/api", authMiddleware.Handle)
app.Use("/api/internal", internalAuthMiddleware.Handle)
app.GET("/api/internal/status", statusHandler)
```

## 4. 自定义中间件模板

```go
func AccessLog() core.HandlerFunc {
    return func(c core.Ctx) error {
        start := time.Now()
        err := c.Next()
        core.Info("%s %s status=%d cost=%s",
            c.Method(), c.Path(), c.GetStatus(), time.Since(start))
        return err
    }
}
```

## 5. 限流建议

- 默认按 `method + path + ip` 作为限流 key。
- 用户级限流请用 `KeyFunc`，例如取 `user_id`。
- 多实例部署优先 Redis 后端，避免每台机器独立计数。
- 默认算法是固定窗口；需要更平滑的限制时使用 `ratelimit.SlidingWindow`。

```go
app.Use(ratelimit.New(ratelimit.Config{
    Max:       120,
    Window:    time.Minute,
    Algorithm: ratelimit.SlidingWindow,
    KeyFunc: func(c core.Ctx) string {
        return c.GetString("user_id", "anonymous")
    },
    Redis: core.RConn(),
}))
```

## 6. 请求体大小限制 (bodylimit)

```go
app.Use(bodylimit.New(bodylimit.Config{
    MaxBytes: 10 << 20, // 10MB
}))
```

- 超过限制返回 HTTP `413 Request Entity Too Large`。
- 先读 Content-Length 快速拒绝，再包装 ReadCloser 限流。
- 可通过 `Skip` 按路径排除（如文件上传路由）。

## 7. 请求超时 (timeout)

```go
app.Use(timeout.New(timeout.Config{
    Timeout: 30 * time.Second,
}))
```

- 超时后返回 HTTP 503，handler 通过 `c.Context().Done()` 感知取消。
- 对健康检查等路径建议用 `Skip` 排除。

## 8. 安全响应头 (security)

```go
app.Use(security.New())                          // 推荐默认配置
app.Use(security.New(security.StrictConfig))     // 严格配置（含 PermissionsPolicy）
```

设置的 header：`Strict-Transport-Security`、`X-Frame-Options: DENY`、`X-Content-Type-Options: nosniff`、`X-XSS-Protection`、`Referrer-Policy`、`Permissions-Policy`。

## 9. OpenTelemetry Trace 传播 (otel)

```go
app.Use(otel.New())
```

- 从 `traceparent` / `tracestate` header 提取 trace-id，注入 context。
- 通过 `otel.TraceID(ctx)` 在 logger 中使用。
- 不强制依赖 OTel SDK，不创建 span（零开销）。

## 10. ETag 响应缓存 (etag)

```go
app.Use(etag.New())
```

- 基于请求 URL 生成 SHA-256 ETag。
- 自动处理 `If-None-Match` → `304 Not Modified`。
- 仅对 GET/HEAD 生效。

## 11. 服务器监控 (monitor)

```go
// 方式 1：仅 Prometheus endpoint
app.Get("/metrics", monitor.PrometheusHandler())

// 方式 2：后台采集 + JSON endpoint
app.Use(monitor.New(monitor.Config{Interval: 5 * time.Second}))
app.Get("/sys/stats", monitor.StatsHandler())
```

暴露指标：CPU 使用率、内存 (Alloc/HeapAlloc/Sys)、GC 次数/暂停时间、Goroutine 数量、打开文件描述符数、连接数、累计请求数。
支持 JSON (`/sys/stats`) 和 Prometheus (`/metrics`) 两种输出格式。

## 12. 健康检查探针

```go
// 注册探针（在启动时，DB/Redis 初始化后）
core.AddHealthProbe(func() error {
    sqlDB, err := core.DB().DB()
    if err != nil { return err }
    return sqlDB.Ping()
})

// 执行全量检查
result := core.CheckHealth()
```

内建 `/health` 端点（204 No Content）；可结合 `CheckHealth()` 实现自定义 `/healthz` 端点。

## 13. 环境变量配置

YAML 配置文件中使用 `${ENV:VAR}` 或 `${ENV:VAR:default}` 引用环境变量：

```yaml
# config.yaml
database:
  dsn: "mysql://${ENV:DB_USER}:${ENV:DB_PASS:default}@tcp(${ENV:DB_HOST:127.0.0.1}:3306)/mydb"
redis:
  addr: "${ENV:REDIS_ADDR:127.0.0.1:6379}"
```

在 `LoadConfigFile()` 启动时自动替换，运行时零开销。

## 14. 请求体验证便捷方法

```go
func (h *Handler) PostCreate(c core.Ctx) error {
    var req CreateUserReq
    if err := c.ReadBodyAndValidate(&req); err != nil {
        return err // 400/422 + 友好错误信息
    }
    // req 已解析并通过验证
}
```

`ReadBodyAndValidate()` 组合 `ReadBody` + `Validate`，解析失败返 400，验证失败返 422 并附带字段级错误信息。

## 15. 优雅关闭带超时

```go
// 替换默认的 app.Start()，关闭时最多等待 30s 排空连接
if err := app.ShutdownWithTimeout(30 * time.Second); err != nil {
    log.Fatal("shutdown failed:", err)
}
```

## 生成代码时避免

- 不要在中间件里吞掉 `c.Next()` 返回错误。
- 不要使用 `app.Use("/api/*", middleware)` 表示路径前缀；应使用 `app.Use("/api", middleware)`。
- 不要在限流失败时返回 200。
- 不要把 metrics、鉴权、限流都写进一个巨大中间件。
- 不要在中间件里进行阻塞式慢 IO。
- `bodylimit` 和 `timeout` 建议放在中间件链首位。
- `security` 和 `requestid` 紧跟其后。
- `metrics` 和 `ratelimit` 在鉴权中间件之后。
- `etag` 对动态 API 禁止使用（使用 `Skip` 排除 `POST`/`PUT`/`DELETE` 路由）。
- 限流中间件已采用 16 分片锁优化，多核场景下线性扩展。
