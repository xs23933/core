# Core Framework v3 技能模块总览（core-skills.md）

## 一、项目概述

### 1.1 框架简介

Core Framework v3 是一个轻量级企业级 Go Web 框架，模块路径为 `github.com/xs23933/core/v3`。框架遵循"约定优于配置"的设计哲学，提供自动路由注册、GORM 数据库集成、中间件链、WebSocket 实时通信、gRPC/etcd 微服务治理等核心能力，适用于构建高性能、可扩展的企业级应用。

### 1.2 技术栈

| 层级 | 技术选型 | 说明 |
| ---- | -------- | ---- |
| 语言 | Go 1.24+ | 高性能编译型语言 |
| Web 框架 | Core Framework v3 | 自研轻量级框架 |
| ORM | GORM v2 | 支持 MySQL/PostgreSQL/SQLite/ClickHouse |
| 缓存 | go-redis/v9 | Redis 客户端 |
| 消息队列 | go-nsq | NSQ 消息队列 |
| RPC | gRPC + Protobuf | 微服务间通信 |
| 服务发现 | etcd v3 | 服务注册与发现 |
| HTTP 客户端 | fetch 子包 | 封装外部 API 调用 |
| WebSocket | gorilla/websocket | 实时双向通信 |
| 序列化 | sonic | 高性能 JSON 序列化 |
| ID 生成 | uid + sid + xid + nid | 多种分布式 ID 方案 |
| 加密 | AES-GCM + bcrypt + SHA-256 | 安全加密体系 |

### 1.3 核心特性

- **自动路由注册**：Handler 方法名即路由，无需手动配置路由表
- **默认健康检查**：内置 `GET /health` 返回 `204 No Content`，业务同路径注册会覆盖默认实现
- **DDD 分层架构**：handler → service → dao → model 清晰分层
- **双协议兼容**：Service 层同时支持 HTTP 和 gRPC 调用
- **多数据库支持**：MySQL、PostgreSQL、SQLite、ClickHouse 统一 GORM 接口
- **服务治理**：内建 etcd 注册发现、gRPC 网关自动路由映射
- **中间件生态**：CORS、限流、Metrics、RequestID 开箱即用
- **缓存体系**：Redis 封装 + Cache 包装器（singleflight 防击穿、空值缓存）
- **消息队列**：NSQ Producer/Consumer 完整封装
- **文件处理**：单文件/多文件上传、自动校验、路径管理
- **日志系统**：分级日志 + LogMonitor + SSE EventHub 实时推送

---

## 二、技能模块索引

### 2.1 技能模块全景图

```
skills/
├── core-handler.md          # RESTful Handler 创建与自动路由注册
├── core-model.md            # 数据模型定义、GORM 类型、分页查询、事务
├── core-service.md          # 业务逻辑层编写（兼容 HTTP/gRPC）
├── core-middleware.md       # 中间件开发（CORS/限流/Metrics/RequestID）
├── core-grpc.md             # gRPC 服务端（注册/启动/etcd 注册/共用端口）
├── core-grpc-client.md      # gRPC 客户端（服务发现/连接复用/MustGrpcClient）
├── core-gateway.md          # HTTP→gRPC 网关（自动路由/管理接口/metadata）
├── core-websocket.md        # WebSocket 连接管理/按用户推送/广播/心跳
├── core-page.md             # 分页查询（经典分页/滚动分页/条件构建）
├── core-utils.md            # 工具函数（Map/Array/加密/密码/切片）
├── core-redis.md            # Redis 封装（String/JSON/Hash/Set/ZSet/Pipeline/Lua）
├── core-cache.md            # Redis Cache 包装器（DB fallback/singleflight/空值缓存）
├── core-nsq.md              # NSQ 消息队列（Producer/Consumer/JSON/超时）
├── core-logger.md           # 日志系统（分级/Recovery/LogMonitor/EventHub）
├── core-fetch.md            # HTTP API 客户端（Header/Cookie/Hook/响应解包）
├── core-fileupload.md       # 文件上传（SaveFile/FormFile/ReadBody 绑定/校验）
├── core-ctx.md              # Ctx 上下文使用规范（避坑指南/场景模板）
├── core-test.md             # 测试编写（Handler/路由/中间件/Ctx 模拟）
└── core-refactor.md         # 代码重构指南（文件拆分/迁移/性能优化）
```

### 2.2 模块详情

#### core-handler — RESTful Handler 创建

**功能概述**：Core Framework 的 HTTP 请求处理层，基于自动路由注册机制，Handler 方法名自动映射为 RESTful 路由，无需手动配置路由表。

**核心特性**：
- 方法名自动路由映射（Get/Post/Put/Delete + 路径规则）
- 默认 `GET /health` 存活探测，可由业务路由覆盖
- Prefix 前缀设置，`Init()` 生命周期钩子
- `Preload()` 请求前置拦截（鉴权/上下文注入）
- `ReadBody()` 请求体解析 + `Validate()` 参数校验
- `c.ToJSON(data, err)` 统一响应格式

