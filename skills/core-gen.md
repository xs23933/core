---
name: core-gen
description: 使用 core 内置 Enum 代码生成器，快速生成类型安全的枚举类型（含 GORM/JSON 支持）
tags: [go, core-framework, codegen, enum, generator]
---

# Core Enum 代码生成技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "创建枚举类型" / "生成 enum" / "添加 UserStatus 枚举"
- "枚举代码生成"
- "enum 模板"
- "批量生成常量"

---

## 方式一：YAML 配置 + CLI（推荐）

### 1. 安装 CLI

```bash
cd ~/mbp/work/core
go install ./cmd/coregen
# 或直接运行
go run ./cmd/coregen -f enums.yaml -o ./constants/
```

### 2. 编写 YAML 配置

```yaml
# enums.yaml
package: constants
enums:
  - name: UserStatus
    values:
      - none
      - active
      - disabled
      - deleted
  - name: DeviceType
    values:
      - none
      - web
      - ios
      - android
      - other
  - name: Lang
    values:
      - none
      - en
      - zh-CN
    sensitive: true   # UnmarshalJSON 区分大小写
```

### 3. 执行生成

```bash
coregen enum -f enums.yaml -o ./constants/
# 或指定 package
coregen enum -f enums.yaml -o ./constants/ -p constants
```

### 4. 生成结果

```
constants/
  gen_user_status.go
  gen_device_type.go
  gen_lang.go
```

---

## 方式二：`go:generate` 程序化调用

在项目中创建 `gen.go`：

```go
//go:build ignore

package main

import "github.com/xs23933/core/v3/gen"

func main() {
    gen.GenerateEnums(gen.EnumConfig{
        Package: "constants",
        Output:  "./",
        Enums: []gen.Enum{
            {
                Name:   "UserStatus",
                Values: []string{"none", "active", "disabled", "deleted"},
            },
            {
                Name:      "Lang",
                Values:    []string{"none", "en", "zh-CN"},
                Sensitive: true,
            },
        },
    })
}
```

运行：

```bash
go generate ./...
# 或直接运行
go run gen.go
```

---

## 生成代码结构

每个 `gen_xxx.go` 包含以下内容：

```go
// 1. 类型定义
type UserStatus uint8

// 2. 字符串映射表
var UserStatusMapping = []string{"none", "active", "disabled", "deleted"}

// 3. 常量（自动 iota）
const (
    USER_STATUS_NONE    UserStatus = iota
    USER_STATUS_ACTIVE
    USER_STATUS_DISABLED
    USER_STATUS_DELETED
)

// 4. 字符串 → 枚举
func UserStatusFromString(s string) UserStatus

// 5. GORM 支持
func (UserStatus) GormDBDataType(db *core.DB, field *schema.Field) string
func (UserStatus) GormDataType() string
func (v *UserStatus) Scan(value any) error    // ✅ 兼容 int64/uint8/float64
func (v UserStatus) Value() (driver.Value, error)
func (v UserStatus) GormValue(ctx, db) clause.Expr

// 6. JSON 序列化
func (v UserStatus) MarshalJSON() ([]byte, error)
func (v *UserStatus) UnmarshalJSON(data []byte) error

// 7. 工具方法
func (v UserStatus) Int() uint8
func (v UserStatus) String() string
func ParseUserStatus(in any) any   // 万能解析
```

---

## `sensitive` 字段说明

```yaml
sensitive: true   # UnmarshalJSON 不自动 ToLower
                   # 适用于区分大小写的值，如 "en" vs "EN"

sensitive: false  # （默认）UnmarshalJSON 自动 bytes.ToLower
                   # 适用于不区分大小写的场景
```

---

## Scan() 兼容性修复（重要）

旧模板的 `Scan()` 只支持 `int64`，在 ClickHouse 场景会 panic：

```go
// ❌ 旧版 — ClickHouse 返回 uint8 会 panic
func (v *UserStatus) Scan(value any) error {
    *v = UserStatus(value.(int64))  // panic: interface conversion
    return nil
}
```

新版已修复，兼容所有数据库驱动类型：

```go
// ✅ 新版 — 兼容 MySQL/PostgreSQL/ClickHouse
func (v *UserStatus) Scan(value any) error {
    switch val := value.(type) {
    case int64:   *v = UserStatus(uint8(val))
    case uint8:   *v = UserStatus(val)
    case uint16:  *v = UserStatus(uint8(val))
    case uint32:  *v = UserStatus(uint8(val))
    case float64: *v = UserStatus(uint8(val))
    case string:  *v = UserStatusFromString(val)
    case []byte: *v = UserStatusFromString(string(val))
    default:
        return fmt.Errorf("cannot scan %T into UserStatus", value)
    }
    return nil
}
```

---

## 在 model 中使用生成的枚举

```go
package model

import "your-project/constants"

type User struct {
    core.Model
    Status  constants.UserStatus `json:"status" gorm:"type:tinyint;default:0"`
    Lang    constants.Lang     `json:"lang" gorm:"type:tinyint;default:0"`
}

// 创建
user := User{
    Status: constants.USER_STATUS_ACTIVE,
    Lang:   constants.LANG_EN,
}

// 查询
var users []User
core.Conn().Where("status = ?", constants.USER_STATUS_ACTIVE).Find(&users)

// JSON 输入输出
// 输入:  {"status":"active","lang":"en"}
// 输出:  {"status":"active","lang":"en"}
```

---

## YAML 配置字段说明

```yaml
package: constants        # 生成文件的 package 名（必填）

enums:
  - name: UserStatus     # 类型名（Go 标识符，首字母大写）
    values:              # 枚举值列表（第一个必须是 "none"=0）
      - none
      - active
      - disabled
    sensitive: false    # 是否区分大小写（默认 false）
```

### values 命名规则

- 第一个值必须是 `"none"`（对应 iota = 0）
- 名称会出现在 JSON 序列化和 `String()` 输出中
- 含 `-` 的值（如 `zh-CN`）会自动转换为 `ZH_CN` 常量

---

## 禁止事项

- ❌ 不要在项目中手写 `Scan()` 只支持 `int64` 的枚举类型
- ❌ 不要修改生成的 `gen_*.go` 文件（标记了 `DO NOT EDIT`）
- ❌ 不要在 `values` 中省略 `"none"` 作为第一个值
- ❌ 不要在同一个 YAML 中混用不同 `package`（一次生成一个 package）

---

## 输出要求

生成枚举时必须：

1. ✅ 使用 `coregen` CLI 或 `gen.GenerateEnums()`
2. ✅ `values` 第一个元素是 `"none"`
3. ✅ 需要区分大小写时设置 `sensitive: true`
4. ✅ 生成后执行 `go build ./...` 和 `go vet ./...`
