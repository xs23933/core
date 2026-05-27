---
name: core-websocket
description: 使用 Core Framework websocket 子包实现连接管理、按用户推送、广播、心跳和退出清理
tags: [go, core-framework, websocket, realtime]
---

# Core WebSocket 技能

## 触发条件

- "WebSocket 聊天"
- "实时推送"
- "ws 广播"
- "连接管理"
- "按用户发送消息"
- "WebSocket 单播"

## 1. 基本接入

```go
import (
    "github.com/xs23933/core/v3"
    ws "github.com/xs23933/core/v3/websocket"
)

app := core.New()

app.GET("/ws", ws.New().
    OnConnect(func(conn *ws.Conn) {
        conn.Send([]byte("Welcome to WebSocket"))
    }).
    OnMessage(func(conn *ws.Conn, mt ws.MessageType, msg []byte) {
        conn.SendWithType(mt, msg)
    }).
    OnClose(func(conn *ws.Conn) {
        core.Info("websocket closed")
    }).
    Handler())
```

## 2. 按用户推送

框架内置 `websocket.UserManager`，用于维护 `user_id -> []*Conn` 的关系。默认实例是 `websocket.DefaultUserManager`。

```go
app.GET("/ws", func(c core.Ctx) error {
    userID := c.Query("user_id")
    if userID == "" {
        return c.SendStatus(401, "missing user_id")
    }

    return ws.New().
        OnConnect(func(conn *ws.Conn) {
            ws.DefaultUserManager.Add(userID, conn)
        }).
        OnMessage(func(conn *ws.Conn, mt ws.MessageType, msg []byte) {
            // 处理客户端消息
        }).
        OnClose(func(conn *ws.Conn) {
            ws.DefaultUserManager.Remove(conn)
        }).
        Handler()(c)
})

ok := ws.DefaultUserManager.SendToUser("1001", []byte(`{"type":"notice","data":"hello"}`))
if !ok {
    core.Warn("websocket user offline: %s", "1001")
}
```

### UserManager API

| 方法 | 说明 |
| ---- | ---- |
| `ws.NewUserManager()` | 创建独立用户连接管理器 |
| `ws.DefaultUserManager` | 全局默认用户连接管理器 |
| `Add(userID, conn)` | 将连接绑定到用户 |
| `Remove(conn)` | 移除连接 |
| `SendToUser(userID, data)` | 给某个用户的所有在线连接发送消息 |
| `SendToUsers(userIDs, data)` | 给多个用户发送消息，并自动去重连接 |
| `Online(userID)` | 判断用户是否在线 |
| `Count(userID)` | 获取用户在线连接数 |

`UserManager` 使用分片锁和发送快照，适合并发连接、断开和推送。`Conn.Close()` 会自动从 `DefaultUserManager` 移除连接，业务中仍建议在 `OnClose` 显式 `Remove`，保持代码语义清晰。

## 3. 连接上下文

- 建连后立即提取 `user_id`、`room_id` 并绑定到连接对象。
- 不要在 goroutine 中直接复用 `core.Ctx`。
- `OnConnect` 只有 `*websocket.Conn`，如果需要请求参数，必须在外层 Handler 中先从 `core.Ctx` 提取，再通过闭包传入。

## 4. 心跳与清理

```go
// 建议设置读写超时与 pong 处理
// 断开时必须 Remove，防止 manager 泄漏失效连接
```

## 5. 广播与分组

- 广播：使用 `ws.DefaultManager.Broadcast(data)` 发送系统通知、全局消息。
- 房间：按 `room_id` 管理连接集合，减少无效推送。
- 单播：使用 `ws.DefaultUserManager.SendToUser(userID, data)` 按用户精确推送。

## 6. 生成代码时避免

- 不要在读循环里直接做重 CPU/IO 任务。
- 不要忽略 `ReadMessage` 错误。
- 不要忘记 `defer manager.Remove(client)`。
- 不要在项目里重复实现用户连接 map；优先使用 `websocket.DefaultUserManager` 或 `websocket.NewUserManager()`。
