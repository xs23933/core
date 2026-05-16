## Skill 3: core-service.md

```markdown
---
name: core-service
description: 编写 Core Framework 业务逻辑层
tags: [go, core-framework, service, business-logic]
---

# Core Service 开发技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "写业务逻辑"
- "创建 Service 层"
- "实现事务处理"
- "编写 Service 方法"

## 核心规范

### 1. Service 结构

```go
package service

import (
    "context"
    "errors"
    "github.com/xs23933/core/v3"
    "your-project/internal/model"
    "your-project/internal/dao"
    "your-project/internal/dto"
    "your-project/internal/vo"
)

type UserService struct{}

var UserServiceApp = &UserService{}
```

### 2. 标准方法模板

```go
// GetUserList 获取用户列表
func (s *UserService) GetUserList(page, size int) ([]vo.UserVO, int64, error) {
    var users []model.User
    var total int64
    
    db := core.Conn()
    
    // 查询总数
    if err := db.Model(&model.User{}).Count(&total).Error; err != nil {
        return nil, 0, err
    }
    
    // 分页查询
    offset := (page - 1) * size
    if err := db.Offset(offset).Limit(size).Find(&users).Error; err != nil {
        return nil, 0, err
    }
    
    // 转换为 VO
    result := make([]vo.UserVO, len(users))
    for i, u := range users {
        result[i] = vo.ToUserVO(&u)
    }
    
    return result, total, nil
}

// GetUserByID 根据 ID 获取用户
func (s *UserService) GetUserByID(id string) (*vo.UserVO, error) {
    var user model.User
    
    db := core.Conn()
    if err := db.First(&user, "id = ?", id).Error; err != nil {
        return nil, err
    }
    
    return vo.ToUserVO(&user), nil
}

// CreateUser 创建用户
func (s *UserService) CreateUser(req *dto.CreateUserRequest) (*vo.UserVO, error) {
    user := &model.User{
        Username: req.Username,
        Email:    req.Email,
        Password: req.Password,
        Phone:    req.Phone,
    }
    
    db := core.Conn()
    if err := db.Create(user).Error; err != nil {
        return nil, err
    }
    
    return vo.ToUserVO(user), nil
}

// UpdateUser 更新用户
func (s *UserService) UpdateUser(id string, req *dto.UpdateUserRequest) (*vo.UserVO, error) {
    db := core.Conn()
    
    // 检查用户是否存在
    var user model.User
    if err := db.First(&user, "id = ?", id).Error; err != nil {
        return nil, err
    }
    
    // 更新字段
    updates := map[string]any{}
    if req.Username != "" {
        updates["username"] = req.Username
    }
    if req.Email != "" {
        updates["email"] = req.Email
    }
    if req.Phone != "" {
        updates["phone"] = req.Phone
    }
    
    if err := db.Model(&user).Updates(updates).Error; err != nil {
        return nil, err
    }
    
    // 重新查询
    db.First(&user, "id = ?", id)
    return vo.ToUserVO(&user), nil
}

