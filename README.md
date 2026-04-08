# Core Web Framework v3.1.1 - 完整帮助文档

## 概述

Core 是一个用于快速开发企业级 Go 应用程序的 Web 框架，包括 RESTful API、Web 应用和后端服务。它受到 Tornado、Sinatra 和 Koa (Node.js) 的启发，并具有一些 Go 特有的功能，如接口和结构体嵌入。

### 主要特性

- **自动路由注册** - 无需手动配置路由
- **Koa 风格开发** - 类似 Node.js Koa 框架的中间件链式调用
- **轻量级框架** - 简单易用，性能高效
- **多数据库支持** - MySQL、PostgreSQL、SQLite、ClickHouse
- **内置验证器** - 基于 go-playground/validator
- **JSON/XML 支持** - 内置 JSON 和 XML 响应处理
- **文件上传** - 支持单文件和多文件上传
- **WebSocket 支持** - 内置 WebSocket 处理
- **模板引擎** - 支持 HTML 模板渲染
- **中间件系统** - 灵活的中间件扩展机制

## 快速开始

### 安装

```bash
go get github.com/xs23933/core/v3
```

### 最小示例

```go
package main

import "github.com/xs23933/core/v3"

type Handler struct {
    core.Handler
}

func (Handler) Get(c core.Ctx) {
    c.SendString("Hello, World!")
}

func init() {
    core.RegHandle(new(Handler))
}

func main() {
    app := core.New()
    app.Listen(":8080")
}
```

## 项目结构

```
project/
├── main.go          # 应用入口
├── config.yaml      # 配置文件
├── handler/         # 处理器目录
│   └── handler.go   # 业务逻辑
├── models/          # 数据模型
│   └── models.go    # 数据库模型定义
├── middleware/      # 中间件
├── views/           # 模板文件
└── static/          # 静态文件
```

## 配置

### config.yaml 示例

```yaml
debug: true # 调试模式
network: tcp4 # 网络协议
listen: 8080 # 监听端口
prefork: false # 是否启用 prefork 模式

# RESTful 响应格式配置
restful:
  data: data # 数据字段名
  status: success # 状态字段名
  message: msg # 消息字段名

# 数据库配置
database:
  type: sqlite3 # 数据库类型: mysql, pg, sqlite3, clickhouse
  dsn: dat # 数据库连接字符串
  # MySQL 示例: user:password@tcp(localhost:3306)/dbname?charset=utf8mb4&parseTime=True&loc=Local
  # PostgreSQL 示例: host=localhost user=postgres password=postgres dbname=test port=5432 sslmode=disable
```

## 核心概念

### 1. 应用 (Core)

应用是框架的核心，负责管理路由、中间件和服务器。

```go
// 创建应用
app := core.New(core.LoadConfigFile("config.yaml"))

// 启动服务器
app.Listen()
```

### 2. 上下文 (Ctx)

上下文对象封装了 HTTP 请求和响应，提供了丰富的操作方法。

```go
func (Handler) Get(c core.Ctx) {
    // 获取请求信息
    method := c.Method()      // 请求方法
    path := c.Path()          // 请求路径
    ip := c.RemoteIP()        // 客户端 IP

    // 获取参数
    id := c.Params("id")              // 路径参数
    name := c.Query("name")           // 查询参数
    age := c.QueryInt("age", 18)      // 查询参数转int

    // 获取表单数据
    email := c.FormValue("email")

    // 获取请求头
    token := c.GetHeader("Authorization")

    // 设置响应头
    c.SetHeader("Content-Type", "application/json")

    // 发送响应
    c.SendString("Hello")
    c.JSON(map[string]any{"code": 200, "msg": "success"})
    c.SendStatus(404, "Not Found")
}
```

### 3. 处理器 (Handler)

处理器是业务逻辑的载体，通过嵌入 `core.Handler` 来获得框架功能。

```go
type UserHandler struct {
    core.Handler
}

// Init 定义Prefix 此Handler 下都加上了 /users group
func (UserHandler) Init() {
	core.Prefix("/users")
}

// GET /users
func (UserHandler) Get(c core.Ctx) {
    users, err := models.GetUsers()
    c.ToJSON(users, err)
}

// POST /users
func (UserHandler) Post(c core.Ctx) {
    var user models.User
    if err := c.ReadBody(&user); err != nil {
        c.ToJSON(nil, err)
        return
    }
    c.ToJSON(user, user.Save())
}

// GET /users/:id
func (UserHandler) Get_id(c core.Ctx) {
    id, err := c.ParamsUid("id")
    if err != nil {
        c.ToJSON(nil, err)
        return
    }
    user, err := models.GetUserByID(id)
    c.ToJSON(user, err)
}

// 自动注册路由
func init() {
    core.RegHandle(new(UserHandler))
}
```

