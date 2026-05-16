# [AI Context] Core Framework v3.1.1

## 1. 框架身份
- **名称**: Core Framework
- **语言**: Go (Golang 1.24+)
- **模块**: `github.com/xs23933/core/v3`
- **类型**: 轻量级企业级 Web 框架 (类似 Koa.js, Sinatra)
- **核心特性**: 自动路由注册、GORM 集成、中间件、WebSocket、gRPC/etcd 网关。

### 1.1 框架设计哲学

Core Framework 优先级：

1. 简洁
2. 自动化
3. 约定优于配置
4. 高性能
5. 微服务友好
6. 低依赖
7. 可读性优先

生成代码时：
- 优先简洁方案
- 避免过度抽象
- 避免 Java 风格设计
- 避免过度 interface 化

## 2. 核心规范 (AI 强制规则)

### 2.1 自动路由规范
AI 在生成 Handler 时必须严格遵守以下命名约定，禁止手动配置 `router.GET(...)`。

- **Handler 结构体**: 必须以 `Handler` 结尾，并嵌入 `core.Handler`。
    ```go
    type UserHandler struct { core.Handler }
    ```
- **HTTP 方法映射**: 方法名首字母必须是 HTTP 方法 (`Get`, `Post`, `Put`, `Delete`, `Patch`, `All`)。
- **路径转换规则**:
    | 代码写法 | 生成路由 | 参数获取 |
    | :--- | :--- | :--- |
    | `func (h *H) Get` | `GET /` | - |
    | `func (h *H) GetProfile` | `GET /profile` | - |
    | `func (h *H) GetById` | `GET /:id` | `c.Params("id")` |
    | `func (h *H) Get_userId` | `GET /:userId` | `c.Params("userId")` |
    | `func (h *H) GetParam` | `GET /:param` (**必选**) | `c.Params("param")` |
    | `func (h *H) GetParams` | `GET /:param?` (**可选**) | `c.Params("param")` |
    | `func (h *H) GetParam_Callback` | `GET /:param/callback` | `c.Params("param")` |
    | `func (h *H) GetUser__Create` | `GET /user-create` (双下划线转连字符) | - |
    | `func (h *H) GetUser__dot__Html` | `GET /user.html` | - |
- **⚠️ 警告**: `GetId` (大写 I) 生成 `/id` (静态路由)，不是动态参数。动态参数必须使用下划线 `_id`。

### 2.2 项目结构规范
AI 推荐使用以下 DDD 分层结构：
```
/internal
├── handler/    (控制器：处理请求/响应，调用 service)
├── service/    (业务逻辑：事务操作，调用 dao/model)
├── model/      (数据模型：定义表结构)
├── dao/        (数据访问：复杂查询封装)
└── middleware/ (中间件)
```

### 2.3 禁止行为 (AI MUST NOT)

AI 生成代码时禁止：

#### 路由相关
- 禁止手动注册路由：
```go
app.Get(...)
router.GET(...)
http.HandleFunc(...)
````

* 禁止生成 gin/fiber/echo 风格代码。

* 禁止生成：

```go
func RegisterRoutes(...)
```

Core 使用自动路由注册机制。

#### Handler 相关

* 禁止直接操作：

```go
http.ResponseWriter
*http.Request
```

* 禁止在 handler 中：

  * 编写复杂业务逻辑
  * 操作数据库
  * 开启事务
  * 拼接 SQL
  * 调用多个 DAO

Handler 只负责：

* 参数解析
* 权限校验
* 调用 service
* 返回响应

#### 分层相关

禁止跨层调用：

* handler -> dao
* dao -> handler
* model -> handler
* model -> service

推荐：

```text
handler -> service -> dao -> model
```

#### 错误处理

禁止：

```go
panic(err)
log.Fatal(err)
fmt.Println(err)
```

禁止忽略错误：

```go
data, _ := xxx()
```

#### 状态相关

禁止：

* 使用全局变量保存请求状态
* 保存 core.Ctx 到全局
* 在 goroutine 中直接使用 core.Ctx

---

### 2.4 DTO / VO 规范

推荐使用：

```text
model -> 数据库存储结构
dto   -> 请求参数结构
vo    -> 响应结构
```

禁止直接返回数据库 model 给前端。

示例：

```go
type User struct {
    core.Model
    Username string
    Password string
}

