
## Skill 1: core-handler.md

```markdown
---
name: core-handler
description: 创建 Core Framework RESTful Handler，遵循自动路由注册规范
tags: [go, core-framework, handler, restful]
---

# Core Handler 开发技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "创建一个 User Handler"
- "写一个 Core 接口"
- "添加 CRUD 路由"
- "实现 RESTful API"

## 核心规范

### 1. Handler 结构

```go
type XxxHandler struct {
    core.Handler
    // 可选：注入 service
    // service *xxx.XxxService
}

func init() {
    core.RegHandle(new(XxxHandler))
}
```

### 2. 路由命名规则

| 需求           | 方法名              | 生成路由             |
| -------------- | ------------------- | -------------------- |
| 列表查询       | `Get`               | `GET /`              |
| 详情查询       | `GetByID`            | `GET /:id`           |
| 创建           | `Post`              | `POST /`             |
| 更新           | `Put_id`            | `PUT /:id`           |
| 删除           | `Delete_id`         | `DELETE /:id`        |
| 子资源         | `GetByIDProfile`    | `GET /:id/profile`   |
| 自定义参数     | `GetParam`          | `GET /:param`        |
| 可选参数       | `GetParams`         | `GET /:param?`       |
| 静态路径       | `GetProfile`        | `GET /profile`       |
| 连字符         | `GetUser__Create`   | `GET /user-create`   |
| 点号           | `GetUser__dot__Html`| `GET /user.html`     |

### 3. 前缀设置

```go
func (h *XxxHandler) Init() {
    h.Prefix("/api/v1/xxx")  // 覆盖默认前缀
}
```

### 4. Handler 生命周期

Handler 可以按需实现以下生命周期方法：

| 方法 | 触发时机 | 用途 |
| ---- | -------- | ---- |
| `Init()` | Handler 注册/应用初始化阶段 | 设置 `Prefix`、初始化轻量配置 |
| `Preload(c core.Ctx) error` | 每个请求进入业务方法前 | Handler 级鉴权、上下文注入、审计 |
| `Start(app *core.Core) error` | 应用启动阶段 | 启动后台任务、预热缓存、连接外部依赖 |
| `Stop(app *core.Core) error` | 应用关闭阶段 | 释放资源、停止后台任务 |

```go
func (h *UserHandler) Init() {
    h.Prefix("/api/v1/users")
}

func (h *UserHandler) Preload(c core.Ctx) error {
    token := c.GetHeader("Authorization")
    if token == "" {
        return c.SendStatus(401, "unauthorized")
    }
    c.Set("token", token)
    return c.Next()
}

func (h *UserHandler) Start(app *core.Core) error {
    return nil
}

func (h *UserHandler) Stop(app *core.Core) error {
    return nil
}
```

生命周期规则：

- `Init()` 不能读取请求数据，只做注册期配置。
- `Preload()` 不放行时直接返回错误或响应；放行必须 `return c.Next()`。
- `Start()` 不能长期阻塞；后台任务要可停止。
- `Stop()` 用于清理资源，不要 panic。

### 5. 标准模板

```go
package handler

import (
    "github.com/xs23933/core/v3"
    "your-project/internal/service"
    "your-project/internal/dto"
)

type UserHandler struct {
    core.Handler
}

func init() {
    core.RegHandle(new(UserHandler))
}

func (h *UserHandler) Init() {
    h.Prefix("/api/v1/users")
}

func (h *UserHandler) Preload(c core.Ctx) error {
    return c.Next()
}

// GET /api/v1/users
func (h *UserHandler) Get(c core.Ctx) {
    page := c.QueryInt("page", 1)
    size := c.QueryInt("size", 20)
    
    users, total, err := service.GetUserList(page, size)
    c.ToJSON(core.Map{
        "list":  users,
        "total": total,
        "page":  page,
        "size":  size,
    }, err)
}

// GET /api/v1/users/:id
func (h *UserHandler) GetByID(c core.Ctx) {
    id := c.Params("id")
    user, err := service.GetUserByID(id)
    c.ToJSON(user, err)
}

// POST /api/v1/users
func (h *UserHandler) Post(c core.Ctx) {
    var req dto.CreateUserRequest
    if err := c.ReadBody(&req); err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    if err := c.Validate(&req); err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    user, err := service.CreateUser(&req)
    c.ToJSON(user, err)
}

// PUT /api/v1/users/:id
func (h *UserHandler) Put_id(c core.Ctx) {
    id := c.Params("id")
    var req dto.UpdateUserRequest
    if err := c.ReadBody(&req); err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    user, err := service.UpdateUser(id, &req)
    c.ToJSON(user, err)
}

// DELETE /api/v1/users/:id
func (h *UserHandler) Delete_id(c core.Ctx) {
    id := c.Params("id")
    err := service.DeleteUser(id)
    c.ToJSON(nil, err)
}
```

### 6. 禁止事项

- ❌ 手动调用 `app.GET()` 注册路由
- ❌ 在 Handler 中直接操作数据库
- ❌ 使用 `GetId` 期望得到 `/:id`（应使用 `Get_id` 或 `GetByID`）
- ❌ Handler 中包含复杂业务逻辑
- ❌ 在 `Preload()` 中忘记 `return c.Next()`
- ❌ 在 goroutine 中保存或复用 `core.Ctx`

### 7. 参数获取速查

```go
// 路径参数
id := c.Params("id")

// Query 参数
name := c.Query("name")
age := c.QueryInt("age", 18)
tags := c.QuerySlice("tags")  // ?tags=a&tags=b

// 请求体
var req CreateRequest
c.ReadBody(&req)

// 请求头
token := c.GetHeader("Authorization")

// 表单
email := c.FormValue("email")
```

### 8. 响应输出速查

```go
// 标准 JSON 响应 (自动处理错误)
c.ToJSON(data, err)

// 原生 JSON
c.JSON(map[string]any{"key": "value"})

// 字符串
c.SendString("Hello")

// 状态码
c.SendStatus(404, "Not Found")
```

## 输出要求

生成 Handler 时必须包含：

1. ✅ 结构体定义（嵌入 `core.Handler`）
2. ✅ `init()` 函数注册
3. ✅ 方法按命名规范命名
4. ✅ 使用 `c.ToJSON()` 返回响应
5. ✅ 错误处理完整
```