### 4. 路由系统

框架支持自动路由注册，基于方法名和注释生成路由。

#### 路由规则

| 关键字 | 路由规则   | 结果     |
| ------ | ---------- | -------- |
| Param  | 参数关键词 | :param   |
| Params | 参数关键词 | :params? |
| \_id   | 下划线     | /:id     |

_Params 是可选关键词 即:params?_

- `Get` → `GET /`
- `GetParam` → `GET /:param`
- `GetParams` → `GET /:params?`
- `Get_id` → `GET /:id`
- `GetDetailParam` → `GET /detail/:param`
- `PostUser` → `POST /user`
- `PutUserParam` → `PUT /user/:param`

#### 路由注释

```go
// GetUser 获取用户信息
// get /user/:param
// @id: uid.UID 用户ID
func (Handler) GetUserParam(c core.Ctx) {
    id, err := c.ParamsUid("param")
	c.ToJSON(nil, err)
}
```

### 5. 数据模型

框架集成了 GORM，提供了便捷的数据库操作。

```go
package models

import "github.com/xs23933/core/v3"

type User struct {
    core.Model
    Username string `json:"username" gorm:"size:32;uniqueIndex"`
    Email    string `json:"email" gorm:"size:128;uniqueIndex"`
    Password string `json:"-" gorm:"size:96"`
    Age      int    `json:"age" gorm:"default:18"`
}

// 创建用户
func (u *User) Create() error {
    return core.DB().Create(u).Error
}

// 根据ID查询用户
func GetUserByID(id uid.UID) (user User, err error) {
    err = core.DB().First(&user, "id = ?", id).Error
    return
}

// 分页查询用户
func GetUsers(page, size int) (users []User, total int64, err error) {
    err = core.DB().Model(&User{}).Count(&total).Error
    if err != nil {
        return
    }
    offset := (page - 1) * size
    err = core.DB().Offset(offset).Limit(size).Find(&users).Error
    return
}

// 初始化数据库
func InitDB() {
    db := core.Conn()
    db.AutoMigrate(&User{})
}
```

### 6. 中间件

框架支持灵活的中间件系统。

```go
// 自定义中间件
func AuthMiddleware(next core.HandlerFunc) core.HandlerFunc {
    return func(c core.Ctx) error {
        token := c.GetHeader("Authorization")
        if token == "" {
            return c.SendStatus(401, "Unauthorized")
        }
        // 验证 token
        // ...
        return next(c)
    }
}

// 使用中间件
app.Use(AuthMiddleware)

// 路由级中间件
app.Get("/admin", AdminHandler, AuthMiddleware)
```

#### 内置中间件

```go
import (
    "github.com/xs23933/core/v3/middleware/requestid"
    "github.com/xs23933/core/v3/middleware/cors"
    "github.com/xs23933/core/v3/middleware/logger"
    "github.com/xs23933/core/v3/middleware/recover"
)

app.Use(requestid.New())  // 请求ID
app.Use(cors.New())       // CORS 支持
app.Use(logger.New())     // 请求日志
app.Use(recover.New())    // 异常恢复
```

### 7. 验证器

框架集成了 go-playground/validator，支持结构体验证。

```go
type LoginForm struct {
    Username string `json:"username" validate:"required,min=3,max=32"`
    Password string `json:"password" validate:"required,min=6,max=128"`
    Email    string `json:"email" validate:"omitempty,email"`
}

func (Handler) Login(c core.Ctx) {
    var form LoginForm
    if err := c.ReadBody(&form); err != nil {
        c.ToJSON(nil, err)
        return
    }

    // 自动验证
    if err := c.Validate(&form); err != nil {
        c.ToJSON(nil, err)
        return
    }

    // 业务逻辑
    // ...
}
```

### 8. 文件上传

```go
// 单文件上传
func (Handler) Upload(c core.Ctx) {
    file, err := c.FormFile("file")
    if err != nil {
        c.ToJSON(nil, err)
        return
    }

    // 保存文件
    relpath, abspath, err := c.SaveFile("file", "./uploads")
    if err != nil {
        c.ToJSON(nil, err)
        return
    }

    c.ToJSON(map[string]string{
        "filename": file.Filename,
        "path":     relpath,
        "size":     fmt.Sprintf("%d", file.Size),
    })
}

// 多文件上传
func (Handler) UploadMultiple(c core.Ctx) {
    files, err := c.SaveFiles("files", "./uploads")
    if err != nil {
        c.ToJSON(nil, err)
        return
    }

    c.ToJSON(files)
}
```

