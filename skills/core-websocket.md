---
name: core-websocket
description: 使用 Core Framework websocket 子包实现连接管理、广播、心跳和退出清理
tags: [go, core-framework, websocket, realtime]
---

# Core WebSocket 技能

## 触发条件

- "WebSocket 聊天"
- "实时推送"
- "ws 广播"
- "连接管理"

## 1. 基本接入

```go
import (
    "github.com/xs23933/core/v3"
    ws "github.com/xs23933/core/v3/websocket"
)

app := core.New()
manager := ws.NewManager()

app.GET("/ws", func(c core.Ctx) error {
    conn, err := ws.Upgrade(c.Response(), c.Request(), nil)
    if err != nil {
        return c.SendStatus(400, err.Error())
    }
    client := ws.NewConn(conn)
    manager.Add(client)
    defer manager.Remove(client)

    for {
        mt, msg, err := client.ReadMessage()
        if err != nil {
            return nil
        }
        manager.Broadcast(mt, msg)
    }
})
```

## 2. 连接上下文

- 建连后立即提取 `user_id`、`room_id` 并绑定到连接对象。
- 不要在 goroutine 中直接复用 `core.Ctx`。

## 3. 心跳与清理

```go
// 建议设置读写超时与 pong 处理
// 断开时必须 Remove，防止 manager 泄漏失效连接
```

## 4. 广播与分组

- 广播：系统通知、全局消息。
- 房间：按 `room_id` 管理连接集合，减少无效推送。
- 单播：按 `user_id` 精确推送。

## 5. 生成代码时避免

- 不要在读循环里直接做重 CPU/IO 任务。
- 不要忽略 `ReadMessage` 错误。
- 不要忘记 `defer manager.Remove(client)`。

