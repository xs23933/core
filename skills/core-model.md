---
name: core-model
description: Core Framework 数据模型定义、自定义类型、分页查询、事务和数据库连接管理
tags: [go, core-framework, model, gorm, database, uuid, money]
---

# Core Model 开发技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "创建数据模型" / "定义表结构" / "添加 Model"
- "使用 UUID / Money / JSON / Date 类型"
- "分页查询" / "FindPage" / "Where 条件"
- "数据库事务" / "WithTransaction"
- "ClickHouse DDL" / "多数据库连接"
- "Enum 枚举类型" → 见 core-utils.md

## 1. 基础模型选择

Core 提供四种基础模型，按主键类型选择：

| 基础模型 | 主键类型 | 适用场景 |
| -------- | -------- | -------- |
| `core.Model` | `uid.UID`（12字节短ID） | 默认选择，URL友好 |
| `core.Models` | `core.UUID`（32字节hex） | 需要全局唯一性、分布式系统 |
| `core.SModels` | `sid.ID`（雪花ID，int64） | 高性能自增、排序需求 |
| `core.IModel` | `core.IntID`（uint自增） | 遗留系统、简单自增 |

```go
// 默认选择
type User struct {
    core.Model  // ID uid.UID + CreatedAt + UpdatedAt + DeletedAt
    Username string `json:"username" gorm:"size:64;uniqueIndex;not null"`
}

// 需要 UUID 主键
type Tenant struct {
    core.Models  // ID core.UUID + CreatedAt + UpdatedAt + DeletedAt
    Name string `json:"name" gorm:"size:128;not null"`
}

// 需要雪花ID
type Order struct {
    core.SModels  // ID sid.ID + CreatedAt + UpdatedAt + DeletedAt
    Amount core.Money `json:"amount"`
}
```

### BeforeCreate 自动填充

所有基础模型的 `BeforeCreate` 已内置主键自动生成，无需手动处理：

```go
// core.Model — 自动生成 uid.UID
func (m *Model) BeforeCreate(tx *DB) error {
    if m.ID.IsEmpty() { m.ID = uid.New() }
    return nil
}

// core.Models — 自动生成 UUID
func (m *Models) BeforeCreate(tx *DB) error {
    if m.ID.IsEmpty() { m.ID = NewUUID() }
    return nil
}

// core.SModels — 自动生成雪花ID
func (m *SModels) BeforeCreate(tx *DB) error {
    if m.ID == 0 { m.ID = SnID.MustGenerate() }
    return nil
}
```

## 2. 自定义数据库类型

### UUID（32字节hex，CHAR(32)）

```go
type Article struct {
    core.Models
    Title string    `json:"title" gorm:"size:255"`
    TagID core.UUID `json:"tag_id" gorm:"size:32;index"`
}

// 创建
id := core.NewUUID()

// 解析
u, err := core.UUIDFromString("a1b2c3d4...")
u := core.MustUUID("a1b2c3d4...")  // 解析失败返回 UuidNil

// 判空
if id.IsEmpty() { ... }

// 集合操作（定义在 utils.go）
ids := core.SafeToUUIDs(anyValue)       // any → []UUID，安全转换
ids := core.ExtractUUIDs(items)          // []T{GetUUID() UUID} → []UUID
strs := core.ToStrings(uuids)            // []UUID → []string
```

### JSON（json 字段）

```go
type Config struct {
    core.Model
    Data core.JSON `json:"data" gorm:"type:json"`
}

// 赋值
config.Data = core.JSON(`{"key":"value"}`)

// MySQL 自动 CAST AS JSON，PostgreSQL 使用 JSONB
```

### Money（浮点金额，DECIMAL）

> ⚠️ `Money` 已标记 Deprecated，新项目建议使用 `coins.Money`（coin 包）。现有代码可继续使用。

```go
type Product struct {
    core.Model
    Price core.Money `json:"price" gorm:"type:decimal(12,2)"`
}

// 运算
total := price.MulInt(3)        // 乘整数
each := total.DivInt(3)         // 除整数
sum := price1.AddInt(100)       // 加整数
diff := price1.SubInt(50)       // 减整数
fixed := price.ToFixed(2)       // 保留2位
rounded := price.ToRound(2)     // 四舍五入
floored := price.ToFloor(2)     // 向下取整
equal := price1.IsEqual(price2) // 精度比较

// 解析
m := core.ParseMoney(anyValue)
```

### IntMoney（整数金额，存储分，BIGINT）

```go
type Wallet struct {
    core.Model
    Balance core.IntMoney `json:"balance" gorm:"type:bigint"`
}

// 创建（元 → 分）
m := core.NewIntMoneyFromFloat(99.99)  // 9999

// 读取
yuan := m.Float64()  // 99.99

// 运算
sum := m1.Add(m2)
diff := m1.Sub(m2)
prod := m.MulInt(3)
quot := m.DivInt(3)

// 解析
m := core.ParseIntMoney(anyValue)
```

### Int（BIGINT 封装）

```go
type Stat struct {
    core.Model
    Count core.Int `json:"count" gorm:"type:bigint"`
}

count := core.Int(42)
n := count.Int64()
m := count.Int()
```

### Date（仅日期，time.Time 封装）