### 9. WebSocket 支持

```go
import "github.com/xs23933/core/v3/websocket"


func main() {
	app := core.New()
	app.GET("/ws", websocket.New().
		OnConnect(func(c *websocket.Conn) {
			c.Send([]byte("Welcome to WebSocket"))
		}).
		OnMessage(
			func(c *websocket.Conn, mt websocket.MessageType, b []byte) {
				c.SendWithType(mt, b) //reply
                core.Info("Received message: %s", string(b))
			},
		).OnClose(func(c *websocket.Conn) {
		println("Connection closed")
	}).Handler())
	app.Run()
}

```

### 10. 模板渲染

```go
import (
    "github.com/xs23933/core/v3/middleware/view"
    "github.com/xs23933/core/v3/middleware/view/html"
)

// 配置模板引擎
var viewEngine view.IEngine = html.NewHtmlView("./views", ".html", app.Debug)
app.Use(viewEngine)

// 渲染模板
func (Handler) Index(c core.Ctx) {
    c.Render("index", core.Map{
        "title": "首页",
        "users": []User{...},
    })
}
```

## 高级功能

### 1. 数据库事务

```go
func CreateUserWithProfile(db *core.DB, user User, profile Profile) error {
    return db.Transaction(func(tx *core.DB) error {
        if err := tx.Create(&user).Error; err != nil {
            return err
        }

        profile.UserID = user.ID
        if err := tx.Create(&profile).Error; err != nil {
            return err
        }

        return nil
    })
}
```

### 2. 连接多个数据库

```yaml
database:
  default:
    type: mysql
    dsn: user:pass@tcp(localhost:3306)/main
  log:
    type: clickhouse
    dsn: tcp://localhost:9000?database=logs
```

```go
// 使用默认数据库
db := core.Conn()

// 使用指定数据库
logDB := core.Conn("log")
```

### 3. 查询构建器

```go
// 链式查询
var users []User
err := core.Conn().
    Select("id, username, email").
    Where("age > ?", 18).
    Where("status = ?", "active").
    Order("created_at DESC").
    Limit(10).
    Offset(0).
    Find(&users).Error

// 原生 SQL
var count int
core.Conn().Raw("SELECT COUNT(*) FROM users WHERE age > ?", 18).Scan(&count)

// 子查询
subQuery := core.Conn().Model(&Order{}).Select("user_id").Where("amount > ?", 1000)
core.Conn().Where("id IN (?)", subQuery).Find(&users)
```

### 4. 事件钩子

```go
type User struct {
    core.Model
    Username string
    Password string
}

// 保存前钩子 具体可参照 gorm官方文档
func (u *User) BeforeSave(tx *core.DB) error {
    if u.Password != "" {
        hashed, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
        if err != nil {
            return err
        }
        u.Password = string(hashed)
    }
    return nil
}

// 查询后钩子
func (u *User) AfterFind(tx *core.DB) error {
    u.Password = "" // 隐藏密码字段
    return nil
}
```

## 错误处理

### 自定义错误处理

```go
// 全局错误处理
app.ErrorHandler = func(c core.Ctx, err error) {
    if e, ok := err.(*core.HTTPError); ok {
        c.SendStatus(e.Code, e.Message)
        return
    }

    // 记录错误日志
    core.Erro("请求处理失败: %v", err)

    // 生产环境隐藏详细错误
    if app.Debug {
        c.SendStatus(500, err.Error())
    } else {
        c.SendStatus(500, "Internal Server Error")
    }
}

// 业务错误
func (Handler) GetUser(c core.Ctx) {
    user, err := models.GetUserByID(id)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            c.SendStatus(404, "用户不存在")
            return
        }
        c.SendStatus(500, "服务器错误")
        return
    }
    c.ToJSON(user)
}
```

## 性能优化

### 1. 连接池配置

```yaml
database:
  type: mysql
  dsn: user:pass@tcp(localhost:3306)/dbname
  pool:
    max_idle: 10 # 最大空闲连接数
    max_open: 100 # 最大打开连接数
    max_lifetime: 1h # 连接最大生命周期
```

### 2. 启用 Prefork 模式

```yaml
prefork: true # 启用多进程模式，充分利用多核CPU
```

## 测试

### 单元测试

```go
package handler_test

import (
    "testing"
    "github.com/xs23933/core/v3"
)

func TestUserHandler(t *testing.T) {
    app := core.New()

    // 模拟请求
    req := core.TestRequest{
        Method: "GET",
        Path:   "/users/1",
    }

    resp := app.Test(req)

    if resp.StatusCode != 200 {
        t.Errorf("期望状态码 200，得到 %d", resp.StatusCode)
    }
}
```

