---
name: core-middleware
description: Core Framework 中间件开发与组合，包含 gzip、requestid、cors、metrics、ratelimit、bodylimit、timeout、security、otel、etag、monitor 以及自定义中间件规范
tags: [go, core-framework, middleware, gzip, cors, metrics, ratelimit, requestid, bodylimit, timeout, security, otel, etag, monitor]
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
- "gzip" / "响应压缩" / "压缩内容类型"

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

所有未匹配显式路由的请求都会经过全局中间件，再由框架 fallback handler 返回 404；路径级中间件不会为未匹配路由执行。因此显式注册的 `app.Use(core.Logger())` 可记录 404；`core.New()` 默认不安装请求日志中间件。`core.IsRouteFallback(c)` 仅在未匹配 OPTIONS 预检时返回 `true`，普通 404 请求返回 `false`。

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
// 必须先启动采集；单独注册 Handler 不会启动后台任务。
app.Use(monitor.New(monitor.Config{
    Interval: 5 * time.Second,
    ResourceInterval: 30 * time.Second,
    DiskPath: "/var/lib/my-service",
}))
app.GET("/sys/stats", monitor.StatsHandler())
app.GET("/metrics", monitor.PrometheusHandler())
// 只读缓存，不触发文件或系统调用；可空字段只读使用。
stats := monitor.Snapshot()
_ = stats
```

- 首次安装立即采集，后续固定间隔缓存；重复安装复用首次配置和同一个进程级循环。`Interval` 最小 1 秒，`ResourceInterval` 不短于常规间隔，默认 30 秒。循环在进程退出时结束。
- `cpu_percent` 为 Linux 进程累计 CPU 的采样差分，单核 100%，多核可超过 100；首次采样与无效差分以 `cpu_available=false` 表示。`host_cpu_percent` 为可见内核全部 CPU 忙碌比例，不含 idle/iowait，不代表容器配额使用率。
- `max_fds` 是当前进程软限制，`max_fds_hard` 是硬限制，`-1` 为无限制；`fd_available=false` 时不得显示为健康零值。FD 每轮最多计数 131072 项，超预算不可用。
- 保留 Go 内存/GC/goroutine 等原 JSON 字段。新增 RSS/线程、主机总内存/可用内存/Swap、load、PSI、磁盘容量/inode、cgroup v2 当前值与有限限制；可空字段遇到未知或不支持返回 `null`。非 Linux 仅提供 Go 运行时指标，不执行外部命令探测。PSI `*_some_avg10` 单位为百分比。
- `DiskPath` 必须选择本地数据目录；只对所在文件系统执行 statfs，不扫描文件或其它挂载。跨服务消息/前端投影按白名单排除 `disk_path`。
- cgroup v2 有限配额取当前与最多 64 层可见祖先的最小值；无限制或不可读取时 limit 为 `null`，不支持 cgroup v1，也无法越过挂载根观察祖先。cgroup memory/pids 当前值包含当前组全部进程。 `cgroup_memory_available_bytes` 与 `cgroup_pids_available` 取各可见祖先自身 limit-current 的最小值，以包含同父组其它进程占用；受限祖先读取失败时为 `null`，耗尽时为 0。不要用叶组 current 除以继承 limit 推断祖先余量。
- `sampled_at`、`resource_sampled_at`、`disk_sampled_at` 表达采样新鲜度；后者仅在成功读到磁盘值时存在。`host_id` 是 Linux boot_id 的 SHA-256，同一内核启动下聚合，重启改变，失败为空；不传原始主机名、IP、boot_id。 `disk_id` 是同一主机启动身份与文件系统设备号的 SHA-256；不同磁盘分别聚合，未取得主机身份时为空，不包含监控路径。
- Prometheus 省略未知 CPU、FD 与新增资源序列，并提供 CPU/FD 可用性 gauge。不要将采集失效、缓存过期或缺字段解释成正常。

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

## 16. gzip 响应压缩

```go
import "github.com/xs23933/core/v3/middleware/gzip"

app.Use(gzip.New())
// 或覆盖配置：非 nil 的 ContentTypes 替换整个默认列表。
app.Use(gzip.New(gzip.Config{
    ContentTypes: []string{"application/json", "text/html", "text/css", "text/javascript", "application/javascript"},
    MinLength: 1024,
    Skip: func(c core.Ctx) bool { return c.Path() == "/private/export" },
}))
```

上述两种注册方式选择一种，不要重复安装。默认类型为 `application/json`、`text/html`、`text/css`、`text/javascript`、`application/javascript`、`text/plain`、`application/xml`、`text/xml`、`image/svg+xml`。PNG/WebP/JPEG 默认不压缩。

- `ContentTypes: nil` 保留默认；非 nil 空切片禁用压缩。MIME 匹配忽略大小写和 charset 等参数。
- `Level: 0` 使用标准库默认压缩级别；其它值使用 `compress/gzip` 级别（如 BestSpeed、BestCompression、HuffmanOnly）。无效级别或 MIME 会使中间件返回配置错误。
- `MinLength: 0` 使用默认 1024 字节；负数取消阈值。只缓冲不足阈值的前缀，之后流式发送，不缓冲整份文件。
- 协商 `Accept-Encoding`，尊重 `gzip;q=0` 并设置 `Vary: Accept-Encoding`；压缩后删除原始长度、Accept-Ranges、内容摘要，强 ETag 转弱 ETag。
- HEAD/CONNECT/Upgrade/Range、204/304、SSE、已有 Content-Encoding、`Cache-Control: no-transform` 均跳过压缩。
- 提前 Flush/DoWriteHeader 会提交原始响应，不等待阈值；已开始压缩后 Flush 同步刷新 gzip 与底层响应。SSE 始终直接发送。
- 当前响应已提交后返回错误，Core 只记录错误，不追加明文或重写状态，避免损坏压缩流。未提交前保持原有错误处理。
- 响应包装使用 `c.SetResponseWriter(wrapped)`，中间件退出时必须恢复原 writer。压缩池在请求结束时清除对请求 writer 的引用。
- 静态资源的浏览器/CDN 长缓存独立由 [core-static.md](core-static.md) 的 `StaticConfig` 控制。

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
