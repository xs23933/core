---
name: core-corectl
description: 使用 corectl 脚手架工具快速创建符合 Core Framework v3 规范的项目骨架
tags: [go, core-framework, cli, scaffolding, project-generator]
---

# corectl — Core Framework 项目脚手架工具

## 功能概述

`corectl` 是 Core Framework v3 的官方项目脚手架工具，用于快速创建符合 Core Framework 规范的项目骨架。该工具自动生成标准目录结构及基础代码文件，帮助开发者一分钟内启动新项目。

### 核心特性

- 一键生成完整的项目目录结构和基础代码
- 自动填充项目名称、模块路径等元信息
- 生成的代码遵循 Core Framework 三层架构规范（Handler → Service → DAO → Model）
- 基于 Go `text/template` 模板引擎，模板可定制
- 使用 `cobra` 命令行框架，提供友好的 CLI 交互体验
- 内置输入校验，防止非法参数

---

## 安装

```bash
# 从源码安装
cd ~/mbp/work/core
go install ./cmd/corectl

# 或直接构建
go build -o corectl ./cmd/corectl/
```

---

## 快速开始

```bash
# 创建新项目
corectl new myapp -m github.com/example/myapp

# 指定 Go 版本
corectl new myapp -m github.com/example/myapp --go-version 1.24

# 指定输出目录
corectl new myapp -m github.com/example/myapp -o /path/to/projects
```

---

## 命令参考

### `corectl new` — 创建新项目

```bash
corectl new [project-name] [flags]
```

| 参数 | 简写 | 类型 | 必填 | 默认值 | 说明 |
| ---- | ---- | ---- | ---- | ------ | ---- |
| `project-name` | — | `string` | 是 | — | 项目名称（用作目录名） |
| `--module` | `-m` | `string` | 是 | — | Go module 路径，如 `github.com/example/myapp` |
| `--go-version` | — | `string` | 否 | 当前 Go 版本 | Go 版本号声明 |
| `--output` | `-o` | `string` | 否 | `.` | 输出根目录 |

---

## 生成的项目结构

```
myapp/
├── cmd/
│   └── main.go                     # 应用入口，初始化 Core 框架并启动服务
├── config.yaml                     # 配置文件（数据库/Redis/日志等）
├── go.mod                          # Go Module 定义文件
└── internal/
    ├── handler/
    │   └── handler.go              # HTTP Handler 层（BaseHandler + UserHandler）
    ├── service/
    │   └── user.go                 # 业务逻辑层（UserService + UserVO）
    ├── dao/
    │   └── user.go                 # 数据访问层（UserDAO，CRUD 操作）
    ├── model/
    │   └── user.go                 # 数据模型层（User 结构体 + GORM 定义）
    └── middleware/
        └── auth.go                 # 认证中间件示例
```

共生成 8 个文件，覆盖完整的 `Handler → Service → DAO → Model` 调用链路。

---

## 各文件详细说明

### `cmd/main.go` — 应用入口

```go
package main

import (
	"log"

	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/middleware/cors"
	"github.com/xs23933/core/v3/middleware/requestid"

	_ "github.com/example/myapp/internal/handler"
)

func main() {
	app := core.New(core.LoadConfigFile("config.yaml"))

	app.Use(requestid.New())
	app.Use(cors.New(app))

	if err := app.Run(); err != nil {
		log.Fatalf("server startup failed: %v", err)
	}
}
```

**说明**：
- 通过 `core.LoadConfigFile("config.yaml")` 加载配置文件
- `_ "your-project/internal/handler"` 触发 Handler 的 `init()` 自动路由注册
- 全局中间件：RequestID（请求追踪）、CORS（跨域）

### `internal/handler/handler.go` — HTTP Handler

**BaseHandler**：可嵌入的基础 Handler 结构体，方便快速创建新 Handler。

**UserHandler**：
- `GET /api/v1/users?page=1&size=20` → 分页获取用户列表
- `GET /api/v1/users/:id` → 根据 ID 获取用户详情