### 集成测试

```http
@baseURL = http://localhost:8080
@contentType = application/json

### 创建用户
POST {{baseURL}}/users
Content-Type: {{contentType}}

{
    "username": "testuser",
    "email": "test@example.com",
    "password": "password123"
}

### 获取用户
GET {{baseURL}}/users/1

### 获取用户列表
GET {{baseURL}}/users?page=1&size=10
```

## 部署

### 1. 编译

```bash
go build -o app main.go
```

### 2. 使用 Systemd

```ini
# /etc/systemd/system/myapp.service
[Unit]
Description=My Core Application
After=network.target

[Service]
Type=simple
User=appuser
WorkingDirectory=/opt/myapp
ExecStart=/opt/myapp/app
Restart=always
RestartSec=10
Environment=GIN_MODE=release

[Install]
WantedBy=multi-user.target
```

### 3. 使用 Docker

```dockerfile
FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o app main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/app .
COPY --from=builder /app/config.yaml .
EXPOSE 8080
CMD ["./app"]
```

## 常见问题

### 1. 路由不生效

**问题**: 处理器方法已定义，但路由不生效。

**解决方案**:

- 确保处理器嵌入了 `core.Handler`
- 确保在 `init()` 函数中调用 `core.RegHandle()`
- 检查方法名是否符合路由规则
- 查看日志确认路由注册情况

### 2. 数据库连接失败

**问题**: 数据库连接失败，返回错误。

**解决方案**:

- 检查 `config.yaml` 中的数据库配置
- 确认数据库服务是否运行
- 检查连接字符串格式是否正确
- 查看数据库日志获取详细错误信息

### 3. 跨域问题

**问题**: 前端请求被浏览器阻止，提示跨域错误。

**解决方案**:

- 启用 CORS 中间件
- 配置正确的 CORS 选项

```go
import "github.com/xs23933/core/v3/middleware/cors"

app.Use(cors.New(cors.Options{
    AllowedOrigins: []string{"http://localhost:3000"},
    AllowedMethods: []string{"GET", "POST", "PUT", "DELETE"},
    AllowedHeaders: []string{"Content-Type", "Authorization"},
}))
```

### 4. 文件上传大小限制

**问题**: 上传大文件时失败。

**解决方案**:

- 调整最大文件大小限制

```go
app := core.New(core.Options{
    "max_body_size": "10MB", // 10MB 限制
})
```

## API 参考

### 核心方法

#### Ctx 接口主要方法

| 方法                    | 说明           | 示例                                              |
| ----------------------- | -------------- | ------------------------------------------------- |
| `Params(key)`           | 获取路径参数   | `c.Params("id")`                                  |
| `Query(key)`            | 获取查询参数   | `c.Query("name")`                                 |
| `FormValue(key)`        | 获取表单值     | `c.FormValue("email")`                            |
| `ReadBody(out)`         | 读取请求体     | `c.ReadBody(&user)`                               |
| `Validate(out)`         | 验证结构体     | `c.Validate(&form)`                               |
| `ToJSON(data, err)`     | 返回 JSON 响应 | `c.ToJSON(user, nil)`                             |
| `JSON(data)`            | 返回 JSON 响应 | `c.JSON(user)`                                    |
| `Type(ty)`              | 设置响应类型   | `c.Type("json")`                                  |
| `Send(msg)`             | 发送[]byte     | `c.Send([]byte( "Not Found"))`                    |
| `SendString(msg)`       | 发送状String   | `c.SendString("Not Found")`                       |
| `SendStatus(code, msg)` | 发送状态码     | `c.SendStatus(404, "Not Found")`                  |
| `SetHeader(key, value)` | 设置响应头     | `c.SetHeader("Content-Type", "application/json")` |
| `GetHeader(key)`        | 获取请求头     | `c.GetHeader("Authorization")`                    |
| `FormFile(key)`         | 获取上传文件   | `c.FormFile("file")`                              |
| `SaveFile(key, dst)`    | 保存上传文件   | `c.SaveFile("file", "./uploads")`                 |

#### 数据库操作

| 方法                     | 说明               | 示例                                 |
| ------------------------ | ------------------ | ------------------------------------ |
| `core.DB`                | gorm.DB别名        | `db *core.DB`                        |
| `core.Conn(name)`        | 获取指定数据库连接 | `core.Conn("log")`                   |
| `core.Transaction(fn)`   | 执行事务           | `core.Transaction(func(tx) { ... })` |
| `core.Find(out)`         | 查询数据           | `core.Find(&users)`                  |
| `core.First(out, where)` | 查询单条数据       | `core.First(&user, "id = ?", id)`    |

