# Core Redis 封装

## 概述

Core 框架内置 Redis 封装，基于 `github.com/redis/go-redis/v9`，提供：
- 自动初始化（从 config.yaml 读取配置）
- 全局访问 `core.RConn()`
- 常用操作封装（String/JSON/Hash/Set/ZSet/List/BitMap/Pipeline/Lua/PubSub/Stream）

## 配置

### 单实例

```yaml
redis:
  addr: "127.0.0.1:6379"
  password: ""
  db: 0
  pool_size: 100
  min_idle_conns: 10
```

### 多实例

```yaml
redis:
  default:
    addr: "127.0.0.1:6379"
    db: 0
  cache:
    addr: "127.0.0.1:6380"
    db: 1
```

## 获取连接

```go
// 默认实例
rdb := core.RConn()

// 命名实例
rdb := core.RConn("cache")
```

## 基础操作

```go
ctx := context.Background()

// Set/Get
_ = rdb.Set(ctx, "key", "value", time.Minute)
val, _ := rdb.Get(ctx, "key")

// Delete/Exists/Expire/TTL
_ = rdb.Delete(ctx, "key")
ok, _ := rdb.Exists(ctx, "key")
_ = rdb.Expire(ctx, "key", time.Hour)
ttl, _ := rdb.TTL(ctx, "key")
```

## JSON 操作

```go
type User struct { Name string `json:"name"` }

// 存
_ = rdb.SetJSON(ctx, "user:1", User{Name: "tom"}, time.Hour)

// 取
var u User
_ = rdb.GetJSON(ctx, "user:1", &u)
```

## 计数器

```go
// 递增/递减
n, _ := rdb.Incr(ctx, "counter")
n, _ := rdb.Decr(ctx, "counter")
n, _ := rdb.IncrBy(ctx, "counter", 10)

// 限流常用：递增 + 设置过期
n, _ := rdb.IncrWithTTL(ctx, "rate:ip:1.2.3.4", time.Minute)
if n > 100 {
    // 超过限流
}
```

## Hash

```go
_ = rdb.HSet(ctx, "user:1", "name", "tom", "age", 20)
val, _ := rdb.HGet(ctx, "user:1", "name")
all, _ := rdb.HGetAll(ctx, "user:1")
_ = rdb.HDel(ctx, "user:1", "age")
ok, _ := rdb.HExists(ctx, "user:1", "name")
n, _ := rdb.HIncrBy(ctx, "user:1", "age", 1)
n, _ := rdb.HLen(ctx, "user:1")

// Hash JSON
_ = rdb.HSetJSON(ctx, "users", "1", User{Name: "tom"})
var u User
_ = rdb.HGetJSON(ctx, "users", "1", &u)
```

## Set（集合）

```go
_ = rdb.SAdd(ctx, "tags", "go", "redis", "core")
members, _ := rdb.SMembers(ctx, "tags")
_ = rdb.SRem(ctx, "tags", "go")
ok, _ := rdb.SIsMember(ctx, "tags", "redis")
n, _ := rdb.SCard(ctx, "tags")
```

## Sorted Set（有序集合）

```go
_ = rdb.ZAdd(ctx, "leaderboard",
    redis.Z{Score: 100, Member: "alice"},
    redis.Z{Score: 200, Member: "bob"},
)

members, _ := rdb.ZRangeByScore(ctx, "leaderboard", &redis.ZRangeBy{
    Min: "0", Max: "+inf", Offset: 0, Count: 10,
})

_ = rdb.ZRem(ctx, "leaderboard", "alice")
n, _ := rdb.ZCard(ctx, "leaderboard")
score, _ := rdb.ZScore(ctx, "leaderboard", "bob")
newScore, _ := rdb.ZIncrBy(ctx, "leaderboard", 50, "alice")
```

## List（列表）

```go
_ = rdb.LPush(ctx, "queue", "task1", "task2")
_ = rdb.RPush(ctx, "queue", "task3")
val, _ := rdb.LPop(ctx, "queue")
items, _ := rdb.LRange(ctx, "queue", 0, -1)
n, _ := rdb.LLen(ctx, "queue")
```

## BitMap（位图）

```go
_ = rdb.SetBit(ctx, "sign:20240101", 100, 1)
bit, _ := rdb.GetBit(ctx, "sign:20240101", 100)
count, _ := rdb.BitCount(ctx, "sign:20240101", &redis.BitCount{Start: 0, End: -1})
```

