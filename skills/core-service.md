---
name: core-service
description: Core 业务逻辑与 DAO 边界、context 传播、DTO/VO 和事务
tags: [go, core-framework, service, dao, context, transaction]
---

# Core Service 开发技能

## 适用范围与规则

编写业务逻辑、事务、HTTP/gRPC 共享用例时使用。默认调用链为
`handler → service → dao → model`。不要为了单一实现增加 Repository/interface 层。

- Handler 解析和验证参数、鉴权后，传入 `c.StdContext()`；gRPC 直接传入 RPC context。
- Service 的公开业务方法以 `context.Context` 为首参数，不接收或保存 `core.Ctx`。
- 应用装配时通过 `app.Conn()` 注入数据库；DAO 持有实例依赖，不在请求中查找全局 `core.Conn()`。
- DAO 使用 `db.WithContext(ctx)`；事务边界由 Service 控制，将同一个 tx 传给参与事务的 DAO。
- Service 返回 DTO/VO。数据库 Model 只在 Service/DAO 内部流转，不直接作为 HTTP 响应。
- 分页复用 `core.FindPageBy` 或 `core.Finds`，大列表用 cursor，不手写 limit/offset。
- 不把数据库故障一律映射成 not found，不忽略查询、更新、提交或缓存失效错误。

## DAO：显式数据库与 context

以下示例分别位于业务项目的 dao/service 包，`model.User`、`vo.UserVO` 和
`vo.ToUserVO` 由该业务项目定义。

```go
package dao

import (
    "context"

    "github.com/xs23933/core/v3"
    "your-project/internal/model"
)

type UserDAO struct { db *core.DB }

func NewUserDAO(db *core.DB) *UserDAO { return &UserDAO{db: db} }

func (d *UserDAO) GetByID(ctx context.Context, id string) (*model.User, error) {
    var user model.User
    if err := d.db.WithContext(ctx).First(&user, "id = ?", id).Error; err != nil {
        return nil, err
    }
    return &user, nil
}

func (d *UserDAO) List(ctx context.Context, page, size int) (core.Page[model.User], error) {
    whr := core.Map{"p": page, "l": size, "desc": "created_at"}
    var users []model.User
    return core.FindPageBy(&whr, &users, d.db.WithContext(ctx))
}
```

## Service：业务校验与 VO

```go
package service

import (
    "context"
    "errors"

    "gorm.io/gorm"
    "your-project/internal/dao"
    "your-project/internal/vo"
)

var ErrUserNotFound = errors.New("user not found")

type UserService struct { users *dao.UserDAO }

func NewUserService(users *dao.UserDAO) *UserService {
    return &UserService{users: users}
}

func (s *UserService) GetUserByID(ctx context.Context, id string) (*vo.UserVO, error) {
    user, err := s.users.GetByID(ctx, id)
    if errors.Is(err, gorm.ErrRecordNotFound) { return nil, ErrUserNotFound }
    if err != nil { return nil, err }
    result := vo.ToUserVO(user)
    return &result, nil
}
```

分页 Service 应将 `core.Page[model.User]` 的 P/L/Total/Extra 复制到
`core.Page[vo.UserVO]` 并逐项转换 Data，不能把 Model 数组伪装成 VO。

## 事务与装配

事务从注入的实例数据库开启：`db.WithContext(ctx).Transaction(func(tx *core.DB) error { ... })`。
在闭包内创建 `dao.NewUserDAO(tx)` 等 DAO，全部数据库操作复用 tx；不要在 DAO
中另开事务或退回全局连接。外部 HTTP、NSQ 和跨服务 RPC 不能被本地数据库事务回滚，
必须按业务设计幂等或 Outbox。

单应用可以在 `core.New` 后装配 `dao.NewUserDAO(app.Conn())`。需要多个应用共存时，
使用 `core.RegHandleFactory(func() core.Module { return &UserHandler{} })` 创建独立 Handler，
在 `Prepare(app *core.Core) error` 中检查 `app.Conn()` 并装配 DAO/Service。必需依赖的预热也放在
Prepare，并传递 `app.Ctx`；完成后才允许发布 ready。旧 `Start` 保持异步，不能承担就绪屏障。

## 回归验证

- 取消 context 后，DAO 查询应收到取消错误。
- 同进程两个 app 的 DAO 应访问各自数据库，关闭其中一个不影响另一个。
- Service 单测覆盖业务错误映射；事务测试覆盖中途失败后整批回滚。
- 不在 Service 中处理 HTTP Header/Cookie/状态码，不把 Ctx 传入 goroutine。