### 配置选项

| 选项            | 类型   | 默认值    | 说明             |
| --------------- | ------ | --------- | ---------------- |
| `debug`         | bool   | `false`   | 调试模式         |
| `listen`        | string | `":8080"` | 监听地址         |
| `network`       | string | `"tcp4"`  | 网络协议         |
| `prefork`       | bool   | `false`   | 是否启用 prefork |
| `max_body_size` | string | `"1MB"`   | 最大请求体大小   |
| `read_timeout`  | string | `"5s"`    | 读取超时         |
| `write_timeout` | string | `"10s"`   | 写入超时         |

## 最佳实践

### 1. 项目组织

```
project/
├── cmd/
│   └── server/
│       └── main.go          # 应用入口
├── internal/
│   ├── handler/             # 处理器
│   │   ├── user.go
│   │   └── auth.go
│   ├── model/               # 数据模型
│   │   ├── user.go
│   │   └── order.go
│   ├── service/             # 业务逻辑
│   │   ├── user_service.go
│   │   └── auth_service.go
│   └── middleware/          # 中间件
│       ├── auth.go
│       └── logger.go
├── pkg/
│   └── utils/               # 工具函数
├── configs/
│   └── config.yaml          # 配置文件
├── migrations/              # 数据库迁移
├── scripts/                 # 部署脚本
├── tests/                   # 测试文件
├── go.mod
└── go.sum
```

### 2. 错误处理

```go
// 定义业务错误
var (
    ErrUserNotFound = errors.New("用户不存在")
    ErrInvalidToken = errors.New("无效的令牌")
)

// 统一错误响应
func ErrorResponse(c core.Ctx, err error) {
    switch err {
    case ErrUserNotFound:
        c.SendStatus(404, err.Error())
    case ErrInvalidToken:
        c.SendStatus(401, err.Error())
    case gorm.ErrRecordNotFound:
        c.SendStatus(404, "记录不存在")
    default:
        core.Erro("未处理的错误: %v", err)
        c.SendStatus(500, "服务器内部错误")
    }
}

// 使用示例
func (Handler) GetUser(c core.Ctx) {
    user, err := services.GetUserByID(id)
    if err != nil {
        ErrorResponse(c, err)
        return
    }
    c.ToJSON(user)
}
```

### 3. 日志记录

```go
// 配置日志
import "github.com/xs23933/core/v3/middleware/logger"

app.Use(logger.New(logger.Options{
    Format: "${time} ${status} ${method} ${path} ${latency}\n",
    Output: os.Stdout,
}))

// 自定义日志级别
core.Info("用户登录成功: %s", username)
core.Warn("API 调用频繁: %s", ip)
core.Erro("数据库连接失败: %v", err)
core.D("请求参数: %v", params)
```

### 4. 性能监控

```go
import (
    "github.com/xs23933/core/v3/middleware/metrics"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

// 启用指标收集
app.Use(metrics.New())

// 暴露 Prometheus 指标
app.Get("/metrics", func(c core.Ctx) error {
    promhttp.Handler().ServeHTTP(c.Response(), c.Request())
    return nil
})
```

## 扩展开发

### 1. 开发自定义中间件

```go
package middleware

import "github.com/xs23933/core/v3"

func NewRateLimiter(maxRequests int, window time.Duration) core.HandlerFunc {
    store := make(map[string][]time.Time)
    mu := sync.Mutex{}

    return func(c core.Ctx) error {
        ip := c.RemoteIP().String()

        mu.Lock()
        defer mu.Unlock()

        // 清理过期记录
        now := time.Now()
        validWindow := now.Add(-window)
        validRequests := make([]time.Time, 0)
        for _, t := range store[ip] {
            if t.After(validWindow) {
                validRequests = append(validRequests, t)
            }
        }

        // 检查限制
        if len(validRequests) >= maxRequests {
            c.SetHeader("Retry-After", window.String())
            return c.SendStatus(429, "请求过于频繁")
        }

        // 记录本次请求
        validRequests = append(validRequests, now)
        store[ip] = validRequests

        return c.Next()
    }
}
```

### 2. 开发插件

