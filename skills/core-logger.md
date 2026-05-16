---
name: core-logger
description: Core 框架日志系统，包括 D/Info/Warn/Erro/Log/Dump、Panic Recovery、LogMonitor、EventHub
tags: [go, core-framework, logger, sse, events]
---

# Core Logger 日志系统

## 概述

Core 内置日志系统提供：
- 分级日志：`D` / `Info` / `Warn` / `Erro` / `Log`
- 终端彩色输出（自动检测 TTY）
- HTTP 请求日志中间件
- Panic Recovery 中间件
- LogMonitor：日志实时广播
- EventHub：SSE 事件推送

## 日志函数

### D — 调试日志

仅当 `config.yaml` 中 `debug: true` 时输出，灰色标记 `[D]`。

```go
core.D("user %s login", username)
// [D] user tom login
```

### Info — 信息日志

绿色标记 `[I]`。

```go
core.Info("server started on :%d", port)
// [I] server started on :8080
```

### Warn — 警告日志

黄色标记 `[W]`。

```go
core.Warn("rate limit approaching: %d/%d", current, max)
// [W] rate limit approaching: 95/100
```

### Erro — 错误日志

红色标记 `[E]`。

```go
core.Ero("db query failed: %v", err)
// [E] db query failed: connection refused
```

### Log — 原始日志

无前缀标记，直接输出。

```go
core.Log("raw output: %s", data)
```

### Dump — 数据结构打印

使用 `spew.Dump` 打印完整数据结构（递归展开）。

```go
core.Dump(user)
// (main.User) {
//  ID: (string) "123",
//  Name: (string) "tom",
// }
```

### Writers — 兼容 io.Writer 的日志适配

```go
// 可传给需要 Printf 的第三方库
var w core.Writers
w.Printf("formatted message %s", arg)
```

## 格式规则

- 所有日志函数自动追加 `\n`（无需手动加换行）
- 终端模式下自动着色，非终端（如日志文件、管道）自动去色
- `D` 只在 `debug: true` 时输出，生产环境无性能开销

## HTTP 请求日志

`Logger()` 中间件自动记录每个请求：

```go
// core.New() 内部自动注册，无需手动添加
// 输出格式（debug 模式）：
// [I] 200 GET /api/users 1.2ms
// [W] 301 POST /api/old 0.3ms
// [E] 500 GET /api/error 5.1ms
```

状态码着色规则：
- 2xx → 绿色 `[I]`
- 3xx → 黄色 `[W]`
- 4xx+ → 红色 `[E]`

**生产环境**（`debug: false`）：请求日志不输出，减少 I/O。

## Panic Recovery

`Recovery()` 中间件自动捕获 panic：

```go
// core.New() 内部自动注册
// panic 时：
// 1. 记录堆栈到 stderr（红色）
// 2. 返回 500 响应
// 3. Debug 模式下输出完整堆栈 + 请求头
```

自定义 Recovery 行为：

```go
app.Use(core.RecoveryWithWriter(os.Stdout, func(c core.Ctx, err any) {
    // 自定义 panic 处理
    core.Ero("panic: %v", err)
    c.Abort(500)
}))
```

## LogMonitor — 日志实时广播

`LogHub` 是全局日志监视器，将日志实时推送给订阅者（常用于 Web 控制台实时查看日志）。

```go
// 注册订阅
ch := make(chan string, 100)
core.LogHub.Register(ch)
defer core.LogHub.UnRegister(ch)

// 读取日志
for msg := range ch {
    fmt.Print(msg)
}
```

原理：`HookWriter` 在写入日志时同步 `Broadcast` 给所有注册的 channel。阻塞的客户端自动跳过，不影响整体性能。

## EventHub — SSE 事件推送

`EventHub` 提供 Server-Sent Events (SSE) 封装，支持事件批量聚合和 WebSocket 式推送。

### 创建 EventHub

```go
hub := core.NewEventHub()             // 默认 1s 批量间隔
hub := core.NewEventHub(5 * time.Second) // 自定义间隔
```

### 前端 SSE 订阅

```go
app.Get("/events", hub.Get)
// 前端：
// const es = new EventSource('/events')
// es.addEventListener('order_created', e => console.log(e.data))
// es.addEventListener('batch', e => console.log('batch:', JSON.parse(e.data)))
```

### 后端推送事件

```go
hub.Broadcast(core.EventData{
    Event: "order_created",
    Data:  map[string]any{"order_id": "123"},
})

// Data 支持任意类型，自动序列化：
// string/[]byte → 原样
// int/float    → 转字符串
// 其他         → sonic JSON 序列化
```

### POST 推送

```go
app.Post("/events", hub.PostData)
// POST body: {"event": "order_created", "data": {"order_id": "123"}}
```

### 批量聚合

当短时间内有多个事件推送时，EventHub 按配置间隔聚合成 `batch` 事件：

```
// 1s 内推送 3 个事件 → 前端收到 1 个 batch：
event: batch
data: [{"event":"a","data":"1"},{"event":"b","data":"2"},{"event":"c","data":"3"}]
```

### 心跳

SSE 连接自动每 10s 发送心跳（`:\n\n`），防止连接超时。

### 自定义 SSE 端点

```go
app.Get("/ws/events", func(c core.Ctx) error {
    ch := make(chan core.EventData, 100)
    hub.Register(ch)
    defer hub.UnRegister(ch)

    c.Stream(func(w io.Writer) bool {
        select {
        case msg, ok := <-ch:
            if !ok {
                return false
            }
            fmt.Fprint(w, msg.String())
            return true
        case <-time.After(30 * time.Second):
            fmt.Fprint(w, ":\n\n") // 心跳
            return true
        }
    })
    return nil
})
```

## EventData 序列化规则

```go
type EventData struct {
    Event string `json:"event"` // 事件名，空则只输出 data
    Data  any    `json:"data"`  // 数据
}
```

SSE 输出格式：
- 有 Event：`event: xxx\ndata: 序列化值\n\n`
- 无 Event：`data: 序列化值\n\n`

Data 序列化优先级：`string` > `[]byte` > `int/float` > `JSON Marshal`

## 完整示例：日志 + 事件推送

```go
func main() {
    cfg := core.LoadConfigFile("config.yaml")
    app := core.New(cfg)

    // 创建事件中心
    hub := core.NewEventHub(2 * time.Second)

    // SSE 端点
    app.Get("/events", hub.Get)
    app.Post("/events", hub.PostData)

    // 业务中推送事件
    app.Post("/api/orders", func(c core.Ctx) error {
        // ... 创建订单 ...
        core.Info("order created: %s", orderID)

        hub.Broadcast(core.EventData{
            Event: "order_created",
            Data:  order,
        })
        return c.ToJSON(order, nil)
    })

    app.Run()
}
```

## 注意事项

1. `D()` 只在 `debug: true` 时输出，生产环境放心调用
2. `Erro()` 格式化动词用 `%w` 不会自动 wrap error，只是打印
3. LogMonitor 慢客户端自动跳过，不影响主流程
4. EventHub 批量间隔不宜太短（建议 ≥500ms），避免频繁推送
5. SSE 连接断开后 `UnRegister` 会自动 close channel
6. Recovery 中间件在 `Authorization` 头中会用 `*` 替换敏感信息