**模块调用流程**：
```
HTTP 请求 → Preload（鉴权） → Handler 方法（参数解析/调用 Service） → ToJSON 响应
```

**参数说明**：

| 参数获取方法 | 用途 | 示例 |
| ----------- | ---- | ---- |
| `c.Params("id")` | 路径参数 `/users/:id` | `c.Params("id")` → `"123"` |
| `c.ParamsUid("id")` | 路径参数（UID 类型） | `c.ParamsUid("id")` → `uid.UID` |
| `c.Query("name")` | Query 参数 `?name=tom` | `c.Query("name")` → `"tom"` |
| `c.QueryInt("page", 1)` | Query 整数（含默认值） | `c.QueryInt("page", 1)` → `1` |
| `c.FormValue("email")` | 表单参数 | `c.FormValue("email")` |
| `c.ReadBody(&req)` | JSON Body 解析 | 反序列化到结构体 |
| `c.SaveFile("file", dir)` | 文件上传保存 | 返回相对/绝对路径 |

**返回值格式**：
```json
// 成功：c.ToJSON(data, nil)
{
  "data": { "id": "123", "name": "tom" },
  "msg": "ok",
  "status": true
}

// 失败：c.ToJSON(nil, err)
{
  "data": null,
  "msg": "user not found",
  "status": false
}
```

---

#### core-model — 数据模型定义

**功能概述**：基于 GORM 的数据模型定义规范，提供四种基础模型模板、自定义数据库类型（UUID/JSON/Money/Date）、分页查询、事务管理和多数据库连接。

**核心特性**：
- 四种基础模型：`core.Model`（uid.UID）/ `core.Models`（UUID）/ `core.SModels`（雪花ID）/ `core.IModel`（自增）
- `BeforeCreate` 自动填充主键
- 自定义类型：UUID（CHAR(32)）、JSON（json）、Money/IntMoney（金额）、Date（日期）
- 分页查询：`FindPageBy`（经典分页）/ `FindNextBy`（滚动分页）
- 条件构建：`core.Map` 支持精确/模糊/比较/IN/排序查询
- 事务：`core.WithTransaction(tx, fn)` 自动提交/回滚

**模块调用流程**：
```
Service → DAO → core.Conn() / core.FindPageBy(whr, &models, db) → GORM → DB
```

**参数说明（core.Map 条件）**：

| key 写法 | SQL 语义 | 示例 |
| -------- | -------- | ---- |
| `"name": "john"` | `WHERE name = 'john'` | 精确匹配 |
| `"name*": "john"` | `WHERE name LIKE '%john%'` | 包含匹配 |
| `"^name": "john"` | `WHERE name LIKE 'john%'` | 前缀匹配 |
| `"name$": "john"` | `WHERE name LIKE '%john'` | 后缀匹配 |
| `"age >": 18` | `WHERE age > 18` | 比较查询 |
| `"status IN": []int{1,2}` | `WHERE status IN (1,2)` | IN 查询 |
| `"p": 1` | 页码 | 第 1 页 |
| `"l": 20` | 每页数量 | 每页 20 条 |
| `"desc": "created_at"` | `ORDER BY created_at DESC` | 降序排序 |

**返回值格式**：
```json
// FindPageBy
{ "p": 1, "l": 20, "total": 128, "data": [...] }

// FindNextBy
{ "p": 1, "l": 20, "next": true, "prev": false, "data": [...] }
```

---

#### core-service — 业务逻辑层

**功能概述**：Service 层是业务逻辑的核心，设计为协议无关（同时兼容 HTTP 和 gRPC），通过调用 DAO 层完成数据操作，处理事务和业务规则。该层是唯一可以同时被 HTTP Handler 和 gRPC Handler 调用的业务层。

**核心特性**：
- 协议无关设计：不依赖 core.Ctx，仅使用 context.Context
- 输入使用 DTO 结构体，输出使用 VO 结构体
- 事务操作：`core.WithTransaction()` 自动 commit/rollback
- 依赖注入：通过 `NewXxxService()` 构造函数注入 DAO 和外部依赖
- 错误传播：使用标准 error 返回，由上层处理协议适配

**模块调用流程**：
```
                ┌─ HTTP Handler ──┐
HTTP 请求 ────→│  ReadBody (DTO)  │
               │  ToJSON (VO)     │──→ Service.GetUser(ctx, dto) → DAO → DB
               └─────────────────┘              ↑
                                                │
               ┌─ gRPC Handler ──┐              │
gRPC 请求 ────→│  proto → DTO     │─────────────┘
               │  VO → proto      │
               └─────────────────┘
```

**参数说明**：

| 层级 | 输入类型 | 输出类型 | 上下文 |
| ---- | -------- | -------- | ------ |
| Handler | `dto.XxxRequest` | `core.Ctx` 响应 | `core.Ctx` |
| Service | `dto.XxxRequest` / 基础类型 | `vo.XxxVO` + `error` | `context.Context` |
| DAO | `context.Context` + 查询条件 | `model.Xxx` + `error` | `context.Context` |