```go
package plugin

import "github.com/xs23933/core/v3"

type Plugin struct {
    core.Handler
    config map[string]any
}

func New(config map[string]any) *Plugin {
    return &Plugin{
        config: config,
    }
}

func (p *Plugin) Install(app *core.Core) {
    // 注册路由
    app.Get("/plugin/status", p.Status)

    // 注册中间件
    app.Use(p.Middleware)

    // 初始化资源
    p.Init()
}

func (p *Plugin) Status(c core.Ctx) {
    c.ToJSON(map[string]any{
        "name":    p.config["name"],
        "version": p.config["version"],
        "status":  "running",
    })
}

func (p *Plugin) Middleware(c core.Ctx) error {
    // 插件中间件逻辑
    c.Set("plugin_data", p.config)
    return c.Next()
}

func (p *Plugin) Init() {
    // 初始化逻辑
    core.Info("插件 %s 已初始化", p.config["name"])
}
```

# Core/V3 框架路由自动注册规范

> 本文档描述 `github.com/xs23933/core/v3` 框架的路由自动注册机制。

---

## 一、命名约定

### 1.1 Handler 结构体

```go
type UserHandler struct {
    core.Handler  // 必须嵌入 core.Handler
    // ... 依赖字段
}
```

- Handler 结构体名以 `Handler` 结尾
- 框架自动注册时会去掉 `Handler` 后缀作为路由组名
- 例：`UserHandler` → 路由组前缀为 `/user`（可通过 `Init()` 覆盖）

### 1.2 方法命名规则

HTTP 方法前缀 + 路径片段（驼峰命名）：

| 方法名前缀 | HTTP 方法 |
| ---------- | --------- |
| `Get`      | GET       |
| `Post`     | POST      |
| `Put`      | PUT       |
| `Delete`   | DELETE    |
| `Patch`    | PATCH     |

---

## 二、路径转换规则（核心）

### 2.1 基本规则

| 字符特征               | 转换结果           | 示例                   |
| ---------------------- | ------------------ | ---------------------- |
| 大写字母开头           | 路径片段 `/xxx`    | `Profile` → `/profile` |
| 下划线 + 小写字母      | 路径参数 `:xxx`    | `_id` → `:id`          |
| `Param`（固定关键字）  | 路径参数 `:param`  | `Param` → `:param`     |
| `Params`（固定关键字） | 可选参数 `:param?` | `Params` → `:param?`   |

### 2.2 大写字母 = 路径分隔

每个大写字母开头的片段都会生成一个新的路径层级：

```go
// 方法名 → 路由
func (h *Handler) GetProfile(c core.Ctx) error {}
// GET /profile

func (h *Handler) GetCurrentUser(c core.Ctx) error {}
// GET /current/user

func (h *Handler) GetId(c core.Ctx) error {}
// GET /id  （Id 是路径片段，不是参数！）
```

### 2.3 下划线 = 自定义路径参数

下划线开头 + 小写字母 = 自定义路径参数名：

```go
// 方法名 → 路由
func (h *Handler) Get_id(c core.Ctx) error {}
// GET /:id

func (h *Handler) Get_userId(c core.Ctx) error {}
// GET /:userId

func (h *Handler) Get_id_Profile(c core.Ctx) error {}
// GET /:id/profile
```

### 2.4 固定关键字 Param / Params

`Param` 和 `Params` 是框架固定关键字：

```go
// 方法名 → 路由
func (h *Handler) GetParam(c core.Ctx) error {}
// GET /:param  （必选参数）

func (h *Handler) GetParams(c core.Ctx) error {}
// GET /:param?  （可选参数）

func (h *Handler) GetParam_Callback(c core.Ctx) error {}
// GET /:param/callback

func (h *Handler) GetParams_Detail(c core.Ctx) error {}
// GET /:param?/detail
```

> ⚠️ 关键区别：
>
> - `Param` → `:param`（必选）
> - `Params` → `:param?`（可选）
> - 参数名固定为 `param`，通过 `c.Params("param")` 获取

---

## 三、参数获取

### 3.1 固定关键字参数

```go
// 路由: GET /:param
func (h *Handler) GetParam(c core.Ctx) error {
    val := c.Params("param")  // 参数名固定为 "param"
    // ...
}

// 路由: GET /:param/callback
func (h *Handler) GetParam_Callback(c core.Ctx) error {
    provider := c.Params("param")  // 同样是 "param"
    // ...
}
```

### 3.2 自定义参数名

```go
// 路由: GET /:id
func (h *Handler) Get_id(c core.Ctx) error {
    id := c.Params("id")  // 参数名为 "id"
    // ...
}

// 路由: GET /:userId/profile
func (h *Handler) Get_userId_Profile(c core.Ctx) error {
    userId := c.Params("userId")  // 参数名为 "userId"
    // ...
}
```

---

## 四、路由前缀设置

