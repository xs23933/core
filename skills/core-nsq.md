---
name: core-nsq
description: Core 框架 NSQ 消息队列封装，Producer/Consumer 一站式使用
tags: [go, core-framework, nsq, message-queue]
---

# Core NSQ 封装

## 概述

Core 框架内置 NSQ 封装，基于 `github.com/nsqio/go-nsq`，提供：
- 自动初始化 Producer（从 config.yaml 读取配置）
- Consumer 封装（支持 lookupd/nsqd 直连、并发处理、优雅关闭）
- JSON 消息自动序列化/反序列化
- 链式 Builder 创建 Consumer
- 全局快捷函数发布消息

## 配置

```yaml
nsq:
  nsqd: "127.0.0.1:4150"
  lookupd: "127.0.0.1:4161"       # 可选，Consumer 用
  max_in_flight: 200               # 可选
  default_requeue_delay_sec: 90    # 可选，秒
  max_attempts: 5                  # 可选
  client_id: "my-service"          # 可选
  auth_secret: ""                  # 可选
```

> `core.New(cfg)` 自动初始化 Producer，应用退出自动关闭。

## Producer

### 获取 Producer

```go
// 全局 Producer（core.New 自动创建）
p := core.NProducer()
```

### 发布消息

```go
ctx := context.Background()

// 字节消息
_ = p.Publish("order_events", []byte("hello"))

// 字符串消息
_ = p.PublishString("order_events", "hello")

// JSON 消息（sonic 自动序列化）
type Order struct { ID string `json:"id"` }
_ = p.PublishJSON("order_events", Order{ID: "123"})

// 延迟发布
_ = p.DeferredPublish("order_events", 5*time.Second, []byte("delayed"))
_ = p.DeferredPublishJSON("order_events", 5*time.Second, Order{ID: "456"})

// 批量发布
_ = p.MultiPublish("order_events", [][]byte{
    []byte("msg1"), []byte("msg2"),
})
```

### 全局快捷函数（无需获取 Producer 实例）

```go
_ = core.NSQPublish("topic", []byte("msg"))
_ = core.NSQPublishJSON("topic", Order{ID: "123"})
_ = core.NSQPublishString("topic", "msg")
_ = core.NSQDeferredPublish("topic", 5*time.Second, []byte("msg"))
_ = core.NSQDeferredPublishJSON("topic", 5*time.Second, Order{ID: "123"})
```

## Consumer

### 方式一：直接创建

```go
consumer, err := core.NewNSQConsumer(core.NSQConsumerConfig{
    Topic:       "order_events",
    Channel:     "worker",
    Lookupds:    []string{"127.0.0.1:4161"},  // 推荐：通过 lookupd 自动发现
    Concurrency: 5,
    MaxInFlight: 200,
    MaxAttempts: 5,
    MaxBackoff:  2 * time.Minute,
})
if err != nil {
    log.Fatal(err)
}

// 阻塞运行（直到 Stop 或连接断开）
err = consumer.Start(func(msg *nsq.Message) error {
    // 返回 nil → FIN（确认）
    // 返回 error → REQ（重新入队，自动退避重试）
    fmt.Println(string(msg.Body))
    return nil
})
```

### 方式二：Builder 链式创建

```go
consumer, err := core.NewNSQConsumerBuilder("order_events", "worker").
    WithLookupd("127.0.0.1:4161").
    WithConcurrency(5).
    WithMaxInFlight(200).
    WithMaxAttempts(5).
    Build()

// 构建并异步启动
consumer, err := core.NewNSQConsumerBuilder("order_events", "worker").
    WithNSQD("127.0.0.1:4150").
    WithConcurrency(10).
    BuildAndStart(func(msg *nsq.Message) error {
        return nil
    })
```

### 方式三：异步启动

```go
consumer, _ := core.NewNSQConsumer(core.NSQConsumerConfig{
    Topic:    "order_events",
    Channel:  "worker",
    Lookupds: []string{"127.0.0.1:4161"},
})

// 异步启动，不阻塞
_ = consumer.StartAsync(func(msg *nsq.Message) error {
    return nil
})

// 主逻辑继续...
```

### 停止消费者

```go
consumer.Stop()  // 优雅停止
```

### 消费者统计

```go
stats := consumer.Stats()
fmt.Printf("messages: %d, connections: %d\n", stats.MessagesReceived, stats.Connections)
```