**返回值格式**：
```go
// Service 标准方法签名
func (s *UserService) GetUserByID(ctx context.Context, id string) (*vo.UserVO, error)
func (s *UserService) GetUserList(ctx context.Context, page, size int) ([]vo.UserVO, int64, error)
func (s *UserService) CreateUser(ctx context.Context, req *dto.CreateUserRequest) (*vo.UserVO, error)
func (s *UserService) UpdateUser(ctx context.Context, id string, req *dto.UpdateUserRequest) (*vo.UserVO, error)
func (s *UserService) DeleteUser(ctx context.Context, id string) error
```

---

#### core-middleware — 中间件开发

**功能概述**：Core Framework 中间件体系，提供 CORS 跨域、请求限流、Metrics 监控、RequestID 追踪等内置中间件，支持自定义中间件开发。

**核心特性**：
- 统一签名：`func(c core.Ctx) error`
- 放行规则：`return c.Next()`
- 内置中间件：requestid / cors / metrics / ratelimit
- 限流支持：固定窗口 / 滑动窗口，支持 Redis 分布式限流

**模块调用流程**：
```
HTTP 请求 → Middleware1 → Middleware2 → ... → Handler → Response
                 │            │
                 └─ c.Next() ─┘
```

---

#### core-grpc — gRPC 服务端

**功能概述**：Core Framework 的 gRPC 服务端支持，包括 proto service 注册、独立端口/共用端口部署、etcd 服务注册与发现、gRPC reflection。

**核心特性**：
- `RegisterGRPCService()` 注册 proto service
- HTTP/gRPC 同端口分流（基于 HTTP/2 + Content-Type）
- `EnableEtcdRegistry()` 一站式 etcd 注册 + discovery + reflection
- `errorWrapInterceptor` 自动将非 status.Error 包装为 Internal

**模块调用流程**：
```
app.New() → RegisterGRPCService() → EnableEtcdRegistry() → app.Run()
                    │                       │
            proto service 注册          etcd 注册 + discovery + reflection
```

---

#### core-grpc-client — gRPC 客户端

**功能概述**：基于 etcd 服务发现的 gRPC 客户端，支持 `GrpcClient`（返回 error）和 `MustGrpcClient`（启动期硬依赖）两种调用方式。

**核心特性**：
- 按服务名自动发现（etcd resolver）
- 连接复用，禁止每次请求创建新连接
- 推荐启动期创建 client 并注入 Service

**参数说明**：
```go
conn, err := app.GrpcClient("user-service")     // 返回 (*grpc.ClientConn, error)
conn := app.MustGrpcClient("user-service")       // 失败 panic，适合 main 启动期
```

---

#### core-gateway — HTTP→gRPC 网关

**功能概述**：自动将 etcd 中的 gRPC 服务映射为 HTTP 路由，通过 gRPC reflection 读取 proto 定义，按方法名自动生成 RESTful 路由。

**核心特性**：
- 自动路由命名（proto package + service + method → HTTP 路径）
- Metadata 透传（authorization/x-request-id/x-user-id）
- 管理接口：路由 CRUD（仅 loopback 访问）

**模块调用流程**：
```
业务服务 EnableEtcdRegistry → 网关 EnableEtcdDiscovery → NewEtcdGateway → 自动生成 HTTP 路由
```

---

#### core-page — 分页查询

**功能概述**：提供经典分页（FindPageBy，含总数）和滚动分页（FindNextBy，无 Count）两种分页方式，通过 `core.Map` 构建灵活的筛选条件。

**核心特性**：
- `FindPageBy`：适用于后台管理表格，返回 total
- `FindNextBy`：适用于移动端无限滚动，返回 next/prev
- 丰富的筛选操作符：精确/模糊/前缀/后缀/比较/IN/Omit

---

#### core-redis — Redis 封装

**功能概述**：基于 go-redis/v9 的 Redis 完整封装，支持单实例/多实例、String/JSON/Hash/Set/ZSet/List/BitMap/Pipeline/Lua/PubSub/Stream。

**核心特性**：
- 自动初始化（config.yaml 配置）
- 全局访问：`core.RConn()` / `core.RConn("cache")`
- 完整的 Redis 数据结构操作

**参数说明**：
```yaml
redis:
  addr: "127.0.0.1:6379"
  password: ""
  db: 0
  pool_size: 100
  min_idle_conns: 10
```

---

#### core-cache — Redis Cache 包装器

**功能概述**：数据库查询缓存的 Redis 包装器，支持 DB fallback、自动回填、singleflight 防击穿、空值缓存和 TTL 随机抖动。

**核心特性**：
- `cache.New()` 创建可复用实例
- `Take()` / `Load()` 先读 Redis，未命中执行 loader 并回填
- singleflight 合并同进程并发 loader
- `CacheNil(true)` 空值缓存防穿透
- `Jitter()` TTL 抖动降低批量过期

**模块调用流程**：
```
Take(ctx, key, &out, loader) → Redis Hit? → 返回
                                ↓ Miss
                              singleflight.Do → loader (DB) → 自动回填 Redis → 返回
```

---

#### core-nsq — NSQ 消息队列