### 4.1 默认前缀

框架根据 Handler 名称自动推断前缀：

```go
type UserHandler struct { core.Handler }
// 默认前缀: /user

type OAuthHandler struct { core.Handler }
// 默认前缀: /oauth
```

### 4.2 自定义前缀

在 `Init()` 方法中使用 `Prefix()` 设置：

```go
func (h *OAuthHandler) Init() {
    h.Prefix("/api/v1/oauth")
}
```

---

## 五、完整示例

### 5.1 OAuth Handler 示例

```go
type OAuthHandler struct {
    core.Handler
    oauthService *OAuthService
}

func (h *OAuthHandler) Init() {
    h.Prefix("/api/v1/oauth")
}

// GET /api/v1/oauth/:param
func (h *OAuthHandler) GetParam(c core.Ctx) error {
    provider := c.Params("param") // google, facebook, twitter, github
    // ...
}

// GET /api/v1/oauth/:param/callback
func (h *OAuthHandler) GetParam_Callback(c core.Ctx) error {
    provider := c.Params("param")
    // ...
}
```

**生成的路由**：

```
GET /api/v1/oauth/:param           → OAuthHandler.GetParam
GET /api/v1/oauth/:param/callback  → OAuthHandler.GetParam_Callback
```

**访问示例**：

| URL                                   | 匹配路由            | param 值     |
| ------------------------------------- | ------------------- | ------------ |
| `GET /api/v1/oauth/google`            | `GetParam`          | `"google"`   |
| `GET /api/v1/oauth/facebook/callback` | `GetParam_Callback` | `"facebook"` |
| `GET /api/v1/oauth`                   | 404                 | 必选参数缺失 |

### 5.2 User Handler 示例

```go
type UserHandler struct {
    core.Handler
}

func (h *UserHandler) Init() {
    h.Prefix("/api/v1/users")
}

// GET /api/v1/users
func (h *UserHandler) Get(c core.Ctx) error {}

// GET /api/v1/users/:id
func (h *UserHandler) Get_id(c core.Ctx) error {
    id := c.Params("id")
}

// GET /api/v1/users/:id/profile
func (h *UserHandler) Get_id_Profile(c core.Ctx) error {
    id := c.Params("id")
}

// GET /api/v1/users/current
func (h *UserHandler) GetCurrent(c core.Ctx) error {}

// POST /api/v1/users
func (h *UserHandler) Post(c core.Ctx) error {}

// PUT /api/v1/users/:id
func (h *UserHandler) Put_id(c core.Ctx) error {}

// DELETE /api/v1/users/:id
func (h *UserHandler) Delete_id(c core.Ctx) error {}
```

---

## 六、命名转换规则（toNamer）

框架内部使用 `toNamer` 函数进行命名转换：

**转换逻辑**：

1. 提取 HTTP 方法前缀（`Get`/`Post`/`Put`/`Delete`/`Patch`）
2. 遍历剩余字符：
   - 大写字母 → 新路径片段（转小写）
   - `_` + 小写字母 → 自定义路径参数
   - `Param`（关键字）→ `:param`
   - `Params`（关键字）→ `:param?`

**转换示例**：

| 方法名              | 解析过程                                  | 最终路由                |
| ------------------- | ----------------------------------------- | ----------------------- |
| `GetProfile`        | `Profile` → `/profile`                    | `GET /profile`          |
| `GetCurrentUser`    | `Current` + `User` → `/current/user`      | `GET /current/user`     |
| `Get_id`            | `_id` → `/:id`                            | `GET /:id`              |
| `Get_userId`        | `_userId` → `/:userId`                    | `GET /:userId`          |
| `Get_id_Profile`    | `_id` + `Profile` → `/:id/profile`        | `GET /:id/profile`      |
| `GetParam`          | `Param` → `/:param`                       | `GET /:param`           |
| `GetParam_Callback` | `Param` + `Callback` → `/:param/callback` | `GET /:param/callback`  |
| `GetParams`         | `Params` → `/:param?`                     | `GET /:param?`          |
| `GetParams_Detail`  | `Params` + `Detail` → `/:param?/detail`   | `GET /:param?/detail`   |
| `GetId`             | `Id` → `/id`                              | `GET /id`（不是参数！） |
| `PostLogin`         | `Login` → `/login`                        | `POST /login`           |

---

## 七、常见错误

### 7.1 混淆大写和下划线

```go
// ❌ 错误：期望 /:id，实际得到 /id
func (h *Handler) GetId(c core.Ctx) error {}
// GET /id  （Id 是路径片段，不是参数）

// ✅ 正确：使用下划线定义参数
func (h *Handler) Get_id(c core.Ctx) error {}
// GET /:id
```