type UserVO struct {
    ID       uint   `json:"id"`
    Username string `json:"username"`
}
```

推荐：

* handler 使用 dto 接收参数
* service 返回 vo
* model 不直接暴露给前端

---

### 2.5 Go 代码风格

推荐：

* 单一职责
* 小函数
* 显式错误处理
* 提前 return
* 文件按业务拆分
* 优先组合而非继承

推荐文件命名：

```text
user_handler.go
user_service.go
user_model.go
user_dao.go
user_dto.go
user_vo.go
```

禁止：

* 超大 handler
* 超大 service
* 一个文件多个业务
* 超长函数
* 滥用 interface

## 3. API 速查手册

### 3.1 上下文 (Ctx) 常用方法
```go
// 请求解析
c.Params("id")          // 路径参数 /user/:id
c.Query("name")         // Query 参数 ?name=john
c.FormValue("email")    // 表单参数
c.ReadBody(&user)       // 解析 JSON/XML Body

// 响应输出
c.ToJSON(data, err)     // 自动处理错误响应 {data: ..., msg: ...}
c.JSON(map[string]any{})// 原生 JSON
c.SendString("text")
c.SendStatus(http.StatusOK, "OK")

// 文件操作
file, _ := c.FormFile("file")
relPath, absPath, _ := c.SaveFile("file", "./uploads")
```

### 3.2 数据库操作 (基于 GORM)
- **连接**: `db := core.Conn()` (默认) 或 `core.Conn("log")` (多库)
- **模型**: 必须嵌入 `core.Model` (包含 `ID`, `CreatedAt`, `UpdatedAt`, `DeletedAt`)
    ```go
    type User struct {
        core.Model
        Username string `gorm:"uniqueIndex"`
    }
    ```
- **分页查询**:
    - **总数分页 (后台)**: `core.FindPageBy(whr, &users, tx)`
        - 支持参数: `p`(页码), `l`(数量), `desc`/`asc`(排序), `field*`(包含), `field IN`(范围)。
    - **滚动分页 (移动端)**: `core.FindNextBy(whr, &users, tx)`

- **钩子**: 支持 GORM 钩子 (`BeforeSave`, `AfterFind` 等)。

### 3.3 中间件
```go
// 使用内置中间件
app.Use(cors.New(app))
app.Use(logger.New())

// 自定义中间件
func Auth(next core.HandlerFunc) core.HandlerFunc {
    return func(c core.Ctx) error {
        token := c.GetHeader("Authorization")
        // ... 验证逻辑
        return next(c)
    }
}
```

### 3.4 gRPC 与网关
- **服务注册**: `app.EnableEtcdRegistry(&etcd.Options{...})` 自动开启 Reflection。
- **网关路由**: 网关自动将 gRPC 方法 (`PostLogin`, `GetUserById`) 转换为 HTTP RESTful 路由。
    - *规则*: `PostLogin` -> `POST /v1/auth/user/login`
    - *规则*: `GetUserById` -> `GET /v1/auth/user/:id`

### 3.5 事务规范

事务必须在 service 层处理：

```go
func CreateOrder(req *dto.CreateOrderDTO) error {
    return core.Conn().Transaction(func(tx *gorm.DB) error {

        order := model.Order{}

        if err := tx.Create(&order).Error; err != nil {
            return err
        }

        return nil
    })
}
```

禁止：

* handler 中开启事务
* dao 中提交事务
* 多层重复事务
* 跨 service 共享 tx

推荐：

* 一个 service 管理一个事务
* 使用 Transaction 包裹完整业务流程

---

### 3.6 错误处理规范

推荐：

```go
if err != nil {
    return err
}
```

Handler 中统一：

```go
c.ToJSON(data, err)
```

推荐：

```go
if err := service.CreateUser(&req); err != nil {
    c.ToJSON(nil, err)
    return
}
```

禁止：

```go
panic(err)
log.Fatal(err)
fmt.Println(err)
```

---

### 3.7 日志规范

统一使用：

```go
core.Logger.Info(...)
core.Logger.Warn(...)
core.Logger.Error(...)
```

禁止：

```go
fmt.Println(...)
log.Println(...)
```

日志要求：

* 必须包含错误上下文
* 不记录敏感信息
* 不打印密码/token

推荐：

```go
core.Logger.Error("create user failed", err)
```

---

### 3.8 Context 使用规范

`core.Ctx` 仅在当前请求生命周期有效。

禁止：

```go
go func() {
    c.JSON(...)
}()
```

禁止：

* 保存 c 到全局
* 请求结束后继续使用 c
* goroutine 中直接使用 c

异步任务必须提前提取数据：

```go
userID := c.Params("id")