**功能概述**：基于 go-nsq 的消息队列封装，提供 Producer/Consumer 一站式解决方案，支持 JSON 序列化、延迟消息、批量发布、带超时处理。

**核心特性**：
- 全局 Producer：`core.NProducer()` + 快捷函数（`NSQPublish`/`NSQPublishJSON`）
- Consumer Builder：链式创建 + 并发控制 + 优雅关闭
- JSON 消息自动序列化/反序列化
- `NSQHandlerWithTimeout` 带超时的消息处理

---

#### core-logger — 日志系统

**功能概述**：内置分级日志系统，支持终端彩色输出、HTTP 请求日志、Panic Recovery、LogMonitor 实时广播和 EventHub SSE 事件推送。

**核心特性**：
- 分级日志：D（Debug）/ Info / Warn / Erro / Log / Dump
- 终端彩色输出（TTY 自动检测）
- Recovery 中间件：自动捕获 panic → 500 响应 + 堆栈记录
- LogMonitor：日志实时广播给 Web 控制台
- EventHub：SSE 事件推送（支持批量聚合）

---

#### core-fetch — HTTP API 客户端

**功能概述**：独立的 HTTP 客户端子包，支持可复用 Client、公共/单次 Header、Cookie、请求签名 Hook、响应解包 Hook 和响应 Header 提取。

**核心特性**：
- 包级快捷函数：`fetch.Get/Post/Put/Delete`
- 可复用 Client：统一 baseURL + 公共 Header + Hook
- Before Hook：请求签名、鉴权、trace id
- After Hook：响应解密、统一 envelope 解包
- Cookie 管理：`UseCookie(true)` 开启会话

---

#### core-ctx — Ctx 上下文使用规范

**功能概述**：Core Ctx 使用规范，重点说明与 Gin 框架的关键差异，避免 AI 生成错误代码。

**核心注意**：
- ✅ `c.Params("id")` ← Core 风格（有 s）
- ❌ `c.Param("id")` ← Gin 风格（错误）
- ✅ `c.ReadBody(&req)` ← Core 风格
- ❌ `c.ShouldBindJSON(&req)` ← Gin 风格（错误）
- ✅ `c.ToJSON(data, err)` ← 统一响应
- ❌ `c.JSON(gin.H{...})` ← Gin 风格（错误）

---

#### core-test — 测试编写

**功能概述**：基于 `net/http/httptest` 的测试编写指南，覆盖 Handler 测试、路由测试、中间件测试、Ctx 模拟和文件上传测试。

**核心特性**：
- `httptest.NewRequest()` + `app.ServeHTTP()` 标准测试流程
- `app.AcquireCtx()` 直接构造上下文
- 路由注册/移除验证

---

## 三、完整使用规范

### 3.1 标准项目目录结构

```
your-project/
├── main.go                      # 应用入口
├── config.yaml                  # 配置文件
├── proto/                       # Protobuf 定义
│   └── user/v1/
│       └── user.proto
├── internal/
│   ├── handler/                 # HTTP Handler 层
│   │   └── user_handler.go
│   ├── grpc/                    # gRPC 服务实现层
│   │   └── user_grpc.go
│   ├── service/                 # 业务逻辑层（协议无关）
│   │   └── user_service.go
│   ├── dao/                     # 数据访问层
│   │   └── user_dao.go
│   ├── model/                   # 数据模型层
│   │   └── user_model.go
│   ├── dto/                     # 请求参数定义
│   │   └── user_dto.go
│   ├── vo/                      # 响应数据结构
│   │   └── user_vo.go
│   └── middleware/              # 自定义中间件
│       └── auth.go
└── go.mod
```

### 3.2 完整 CRUD 调用链路示例

以用户模块为例，展示 HTTP 和 gRPC 两种协议如何共享同一个 Service 层。

#### Step 1: 定义 Model（internal/model/user_model.go）

```go
package model

import "github.com/xs23933/core/v3"

type User struct {
    core.Model
    Username string `json:"username" gorm:"size:64;uniqueIndex;not null"`
    Email    string `json:"email" gorm:"size:128;uniqueIndex"`
    Password string `json:"-" gorm:"size:96;not null"`
    Phone    string `json:"phone" gorm:"size:20"`
    Status   int    `json:"status" gorm:"default:1"`
}

func (User) TableName() string {
    return "users"
}
```

#### Step 2: 定义 DTO/VO

```go
// internal/dto/user_dto.go
package dto

type CreateUserRequest struct {
    Username string `json:"username" validate:"required,min=3,max=64"`
    Email    string `json:"email" validate:"required,email"`
    Password string `json:"password" validate:"required,min=6,max=96"`
    Phone    string `json:"phone" validate:"omitempty,len=11"`
}

type UpdateUserRequest struct {
    Email  string `json:"email" validate:"omitempty,email"`
    Phone  string `json:"phone" validate:"omitempty,len=11"`
    Status *int   `json:"status" validate:"omitempty,oneof=0 1"`
}

// internal/vo/user_vo.go
package vo

type UserVO struct {
    ID        string `json:"id"`
    Username  string `json:"username"`
    Email     string `json:"email"`
    Phone     string `json:"phone"`
    Status    int    `json:"status"`
    CreatedAt string `json:"created_at"`
    UpdatedAt string `json:"updated_at"`
}

func ToUserVO(m *model.User) *UserVO {
    return &UserVO{
        ID:        m.ID.String(),
        Username:  m.Username,
        Email:     m.Email,
        Phone:     m.Phone,
        Status:    m.Status,
        CreatedAt: m.CreatedAt.Format("2006-01-02 15:04:05"),
        UpdatedAt: m.UpdatedAt.Format("2006-01-02 15:04:05"),
    }
}
```

