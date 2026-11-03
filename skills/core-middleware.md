---
name: core-middleware
description: Core Framework 中间件开发与组合，包含 gzip、zstd、requestid、cors、metrics、ratelimit、bodylimit、timeout、security、otel、etag、monitor 以及自定义中间件规范
tags: [go, core-framework, middleware, gzip, zstd, cors, metrics, ratelimit, requestid, bodylimit, timeout, security, otel, etag, monitor]
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
- "gzip" / "zstd" / "响应压缩" / "压缩内容类型"

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

`middleware/timeout` 采用协作式取消：默认 30 秒（Timeout 非正值仍用默认），deadline 同时作用于 `c.Context()`、`c.Ctx()`、`c.Request().Context()` 和 `c.Stream()`。保留 JSON/XML/multipart 正文。取消后的 writer 写入返回 `http.ErrHandlerTimeout`；中间件等待 handler 返回后才恢复 request/writer、回收 Ctx。未提交响应时清除旧 Content-Length、Content-Encoding、validator 等正文相关头，返回配置的超时状态（默认 503），使用 text/plain 和 no-store；已提交响应保留原状态与正文，不追加超时消息。handler 必须把 context 传给下游并主动退出；不响应取消的 handler 会继续占用请求，**不保证立即返回 503**。这是对原先不安全的 goroutine 抢先返回行为的兼容调整；流式连接建议用 Skip 排除。
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

`middleware/etag` 对 GET 的普通 200 响应正文计算 SHA-256 ETag（默认弱 tag），每次仍执行 handler，内容改变会改变 tag；优先保留 handler 的显式 validator。GET/HEAD 按弱比较支持 `If-None-Match` 列表和 `*`，命中提交无正文 304。HEAD 不用空正文生成 tag，只使用显式 validator。最多缓冲 1 MiB；超出、SSE、Flush/DoWriteHeader/Hijack 转为直通，不自动计算 tag；Range、Upgrade、非 GET/HEAD 跳过，no-store 和非 200 不生成或比较 tag。静态文件继续复用标准库 Last-Modified/If-Modified-Since 等处理。不要用于需要流式传输或敏感动态 API；用 Skip 排除，etag 不提供 public 缓存或授权策略。普通正文写出不提前强制提交底层 writer，gzip 在 etag 前后均可压缩；Hijack 前已接受的缓冲正文先发送，未写正文的协议升级不预发状态。原 URL hash tag 已废弃，旧 tag 首次请求会重新获取内容。

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
- HEAD/CONNECT/Upgrade/Range、204/304、默认 SSE、已有 Content-Encoding、`Cache-Control: no-transform` 均跳过压缩。
- 提前 Flush/DoWriteHeader 会提交原始响应，不等待阈值；已开始压缩后 Flush 同步刷新 gzip 与底层响应。SSE 始终直接发送。
- 当前响应已提交后返回错误，Core 只记录错误，不追加明文或重写状态，避免损坏压缩流。未提交前保持原有错误处理。
- 响应包装使用 `c.SetResponseWriter(wrapped)`，中间件退出时必须恢复原 writer。压缩池在请求结束时清除对请求 writer 的引用。
- 静态资源的浏览器/CDN 长缓存独立由 [core-static.md](core-static.md) 的 `StaticConfig` 控制。


## 17. zstd 响应压缩

```go
import "github.com/xs23933/core/v3/middleware/zstd"

app.Use(zstd.New())
// 或自定义配置：
// app.Use(zstd.New(zstd.Config{MinLength: 2048}))
```

`middleware/zstd` 与 gzip 使用相同的 `New(config ...Config)` 形式，支持 `ContentTypes`、`Level`、`MinLength`、`EnableSSE`、`Skip`；默认 MIME 列表、1024 字节阈值和跳过规则同 gzip。`Level` 使用 `github.com/klauspost/compress/zstd.EncoderLevel`，0 使用 `SpeedDefault`，可选 `SpeedFastest`、`SpeedDefault`、`SpeedBetterCompression`、`SpeedBestCompression`；非法级别或 MIME 返回配置错误。复用已有 klauspost/compress 依赖，单个 encoder 使用并发度 1，请求间池化复用并清除请求 writer 引用。