Handler 方法仅负责：
1. 解析 HTTP 请求参数
2. 调用 Service 层方法
3. 通过 `c.ToJSON()` 返回统一响应

### `internal/service/user.go` — 业务逻辑层

**UserService** 是协议无关的业务逻辑层：
- 不依赖 `core.Ctx`，仅使用 `context.Context`
- 输入为基础类型，输出为 `UserVO` + `error`
- 可同时被 HTTP Handler 和 gRPC Handler 调用

**UserVO**：视图对象，禁止将 Model 直接暴露给前端。

### `internal/dao/user.go` — 数据访问层

**UserDAO** 统一管理 `User` 的所有数据库操作：
- `Create` / `GetByID` / `GetByUsername` / `List` / `Update` / `Delete`
- 使用 `core.Conn().WithContext(ctx)` 获取数据库连接
- 使用 `core.FindPageBy` 进行分页查询

### `internal/model/user.go` — 数据模型

```go
type User struct {
	core.Model                              // 嵌入基础模型（ID/CreatedAt/UpdatedAt/DeletedAt）
	Username string `json:"username" gorm:"size:64;uniqueIndex;not null"`
	Email    string `json:"email" gorm:"size:128;uniqueIndex"`
	Password string `json:"-" gorm:"size:96;not null"`  // json:"-" 防止序列化泄露
	Phone    string `json:"phone" gorm:"size:20"`
	Status   int    `json:"status" gorm:"default:1"`
}
```

### `config.yaml` — 配置文件

```yaml
debug: true
listen: ":8080"

database:
  default:
    type: "sqlite"
    dsn: "data.db"

restful:
  status: "status"
  data: "data"
  message: "msg"
```

### `internal/middleware/auth.go` — 认证中间件

提供 Token 认证中间件模板，开发者可根据实际需求实现具体的认证逻辑。

---

## 架构约定

生成的代码严格遵循 `.cursorrules` 中定义的分层规范：

```
HTTP 请求 → Handler（参数解析，协议适配）
                │
                ▼
           Service（业务逻辑，协议无关，兼容 HTTP/gRPC）
                │
                ▼
           DAO（数据访问，CRUD 操作）
                │
                ▼
           Model（数据结构定义，GORM 模型）
```

### 禁止事项

| 类别 | 禁止行为 |
| ---- | -------- |
| Handler | 直接操作数据库 |
| Handler | 包含复杂业务逻辑 |
| Service | 依赖 `core.Ctx` 或 HTTP/gRPC 协议对象 |
| DAO | 包含业务逻辑 |
| Model | 直接暴露给前端（使用 VO 层转换） |

---

## 后续步骤

项目创建完成后：

```bash
cd myapp
go mod tidy          # 下载依赖
go run cmd/main.go   # 启动服务

# 访问
curl http://localhost:8080/api/v1/users
```

---

## 源代码结构

```
cmd/corectl/
├── main.go         # CLI 入口，cobra 命令定义，参数校验
├── new.go          # ProjectConfig，CreateProject()，renderTemplate()
└── templates.go    # 所有代码生成模板（goModTemplate/mainGoTemplate/handlerGoTemplate 等）
```

### 模板变量

所有模板使用 `text/template` 模板引擎，注入 `ProjectConfig` 结构体：

```go
type ProjectConfig struct {
	ProjectName string // 项目名称
	ModulePath  string // Go module 路径
	GoVersion   string // Go 版本号
	OutputDir   string // 输出目录
}
```

---

## 扩展指南

如需添加新的生成模板：

1. 在 `templates.go` 中添加新模板常量
2. 在 `new.go` 的 `getProjectTemplates()` 中注册新的 `TemplateFile`
3. 重新编译：`go build ./cmd/corectl/`

---

## 依赖

| 依赖 | 用途 | 版本 |
| ---- | ---- | ---- |
| `github.com/spf13/cobra` | CLI 命令行框架 | v1.10.2 |
| `text/template` | Go 标准库模板引擎 | — |
| `github.com/xs23933/core/v3` | Core Framework（生成项目依赖） | latest |