```go
type Schedule struct {
    core.Model
    StartDate core.Date `json:"start_date" gorm:"type:date"`
}

// JSON 序列化为 "2024-01-15"
// 数据库读写自动转换
```

## 3. 分页查询

### 经典分页（总条数 + 页码）

```go
// 新代码推荐
result, err := core.Finds[User](core.FindsParams{Where: whr, DB: db})

// 泛型版本（推荐）
result, err := core.FindPageBy[User](whr, &users)
// result.P, result.L, result.Total, result.Data

// any 版本
result, err := core.FindPage(whr, &users)
// result.P, result.L, result.Total, result.Data (any)
```

### 游标分页（Next/Prev）

```go
// 新代码推荐
result, err := core.Finds[User](core.FindsParams{Where: whr, DB: db, Mode: core.FindsModeNext})

// 泛型版本（推荐）
result, err := core.FindNextBy[User](whr, &users)
// result.P, result.L, result.Next, result.Prev, result.Data

// any 版本
result, err := core.FindNext(whr, &users)
```

### Where 条件构建

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
| `asc` / `desc` | 排序字段 |
| `field IN` | `IN (?)` |
| `field NOTIN` | `NOT IN (?)` |
| `field > / < / >= / <=` | 比较查询 |
| `field*` | `%value%` 包含 |
| `^field` | `value%` 前缀 |
| `field$` | `%value` 后缀 |
| `field !=` | 不等于 |
| `omitFields` | Omit 排除字段 |

### 简单查询

```go
// Find 最多返回 10000 条
err := core.Find(&users, &whr)
err := core.Find(&users, &whr, db)  // 指定连接
```

## 4. 数据库连接管理

### 多数据库连接

```go
// 默认连接
db := core.Conn()

// 命名连接
chDB := core.Conn("clickhouse")

// 查看连接类型
tp := core.DBType()          // "mysql"
tp := core.DBType("clickhouse")  // "clickhouse"
```

### 配置格式

```yaml
database:
  default:
    type: mysql
    dsn: "user:pass@tcp(127.0.0.1:3306)/db?charset=utf8mb4&parseTime=True"
    max_open_conns: 100
    max_idle_conns: 20
    conn_max_lifetime: 300s
  clickhouse:
    type: clickhouse
    dsn: "clickhouse://default:@127.0.0.1:9000/db?dial_timeout=10s"
```

支持的数据库类型：`mysql`、`pg`、`sqlite`/`sqlite3`、`clickhouse`

### ClickHouse 注意事项

- ⚠️ **禁止使用 `AutoMigrate`** 创建 ClickHouse 表，ORDER BY 不匹配会导致查询全表扫描卡死
- 必须手动写 DDL，指定 `PARTITION BY`、`ORDER BY`、`TTL`
- ClickHouse Go 驱动对 UInt8 列返回 `uint8`，自定义类型的 `Scan()` 需兼容（参考 nt5 clickLogRow 方案）
- `SELECT *` 列顺序可能与 struct 定义不一致，必须显式列出列名

```go
// ✅ 正确：手动 DDL
db.Exec(`CREATE TABLE IF NOT EXISTS click_logs (
    link_id UInt64,
    click_time DateTime,
    ...
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(click_time)
ORDER BY (link_id, click_time)
TTL click_time + INTERVAL 90 DAY`)

// ❌ 错误：AutoMigrate 不适用 ClickHouse
db.AutoMigrate(&entity.ClickLog{})
```

## 5. 事务

```go
err := core.WithTransaction(db, func(tx *core.DB) error {
    if err := tx.Create(&order).Error; err != nil {
        return err  // 自动 Rollback
    }
    if err := tx.Create(&log).Error; err != nil {
        return err  // 自动 Rollback
    }
    return nil  // 自动 Commit
})
```

- 函数返回 `error` → Rollback
- 函数返回 `nil` → Commit
- 函数 panic → Rollback + 记录错误日志

## 6. GORM 表达式

```go
// 原生表达式
db.Where("price > ?", core.Expr("GREATEST(?, 0)", basePrice))

// 在 Map 条件中
whr["amount >"] = core.Expr("?", 100)
```

## 7. ID 生成器

```go
// uid.UID — 12字节短ID（core.Model 默认）
id := core.NewUID()  // 内部自动调用，无需手动

// sid.ID — 雪花ID（core.SModels 默认）
id := core.NewSnID()

// xid.ID — 自定义分布式ID
id := core.NewXID()

// UUID — 32字节hex（core.Models 默认）
id := core.NewUUID()
```

## 禁止事项

- ❌ 不使用 `core.Model` 同时手动定义 `ID` 字段
- ❌ 不用 `AutoMigrate` 创建 ClickHouse 表
- ❌ 不在模型中写业务逻辑
- ❌ 不把密码明文存入数据库
- ❌ 不在 `BeforeCreate` 中执行复杂查询
- ❌ ClickHouse 查询不用 `SELECT *`，必须显式列名

## 输出要求

生成 Model 时必须包含：

1. ✅ 嵌入合适的基础模型（Model / Models / SModels / IModel）
2. ✅ 必要的 GORM 标签和 JSON 标签
3. ✅ 金额使用 `core.Money` 或 `core.IntMoney`
4. ✅ JSON 字段使用 `core.JSON`
5. ✅ ClickHouse 表写手动 DDL
