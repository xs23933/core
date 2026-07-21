---
name: core-utils
description: 使用 Core Framework 内置工具函数、Map/Array、加密、密码哈希和客户端信息解析
tags: [go, core-framework, utils, crypto, map, array]
---

# Core Utils 开发技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "使用 core.Map"
- "写分页筛选条件"
- "密码加密/校验"
- "AES 加密"
- "获取真实 IP"
- "文件上传路径"
- "数组去重/过滤"
- "对象池 / sync.Pool / 复用 buffer"
- "工具函数说明"

## 1. Map / Array

`core.Map` 是 `map[string]any` 的框架封装，支持 JSON、GORM `Value/Scan` 和安全取值。

```go
whr := &core.Map{
    "p":     1,
    "l":     20,
    "desc":  "created_at",
    "name*": "tom",
}

name := whr.GetString("name")
page := whr.GetInt("p", 1)
enabled := whr.GetBool("enabled")
ok := whr.Contains("desc")
```

常用方法：

| 方法 | 说明 |
| ---- | ---- |
| `GetString(k, def...)` | 读取字符串 |
| `GetInt(k, def...)` | 读取整数 |
| `GetBool(k)` | 读取布尔值 |
| `GetAs(k, &out)` | 将某个字段反序列化到结构体 |
| `UnmarshalTo(k, &out)` | 将字段内容转换到结构体 |
| `Contains(k)` | 判断 key 是否存在 |

`core.Array` 是 `[]any` 封装，适合 JSON 数组字段。

```go
arr := core.ParseAndDeduplicate("admin,user,admin")
roles := arr.String()
joined := arr.StringsJoin(",")
```

## 2. 查询条件 Map

`core.Where`、`FindPageBy`、`FindNextBy` 会识别 `core.Map` 中的特殊 key。

```go
whr := &core.Map{
    "p":          1,
    "l":          20,
    "desc":       "created_at",
    "status IN":  []int{1, 2},
    "age >":      18,
    "name*":      "tom",
    "^email":     "admin",
    "deleted !=": 1,
}
```

| key 写法 | SQL 语义 |
| -------- | -------- |
| `asc` | 升序排序 |
| `desc` | 降序排序 |
| `field IN` | `field IN (?)` |
| `field NOTIN` | `field NOT IN (?)` |
| `field >` / `<` / `>=` / `<=` | 比较查询 |
| `field*` | 包含匹配 `%value%` |
| `^field` | 前缀匹配 `value%` |
| `field$` | 后缀匹配 `%value` |
| `field !=` | 不等于 |

## 3. 切片工具

```go
core.Contains([]string{"a", "b"}, "a")
core.Index([]int{1, 2, 3}, 2)
core.Remove([]int{1, 2, 3}, 2)
core.RemoveAll([]string{"a", "b", "a"}, "a")
core.Unique([]string{"a", "a", "b"})
core.Filter(users, func(u User) bool { return u.Active })
```

适用原则：

- 简单去重、过滤、删除优先使用框架工具。
- 不要为了一行 slice 操作引入额外第三方库。

## 4. 文件与路径

```go
rel, abs, err := core.MakePath("avatar.png", "./uploads")
if err != nil {
    return err
}

if !core.Exists(abs) {
    return core.NewError(404, "file not found")
}

fs := core.Dir("./static", false) // false 禁止目录列表
```

## 5. 网络与客户端信息

```go
ip, err := core.LocalIP()
clientIP := core.RemoteIP(req.Header, req.RemoteAddr)
domain := core.ExtractPrimaryDomain("api.example.com")
```

gRPC 场景：

```go
info := core.ExtractClientInfo(ctx)
ua := core.GrpcHeader(ctx, "user-agent")
```

`RemoteIP` 的优先级：

1. `Cf-Connecting-Ip`
2. `X-Real-Ip`
3. `X-Forwarded-For` 的第一个 IP
4. `RemoteAddr`

## 6. 错误工具

```go
err := core.NewError(40001, "invalid token")
code, msg := err.Errors()
```

推荐：

- 业务错误使用 `core.NewError(code, msg)`。
- Handler 中使用 `c.ToJSON(data, err)` 统一输出。

## 7. 加密与密码

### AES-GCM

```go
encrypted, err := core.EncryptAES("secret")
plain, err := core.DecryptAES(encrypted)

encryptedBytes, err := core.EncryptAESBytes([]byte("secret"))
plainBytes, err := core.DecryptAESBytes(encryptedBytes)
```

注意：

- AES 使用全局 `core.AESKey`。
- 生产环境必须在启动时设置安全的 32 字节密钥。
- 不要把默认 `AESKey` 用于生产环境。

### bcrypt 密码哈希

```go
hash, err := core.HashPassword("password")
ok := core.CheckPassword("password", hash)
```