#### Step 3: 定义 DAO（internal/dao/user_dao.go）

```go
package dao

import (
    "context"
    "github.com/xs23933/core/v3"
    "your-project/internal/model"
)

type UserDAO struct{}

var UserDAOApp = &UserDAO{}

func (d *UserDAO) Create(ctx context.Context, user *model.User) error {
    return core.Conn().WithContext(ctx).Create(user).Error
}

func (d *UserDAO) GetByID(ctx context.Context, id string) (*model.User, error) {
    var user model.User
    err := core.Conn().WithContext(ctx).First(&user, "id = ?", id).Error
    return &user, err
}

func (d *UserDAO) List(ctx context.Context, page, size int) ([]model.User, int64, error) {
    var users []model.User
    whr := &core.Map{"p": page, "l": size, "desc": "created_at"}
    result, err := core.FindPageBy(whr, &users, core.Conn().WithContext(ctx))
    return users, result.Total, err
}

func (d *UserDAO) Update(ctx context.Context, id string, updates map[string]any) error {
    return core.Conn().WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
}

func (d *UserDAO) Delete(ctx context.Context, id string) error {
    return core.Conn().WithContext(ctx).Delete(&model.User{}, "id = ?", id).Error
}
```

#### Step 4: 定义 Service（internal/service/user_service.go）

```go
package service

import (
    "context"
    "errors"
    "github.com/xs23933/core/v3"
    "your-project/internal/dao"
    "your-project/internal/dto"
    "your-project/internal/model"
    "your-project/internal/vo"
    "gorm.io/gorm"
)

type UserService struct {
    dao *dao.UserDAO
}

func NewUserService() *UserService {
    return &UserService{dao: dao.UserDAOApp}
}

func (s *UserService) GetUserList(ctx context.Context, page, size int) ([]vo.UserVO, int64, error) {
    users, total, err := s.dao.List(ctx, page, size)
    if err != nil {
        return nil, 0, err
    }
    result := make([]vo.UserVO, len(users))
    for i, u := range users {
        result[i] = *vo.ToUserVO(&u)
    }
    return result, total, nil
}

func (s *UserService) GetUserByID(ctx context.Context, id string) (*vo.UserVO, error) {
    user, err := s.dao.GetByID(ctx, id)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, core.NewError(404, "user not found")
        }
        return nil, err
    }
    return vo.ToUserVO(user), nil
}

func (s *UserService) CreateUser(ctx context.Context, req *dto.CreateUserRequest) (*vo.UserVO, error) {
    hash, err := core.HashPassword(req.Password)
    if err != nil {
        return nil, err
    }
    user := &model.User{
        Username: req.Username,
        Email:    req.Email,
        Password: hash,
        Phone:    req.Phone,
    }
    if err := s.dao.Create(ctx, user); err != nil {
        return nil, err
    }
    return vo.ToUserVO(user), nil
}

func (s *UserService) UpdateUser(ctx context.Context, id string, req *dto.UpdateUserRequest) (*vo.UserVO, error) {
    updates := map[string]any{}
    if req.Email != "" {
        updates["email"] = req.Email
    }
    if req.Phone != "" {
        updates["phone"] = req.Phone
    }
    if req.Status != nil {
        updates["status"] = *req.Status
    }
    if err := s.dao.Update(ctx, id, updates); err != nil {
        return nil, err
    }
    return s.GetUserByID(ctx, id)
}

func (s *UserService) DeleteUser(ctx context.Context, id string) error {
    return s.dao.Delete(ctx, id)
}
```

#### Step 5: 定义 HTTP Handler（internal/handler/user_handler.go）

```go
package handler

import (
    "github.com/xs23933/core/v3"
    "your-project/internal/dto"
    "your-project/internal/service"
)

type UserHandler struct {
    core.Handler
    svc *service.UserService
}

func init() {
    core.RegHandle(&UserHandler{svc: service.NewUserService()})
}

func (h *UserHandler) Init() {
    h.Prefix("/api/v1/users")
}

func (h *UserHandler) Preload(c core.Ctx) error {
    return c.Next()
}

func (h *UserHandler) Get(c core.Ctx) {
    page := c.QueryInt("page", 1)
    size := c.QueryInt("size", 20)
    users, total, err := h.svc.GetUserList(c.StdContext(), page, size)
    c.ToJSON(core.Map{"list": users, "total": total, "page": page, "size": size}, err)
}

func (h *UserHandler) GetByID(c core.Ctx) {
    id := c.Params("id")
    user, err := h.svc.GetUserByID(c.StdContext(), id)
    c.ToJSON(user, err)
}

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
    user, err := h.svc.CreateUser(c.StdContext(), &req)
    c.ToJSON(user, err)
}

func (h *UserHandler) Put_id(c core.Ctx) {
    id := c.Params("id")
    var req dto.UpdateUserRequest
    if err := c.ReadBody(&req); err != nil {
        c.ToJSON(nil, err)
        return
    }
    user, err := h.svc.UpdateUser(c.StdContext(), id, &req)
    c.ToJSON(user, err)
}

func (h *UserHandler) Delete_id(c core.Ctx) {
    id := c.Params("id")
    err := h.svc.DeleteUser(c.StdContext(), id)
    c.ToJSON(nil, err)
}
```

