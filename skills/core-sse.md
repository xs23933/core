---
name: core-sse
description: 使用 Core Framework SSE（Server-Sent Events）实现服务端实时推送
tags: [go, core-framework, sse, server-sent-events, realtime]
---

# Core SSE（Server-Sent Events）技能

## 触发条件

- 用户要求实现 SSE、Server-Sent Events、服务端推送、实时消息推送
- 用户提到 `EventHub`、`SSEWrite`、`SSESend`、`SSEComment`
- 需要从服务端单向推送数据到浏览器/客户端

## 1. SSE 基本概念

SSE（Server-Sent Events）是一种基于 HTTP 的轻量级服务端推送协议。与 WebSocket 不同，SSE 是单向的（服务器 → 客户端），使用标准 HTTP 连接，自动重连，适合通知、日志推送、实时状态更新等场景。

Core Framework 在 Ctx 层提供了三个 SSE 核心方法，并在 EventHub 中封装了完整的连接管理和广播能力。

## 2. Ctx SSE 核心方法

### 2.1 SSEWrite — 写入 SSE 事件

最底层的 SSE 事件写入方法，直接拼接 `id:` / `event:` / `data:` 字段。

```go
// 写入基本事件
c.SSEWrite("message", "hello world")

// 写入带 ID 的事件（客户端可据此恢复断线后的数据）
c.SSEWrite("update", `{"status":"ok"}`, "evt-001")

// 多行 data 自动添加 "data: " 前缀
c.SSEWrite("report", "line1\nline2\nline3")
```

参数说明：

| 参数 | 类型 | 说明 |
| ---- | ---- | ---- |
| `event` | `string` | 事件名，为空时不输出 event 字段 |
| `data` | `string` | 事件数据，为空时输出空 data 字段；多行自动拆分并每行加 `data: ` 前缀 |
| `id` | `...string` | 可选，事件 ID，用于断线重连 |

### 2.2 SSESend — 发送结构化事件

自动将 data 序列化为 JSON（非 string 类型），适合发送结构体/Map 等复杂数据。

```go
type Notification struct {
    Title   string `json:"title"`
    Message string `json:"message"`
}

// 发送字符串
c.SSESend("msg", "plain text")

// 发送结构体（自动 JSON 序列化）
c.SSESend("notify", Notification{Title: "Alert", Message: "Server restart"})

// 发送 nil（空 data）
c.SSESend("ping", nil)

// 带 ID
c.SSESend("msg", "payload", "id-001")
```

参数说明：

| 参数 | 类型 | 说明 |
| ---- | ---- | ---- |
| `event` | `string` | 事件名 |
| `data` | `any` | 事件数据，string/[]byte 直接使用，nil 为空，其他类型自动 JSON 序列化 |
| `id` | `...string` | 可选，事件 ID |

### 2.3 SSEComment — 发送心跳注释

SSE 规范中 `:` 开头的行视为注释，客户端忽略。用于保持连接活跃（心跳）。

```go
// 发送自定义注释
c.SSEComment("keepalive")

// 空参数默认为 "ping"
c.SSEComment("")  // 输出 ": ping"
```

## 3. EventHub 连接管理

EventHub 是对 SSE 的高层封装，提供连接注册/注销、广播、定向推送、心跳、批量聚合等能力。

### 3.1 基本用法

```go
package main

import "github.com/xs23933/core/v3"

func main() {
    app := core.New()

    // 创建 EventHub，参数为广播聚合间隔（默认 1s）
    hub := core.NewEventHub()
    defer hub.Close()

    // 注册 SSE 端点
    app.GET("/events", hub.Get)

    // 注册推送接口
    app.POST("/events/push", hub.PostData)

    app.Run()
}
```

### 3.2 Get — SSE 连接 Handler

`hub.Get` 是标准的 SSE 连接端点 Handler，自动完成：

1. 设置 `Content-Type: text/event-stream` 等必要响应头
2. 发送 `connected` 事件确认连接建立
3. 监听 EventHub 消息管道，通过 SSESend 推送给客户端
4. 每 15 秒发送心跳注释保持连接
5. 客户端断开或 Hub 关闭时自动清理

```go
// 匿名连接（所有客户端接收广播）
app.GET("/events", hub.Get)

// 命名连接（可按 ID 定向推送）
app.GET("/events/:clientId", hub.Get)
```

### 3.3 PostData — 推送数据接口

接收 JSON 请求体，广播或定向推送到 SSE 客户端。

```go
type EventData struct {
    ID    string `json:"id,omitempty"`  // 目标客户端 ID（定向推送）
    Event string `json:"event"`          // SSE 事件名
    Data  any    `json:"data"`           // 事件数据
}
```

```json
// 广播到所有客户端
{
    "event": "notification",
    "data": {"title": "System Update", "level": "info"}
}

// 定向推送到指定客户端
{
    "id": "client-001",
    "event": "private",
    "data": "hello user"
}
```

### 3.4 Broadcast / SendTo — 编程式推送

```go
// 广播到所有连接的客户端
hub.Broadcast(core.EventData{
    Event: "alert",
    Data:  "server maintenance in 5 minutes",
})

// 定向推送到指定 ID 的客户端
hub.SendTo("admin-01", core.EventData{
    Event: "private",
    Data:  map[string]any{"action": "reload"},
})
```

### 3.5 EventHub 生命周期

```go
hub := core.NewEventHub()        // 创建，启动后台广播 goroutine
hub.Register(ch, id)             // 注册连接（通常由 Get 自动调用）
hub.UnRegister(ch, id)           // 注销连接（通常由 Get defer 调用）
hub.Close()                      // 关闭 Hub，断开所有客户端
```

## 4. 手动 SSE 流式处理

如果不使用 EventHub，可以直接用 Ctx 方法和 Stream 实现自定义 SSE 逻辑：

```go
app.GET("/custom-sse", func(c core.Ctx) error {
    c.SetHeader("Content-Type", "text/event-stream;charset=utf-8")
    c.SetHeader("Cache-Control", "no-cache")
    c.SetHeader("Connection", "keep-alive")

    // 发送连接确认
    c.SSEWrite("connected", "{}")

    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    c.Stream(func(w io.Writer) bool {
        select {
        case <-ticker.C:
            // 发送业务数据
            c.SSESend("update", map[string]any{
                "time": time.Now().Format(time.RFC3339),
            })
        case <-c.Ctx().Done():
            // 客户端断开
            return false
        }
        return true
    })
})
```

## 5. 注意事项

- **单连接限制**：同一 ID 注册新连接时，旧连接会被自动关闭
- **缓冲大小**：每个客户端管道缓冲 100 条消息，客户端读取过慢会丢消息
- **Ctx 生命周期**：`c.Ctx()` 返回的 context 只在当前请求有效，不要在 goroutine 中继续使用 Ctx
- **心跳间隔**：EventHub 默认心跳 15 秒，SSE 代理（Nginx）的 proxy_read_timeout 应大于此值
- **Nginx 缓冲**：`Get` 方法自动设置了 `X-Accel-Buffering: no`，但建议同时配置 `proxy_buffering off`
- **浏览器限制**：同一域名下浏览器默认允许 6 个 SSE 连接
- **EventData.ID**：设置后可实现断线重连（客户端重连时发送 `Last-Event-ID` header）