### 7.2 混淆 Param 和 Params

```go
// ❌ 错误：期望可选参数，实际是必选
func (h *Handler) GetParam(c core.Ctx) error {}
// GET /:param  （必选）

// ✅ 正确：使用 Params 表示可选
func (h *Handler) GetParams(c core.Ctx) error {}
// GET /:param?  （可选）
```

### 7.3 Param 与自定义参数混用

```go
// ⚠️ 注意：Param 固定参数名为 "param"
func (h *Handler) GetParam(c core.Ctx) error {}
// GET /:param，用 c.Params("param") 获取

// 自定义参数名用下划线
func (h *Handler) Get_id(c core.Ctx) error {}
// GET /:id，用 c.Params("id") 获取
```

---

## 八、调试技巧

### 8.1 查看注册的路由

框架启动时会打印所有注册的路由：

```
[DBUG] AutoRoute handler.UserHandler
[DBUG] route: GET /profile > handler.UserHandler.GetProfile
[DBUG] route: POST /login > handler.AuthHandler.PostLogin
[DBUG] route: GET /api/v1/oauth/:param > handler.OAuthHandler.GetParam
```

### 8.2 常见问题排查

| 问题           | 原因                    | 解决方案                                    |
| -------------- | ----------------------- | ------------------------------------------- |
| 404 路由不匹配 | 参数必选但未提供        | 使用 `Params` 改为可选，或确保 URL 包含参数 |
| 参数值为空     | 使用 `Params` 但未传参  | 检查是否应该用 `Param` 改为必选             |
| 路由不是参数   | 用了大写开头（如 `Id`） | 改用下划线开头（如 `_id`）                  |
| 路由冲突       | 多个方法映射到同一路径  | 检查方法命名是否重复                        |

---

## 九、快速参考卡

```
┌─────────────────────────────────────────────────────────────┐
│  命名规则                                                    │
├─────────────────────────────────────────────────────────────┤
│  大写字母开头    → 路径片段 /xxx                              │
│  _xxx（下划线）  → 自定义路径参数 :xxx                        │
│  Param（关键字） → 路径参数 :param（必选）                    │
│  Params（关键字）→ 路径参数 :param?（可选）                   │
├─────────────────────────────────────────────────────────────┤
│  方法名               →  路由                               │
├─────────────────────────────────────────────────────────────┤
│  Get                  →  GET /                              │
│  GetProfile           →  GET /profile                       │
│  GetCurrent           →  GET /current                       │
│  GetCurrentProfile    →  GET /current/profile               │
│  Get_id               →  GET /:id       (自定义参数)        │
│  Get_userId           →  GET /:userId   (自定义参数)        │
│  Get_id_Profile       →  GET /:id/profile                   │
│  GetParam             →  GET /:param     (必选关键字)       │
│  GetParam_Callback    →  GET /:param/callback               │
│  GetParams            →  GET /:param?    (可选关键字)       │
│  GetParams_Detail     →  GET /:param?/detail                │
│  GetId                →  GET /id       (不是参数！)         │
│  Post                 →  POST /                             │
│  PostLogin            →  POST /login                        │
│  Put_id               →  PUT /:id                           │
│  Delete_id            →  DELETE /:id                        │
└─────────────────────────────────────────────────────────────┘

参数获取:
  - 关键字参数: c.Params("param")
  - 自定义参数: c.Params("id"), c.Params("userId") 等
```

## 版本历史

### v3.1.0 (当前版本)

- 支持 Go 1.24
- 性能优化，提升 30% 吞吐量
- 新增 WebSocket 支持
- 改进中间件系统
- 增强数据库支持

### v2.0

- 引入自动路由注册
- 支持多数据库连接
- 改进错误处理机制
- 新增文件上传功能

### v1.0

- 初始版本发布
- 基础路由功能
- 数据库集成
- 中间件支持

## 社区支持

- **GitHub**: https://github.com/xs23933/core
- **问题反馈**: https://github.com/xs23933/core/issues

## 贡献指南

欢迎贡献代码！请遵循以下步骤：

1. Fork 项目仓库
2. 创建功能分支 (`git checkout -b feature/amazing-feature`)
3. 提交更改 (`git commit -m 'Add some amazing feature'`)
4. 推送到分支 (`git push origin feature/amazing-feature`)
5. 创建 Pull Request

## 许可证

本项目采用 MIT 许可证。详见 [LICENSE](LICENSE) 文件。

---

_文档最后更新: 2026-03-31_
_Core Framework v3.0.0_