## Pipeline

```go
_, _ = rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
    p.Set(ctx, "a", 1, 0)
    p.Set(ctx, "b", 2, 0)
    p.Incr(ctx, "counter")
    return nil
})

// 事务管道
pipe := rdb.TxPipeline()
pipe.Set(ctx, "a", 1, 0)
pipe.Incr(ctx, "a")
_, _ = pipe.Exec(ctx)
```

## Lua 脚本

```go
// 直接执行
result, _ := rdb.Eval(ctx, `return redis.call('GET', KEYS[1])`, []string{"key"})

// 预加载（生产推荐）
sha, _ := rdb.ScriptLoad(ctx, `return redis.call('GET', KEYS[1])`)
result, _ = rdb.EvalSha(ctx, sha, []string{"key"})
```

## Pub/Sub

```go
// 发布
_ = rdb.Publish(ctx, "events", "hello")

// 订阅（阻塞）
go func() {
    _ = rdb.Subscribe(context.Background(), "events", func(msg string) {
        fmt.Println("received:", msg)
    })
}()
```

## Scan（迭代扫描）

```go
var cursor uint64
for {
    keys, nextCursor, err := rdb.Scan(ctx, cursor, "user:*", 100).Result()
    if err != nil {
        break
    }
    // 处理 keys...
    cursor = nextCursor
    if cursor == 0 {
        break
    }
}
```

## 限流中间件示例

```go
func RateLimit(rdb *core.RedisClient) core.HandlerFunc {
    return func(c core.Ctx) error {
        ip := c.RemoteIP().String()
        key := fmt.Sprintf("rate:%s", ip)
        
        count, _ := rdb.IncrWithTTL(c, key, time.Minute)
        if count > 100 {
            return c.SendStatus(429, "too many requests")
        }
        return c.Next()
    }
}

// 使用
app.Use(RateLimit(core.RConn()))
```

## 应用中使用（无需手动初始化）

在 `core.New(cfg)` 后，Redis 已自动初始化，直接使用：

```go
func main() {
    cfg := core.LoadConfigFile("config.yaml")
    app := core.New(cfg)
    
    // 直接使用 core.RConn()，无需手动 new
    _ = core.RConn().Set(context.Background(), "hello", "world", time.Minute)
    
    app.Run()
}
```

## 注意事项

1. `Get` 对 `redis.Nil` 返回空字符串不报错，与其他框架风格一致
2. `Set` 的 TTL 参数可选，不传则永不过期
3. JSON 操作使用 sonic 序列化，与框架统一
4. 应用关闭时自动清理连接（通过 `OnShutdown` 注册）
5. 多实例场景通过 `core.RConn("name")` 访问命名实例

## Stream（流）

Redis Stream 适用于：事件驱动、消息队列、实时通知、审计日志等场景。

与 Pub/Sub 的区别：Stream 持久化存储、支持消费者组、可回溯、确保至少一次投递。

### 快速开始

```go
ctx := context.Background()
rdb := core.RConn()

// 发布消息（自动生成 ID）
id, _ := rdb.XAdd(ctx, "orders", map[string]any{
    "type":     "created",
    "order_id": "123",
    "amount":   99.9,
})

// 发布 JSON 消息（自动序列化到 _data 字段）
type OrderCreated struct {
    OrderID string  `json:"order_id"`
    Amount  float64 `json:"amount"`
}
id, _ = rdb.XAddJSON(ctx, "orders", OrderCreated{OrderID: "123", Amount: 99.9})

// 按范围读取消息
msgs, _ := rdb.XRange(ctx, "orders", "-", "+", 10)
for _, msg := range msgs {
    fmt.Println(msg.ID, msg.Fields)
}

// Stream 长度
n, _ := rdb.XLen(ctx, "orders")

// 裁剪 Stream（保留最新 1000 条）
_ = rdb.XTrim(ctx, "orders", 1000)
```

### StreamMessage 结构

```go
type StreamMessage struct {
    ID     string         // 消息 ID（如 "1672531200000-0"）
    Fields map[string]any // 消息字段
}
```

### 消费者组模式（推荐生产使用）