客户端通过 `Accept-Encoding: zstd`（支持 q 值、通配符和显式 `zstd;q=0` 拒绝）协商，响应设置 `Content-Encoding: zstd` 和 `Vary: Accept-Encoding`。不足阈值时保留原文，达到后流式压缩；Flush 刷新压缩流，普通响应压缩前提前 Flush/DoWriteHeader 则保持原文；显式启用的 SSE 会先建立编码器再提交/刷新。HEAD/CONNECT/Upgrade/Range、204/304、默认 SSE、已有编码和 no-transform 跳过；长度、摘要、ETag 的处理同 gzip，未协商压缩时保留静态 ReaderFrom 传输路径。独立入口选择 gzip 或 zstd 之一安装；需要自动协商二者时使用 `middleware/compress`。叠加不提供跨算法 q 值优先选择，只由先处理正文的中间件决定编码，已有编码阻止重复压缩。

## 18. 统一响应压缩（zstd、Brotli、gzip）

```go
import "github.com/xs23933/core/v3/middleware/compress"

app.Use(compress.New())
// 可选：共享 MIME/阈值，分别配置算法级别。
// app.Use(compress.New(compress.Config{MinLength: 2048, GzipLevel: 1}))
```

`compress.New(config ...compress.Config)` 返回 `core.HandlerFunc`。`Config` 提供 `ContentTypes`、`MinLength`、`GzipLevel`、`ZstdLevel`、`BrotliLevel`、`EnableSSE`、`Skip`。默认 MIME、1024 字节阈值和三种编码器的默认级别沿用独立中间件；`ContentTypes` 为非 nil 空切片时禁用压缩，负 `MinLength` 取消阈值。非法 MIME/算法级别返回配置错误，不按客户端算法静默回退。

按 `Accept-Encoding` 的正 q 值从高到低选择，平局依次优先 zstd、br、gzip；显式编码覆盖通配符，显式 q=0 禁止。q 必须为 0～1、至多三位小数；非法参数视为该编码不可接受。多行头合并解析；同一编码重复时取最低权重，防止禁止声明被覆盖。显式 identity 权重高于压缩编码时发送原文；未显式声明的 identity 只作兜底（显式 identity 不写 q 时按 1 处理）。缺失或空头保持原文，不自动压缩，也不改写下游看到的请求头。

| Accept-Encoding | 结果（响应符合压缩条件时） |
| --- | --- |
| `gzip, zstd` | zstd |
| `br, gzip` | br |
| `zstd;q=0.2, br;q=0.8, gzip;q=0.5` | br |
| `zstd;q=0.2, gzip;q=0.8` | gzip |
| `zstd;q=0, gzip` | gzip |
| `*;q=0, gzip;q=0.5` | gzip；不允许原文 |
| `identity;q=1, gzip;q=0.5` | 原文 |
| `*;q=0` | 406，不调用 handler |

仅安装统一入口即可；现有 `gzip.New()` / `zstd.New()` 默认行为不变，新增 `brotli.New()`。三个入口复用内部流式 writer、池化与清理逻辑。保留 Vary、静态缓存、ReaderFrom、HEAD/Range/Upgrade/CONNECT、204/304、默认 SSE、既有编码和 no-transform 等处理。入口已有非 identity 的 Content-Encoding 时透传，不重新编码；下游 handler 写出时设置既有编码也不重复压缩，其编码协商由生成该响应的一方负责。但请求拒绝 identity 且不接受 br/gzip/zstd 时会提前 406，无法预知下游才生成的其它预编码表示；这种预编码路径应使用 Skip（或在进入统一入口前完成编码协商并设置响应头）。

