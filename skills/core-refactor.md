---
name: core-refactor
description: Core Framework 代码重构指南 — 文件拆分、函数迁移、GORM类型拆分、Map/Array 重构、性能优化
tags: [go, core-framework, refactor, migration, performance]
---

# Core Refactor 技能文档

## 触发条件

当用户请求以下内容时激活此 Skill：

- "拆分/重构 model.go / utils.go"
- "GORM Scan/Value 报错如何修复"
- "model.go 太大了，怎么拆分"
- "迁移到泛型分页 FindPageBy"
- "ClickHouse 查询卡死怎么排查"
- "Enum 类型 Scan 报错"

## 1. model.go vs utils.go 边界

核心原则：

- **model.go** — 只放数据库相关结构体、GORM 类型（Scan/Value/GormDataType）、分页查询函数
- **utils.go** — 放纯工具函数、泛型函数、与数据库无关的类型

### model.go 应该包含

```go
// 数据库连接管理
func NewModel(...) (map[string]*DB, error)
func openDB(...) (*DB, error)
func Conn(name ...string) *DB
func DBType(name ...string) string
type DB = gorm.DB
func Expr(...)

// 基础模型（含 BeforeCreate，GORM 钩子）
type Model struct { ... }
type Models struct { ... }
type SModels struct { ... }
type IModel struct { ... }

// GORM 自定义类型（必须实现 Scan/Value/GormDataType）
type UUID struct { ... }        // Scan/Value/GormDataType/GormDBDataType
type JSON json.RawMessage { ... }
type Money float64 { ... }
type IntMoney int64 { ... }
type Int int64 { ... }
type Date struct { ... }
type IntID uint { ... }
type Enum interface { ~uint8 }  // 枚举类型约束

// 分页结构体 + 函数
type Pages / NextPages / Page[T] / NextPage[T]
func FindPage / FindNext / FindPageBy / FindNextBy / Find
func Where(whr *Map, db ...*DB) (*DB, int, int)

// 事务
func WithTransaction(tx *DB, fn func(*DB) error) error

// ID 生成器实例
var SnID *sid.SnowflakeID
var XID *xid.Generator
func NewSnID() sid.ID
func NewXID() xid.ID
```

### utils.go 应该包含

```go
// 通用切片转换（与 DB 无关）
func ToStrings[T fmt.Stringer](...) []string
func ToAny[T any](...) []any
func ToStringsFromAny(...) []string
func ToUUIDsFromAny(...) []UUID
func SafeToUUIDs(...) []UUID
type HasUUID interface { GetUUID() UUID }
func ExtractUUIDs[T HasUUID](...) []UUID

// 枚举泛型工具
func EnumString[T Enum](...) string
func EnumFromString[T Enum](...) T
func EnumMarshalJSON[T Enum](...) ([]byte, error)
func EnumUnmarshalJSON[T Enum](...) (T, error)
func EnumMarshalText[T Enum](...) ([]byte, error)
func EnumUnmarshalText[T Enum](...) (T, error)

// 解析工具
func ParseMoney(val any) Money
func ParseIntMoney(val any) IntMoney

// UUID 工具函数（非 Scan/Value 方法）
func NewUUID() UUID
func UUIDFromString(s string) (UUID, error)
func MustUUID(s string) UUID
func ParseBytes(b []byte) (UUID, error)
```

### 从 model.go 迁移函数到 utils.go 的步骤

```bash
# 1. 确认函数不依赖 GORM 类型（Scan/Value 等）
# 2. 将函数代码从 model.go 复制到 utils.go
# 3. 从 model.go 删除原函数
# 4. 清理未使用的 import
go build ./...
go vet ./...
```

## 2. GORM 自定义类型 Scan 报错修复

### 问题现象

```
panic: interface conversion: interface {} is uint8, not int64
```

### 根因

ClickHouse Go 驱动对 `UInt8` 列返回 `uint8`，但 `consts.XXX.Scan()` 断言 `value.(int64)`。

### 修复方案

**方案 A：临时 struct 接收（推荐，不改生成代码）**

```go
// ❌ 直接 Scan 会 panic
var logs []entity.ClickLog
db.Raw(sql, args...).Scan(&logs)  // panic!

// ✅ 用临时 struct 接收后再转换
type clickLogRow struct {
    ID       core.UUID
    LinkID   uint64
    DeviceType uint8  // 用原生类型接收
    // ...
}
var rows []clickLogRow
db.Raw(sql, args...).Scan(&rows)

logs := make([]entity.ClickLog, len(rows))
for i, r := range rows {
    logs[i] = entity.ClickLog{
        ID:         r.ID,
        LinkID:     r.LinkID,
        DeviceType: consts.DeviceType(r.DeviceType),  // 显式转换
    }
}
```

**方案 B：修改 code generator，让 Scan 兼容多种类型**

