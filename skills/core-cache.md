---
name: core-cache
description: 使用 Core Framework cache 子包封装 Redis 缓存查询，支持 DB fallback、自动回填、singleflight 防击穿、空值缓存和 TTL 抖动
tags: [go, core-framework, redis, cache, dao, singleflight]
---

# Core Redis Cache 技能

## 触发条件

- "给 DAO 加缓存"
- "Redis cache wrapper"
- "无缓存查 DB 自动回填"
- "缓存击穿"
- "空值缓存"

## 1. 导入路径

```go
import "github.com/xs23933/core/v3/cache"
```

## 2. 推荐写法

为每类缓存域创建可复用 cache 实例，实例不绑定数据类型，业务调用传 `out` 指针决定当前类型。

```go
type UserVO struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

var userCache = cache.New(
    core.RConn("cache"),
    cache.Prefix("user:"),
    cache.TTL(10*time.Minute),
    cache.EmptyTTL(time.Minute),
    cache.Jitter(30*time.Second),
    cache.CacheNil(true),
)

func GetUser(ctx context.Context, id string) (UserVO, error) {
    var user UserVO
    err := userCache.Take(ctx, id, &user, func(ctx context.Context) (any, error) {
        return dao.GetUserByID(ctx, id)
    })
    return user, err
}
```

同一个实例可以缓存不同类型：

```go
var order OrderVO
err := userCache.Take(ctx, "order:"+orderID, &order, func(ctx context.Context) (any, error) {
    return dao.GetOrderByID(ctx, orderID)
})
```

## 3. 返回值写法

包级 `Get` 使用 `core.RConn()` 和默认 TTL，适合简单场景。

```go
user, err := cache.Get(ctx, "user:"+id, func(ctx context.Context) (UserVO, error) {
    return dao.GetUserByID(ctx, id)
})
```

复用已有实例时，用 `Load`：

```go
user, err := cache.Load[UserVO](userCache, ctx, id, func(ctx context.Context) (UserVO, error) {
    return dao.GetUserByID(ctx, id)
})
```

## 4. 空值缓存

默认识别 `cache.ErrNotFound`：

```go
return UserVO{}, cache.ErrNotFound
```

接入项目自己的 not found 错误：

```go
var userCache = cache.New(
    core.RConn("cache"),
    cache.Prefix("user:"),
    cache.CacheNil(true),
    cache.NotFound(func(err error) bool {
        return errors.Is(err, gorm.ErrRecordNotFound)
    }),
)
```

## 5. 删除缓存

更新或删除 DB 后必须删除缓存。

```go
if err := userCache.Delete(ctx, id); err != nil {
    return err
}
```

## 6. 行为规则

- `Take` / `Load` 先读 Redis，命中后直接 decode。
- 未命中时通过 `singleflight` 合并同进程并发 loader。
- loader 成功后自动写入 Redis。
- Redis 读失败时降级执行 loader。
- Redis 写失败不影响返回值。
- `Jitter` 用于给 TTL 增加随机抖动，降低同一时间大量 key 过期的概率。

## 7. 生成代码时避免

- 不要在每个请求里重复 `cache.New`。
- 不要把 `core.RConn()`、TTL、前缀散落到每个 DAO 方法里。
- 不要更新 DB 后忘记 `Delete`。
- 不要用业务零值代表不存在；用 not found error 配合 `CacheNil`。