```go
// 1. 创建消费者组（只需执行一次，重复创建会忽略）
_ = rdb.XGroupCreate(ctx, "orders", "order-workers", "0")

// 2. 从消费者组读取消息
msgs, err := rdb.XReadGroup(ctx, "orders", "order-workers", "worker-1", 10, 5*time.Second)
if err != nil {
    // 处理错误
}
for _, msg := range msgs {
    fmt.Println(msg.ID, msg.Fields)
    // 确认消息已处理
    _ = rdb.XAck(ctx, "orders", "order-workers", msg.ID)
}
```

### 待处理消息与认领（故障恢复）

```go
// 查看消费者组待处理情况
pending, _ := rdb.XPending(ctx, "orders", "order-workers")
fmt.Printf("pending: %d, pending range: %s ~ %s\n",
    pending.Count, pending.Lower, pending.Upper)

// 认领空闲超过 30s 的待处理消息（故障恢复）
claimed, _ := rdb.XClaim(ctx, "orders", "order-workers", "worker-1", 30*time.Second,
    "1672531200000-0", "1672531200001-0")
for _, msg := range claimed {
    fmt.Println("claimed:", msg.ID)
}
```

### StreamConsumer — 完整消费者封装

框架提供 `StreamConsumer`，自动处理：创建消费者组、阻塞读取、自动 ACK、待处理消息认领。

```go
rdb := core.RConn()

consumer := rdb.NewStreamConsumer(core.StreamConsumerConfig{
    Stream:     "orders",          // Stream 名称
    Group:      "order-workers",    // 消费者组
    Consumer:   "worker-" + uuid.New().String(), // 消费者名称（全局唯一）
    Count:      10,                 // 每次读取条数
    Block:      5 * time.Second,    // 阻塞等待时间
    ClaimIdle:  30 * time.Second,   // 认领超时（0 禁用）
    ClaimCount: 10,                 // 每次认领最大条数
    AutoAck:    true,               // 处理成功自动 ACK
})

// 启动消费（阻塞，直到 ctx 取消）
ctx, cancel := context.WithCancel(context.Background())
err := consumer.Run(ctx, func(ctx context.Context, msg core.StreamMessage) error {
    // 处理消息
    orderID := msg.Fields["order_id"].(string)
    fmt.Println("processing order:", orderID)

    // 返回 nil → 自动 ACK
    // 返回 error → 不 ACK，进入 pending 列表等待认领重试
    return nil
})
```

### StreamConsumerConfig 参数说明

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| Stream | string | 必填 | Stream 名称 |
| Group | string | 必填 | 消费者组名称 |
| Consumer | string | 必填 | 消费者名称（建议全局唯一）|
| Count | int64 | 10 | 每次读取最大条数 |
| Block | time.Duration | 5s | 阻塞等待新消息时长（0=不阻塞）|
| ClaimIdle | time.Duration | 30s | pending 消息认领阈值（0=禁用认领）|
| ClaimCount | int64 | 10 | 每次认领最大条数 |
| AutoAck | bool | true | handler 返回 nil 时是否自动 ACK |

### 多消费者横向扩展

```go
// 启动多个 consumer 实例，同一条消息只会被一个 consumer 消费
for i := 0; i < 5; i++ {
    go func(id int) {
        consumer := rdb.NewStreamConsumer(core.StreamConsumerConfig{
            Stream:   "orders",
            Group:    "order-workers",
            Consumer: fmt.Sprintf("worker-%d", id),
        })
        _ = consumer.Run(ctx, handler)
    }(i)
}
```

### 消息重试机制

```go
// handler 返回 error 时消息不 ACK，会进入 pending 列表
// ClaimIdle 超时后会被其他 consumer 认领重试

// 查看某条消息的重试次数（通过 pending 列表）
pendingExt, _ := rdb.Client.XPendingExt(ctx, &redis.XPendingExtArgs{
    Stream:   "orders",
    Group:    "order-workers",
    Start:    "-",
    End:      "+",
    Count:    100,
    Idle:     30 * time.Second,
}).Result()

for _, p := range pendingExt {
    fmt.Printf("id=%s idle=%d deliver=%d\n", p.ID, p.Idle, p.TimesDelivered)
}
```

### 删除消息

```go
// 删除已处理完成的消息（释放内存）
_ = rdb.XDel(ctx, "orders", "1672531200000-0", "1672531200001-0")
```