```go
// 在生成的 Scan() 方法中
func (d *DeviceType) Scan(value any) error {
    switch v := value.(type) {
    case int64:
        *d = DeviceType(v)
    case uint8:
        *d = DeviceType(v)
    case uint16:
        *d = DeviceType(v)
    case uint32:
        *d = DeviceType(v)
    case float64:
        *d = DeviceType(int(v))
    default:
        return fmt.Errorf("unsupported Scan type for DeviceType: %T", value)
    }
    return nil
}
```

已用方案 A 修复的查询：`GetDeviceStatsByLink`、`GetBrowserStatsByLink`、`GetOSStatsByLink`、`GetClickLogsByLink`

## 3. ClickHouse 查询卡死排查

### 排查清单

```
1. PARTITION BY / ORDER BY 是否匹配查询条件？
   → 用 WHERE link_id=? AND click_time BETWEEN 查询
   → ORDER BY 必须是 (link_id, click_time) 才能命中索引

2. 是否用了 AutoMigrate 创建表？
   → AutoMigrate 默认 ORDER BY 不是你想要的
   → 必须手动写 DDL

3. Scan panic 后连接是否未关闭？
   → panic → 连接未正常归还连接池 → 后续请求拿脏连接 → 卡死
   → 修复：Recover + 正确关闭连接

4. SELECT * 列顺序是否和 struct 一致？
   → ClickHouse 列顺序可能变化
   → 必须显式列出列名
```

### 正确建表模板

```go
func createClickLogsTable(db *gorm.DB) error {
    return db.Exec(`CREATE TABLE IF NOT EXISTS click_logs (
        id          String,
        link_id     UInt64,
        date        Date,
        click_time  DateTime,
        ip          String,
        device_type UInt8,
        os_type     UInt8,
        browser_type UInt8
    ) ENGINE = MergeTree()
    PARTITION BY toYYYYMM(toDateTime(click_time))
    ORDER BY (link_id, date, click_time)
    TTL click_time + INTERVAL 90 DAY
    SETTINGS index_granularity = 8192`).Error
}
```

## 4. 分页函数迁移（旧 → 新）

### 旧版 FindPage / FindNext

```go
// 旧：any 版本，需类型断言
var users []User
result, err := core.FindPage(whr, &users)
// result.Data 是 any，已有数据

// 旧：NextPage
var users []User
result, err := core.FindNext(whr, &users)
```

### 新版 FindPageBy / FindNextBy（推荐）

```go
// 新：泛型版本，类型安全
var users []User
result, err := core.FindPageBy[User](whr, &users)
// result.Data 是 []User，无需类型断言

// 新：NextPage 泛型
var users []User
result, err := core.FindNextBy[User](whr, &users)
```

### 迁移步骤

```diff
- result, err := core.FindPage(whr, &users)
+ result, err := core.FindPageBy[User](whr, &users)

- result, err := core.FindNext(whr, &users)
+ result, err := core.FindNextBy[User](whr, &users)
```

## 5. Map 条件重构

### 旧写法（字符串拼接）

```go
// ❌ 不推荐
db.Where("status IN (?)", statuses).
   Where("name LIKE ?", "%"+name+"%").
   Order("created_at DESC").
   Find(&users)
```

### 新写法（core.Map）

```go
// ✅ 推荐
whr := &core.Map{
    "p":          1,
    "l":          20,
    "desc":       "created_at",
    "status IN":  []int{1, 2},
    "name*":      "tom",
}
result, err := core.FindPageBy[User](whr, &users)
```

## 6. 文件拆分建议

当单个文件超过 500 行时考虑拆分：

```
model.go          → model_base.go + model_types.go + model_query.go
utils.go          → utils_slice.go + utils_crypto.go + utils_net.go
handler.go        → handler_auth.go + handler_api.go
service.go        → service_user.go + service_order.go
```

### 拆分原则

1. 按职责拆分，不按字母顺序
2. 拆分后 import 循环依赖检查：`go build ./...`
3. 同一 package 内拆分不需要改 import path

## 7. 性能优化清单

```
□ 数据库查询是否加了索引？（检查 WHERE / ORDER BY 字段）
□ ClickHouse 查询是否命中 PARTITION / ORDER BY？
□ 分页查询是否用了 LIMIT + OFFSET（深分页用 cursor）
□ Redis 缓存是否设置了 TTL？
□ goroutine 是否正确关闭（context.Cancel）？
□ JSON 序列化是否用了 sonic（默认已启用）？
□ 大切片是否预分配容量（make([]T, 0, len)）？
□ 循环中是否有重复分配内存（string + 拼接）？
```

## 禁止事项

- ❌ 不把 GORM 自定义类型（Scan/Value）放到 utils.go
- ❌ 不把纯工具函数留在 model.go
- ❌ 不用 `AutoMigrate` 创建 ClickHouse 表
- ❌ 不在 `Scan()` 中只断言 `int64`，不兼容 `uint8`
- ❌ 不忽略 `go vet` 警告

## 输出要求

重构建议必须包含：

1. ✅ 问题根因分析
2. ✅ 推荐修复方案（代码）
3. ✅ 迁移步骤（旧 → 新）
4. ✅ 验证方式（`go build` / `go vet` / 单元测试）