### 动态调整

```go
// 调整最大在途消息数
consumer.ChangeMaxInFlight(500)

// 判断是否饥饿（可用于背压控制）
if consumer.IsStarved() {
    // 暂停处理或降低速率
}
```

## JSON 消息处理

### 解析 JSON 消息

```go
type Order struct {
    ID     string  `json:"id"`
    Amount float64 `json:"amount"`
}

consumer.Start(func(msg *nsq.Message) error {
    // 方式一：解析到变量
    var order Order
    if err := core.NSQDecodeJSON(msg, &order); err != nil {
        Erro("invalid message: %v", err)
        return nil // 不重试，直接 FIN
    }

    // 方式二：泛型解析
    order, err := core.NSQParse[Order](msg)
    if err != nil {
        return nil
    }

    fmt.Println(order.ID, order.Amount)
    return nil
})
```

## 带超时的消息处理

```go
// 为每条消息创建带超时的 context
handler := core.NSQHandlerWithTimeout(30*time.Second,
    func(ctx context.Context, msg *nsq.Message) error {
        // 如果处理超时，context 会自动取消
        result, err := doSlowWork(ctx, string(msg.Body))
        if err != nil {
            return err // REQ 重试
        }
        return nil // FIN
    },
)

consumer.Start(handler)
```

## 完整示例：订单事件处理

```go
func main() {
    cfg := core.LoadConfigFile("config.yaml")
    app := core.New(cfg)

    // Producer 已自动初始化，直接发布
    _ = core.NSQPublishJSON("order_events", map[string]any{
        "type":     "created",
        "order_id": "123",
    })

    // 启动消费者
    go func() {
        consumer, _ := core.NewNSQConsumer(core.NSQConsumerConfig{
            Topic:       "order_events",
            Channel:     "worker",
            Lookupds:    []string{"127.0.0.1:4161"},
            Concurrency: 10,
            MaxInFlight: 200,
        })

        _ = consumer.Start(func(msg *nsq.Message) error {
            var event map[string]any
            if err := core.NSQDecodeJSON(msg, &event); err != nil {
                Erro("bad message: %v", err)
                return nil // 不重试
            }
            return processOrder(event) // 失败自动重试
        })
    }()

    app.Run()
}
```

## NSQConsumerConfig 参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| Topic | string | 必填 | Topic 名称 |
| Channel | string | 必填 | Channel 名称（同 Channel 的 Consumer 竞争消费）|
| NSQDs | []string | 无 | nsqd 直连地址（如 `["127.0.0.1:4150"]`）|
| Lookupds | []string | 无 | nsqlookupd 地址（推荐，自动发现 nsqd）|
| Concurrency | int | 1 | 并发处理协程数 |
| MaxInFlight | int | 200 | 最大在途消息数 |
| MaxAttempts | uint16 | 5 | 最大重试次数（超限后丢弃）|
| MaxBackoff | time.Duration | 2m | 最大退避时间 |
| MsgTimeout | time.Duration | 服务端默认 | 消息处理超时 |

## NSQ vs Redis Stream vs Pub/Sub 选择

| 特性 | NSQ | Redis Stream | Redis Pub/Sub |
|------|-----|-------------|---------------|
| 持久化 | ✅ 磁盘 | ✅ 内存 | ❌ |
| 消费者组 | ✅ Channel | ✅ Consumer Group | ❌ |
| 消息回溯 | ❌ | ✅ | ❌ |
| 至少一次投递 | ✅ | ✅ | ❌ |
| 延迟消息 | ✅ DeferredPublish | ❌ 原生不支持 | ❌ |
| 水平扩展 | ✅ 轻量 | ✅ | ✅ |
| 适用场景 | 高吞吐消息队列 | 事件溯源/审计 | 实时广播 |

## 注意事项

1. **Lookupds vs NSQDs**：生产环境推荐 Lookupds（自动发现节点），开发环境可用 NSQDs 直连
2. **handler 返回值**：`nil` = FIN，`error` = REQ（自动退避重试），超过 MaxAttempts 后丢弃
3. **不可恢复错误**：如消息格式错误，应返回 `nil`（FIN）避免无限重试
4. **Consumer 生命周期**：`Start` 阻塞运行，`StartAsync` 异步运行，`Stop` 优雅关闭
5. **应用关闭时**：所有 Consumer 自动通过 `CloseNSQ()` 清理