#### Step 6: 定义 gRPC Handler（internal/grpc/user_grpc.go）

```go
package grpc

import (
    "context"
    "github.com/xs23933/core/v3"
    "your-project/internal/dto"
    "your-project/internal/service"
    pb "your-project/proto/user/v1"
    "google.golang.org/grpc"
)

type UserService struct {
    pb.UnimplementedUserServiceServer
    svc *service.UserService
}

func NewUserGRPCService(app *core.Core) {
    pb.RegisterUserServiceServer(app.GetGRPCServer(), &UserService{
        svc: service.NewUserService(),
    })
}

func (s *UserService) GetProfile(ctx context.Context, req *pb.GetProfileRequest) (*pb.User, error) {
    vo, err := s.svc.GetUserByID(ctx, req.Id)
    if err != nil {
        return nil, err
    }
    return &pb.User{
        Id:       vo.ID,
        Username: vo.Username,
        Email:    vo.Email,
        Phone:    vo.Phone,
        Status:   int32(vo.Status),
    }, nil
}

func (s *UserService) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.User, error) {
    dtoReq := &dto.CreateUserRequest{
        Username: req.Username,
        Email:    req.Email,
        Password: req.Password,
        Phone:    req.Phone,
    }
    vo, err := s.svc.CreateUser(ctx, dtoReq)
    if err != nil {
        return nil, err
    }
    return &pb.User{
        Id:       vo.ID,
        Username: vo.Username,
        Email:    vo.Email,
        Phone:    vo.Phone,
        Status:   int32(vo.Status),
    }, nil
}
```

#### Step 7: 应用入口（main.go）

```go
package main

import (
    "github.com/xs23933/core/v3"
    "github.com/xs23933/core/v3/middleware/cors"
    "github.com/xs23933/core/v3/middleware/requestid"
    _ "your-project/internal/handler"
    "your-project/internal/grpc"
    "google.golang.org/grpc"
)

func main() {
    app := core.New(core.LoadConfigFile("config.yaml"))

    app.Use(requestid.New())
    app.Use(cors.New(app))

    app.RegisterGRPCService(func(s *grpc.Server) {
        grpc.NewUserGRPCService(app)
    })

    if err := app.EnableEtcdRegistry(nil); err != nil {
        panic(err)
    }

    if err := app.Run(); err != nil {
        panic(err)
    }
}
```

### 3.3 标准模块调用流程总结

```
                    HTTP 请求                          gRPC 请求
                       │                                  │
                       ▼                                  ▼
              ┌────────────────┐                ┌────────────────┐
              │  Middleware    │                │  Interceptor   │
              │  (鉴权/限流)    │                │  (error wrap)  │
              └───────┬────────┘                └───────┬────────┘
                      │                                  │
                      ▼                                  ▼
              ┌────────────────┐                ┌────────────────┐
              │  Handler       │                │  gRPC Handler  │
              │  参数解析/DTO   │                │  proto → DTO   │
              │  ToJSON/VO     │                │  VO → proto    │
              └───────┬────────┘                └───────┬────────┘
                      │                                  │
                      └──────────────┬───────────────────┘
                                     │
                                     ▼
                            ┌────────────────┐
                            │    Service     │
                            │  (协议无关)     │
                            │  业务逻辑/事务  │
                            └───────┬────────┘
                                    │
                                    ▼
                            ┌────────────────┐
                            │      DAO       │
                            │  数据访问/缓存  │
                            └───────┬────────┘
                                    │
                                    ▼
                            ┌────────────────┐
                            │     Model      │
                            │  数据结构定义   │
                            └────────────────┘
```

---

## 四、行业最佳实践

### 4.1 性能优化建议

#### 数据库优化

- **连接池配置**：`max_open_conns: 100`，`max_idle_conns: 20`，按实际负载调整
- **避免 N+1 查询**：使用 `Preload` / `Joins` 预加载关联数据
- **分页查询优化**：滚动分页（FindNextBy）比经典分页（FindPageBy）更适合大数据量场景，避免 COUNT 全表扫描
- **ClickHouse 专用**：禁止使用 `AutoMigrate`，必须手写 DDL 并确保 `ORDER BY` 匹配查询条件，查询时必须显式列名而非 `SELECT *`
- **索引策略**：为高频查询条件创建复合索引，定期分析慢查询日志

#### 缓存优化