go func(id string) {
    // 使用独立数据
}(userID)
```

---

### 3.9 长连接规范

Core 支持：

* WebSocket
* HTTP/2
* HTTP/3
* gRPC Stream
* Tunnel 长连接

生成代码时：

* 优先 stream
* 避免轮询
* 避免频繁短连接
* 避免阻塞主 handler

推荐：

```go
for {
    msg, err := conn.ReadMessage()
    if err != nil {
        return
    }

    // 异步处理
}
```

禁止：

```go
time.Sleep(...)
for {
}
```

---

### 3.10 gRPC 规范

Core 默认支持：

* etcd 服务注册
* gRPC Reflection
* HTTP -> gRPC 网关
* RESTful 自动转换

推荐：

```go
app.EnableEtcdRegistry(...)
```

禁止：

* 手动维护服务发现
* 手动拼接 grpc gateway 路由

gRPC 方法命名：

| 方法名         | HTTP 路由                  |
| ----------- | ------------------------ |
| PostLogin   | POST /v1/auth/user/login |
| GetUserById | GET /v1/auth/user/:id    |

---

### 3.11 数据库规范

模型必须嵌入：

```go
core.Model
```

推荐：

```go
type User struct {
    core.Model

    Username string `gorm:"uniqueIndex"`
}
```

禁止：

* SELECT *
* 原始 SQL 拼接
* 在 handler 中查询数据库

推荐：

* 使用 GORM
* 使用 DAO 封装复杂查询
* 使用分页接口

---

### 3.12 分页规范

后台管理：

```go
core.FindPageBy(...)
```

移动端：

```go
core.FindNextBy(...)
```

禁止：

* 手写 limit/offset
* 返回无限数据

推荐：

* 默认分页
* 最大限制 limit

## 4. 代码生成模板

### 模板 1: 标准 Handler (CRUD)
```go
package handler

import (
    "your-project/internal/model"
    "your-project/internal/service"
    "github.com/xs23933/core/v3"
)

type UserHandler struct {
    core.Handler
}

func (h *UserHandler) Init() {
    // 可选：覆盖默认路由前缀
    h.Prefix("/api/v1/users")
}

// GET /api/v1/users
func (h *UserHandler) Get(c core.Ctx) {
    users, err := service.GetUserList(c)
    c.ToJSON(users, err)
}

// GET /api/v1/users/:id
func (h *UserHandler) GetByID(c core.Ctx) {
    id := c.Params("id")
    user, err := service.GetUserByID(id)
    c.ToJSON(user, err)
}

// POST /api/v1/users
func (h *UserHandler) Post(c core.Ctx) {
    var user model.User
    if err := c.ReadBody(&user); err != nil {
        c.ToJSON(nil, err)
        return
    }
    err := service.CreateUser(&user)
    c.ToJSON(user, err)
}

// PUT /api/v1/users/:id
func (h *UserHandler) Put_id(c core.Ctx) {
    id := c.Params("id")
    var updates map[string]any
    c.ReadBody(&updates)
    err := service.UpdateUser(id, updates)
    c.ToJSON(nil, err)
}

// DELETE /api/v1/users/:id
func (h *UserHandler) Delete_id(c core.Ctx) {
    id := c.Params("id")
    err := service.DeleteUser(id)
    c.ToJSON(nil, err)
}
```

### 模板 2: 模型定义
```go
package model