规则：

- 密码存储只能使用 bcrypt。
- 禁止保存明文密码。
- 禁止用 SHA-256 直接存密码。

### SHA-256

```go
hash := core.SHA256Hash("user@example.com")
routeID := core.SHA256("route-key")
```

适用场景：

- 邮箱、手机号等索引哈希。
- 路由 ID、缓存 key、去重 key。
- 不适合密码存储。

## 8. 通用类型转换（从 model.go 迁移）

```go
// 切片转换
strs := core.ToStrings(uuids)         // []T:fmt.Stringer → []string
anySlice := core.ToAny(ids)           // []T → []any
strs := core.ToStringsFromAny(anys)   // []any → []string

// UUID 集合
uuids := core.ToUUIDsFromAny(anys)    // []any → []UUID
uuids := core.SafeToUUIDs(val)        // any → []UUID（安全）
uuids := core.ExtractUUIDs(items)     // []T{GetUUID()} → []UUID
```

## 9. Enum 枚举泛型（从 model.go 迁移）

```go
type DeviceType uint8

var DeviceTypeMap = []string{"Unknown", "Mobile", "Desktop", "Tablet"}

func (d DeviceType) String() string       { return core.EnumString(d, DeviceTypeMap) }
func DeviceTypeFromString(s string) DeviceType { return core.EnumFromString[DeviceType](s, DeviceTypeMap) }
func (d DeviceType) MarshalJSON() ([]byte, error)  { return core.EnumMarshalJSON(d, DeviceTypeMap) }
func (d *DeviceType) UnmarshalJSON(data []byte) error {
    val, err := core.EnumUnmarshalJSON[DeviceType](data, DeviceTypeMap)
    if err != nil { return err }
    *d = val
    return nil
}
```

## 10. 解析工具（从 model.go 迁移）

```go
m := core.ParseMoney(anyValue)     // any → Money
im := core.ParseIntMoney(anyValue) // any → IntMoney
```

## 11. 对象池（utils 包）

`utils` 包提供三类对象池，封装 `sync.Pool` 并提供类型安全的 API。不要在业务代码中直接使用 `sync.Pool` 重复造轮子。

### 11.1 通用泛型池 `utils.Pool[T]`

适用于任意需要复用的对象（`*struct`、`*Decoder` 等）。

```go
import "github.com/xs23933/core/v3/utils"

var decoderPool = utils.NewPool(func() *schema.Decoder {
    d := schema.NewDecoder()
    d.IgnoreUnknownKeys(true)
    return d
})

decoder := decoderPool.Get()        // 返回 *schema.Decoder，无需类型断言
defer decoderPool.Put(decoder)
```

### 11.2 Buffer 池 `utils.BufferPool`

复用 `*bytes.Buffer`，`Get` 时自动 `Reset`。

```go
var pool = utils.NewBufferPool()

buf := pool.Get()                   // 已 Reset，可直接使用
defer pool.Put(buf)
buf.WriteString("hello")
result := buf.String()
```

### 11.3 字节切片池 `utils.BytePool`

复用 `*[]byte`，支持归还时的容量上限保护，避免偶发大缓冲区常驻池中导致内存膨胀。

```go
// 不限制归还上限
var pool = utils.NewBytePool(256)

// 归还时若 cap > 64KB 则丢弃（不归还）
var pool = utils.NewBytePoolWithMax(4096, 64*1024)

buf := pool.Get()                   // 返回 *[]byte，已重置为 len 0
defer pool.Put(buf)
*buf = append(*buf, data...)
```

### 11.4 使用原则

- 优先使用 `utils.BufferPool` / `utils.BytePool` / `utils.NewBytePoolWithMax`，避免直接写 `sync.Pool`。
- 池化对象只用于单次请求/操作生命周期，禁止跨请求保留状态。
- 归还前应清理对象状态（引用、map、slice 等），避免内存泄漏。
- `BytePool` 的 `maxCap` 仅在偶发大缓冲区场景使用，常态缓冲区大小稳定时可不设。
- 不要把池化对象传入 goroutine 后继续使用，先拷贝数据再归还。

## 禁止事项

- 不要手写重复的 slice 去重/过滤逻辑。
- 不要直接使用 `sync.Pool` 复用 `*bytes.Buffer` / `*[]byte` 等常见对象，使用 `utils.BufferPool` / `utils.BytePool`。
- 不要用 `fmt.Println` 调试工具函数结果，使用 `core.Info/Erro`。
- 不要把 `core.Ctx` 传入 goroutine 后再调用工具函数读请求。
- 不要在生产环境使用默认 AES 密钥。
- 不要用 SHA-256 替代 bcrypt。
- 不要把纯工具函数放到 model.go，应迁移到 utils.go。