- **缓存实例复用**：每个缓存域创建一个 `cache.New()` 实例，禁止每次请求重复创建
- **singleflight 防击穿**：`Take()` / `Load()` 内置 singleflight，高并发下自动合并同 key 请求
- **TTL 抖动**：使用 `Jitter()` 给过期时间加随机偏移，避免缓存雪崩
- **空值缓存**：`CacheNil(true)` 缓存空结果，防止缓存穿透
- **缓存更新策略**：更新/删除 DB 后必须同步 `Delete` 缓存，推荐 Cache-Aside 模式

#### 连接管理

- **gRPC 连接复用**：Service 初始化时创建 gRPC Client，禁止每次请求新建连接
- **HTTP Client 复用**：使用 `fetch.New()` 创建可复用实例，统一管理 baseURL/Header/Hook
- **Redis 连接池**：通过 `pool_size` 和 `min_idle_conns` 控制连接数

#### 并发处理

- **NSQ Consumer 并发**：通过 `Concurrency` 参数控制并发处理数，`MaxInFlight` 控制未确认消息上限
- **goroutine 安全**：禁止在 goroutine 中复用 `core.Ctx`，使用 `context.Context` 传递
- **优雅关闭**：利用 `app.OnShutdown()` 注册清理钩子，确保资源正确释放

### 4.2 安全防护措施

#### 密码安全

- **bcrypt 哈希**：密码存储必须使用 `core.HashPassword()` + `core.CheckPassword()`
- **禁止明文存储**：数据库中禁止保存明文密码
- **禁止弱哈希**：禁止直接使用 SHA-256/MD5 存储密码
- **AES 密钥管理**：生产环境必须在启动时设置安全的 32 字节 AES 密钥，禁止使用框架默认密钥

#### 输入校验

- **参数校验**：使用 `c.Validate(&req)` 对 DTO 进行校验，配合 validate tag 声明规则
- **SQL 注入防护**：使用 GORM 参数化查询，禁止拼接 SQL 字符串
- **XSS 防护**：禁止将 Model 直接暴露给前端，使用 VO 层进行数据过滤
- **文件上传校验**：使用 `max` tag 限制文件大小，`mime` tag 限制文件类型（基于文件内容检测，非扩展名）

#### 访问控制

- **中间件鉴权**：在 `Preload()` 或全局中间件中统一处理身份认证
- **限流保护**：使用 `ratelimit` 中间件防止恶意请求，多实例部署使用 Redis 后端
- **CORS 配置**：通过 `cors.New(app)` 控制跨域访问白名单
- **管理接口保护**：网关管理接口仅允许 loopback 访问（`127.0.0.1`）

#### 数据安全

- **敏感字段保护**：Model 中敏感字段使用 `json:"-"` 标签，防止序列化泄露
- **日志脱敏**：禁止在日志中打印密码、Token、密钥等敏感信息
- **HTTPS/TLS**：生产环境启用 TLS，框架支持 ACME 自动证书和 gRPC TLS
- **etcd 安全**：生产环境 etcd 连接启用 TLS 认证

### 4.3 代码风格指南

#### 文件组织

- **单一职责**：每个文件只放一个业务实体的相关代码
- **命名规范**：`{业务名}_{层级}.go`，如 `user_handler.go`、`user_service.go`、`user_dao.go`、`user_model.go`
- **层级分离**：禁止跨层调用（Handler → DAO、DAO → Handler、Model → Service）
- **包导入**：禁止循环依赖，外层依赖内层

#### 函数设计

- **小函数原则**：每个函数不超过 50 行，职责单一
- **提前 return**：错误处理优先，正常逻辑靠后
- **显式错误处理**：禁止 `_` 忽略 error 返回值，禁止 `panic` 处理业务错误
- **Context 传递**：所有涉及 IO 的方法必须接受 `context.Context` 作为第一个参数

#### 类型使用

- **金额类型**：使用 `core.Money`（DECIMAL 浮点）或 `core.IntMoney`（BIGINT 整数分），避免 float64 精度丢失
- **主键选择**：默认 `core.Model`（uid.UID），需要全局唯一用 `core.Models`（UUID），需要排序用 `core.SModels`（雪花ID）
- **JSON 字段**：使用 `core.JSON` 类型，自动处理数据库序列化

#### 错误处理

- **业务错误**：使用 `core.NewError(code, msg)` 定义业务错误码
- **统一响应**：Handler 中使用 `c.ToJSON(data, err)` 统一处理成功和错误响应
- **错误传播**：Service 层返回标准 error，由 Handler/gRPC 层转换为协议特定格式
- **日志记录**：错误发生时使用 `core.Erro()` 记录详细信息

#### 命名约定

| 元素 | 命名规则 | 示例 |
| ---- | -------- | ---- |
| Handler 结构体 | `XxxHandler` | `UserHandler` |
| Handler 方法 | `[HTTP方法][路径]` | `GetByID`、`Post`、`Put_id` |
| Service 结构体 | `XxxService` | `UserService` |
| Service 变量 | `XxxServiceApp` | `UserServiceApp` |
| DAO 结构体 | `XxxDAO` | `UserDAO` |
| DAO 变量 | `XxxDAOApp` | `UserDAOApp` |
| Model 结构体 | `Xxx` | `User` |
| DTO 结构体 | `[Create/Update]XxxRequest` | `CreateUserRequest` |
| VO 结构体 | `XxxVO` / `XxxResponse` | `UserVO` |
| gRPC Service | `XxxService`（实现 proto 接口） | `UserService` |

