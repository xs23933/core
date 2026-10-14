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

## 4. 有界连接、心跳与清理

敏感/设备通道先用 Handler 鉴权，再调用 Core 的升级和生命周期能力：

```go
options := ws.Options{
    Subprotocols: []string{"example.v1"}, RequireSubprotocol: true,
    CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
    ReadLimit: 512 << 10, ReadTimeout: 60 * time.Second,
    WriteTimeout: 10 * time.Second, HandshakeTimeout: 10 * time.Second,
}
return ws.New().WithOptions(options).
    OnConnect(func(conn *ws.Conn) {
        // Context 来自请求标准上下文，关闭时取消；不要捕获 core.Ctx。
        ctx := conn.Context()
        _ = ctx
    }).
    OnMessage(func(conn *ws.Conn, mt ws.MessageType, data []byte) {
        if !conn.TrySendWithType(mt, data) { return }
    }).
    OnClose(func(conn *ws.Conn) { /* 清理业务会话 */ }).Handler()(c)
```

示例另外需要 `net/http` 和 `time`。Options 零值保持旧默认：任意 Origin、可无子协议、消息大小与网络等待不受限。业务必须按实际通道设置边界，不能以框架默认替代认证。RequireSubprotocol 没有匹配（含配置为空）会拒绝升级；ReadLimit 按完整消息含分片累计，超限不进入回调。ReadTimeout 在消息开始与 pong 时刷新，WriteTimeout 同时用于普通消息和 ping。

TrySend/TrySendWithType 复制输入，仅确认入队，不能作为实际网络送达、设备持久化或业务 ACK。队满返回 false 并关闭连接。Close 幂等取消 Conn.Context、移除默认管理器连接、释放批量与排队缓冲区；读写退出后 OnClose 只调用一次。回调收到的字节切片为池缓存，只可在回调内使用，异步保留必须复制。配置及回调应在注册 Handler 前完成，不在请求中修改共享 WS。

## 5. 广播与分组

- 广播：使用 `ws.DefaultManager.Broadcast(data)` 发送系统通知、全局消息。
- 房间：按 `room_id` 管理连接集合，减少无效推送。
- 单播：使用 `ws.DefaultUserManager.SendToUser(userID, data)` 按用户精确推送。

## 6. 生成代码时避免

- 不要在读循环里直接做重 CPU/IO 任务。
- 不要在业务复制 upgrader、读写循环或用旁路连接绕过 Core Options。
- 不要忘记 `defer manager.Remove(client)`。
- 不要在项目里重复实现用户连接 map；优先使用 `websocket.DefaultUserManager` 或 `websocket.NewUserManager()`。
