---
name: core-middleware
description: Core Framework 中间件开发与组合，包含 requestid、cors、metrics、ratelimit 以及自定义中间件规范
tags: [go, core-framework, middleware, cors, metrics, ratelimit, requestid]
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

## 6. 生成代码时避免

- 不要在中间件里吞掉 `c.Next()` 返回错误。
- 不要使用 `app.Use("/api/*", middleware)` 表示路径前缀；应使用 `app.Use("/api", middleware)`。
- 不要在限流失败时返回 200。
- 不要把 metrics、鉴权、限流都写进一个巨大中间件。
- 不要在中间件里进行阻塞式慢 IO。