#### 注释规范

- **公开 API**：所有导出的类型、函数、方法必须有文档注释
- **复杂逻辑**：非直观的业务逻辑必须添加行内注释说明意图
- **TODO/FIXME**：使用 `// TODO(username): description` 格式标记待办事项

#### 禁止事项清单

| 类别 | 禁止行为 |
| ---- | -------- |
| 路由 | 手动调用 `app.GET()` / `app.POST()` 等注册路由 |
| 路由 | 使用 Gin/Fiber/Echo 风格代码 |
| Handler | 在 Handler 中直接操作数据库 |
| Handler | 在 Handler 中包含复杂业务逻辑 |
| Handler | 在 goroutine 中保存或复用 `core.Ctx` |
| Service | Service 依赖 `core.Ctx` 或 HTTP/gRPC 协议对象 |
| DAO | DAO 层包含业务逻辑 |
| Model | Model 中包含业务逻辑、不嵌入基础模型 |
| 错误 | `panic()`、`log.Fatal()`、`fmt.Println()` 处理错误 |
| 错误 | 使用 `_` 忽略 error 返回值 |
| 密码 | 明文存储密码、使用 SHA-256/MD5 存储密码 |
| ClickHouse | 使用 `AutoMigrate` 创建表、使用 `SELECT *` 查询 |
| gRPC | 在 `Listen` 之后注册 gRPC service |
| gRPC | 每次请求创建新的 gRPC 连接 |
| 缓存 | 每次请求重复 `cache.New()`、更新 DB 后忘记 `Delete` 缓存 |

---

## 五、API 速查总表

### 5.1 Ctx 常用方法

| 方法 | 用途 | 示例 |
| ---- | ---- | ---- |
| `c.Params("id")` | 获取路径参数 | `id := c.Params("id")` |
| `c.ParamsUid("id")` | 获取 UID 类型路径参数 | `uid, _ := c.ParamsUid("id")` |
| `c.Query("name")` | 获取 Query 参数 | `name := c.Query("name")` |
| `c.QueryInt("page", 1)` | 获取整数 Query 参数 | `page := c.QueryInt("page", 1)` |
| `c.FormValue("email")` | 获取表单参数 | `email := c.FormValue("email")` |
| `c.ReadBody(&req)` | 解析 JSON Body | `c.ReadBody(&req)` |
| `c.Validate(&req)` | 校验请求参数 | `c.Validate(&req)` |
| `c.ToJSON(data, err)` | 统一 JSON 响应 | `c.ToJSON(user, nil)` |
| `c.SaveFile("file", dir)` | 保存上传文件 | `rel, abs, _ := c.SaveFile("file", "./uploads")` |
| `c.GetHeader("Authorization")` | 获取请求头 | `token := c.GetHeader("Authorization")` |
| `c.Set("key", val)` | 设置上下文变量 | `c.Set("user_id", uid)` |
| `c.StdContext()` | 获取标准 context.Context | `ctx := c.StdContext()` |

### 5.2 数据库操作

| 方法 | 用途 |
| ---- | ---- |
| `core.Conn()` | 获取默认数据库连接 |
| `core.Conn("name")` | 获取命名数据库连接 |
| `core.FindPageBy[T](whr, &list, db)` | 经典分页（泛型） |
| `core.FindNextBy[T](whr, &list, db)` | 滚动分页（泛型） |
| `core.Where(whr, db)` | 条件构建 |
| `core.WithTransaction(tx, fn)` | 事务处理 |
| `core.Expr(sql, args...)` | 原生 SQL 表达式 |

### 5.3 工具函数速查

| 方法 | 用途 |
| ---- | ---- |
| `core.LocalIP()` | 获取本机 IP |
| `core.RemoteIP(header, addr)` | 获取客户端真实 IP |
| `core.SHA256Hash(s)` | SHA-256 哈希 |
| `core.HashPassword(pwd)` | bcrypt 密码哈希 |
| `core.CheckPassword(pwd, hash)` | bcrypt 密码校验 |
| `core.EncryptAES(s)` | AES-GCM 加密 |
| `core.DecryptAES(s)` | AES-GCM 解密 |
| `core.NewError(code, msg)` | 创建业务错误 |
| `core.Contains(slice, v)` | 切片包含判断 |
| `core.Unique(slice)` | 切片去重 |

---

## 六、版本与维护

| 项目 | 信息 |
| ---- | ---- |
| 框架版本 | v3 |
| Go 版本要求 | 1.24+ |
| 模块路径 | `github.com/xs23933/core/v3` |
| 许可证 | LICENSE 文件 |
| 技能文档版本 | 1.0.0 |
| 最后更新 | 2026-05-19 |
