
## Skill 2: core-model.md

```markdown
---
name: core-model
description: 定义 Core Framework 数据模型，基于 GORM
tags: [go, core-framework, model, gorm]
---

# Core Model 开发技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "创建一个数据模型"
- "定义数据库表结构"
- "添加 Model"
- "生成 GORM 模型"

## 核心规范

### 1. 基础模型

```go
package model

import "github.com/xs23933/core/v3"

// 所有模型必须嵌入 core.Model
type User struct {
    core.Model                     // 包含 ID, CreatedAt, UpdatedAt, DeletedAt
    
    // 业务字段
    Username string `json:"username" gorm:"size:64;uniqueIndex;not null"`
    Email    string `json:"email" gorm:"size:128;uniqueIndex"`
    Password string `json:"-" gorm:"size:255;not null"`  // json:"-" 隐藏敏感字段
    Age      int    `json:"age" gorm:"default:0"`
    Status   int    `json:"status" gorm:"default:1;index"`
    
    // 关联关系
    Profile   Profile   `json:"profile,omitempty" gorm:"foreignKey:UserID"`
    Orders    []Order   `json:"orders,omitempty" gorm:"foreignKey:UserID"`
}
```

### 2. 字段标签说明

| 标签                    | 说明                     | 示例                         |
| ----------------------- | ------------------------ | ---------------------------- |
| `gorm:"size:64"`        | 字段长度                 | `gorm:"size:64"`             |
| `gorm:"not null"`       | 非空约束                 | `gorm:"not null"`            |
| `gorm:"default:0"`      | 默认值                   | `gorm:"default:0"`           |
| `gorm:"uniqueIndex"`    | 唯一索引                 | `gorm:"uniqueIndex"`         |
| `gorm:"index"`          | 普通索引                 | `gorm:"index"`               |
| `gorm:"-"`              | 忽略字段                 | `gorm:"-"`                   |
| `json:"-"`              | JSON 序列化隐藏          | `json:"-"`                   |
| `validate:"required"`   | 验证必填                 | `validate:"required"`        |
| `validate:"email"`      | 验证邮箱格式             | `validate:"email"`           |
| `validate:"min=3"`      | 最小长度                 | `validate:"min=3"`           |

### 3. 关联关系

```go
// 一对一
type User struct {
    core.Model
    Profile Profile `gorm:"foreignKey:UserID"`
}

type Profile struct {
    core.Model
    UserID uint   `gorm:"index"`
    Avatar string `gorm:"size:255"`
}

// 一对多
type User struct {
    core.Model
    Posts []Post `gorm:"foreignKey:UserID"`
}

type Post struct {
    core.Model
    UserID uint   `gorm:"index;not null"`
    Title  string `gorm:"size:255"`
}

// 多对多
type User struct {
    core.Model
    Roles []Role `gorm:"many2many:user_roles;"`
}

type Role struct {
    core.Model
    Name string `gorm:"size:64;uniqueIndex"`
}
```

### 4. 钩子函数

```go
import (
    "golang.org/x/crypto/bcrypt"
    "github.com/xs23933/core/v3"
)

// BeforeCreate - 创建前
func (u *User) BeforeCreate(tx *core.DB) error {
    if u.Password != "" {
        hashed, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
        if err != nil {
            return err
        }
        u.Password = string(hashed)
    }
    return nil
}

// BeforeUpdate - 更新前
func (u *User) BeforeUpdate(tx *core.DB) error {
    // 更新逻辑
    return nil
}

// AfterFind - 查询后
func (u *User) AfterFind(tx *core.DB) error {
    u.Password = "" // 查询后清空密码
    return nil
}

// BeforeDelete - 删除前（软删除）
func (u *User) BeforeDelete(tx *core.DB) error {
    // 级联删除逻辑
    return nil
}
```

### 5. 自动迁移

```go
// 在 main.go 或初始化函数中
func InitDB() {
    db := core.Conn()
    
    // 自动迁移（开发环境）
    db.AutoMigrate(
        &User{},
        &Profile{},
        &Post{},
    )
}
```

### 6. 完整模板

```go
package model

import (
    "time"
    "github.com/xs23933/core/v3"
    "golang.org/x/crypto/bcrypt"
)

// User 用户模型
type User struct {
    core.Model
    Username  string    `json:"username" gorm:"size:64;uniqueIndex;not null" validate:"required,min=3,max=64"`
    Email     string    `json:"email" gorm:"size:128;uniqueIndex" validate:"omitempty,email"`
    Password  string    `json:"-" gorm:"size:255;not null"`
    Phone     string    `json:"phone" gorm:"size:20;index"`
    Avatar    string    `json:"avatar" gorm:"size:255"`
    Status    int       `json:"status" gorm:"default:1;index"` // 1:正常 2:禁用
    LastLogin time.Time `json:"last_login" gorm:"default:null"`
    
    // 关联
    Profile   *Profile  `json:"profile,omitempty" gorm:"foreignKey:UserID"`
    Orders    []Order   `json:"orders,omitempty" gorm:"foreignKey:UserID"`
}

// TableName 指定表名
func (User) TableName() string {
    return "users"
}

// BeforeCreate 创建前加密密码
func (u *User) BeforeCreate(tx *core.DB) error {
    if u.Password == "" {
        return nil
    }
    hashed, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
    if err != nil {
        return err
    }
    u.Password = string(hashed)
    return nil
}

// AfterFind 查询后清理敏感数据
func (u *User) AfterFind(tx *core.DB) error {
    u.Password = ""
    return nil
}

// CheckPassword 验证密码
func (u *User) CheckPassword(password string) bool {
    err := bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password))
    return err == nil
}
```

## 禁止事项

- ❌ 不使用 `core.Model` 直接定义 ID 字段
- ❌ 模型字段不添加 `json` 标签暴露密码
- ❌ 钩子函数中执行复杂查询
- ❌ 在模型中编写业务逻辑

## 输出要求

生成 Model 时必须包含：

1. ✅ 嵌入 `core.Model`
2. ✅ 必要的 GORM 标签
3. ✅ 可选的验证标签
4. ✅ JSON 标签（隐藏敏感字段）
5. ✅ 关联关系正确设置
```