import "github.com/xs23933/core/v3"

type Product struct {
    core.Model
    Name  string  `json:"name" gorm:"not null" validate:"required"`
    Price float64 `json:"price" validate:"gt=0"`
    Stock int     `json:"stock"`
}

func init() {
    // 自动迁移 (建议放在 main.go 或单独初始化函数)
    // core.Conn().AutoMigrate(&Product{})
}
```

### 模板 3: 配置文件 config.yaml
```yaml
debug: true
listen: ":8080"

database:
  type: mysql
  dsn: user:pass@tcp(127.0.0.1:3306)/db?charset=utf8mb4&parseTime=True&loc=Local
  pool:
    max_idle: 10
    max_open: 100

restful:
  data: data
  status: success
  message: msg
```

## 4.1 AI 错误示例

错误：

```go
router.GET("/user/:id", handler.GetUser)
```

正确：

```go
func (h *UserHandler) GetByID(c core.Ctx)
```

---

错误：

```go
func GetUser(c *gin.Context)
```

正确：

```go
func (h *UserHandler) GetByID(c core.Ctx)
```

---

错误：

```go
func (h *UserHandler) Get(c core.Ctx) {
    db.Find(&users)
}
```

正确：

```go
func (h *UserHandler) Get(c core.Ctx) {
    users, err := service.GetUsers()
    c.ToJSON(users, err)
}
```

---

错误：

```go
panic(err)
```

正确：

```go
return err
```

---

## 4.2 AI 生成优先级

生成代码时优先：

1. Core 原生风格
2. 自动路由
3. service 分层
4. GORM
5. RESTful
6. 微服务兼容
7. 高性能
8. 简洁代码

避免：

* Java 风格
* Spring 风格
* 过度设计
* 复杂抽象
* 过多 interface

---

## 4.3 推荐生成模式

推荐：

```text
handler
  -> service
      -> dao
          -> model
```

推荐：

* handler 简洁
* service 管业务
* dao 管查询
* model 只定义结构

推荐：

* RESTful 风格
* 小文件
* 小函数
* 显式错误处理

## 5 ⚡ 快速对比：Core vs Gin（防止 AI 混淆）

| 操作 | Core 方法 | ❌ 不要用 |
|-----|----------|----------|
| 路径参数 | `c.Params("id")` | `c.Param("id")` |
| JSON 绑定 | `c.ReadBody(&user)` | `c.ShouldBindJSON()` |
| 表单绑定 | `c.ReadBody(&form)` | `c.Bind(&form)` |
| 响应 JSON | `c.ToJSON(data, err)` | `c.JSON()` |
| 路径 UID | `c.ParamsUid("id")` | `c.Param("id")` |

## 6 🎯 最常用方法速记

```go
// 1. 获取参数
id := c.Params("id")                    // 路径参数
name := c.Query("name")                 // Query 参数
c.ReadBody(&req)                        // 请求体

// 2. 返回响应
c.ToJSON(data, err)                     // 标准 JSON 响应
c.SendString("ok")                      // 字符串响应
c.SendStatus(404, "Not Found")          // 状态码响应

// 3. 上下文存储
c.Set("user_id", uid)                   // 存储
uid := c.GetSid("user_id")              // 获取
```

## 7. 故障排查
- **Q**: 路由返回 404，但我写了 `GetId` 方法。
    - **A**: `GetId` 生成的是 `/id`。如果你想要 `/users/123`，必须写成 `GetByID`。
- **Q**: 数据库连接报错。
    - **A**: 检查 `config.yaml` 中的 `database.dsn` 格式。MySQL 必须包含 `parseTime=true`。
- **Q**: 如何获取 URL 中的可选参数？
    - **A**: 方法名中使用 `Params` 关键字，如 `GetParams` 生成 `/:param?`。

---
**AI 指令**: 当用户提到“创建 Core 接口”、“写一个 Handler”或“Core 框架路由”时，请严格遵循上述第 2 节和第 4 节的规范生成代码。