客户端允许 identity 时，不符合 MIME/阈值或提前 Flush 的响应保持原文。若显式禁止 identity（含 `*;q=0` 未给 identity 例外），而响应无法压缩，返回无缓存的 406，并向正文生产者返回内部拒绝错误以停止继续写出/读盘；该拒绝在中间件边界消化，不作为服务器故障，但其它写入/关闭错误仍保留。不强制压缩图片、默认 SSE 或 no-transform；HEAD、204/304 等无正文响应保持无正文。后验发现无法压缩时 handler 已执行，406 不回滚业务副作用。`Skip` 为 true 则完全绕过协商（包括 406），由调用方负责该路径。zstd 输出开始后的失败继续返回错误，不能切换为 gzip。


## 19. Brotli 插件与 SSE 流式压缩

```go
// 推荐统一协商：q 权重优先，同权重 zstd → br → gzip。
app.Use(compress.New(compress.Config{EnableSSE: true}))
```

独立入口是 `github.com/xs23933/core/v3/middleware/brotli`，API 为 `brotli.New(config ...brotli.Config)`；`Config` 含 `ContentTypes`、`Level`、`MinLength`、`EnableSSE`、`Skip`。仅安装统一或单算法入口之一。Brotli 复用已有 andybalholm/brotli 依赖，`Level: 0` 使用默认质量6，显式范围1～11；普通响应默认阈值1024字节，图片不默认压缩。`compress.Config.BrotliLevel` 采用相同规则。

`EnableSSE` 在三个独立入口和统一入口中默认 false，保持现有 SSE 不压缩；设为 true 后，`text/event-stream`（允许 charset 参数）绕过 MinLength，并在首次 Flush/DoWriteHeader 时先选择压缩编码。每个 `SSEWrite` / `SSESend` / `SSEComment` 的 Flush 都刷新编码器再刷新 HTTP writer，因此微小事件不等待整个响应关闭。仍尊重请求协商、no-transform、Range、已有编码、无正文状态；非 nil 空 ContentTypes 完全禁用压缩，包括 SSE。EnableSSE 明确放行 SSE，不要求额外把它加进非空的 MIME 列表。

SSE 发送前设置 `Content-Type: text/event-stream` 和 `Cache-Control: no-cache`；不要同时设置 no-transform 来期望开启压缩，因为压缩中间件会尊重它并跳过转换。普通 HTTP 流仍采用原有阈值规则：先写出达到阈值的数据再 Flush，或以负 MinLength 取消阈值；普通响应首次仅 Flush 仍提交原文。

Upgrade 和 CONNECT 完全绕过统一 HTTP 压缩协商，不由 identity/q 规则阻止 WebSocket 握手。WebSocket 使用独立的 RFC7692 permessage-deflate；不是 Brotli/gzip/zstd Content-Encoding。通过 `websocket.Options{EnableCompression: true, ReadLimit: 1 << 20}` 开启标准浏览器消息压缩，仅双方协商成功才生效，默认 false。大消息可分段写入 NextWriter，但必须 Close 才结束该消息；浏览器以完整消息触发回调，实时推送应逐条发送，不把所有事件合成一个无限消息。ReadLimit 同时约束解压后的完整消息大小。

本地真实 HTTP 流测试覆盖连接关闭前逐条解压事件、预先刷新头、三种编码与两种 ETag 安装顺序；不保证 Cloudflare/其它代理不会缓冲，生产链路需单独验证。

## 生成代码时避免

- 不要在中间件里吞掉 `c.Next()` 返回错误。
- 不要使用 `app.Use("/api/*", middleware)` 表示路径前缀；应使用 `app.Use("/api", middleware)`。
- 不要在限流失败时返回 200。
- 不要把 metrics、鉴权、限流都写进一个巨大中间件。
- 不要在中间件里进行阻塞式慢 IO。
- `bodylimit` 和 `timeout` 建议放在中间件链首位。
- `security` 和 `requestid` 紧跟其后。
- `metrics` 和 `ratelimit` 在鉴权中间件之后。
- `etag` 对敏感动态 API 和流式接口使用 `Skip` 排除；非 GET/HEAD 本身已跳过。
- 限流中间件已采用 16 分片锁优化，多核场景下线性扩展。