// DeleteUser 删除用户
func (s *UserService) DeleteUser(id string) error {
    db := core.Conn()
    return db.Delete(&model.User{}, "id = ?", id).Error
}
```

### 3. 事务处理

```go
// 带事务的业务逻辑
func (s *UserService) CreateUserWithProfile(req *dto.CreateUserRequest, profileReq *dto.CreateProfileRequest) error {
    return core.Conn().Transaction(func(tx *core.DB) error {
        // 创建用户
        user := &model.User{
            Username: req.Username,
            Email:    req.Email,
            Password: req.Password,
        }
        if err := tx.Create(user).Error; err != nil {
            return err
        }
        
        // 创建用户档案
        profile := &model.Profile{
            UserID:    user.ID,
            Nickname:  profileReq.Nickname,
            Avatar:    profileReq.Avatar,
            Bio:       profileReq.Bio,
        }
        if err := tx.Create(profile).Error; err != nil {
            return err
        }
        
        return nil
    })
}
```

### 4. 调用 DAO 层

```go
// Service 调用 DAO
func (s *UserService) SearchUsers(keyword string, page, size int) ([]vo.UserVO, int64, error) {
    // 使用 DAO 封装复杂查询
    users, total, err := dao.UserDAO.Search(keyword, page, size)
    if err != nil {
        return nil, 0, err
    }
    
    result := make([]vo.UserVO, len(users))
    for i, u := range users {
        result[i] = vo.ToUserVO(&u)
    }
    return result, total, nil
}
```

### 5. Context 传递

```go
// 带 Context 的方法
func (s *UserService) GetUserByIDWithContext(ctx context.Context, id string) (*vo.UserVO, error) {
    var user model.User
    
    db := core.Conn().WithContext(ctx)
    if err := db.First(&user, "id = ?", id).Error; err != nil {
        return nil, err
    }
    
    return vo.ToUserVO(&user), nil
}
```

### 6. 错误处理

```go
// 定义业务错误
var (
    ErrUserNotFound     = errors.New("用户不存在")
    ErrUserAlreadyExist = errors.New("用户已存在")
    ErrInvalidPassword  = errors.New("密码错误")
)

func (s *UserService) Login(username, password string) (*vo.UserVO, error) {
    var user model.User
    
    db := core.Conn()
    if err := db.Where("username = ?", username).First(&user).Error; err != nil {
        return nil, ErrUserNotFound
    }
    
    if !user.CheckPassword(password) {
        return nil, ErrInvalidPassword
    }
    
    return vo.ToUserVO(&user), nil
}
```

### 7. 完整模板

```go
package service

import (
    "errors"
    "github.com/xs23933/core/v3"
    "your-project/internal/model"
    "your-project/internal/dto"
    "your-project/internal/vo"
)

type UserService struct{}

var UserSvc = &UserService{}

var (
    ErrUserNotFound = errors.New("user not found")
)

// GetUserList 获取用户列表
func (s *UserService) GetUserList(page, size int) (*core.Page[vo.UserVO], error) {
    var users []model.User
    
    tx := core.Conn().Model(&model.User{})
    
    whr := &core.Map{
        "p":    page,
        "l":    size,
        "desc": "created_at",
    }
    
    return core.FindPageBy(whr, &users, tx)
}

// GetUserByID 根据 ID 获取用户
func (s *UserService) GetUserByID(id uint) (*vo.UserVO, error) {
    var user model.User
    
    db := core.Conn()
    if err := db.First(&user, id).Error; err != nil {
        return nil, ErrUserNotFound
    }
    
    return vo.ToUserVO(&user), nil
}

// CreateUser 创建用户
func (s *UserService) CreateUser(req *dto.CreateUserRequest) (*vo.UserVO, error) {
    user := &model.User{
        Username: req.Username,
        Email:    req.Email,
        Password: req.Password,
    }
    
    db := core.Conn()
    if err := db.Create(user).Error; err != nil {
        return nil, err
    }
    
    return vo.ToUserVO(user), nil
}
```

## 分层调用规则

```
✅ 正确：
Service -> DAO -> Model
Service -> Model (简单查询)

❌ 错误：
Service -> Handler (反向依赖)
DAO -> Service (循环依赖)
Model -> Service (模型不应包含业务逻辑)
```

## 禁止事项

- ❌ Service 中处理 HTTP 请求/响应
- ❌ Service 中直接使用 `c *core.Ctx`
- ❌ Service 中返回数据库模型给 Handler（应返回 VO）
- ❌ 在 Service 中开启多个事务嵌套
- ❌ 忽略错误返回

## 输出要求

生成 Service 时必须包含：

1. ✅ 结构体定义
2. ✅ 方法参数使用 DTO
3. ✅ 返回值使用 VO
4. ✅ 错误处理完整
5. ✅ 事务在 Service 层管理
```