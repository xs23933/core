# Core Web Framework v3 - 完整帮助文档

带点协议值可直接进入枚举 values：ToFixed 将点及连字符转换为常量名下划线，原 wire 值保持不变；无需在业务代码追加映射或伪造生成文件。

枚举边界：生成的 Scan 支持 int64/uint8/uint16/uint32/float64/string/[]byte；数值输入必须为 0～255 的整数，拒绝负数、溢出和非有限浮点且不修改原值。EnumString 对越界索引返回空串，EnumMarshalJSON/EnumMarshalText 返回错误；EnumUnmarshalJSON 在转换前检查数值范围，并支持 256 项映射。字符串解析沿用现行合同，领域取值仍由调用方校验。修改模板后须重新生成业务枚举。

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
- **SSE 推送** - SSE 服务端推送 + EventHub 连接管理
- **模板引擎** - 支持 HTML 模板渲染
- **Fetch 客户端** - 独立 `fetch` 包，支持 API 调用、公共/单次 Header、Cookie、HTTP/SOCKS5 代理、请求/响应 Hook
- **Redis Cache** - 独立 `cache` 包，支持 DB fallback、自动回填、singleflight 防击穿、空值缓存
- **中间件系统** - 灵活的中间件扩展机制
- **[ai-context](https://github.com/xs23933/core/blob/v3/AI_CONTEXT.md)** - AI 的工作流程指南
- **[skills](https://github.com/xs23933/core/tree/v3/skills)** - 模式库和示例

<!-- AI_CONFIG_START -->
参考文档：
- ./AI_CONTEXT.md - Core 框架完整上下文
- ./skills/ - 场景化开发模板
<!-- AI_CONFIG_END -->

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
├── cmd/               # 命令行目录
│   └── main.go        # 主入口文件
├── config.yaml        # 配置文件
├── views/             # 模板文件
├── static/            # 静态文件
├── internal/          # 内部目录
    ├── handler/       # 处理器目录
    │   └── handler.go # 业务逻辑
    ├── models/        # 数据模型
    │   └── models.go  # 数据库模型定义
    ├── middleware/    # 中间件
    ├── service/       # (业务逻辑：事务操作，调用 dao/model)
    └── dao/           # (数据访问：复杂查询封装)
```

## 配置

### config.yaml 示例

```yaml
debug: true # 调试模式
network: tcp4 # 网络协议
listen: 8080 # 监听端口
prefork: false # 是否启用 prefork 模式
# 空列表默认信任 IPv4/IPv6 私网和回环；非空列表仅信任配置范围。
# 非空列表覆盖默认规则；非法类型/条目使 New() 初始化失败。
trusted_proxies: [] # 公网反代示例: ["203.0.113.10/32"]；仅作用于 c.RemoteIP()
log: /var/log/myapp/app.log # 可选：日志文件路径
log_rotate:
  - size: 300M # 文件达到 300MB 后切割
  - daily # 每日切割
  - rotate: 30 # 保留最近 30 天的切割日志
  - compress # 切割后的日志 gzip 压缩
  - delaycompress: 24h # 延迟压缩，需配合 compress
  - missingok # 日志文件不存在时不报错
  - notifempty # 空日志不切割
  - copytruncate # 复制后截断原文件，不替换当前文件句柄

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


## corectl 命令行工具

```bash
brew install xs23933/core/corectl

# generate restful app
corectl new myapp

# generate grpc app
corectl new myapp -t=grpc
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

`core.New()` 会默认注册 `GET /health`，返回 `204 No Content`，用于基础存活探测。
业务程序可以显式注册同一路径覆盖默认实现：

```go
app.GET("/health", func(c core.Ctx) error {
    return c.SendString("ok")
})
```

### 2. 上下文 (Ctx)

上下文对象封装了 HTTP 请求和响应，提供了丰富的操作方法。

`c.RemoteIP()` 的 `trusted_proxies` 未配置、为 null 或空列表时，默认信任 IPv4/IPv6 私网和回环（`net.IP.IsPrivate()` / `IsLoopback()`）；非空列表只信任配置的 IP/CIDR，覆盖默认范围，回环也必须显式列入。配置必须是字符串列表，非法类型、空白项或任意非法 IP/CIDR 都使整个配置失败；`New()` 沿用现有初始化错误约定直接 panic，不忽略条目、不回退私网兜底。先校验 TCP 对端，不可信则忽略所有 IP 头。XFF 从右向左使用同一可信规则检查，重复头合并，返回首个不可信 IP，全部可信时返回最左项。仅缺少 XFF 时尝试单个合法的 `X-Real-IP`；XFF 为空或检查途中遇到非法项时直接回退对端。重复或包含列表的 X-Real-IP 无效。不采信其它 IP 头、不内置信任 Cloudflare；无效对端返回 nil。包级 `core.RemoteIP(header, addr)` 不读取应用配置，使用私网/回环默认规则；`ExtractClientInfo(ctx)` 使用独立的 Gateway 信任规则，不再解析原始 XFF/X-Real-IP：仅在当前 Core 的 Gateway 信任规则通过且 `core-client-ip` 为单个合法 IP 时采信，否则返回 gRPC 对端 IP。未设置 `GRPCServerConfig.TrustGateway` 时按实际 gRPC 对端 IP 默认信任私网/回环；设置后完全使用回调结果，拒绝时不启用兜底。

同机 Caddy/Nginx 连接 `127.0.0.1:8080` 时，回环默认可信，无需额外配置：

```yaml
listen: "127.0.0.1:8080"
trusted_proxies: []
```

严格部署应显式列出服务实际看到的代理 IP 或最小 CIDR，例如 `trusted_proxies: ["10.0.0.2/32"]`，此时其它私网及回环地址不再自动可信。配置在初始化时解析，修改需重建应用。空配置的私网兜底意味着其它内网主机也能提交转发头，因此后端端口仅允许受控代理访问。框架无法验证可信代理自己伪造的头。代理不能原样透传用户 XFF，必须覆盖或追加实际连接来源；使用 X-Real-IP 时必须覆盖为验证后的客户端 IP。

普通 Caddy 入口可使用默认 `reverse_proxy 127.0.0.1:8080`（默认防止客户端伪造 XFF）。普通 Nginx 入口可在 `location` 中覆盖为 `proxy_set_header X-Forwarded-For $remote_addr;`；多级代理也可使用 `$proxy_add_x_forwarded_for` 追加来源，Core 从右侧识别信任边界。

Cloudflare DNS 代理开启时，由 Caddy/Nginx 验证回源地址，Core 只信任 Caddy/Nginx。以 Caddy 为例，环境变量 `CLOUDFLARE_CIDRS` 必须填入空格分隔的完整 [Cloudflare 官方 IPv4/IPv6 回源段](https://www.cloudflare.com/ips/)，并随官方变更维护；全局块必须位于 Caddyfile 最前且只出现一次：

```caddyfile
{
    servers {
        trusted_proxies static {$CLOUDFLARE_CIDRS}
        trusted_proxies_strict
        client_ip_headers CF-Connecting-IP
    }
}
example.com {
    reverse_proxy 127.0.0.1:8080 {
        header_up X-Forwarded-For {client_ip}
    }
}
```

Nginx 需要启用 HTTP realip 模块；`/etc/nginx/cloudflare-realip.conf` 由部署侧维护完整官方回源段，每段一行 `set_real_ip_from CIDR;`，不能信任任意来源。在 `server` 中配置：

```nginx
include /etc/nginx/cloudflare-realip.conf;
real_ip_header CF-Connecting-IP;

location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

不可信来源传入的 `CF-Connecting-IP` 不会改写上述客户端地址；不要直接转发 `$http_cf_connecting_ip`。后端禁止绕过受控代理访问，代理层只信任实际的上游来源。

实现依据：[Gin 可信链校验](https://github.com/gin-gonic/gin/blob/master/gin.go)、[Caddy 可信代理选项](https://caddyserver.com/docs/caddyfile/options#trusted-proxies)、[Caddy reverse_proxy 头规则](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#headers)、[Nginx realip 模块](https://nginx.org/en/docs/http/ngx_http_realip_module.html)。



### 轻量 GRPCCtx

在 protobuf 生成的 gRPC 方法内部使用 `c := core.NewGRPCCtx(ctx)`；stream 使用 `core.NewGRPCCtx(stream.Context())`。不改变 protobuf 方法签名，不嵌入 HTTP BaseCtx，不提供路由、JSON 或 HTTP 状态码能力。对象只用于当前 RPC 生命周期；业务 Service 继续接收 `c.StdContext()`，不要保存封装对象或跨 RPC 复用。nil/零值使用 background context，不能据此写有效 RPC 响应。

| 方法 | 方向与语义 |
| --- | --- |
| `StdContext() context.Context` | 原始 context，保留 deadline/cancellation、peer 和 Core 信任配置 |
| `GetHeader(key) string` | 读取 incoming metadata 的第一个值，大小写不敏感；缺失返回空字符串 |
| `HeaderValues(key) []string` | 读取请求多值，返回独立副本 |
| `RequestMetadata() metadata.MD` | 读取全部请求 metadata 的独立副本，修改不会影响请求 |
| `ClientIP() string` | 复用 ExtractClientInfo 的 Gateway 信任校验，不能用原始 core-client-ip 代替 |
| `Cookie(name) (string, error)` | 读取请求 Cookie 并 URL 解码；缺失返回 http.ErrNoCookie，非法转义返回错误 |
| `SetHeader(key, values...) error` | 复用 GrpcSetHeader，排队写入 Gateway HTTP 响应桥接字段，重复调用追加值 |
| `SetCookie(name, value, exp, path, args...) error` | 复用 GrpcSetCookie 的默认值、URL 编码、Domain 与变参规则 |
| `WriteCookie(*http.Cookie) error` | 复用 GrpcCookie，完整响应 Cookie，不补默认值或编码 |
| `RemoveCookie(name, path, domain...) error` | 复用 GrpcRemoveCookie 删除规则 |
| `OutgoingContext(metadata.MD) context.Context` | 构造下游 RPC context，显式添加的键覆盖已有 outgoing 同名键，不自动转发 incoming metadata |

请求读取不代表字段已通过身份验证。响应 Header/Cookie 必须在发送 initial headers 或第一条 stream 消息之前写入，并处理 error；此处的 SetHeader 使用 `core-http-<name>-bin` 响应桥接协议，不是下游调用参数，也不会修改请求。需要原生 gRPC response metadata/trailer 时可直接对 StdContext 使用 gRPC 标准 API。

在现有 RPC 方法中适配协议层，再传递标准 context 给 Service：

```go
c := core.NewGRPCCtx(ctx)
requestID := c.GetHeader("x-request-id")
clientIP := c.ClientIP() // 可供审计；信任规则与 ExtractClientInfo 一致
_ = clientIP
if err := c.SetHeader("X-Request-ID", requestID); err != nil {
    return nil, err
}
return s.service.GetProfile(c.StdContext(), req)
```

调用下游时，只选择需要传递的字段（需要导入 `google.golang.org/grpc/metadata`）：

```go
c := core.NewGRPCCtx(ctx)
callCtx := c.OutgoingContext(metadata.Pairs("x-request-id", c.GetHeader("x-request-id")))
return userClient.GetProfile(callCtx, req)
```

OutgoingContext 保留原 context 的 deadline、取消及已有 outgoing metadata；提供的同名键整体覆盖旧值，并复制 map/slice。不会自动传播 Cookie、Authorization、core-client-ip 或响应字段。显式传播身份字段时，下游仍须按自己的调用方信任策略验证。修改请求/传入 metadata 的副本不会改变已创建的下游 context。已有 GrpcHeader、GrpcSetHeader、GrpcSetCookie 等函数继续可用。

### Gateway 客户端 IP 的可信传递

Gateway 保留 metadata 字段 `core-client-ip`（常量 `core.GatewayClientIPMetadata`），过滤客户端同名 HTTP 头，并在普通 header 和 middleware 变量转换完成后覆盖为 `c.RemoteIP().String()` 的单值结果。对端无法解析时不发送该字段；自动和手工 HTTP→gRPC 路由使用同一实现，重试沿用同一结果。原始 headers/body 中的字段仍是请求数据，不能当作可信身份来源。

下游可在创建 gRPC server 之前设置 `GRPCServerConfig.TrustGateway` 收紧或替换默认规则，然后统一通过 `core.ExtractClientInfo(ctx).IP` 取用户 IP。回调只能依据连接对端或经过验证的 transport 身份判断，不能读取 metadata、XFF 或用户提交的身份字段来决定信任。未设置回调（包括未配置 GRPCServerConfig）时，只根据实际 gRPC 对端 IP 默认信任私网/回环（IPv4/IPv6 的 IsPrivate/IsLoopback）；公网、链路本地、缺少或无效对端均不默认可信。设置回调后完全按回调结果判断，回调拒绝不会启用私网兜底。校验拒绝、缺少 peer、缺少字段或非法/重复字段都返回 gRPC 对端 IP，不回退原始 XFF/X-Real-IP。此规则同时适用于 unary 和 stream。直接使用 `grpc.NewServer()` 或脱离 Core 托管上下文调用 ExtractClientInfo 不会采信该字段。

受控内网按精确 Gateway IP 判断的示例，需要导入 `context`、`net` 和 `google.golang.org/grpc/peer`：

```go
gatewayIP := net.ParseIP("10.0.0.2")
if err := app.ConfigureGRPCServer(core.GRPCServerConfig{
    TrustGateway: func(ctx context.Context) bool {
        p, ok := peer.FromContext(ctx)
        if !ok {
            return false
        }
        addr, ok := p.Addr.(*net.TCPAddr)
        return ok && addr.IP.Equal(gatewayIP)
    },
}); err != nil {
    return err
}
// 随后注册服务、调用 EnableEtcdRegistry(nil)，再启动应用。
```

上面的显式回调没有私网兜底。未设置回调的私网兜底适用于受控内网，后端端口必须限制为仅 Gateway 可访问；内网其它主机能直连时也可以提交该字段。需要 CIDR 时预先使用 `net.ParseCIDR` 解析并检查错误，回调中对实际 `peer.Addr` 做 `Contains` 判断；应用负责限制后端仅允许 Gateway 访问。多 Gateway 应明确列出各个来源。

mTLS 部署在 `ConfigureGRPCServer` 同时设置 `TransportCredentials`，启用客户端证书验证。`TrustGateway` 应检查 `peer.AuthInfo` 中的 `credentials.TLSInfo`、非空 `State.VerifiedChains`，并匹配实际 Gateway 的证书 URI/SAN 身份；仅“证书属于同一 CA”不代表它是 Gateway。Core 不推断业务证书身份，详见 gRPC TLS 配置说明。

兼容性：旧服务依赖 ExtractClientInfo 读取 XFF/X-Real-IP 的用法需要迁移。Gateway 与下游应一起升级；受控私网/回环部署可使用默认规则，严格部署应显式配置回调；旧 Gateway 没有 core-client-ip 时会返回连接对端，不会猜测用户 IP。etcd 自动注册/reflection 不提供调用方认证。用户 IP 仅表示 Gateway 对入站来源的判断，不代替业务鉴权。

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
func (UserHandler) GetByID(c core.Ctx) {
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

| 关键字      | 路由规则   | 结果     |
| ----------- | ---------- | -------- |
| Param       | 参数关键词 | :param   |
| Params      | 参数关键词 | :params? |
| \_id        | 下划线     | /:id     |
| By          | 参数关键字 | /:       |
| \_\_        | 中横线     | -        |
| \_\_dot\_\_ | 点         | .        |

_Params 是可选关键词 即:params?_

- `Get` → `GET /`
- `GetParam` → `GET /:param`
- `GetParams` → `GET /:params?`
- `GetByID` → `GET /:id`
- `GetDetailParam` → `GET /detail/:param`
- `PostUser` → `POST /user`
- `PutUserParam` → `PUT /user/:param`
- `GetUserByID` → `GET /user/:id`
- `GetUser__Create` → `GET /user-create`
- `GetUser__dot__Html` → `GET /user.html`

框架内置默认路由 `GET /health`，返回 `204 No Content`。如果业务代码再次注册
`GET /health`，业务 handler 会覆盖框架默认 handler。

手动路由的动态 segment 必须有明确且无歧义的定义：同一结构位置不能混用不同参数名，
也不能混用必选 `:id` 与可选 `:id?`。可选参数和 catch-all 只能位于最后一段，
路径中不允许空 segment（`//`）。违反这些规则会在注册时 panic，避免由注册顺序决定匹配结果。
`/*` 仍是有效的根 catch-all，捕获值通过 `c.Params("*")` 读取。
通过 `Core` 的 `AddHandle`/`RemoveHandle` 进行的运行期路由更新可与请求匹配并发执行。

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
    return db.Create(u).Error
}

// 根据ID查询用户
func GetUserByID(id uid.UID) (user User, err error) {
    err = db.First(&user, "id = ?", id).Error
    return
}

// 分页查询用户
func GetUsers(page, size int) (users []User, total int64, err error) {
    err = db.Model(&User{}).Count(&total).Error
    if err != nil {
        return
    }
    offset := (page - 1) * size
    err = db.Offset(offset).Limit(size).Find(&users).Error
    return
}


var db *core.DB
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
func AuthMiddleware(c core.Ctx) error {
    token := c.GetHeader("Authorization")
    if token == "" {
        return c.SendStatus(401, "Unauthorized")
    }
    // 验证 token
    // ...
    return c.Next()
}

// 全局中间件
app.Use(AuthMiddleware)

// 路径级中间件：匹配 /api 及其所有子路径，不要添加 /*。
app.Use("/api", AuthMiddleware)

// 路由级中间件
app.GET("/admin", AdminHandler, AuthMiddleware)
```

路径级中间件使用路径前缀节点。`app.Use("/api", middleware)` 会作用于 `/api`、`/api/users`、`/api/v1/orders` 等路由。不要写成 `app.Use("/api/*", middleware)`；`*` 是路由 catch-all 段，不是路径级中间件的前缀标记。

```go
app.Use("/api", authMiddleware.Handle)
app.Use("/internal", internalAuthMiddleware.Handle)
```

中间件执行顺序：

- 全局中间件通过 `app.Use(middleware)` 注册到根节点，后注册的会前置执行。需要按期望执行顺序反向注册。
- 同一路径的中间件按注册顺序执行。
- 嵌套路径始终按父路径到子路径执行，例如 `/api` 中间件先于 `/api/internal` 中间件。
- 路径级中间件之后才执行路由自身的 middleware 和最终 handler。

未匹配到显式路由的请求也会经过全局中间件，然后由框架的 fallback handler 返回 404；因此显式注册的 `core.Logger()` 能记录 `404 METHOD /path`。路径级中间件不会为未匹配路由执行。`IsRouteFallback(c)` 仍只在未匹配的 OPTIONS fallback 链中返回 `true`。

```go
// 期望执行顺序：requestID -> accessLog -> handler。
// 全局中间件需要反向注册。
app.Use(accessLogMiddleware)
app.Use(requestIDMiddleware)

// 同一路径按注册顺序执行：auth -> audit -> handler。
app.Use("/api", authMiddleware.Handle)
app.Use("/api", auditMiddleware.Handle)

// GET /api/internal/status：auth -> internalAuth -> handler。
app.Use("/api", authMiddleware.Handle)
app.Use("/api/internal", internalAuthMiddleware.Handle)
app.GET("/api/internal/status", statusHandler)
```

#### 内置中间件

```go
import (
	core "github.com/xs23933/core/v3"
    "github.com/xs23933/core/v3/middleware/requestid"
    "github.com/xs23933/core/v3/middleware/cors"
)

app.Use(requestid.New())  // 请求ID
app.Use(cors.New())       // CORS 支持
app.Use(core.Logger())    // 显式启用请求日志；Recovery 由 core.New() 自动注册
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
package main

import (
    "net/http"
    "time"
    "github.com/xs23933/core/v3"
    "github.com/xs23933/core/v3/websocket"
)

func main() {
app := core.New()
app.GET("/ws", websocket.New().
    WithOptions(websocket.Options{
        Subprotocols: []string{"example.v1"},
        RequireSubprotocol: true,
        CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
        ReadLimit: 512 << 10,
        ReadTimeout: 60 * time.Second,
        WriteTimeout: 10 * time.Second,
        HandshakeTimeout: 10 * time.Second,
    }).
    OnConnect(func(conn *websocket.Conn) {
        if !conn.TrySend([]byte("已连接")) { return }
    }).
    OnMessage(func(conn *websocket.Conn, mt websocket.MessageType, data []byte) {
        conn.TrySendWithType(mt, data)
    }).
    OnClose(func(conn *websocket.Conn) { core.Info("websocket closed") }).
    Handler())
if err := app.Run(); err != nil { core.Erro("websocket stopped error_type=%T", err) }
}
```

`WithOptions` 可省略。零配置保持原行为：允许任意 Origin、不要求子协议、不限制消息大小或读写等待。需要设备或敏感通道时，调用方必须先鉴权并显式配置以上边界；框架不解释业务身份。

`ReadLimit` 限制完整消息含所有分片的累计字节数，超限不进入回调。`ReadTimeout` 在每条消息开始及收到 pong 时刷新；`WriteTimeout` 同时约束消息和 ping 写入。`RequireSubprotocol` 要求客户端提供至少一个配置中的协议，无匹配或配置为空时拒绝升级。

`TrySend`/`TrySendWithType` 复制输入并返回是否成功入队；成功不代表网络发送或对端持久接纳。队列满或连接已关闭时返回 false，背压会关闭连接并归还未发送缓冲区。既有 `Send`/`SendWithType` 保持无返回值。`OnMessage` 中的字节切片只在回调期间有效，异步保留须复制。

`Conn.Context()` 从升级请求派生并在 `Close()` 时取消；不要把 `core.Ctx` 放入连接或 goroutine。连接关闭会停止读写、移除默认管理器连接并释放排队缓存，读写循环退出后 `OnClose` 调用一次。配置和回调在注册 Handler 前设置。

按用户推送消息：

```go
app.GET("/ws", func(c core.Ctx) error {
	userID := c.Query("user_id")
	if userID == "" {
		return c.SendStatus(401, "missing user_id")
	}

	return websocket.New().
		OnConnect(func(conn *websocket.Conn) {
			websocket.DefaultUserManager.Add(userID, conn)
		}).
		OnClose(func(conn *websocket.Conn) {
			websocket.DefaultUserManager.Remove(conn)
		}).
		Handler()(c)
})

websocket.DefaultUserManager.SendToUser("1001", []byte(`{"type":"notice","data":"hello"}`))
```

### 10. SSE 支持

Core 在 Ctx 层提供了 SSE（Server-Sent Events）核心方法，并通过 EventHub 封装连接管理和广播能力，适用于服务端实时推送通知、日志流、状态更新等场景。

**SSE 核心方法**

```go
// 写入 SSE 事件（底层方法，支持 id/event/data 字段和多行数据）
c.SSEWrite("message", "hello world")
c.SSEWrite("update", `{"status":"ok"}`, "evt-001")

// 发送结构化事件（自动 JSON 序列化）
c.SSESend("notify", map[string]any{"title": "Alert", "message": "Server down"})

// 发送 SSE 注释（心跳保活）
c.SSEComment("ping")
```

**EventHub 快速使用**

```go
import "github.com/xs23933/core/v3"

func main() {
    app := core.New()

    hub := core.NewEventHub()       // 创建 EventHub，支持广播聚合
    defer hub.Close()

    // SSE 连接端点（浏览器 EventSource 连接目标）
    app.GET("/events", hub.Get)

    // 推送接口（其他服务调用此接口推送数据）
    app.POST("/events/push", hub.PostData)

    app.Run()
}
```

**前端连接示例**

```javascript
const es = new EventSource("/events")
es.addEventListener("connected", () => console.log("SSE 已连接"))
es.addEventListener("message", (e) => console.log("收到:", e.data))
es.onerror = () => console.log("连接中断（将自动重连）")
```

**编程式推送**

```go
// 广播到所有客户端
hub.Broadcast(core.EventData{
    Event: "alert",
    Data:  "server maintenance in 5 minutes",
})

// 定向推送到指定 ID
hub.SendTo("admin-01", core.EventData{
    Event: "private",
    Data:  map[string]any{"action": "reload"},
})
```

EventHub 的广播、定向发送、注册、注销和关闭支持并发，慢客户端仍采用非阻塞丢弃。同 ID 的新连接替换旧连接后，旧连接的延迟注销不会影响新连接。手工 `Register` 会把管道的发送和关闭所有权移交给 Hub；调用方只接收，不得自行关闭或复用退役管道。同一管道的多个 ID 别名共享生命周期，注销或替换时一起移除。

> 详细 API 参数和注意事项参见 [skills/core-sse.md](skills/core-sse.md)

### 11. 模板渲染

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

## gRPC、etcd 与网关

Core 支持同时运行 HTTP 与 gRPC，并通过 etcd 做服务注册、服务发现和网关动态路由。推荐把链路拆成三类程序理解：

1. 业务服务：启动 HTTP 或注册 protobuf 生成的 gRPC service，并把实例及对外路由发布到 etcd。
2. 内部客户端：通过 `app.GrpcClient("service-name")` 按服务名发现并调用 gRPC。
3. 网关服务：监听 etcd 实例和路由变化，把 HTTP 请求代理到 HTTP 或 gRPC 上游。

### 1. gRPC 服务

服务端必须先注册 gRPC service，再启动应用。只需要 gRPC 时使用 `EnableGRPC`；需要被网关发现时使用 `EnableEtcdRegistry`，它会自动创建/复用 gRPC server、注册服务到 etcd，并开启 gRPC reflection。


```go
package main

import (
    "context"

    "github.com/xs23933/core/v3"
    "google.golang.org/grpc"

    pb "your_project/proto/user/v1"
)

type UserService struct {
    pb.UnimplementedUserServiceServer
}

func (s *UserService) GetProfile(ctx context.Context, req *pb.GetProfileRequest) (*pb.User, error) {
    return &pb.User{Id: req.Id, Name: "tom"}, nil
}

func main() {
    app := core.New()

    app.RegisterGRPCService(func(s *grpc.Server) {
        pb.RegisterUserServiceServer(s, &UserService{})
    })

    app.EnableGRPC(":9001")

    if err := app.Listen(":8080"); err != nil {
        panic(err)
    }
}
```

HTTP 和 gRPC 使用不同端口时，`Listen(":8080")` 负责 HTTP，`EnableGRPC(":9001")` 负责 gRPC。

需要服务端 TLS、mTLS 或自定义 unary/stream interceptor 时，必须在第一次注册或获取 gRPC server 前调用 `ConfigureGRPCServer`。证书身份和授权策略由应用负责，Core 只托管 server 生命周期和 interceptor chain：

```go
serverTLS := &tls.Config{
    MinVersion:   tls.VersionTLS13,
    Certificates: []tls.Certificate{serverCertificate},
    ClientAuth:   tls.RequireAndVerifyClientCert,
    ClientCAs:    clientCAPool,
}
if err := app.ConfigureGRPCServer(core.GRPCServerConfig{
    TransportCredentials: credentials.NewTLS(serverTLS),
    UnaryInterceptors:     []grpc.UnaryServerInterceptor{identityUnary},
    StreamInterceptors:    []grpc.StreamServerInterceptor{identityStream},
}); err != nil {
    panic(err)
}
app.EnableGRPC(":9001")
app.RegisterGRPCService(registerServices)
```

配置只能执行一次；`GetGRPCServer`、`RegisterGRPCService` 或 `EnableEtcdRegistry` 已经创建 server 后再配置会返回错误。Core 的默认 unary error wrapper 始终保留，并位于调用方 unary interceptor chain 外层。

如果希望 HTTP/1 和 gRPC 共用端口，可以让 `EnableGRPC` 使用和 `Listen` 相同的地址。框架会根据 HTTP/2 与 `Content-Type: application/grpc` 自动分流。

```go
app.RegisterGRPCService(func(s *grpc.Server) {
    pb.RegisterUserServiceServer(s, &UserService{})
})
app.EnableGRPC(":8080")
app.Listen(":8080")
```

共端口模式只适用于未配置 gRPC transport credentials 的兼容路径。配置 credentials 后必须使用独立的 Core-managed gRPC 地址；否则启动返回 `ErrGRPCTLSSharedAddress`，不会把 `ServeHTTP` 错误描述为已执行 gRPC TLS 握手。

需要注册到 etcd 并提供给网关时：

```go
app := core.New(core.LoadConfigFile("config.yaml"))

app.RegisterGRPCService(func(s *grpc.Server) {
    pb.RegisterUserServiceServer(s, &UserService{})
})

if err := app.EnableEtcdRegistry(nil); err != nil {
    panic(err)
}

if err := app.Listen(":8080"); err != nil {
    panic(err)
}
```

### 2. etcd 配置

`EnableEtcdRegistry(nil)` 会读取 `etcd` 配置。服务端最少需要配置 endpoints、service name 和暴露给其它进程访问的 service addr。

```yaml
etcd:
  # 可选：共享同一个 etcd 集群时按项目隔离
  namespace: xpay
  endpoints:
    - 127.0.0.1:2379
  dial_timeout: 5
  service_name: user-service
  service_addr: 127.0.0.1:8080
  service_id: user-service-1
  ttl: 10
  version: 1.0.0
```

配置 `namespace: xpay` 后，服务实例、Gateway 路由和相对配置 key 分别写入 `/xpay/services/`、`/xpay/gateway/routes/` 和 `/xpay/config/`。同一项目的业务服务、内部 gRPC 客户端和 Gateway 必须使用相同 namespace；不配置时仍使用原有 `/services/`、`/gateway/routes/` 和 `/config/`，无需迁移现有数据。

也可以直接传 `etcd.Options`，适合测试或多环境注入：

```go
if err := app.EnableEtcdRegistry(&etcd.Options{
    Endpoints:   []string{"127.0.0.1:2379"},
    ServiceName: "user-service",
    ServiceAddr: "127.0.0.1:8080",
    ServiceID:   "user-service-1",
    TTL:         10,
    Version:     "1.0.0",
    Metadata: map[string]string{
        "env": "dev",
    },
}); err != nil {
    panic(err)
}
```

短期在线记录等场景可以复用 `EtcdDiscovery` 的租约与 revision CAS。这里只提供通用原语，业务所有权和重试策略仍由调用方决定：

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

leaseID, err := app.EtcdDiscovery.GrantLease(ctx, 10)
if err != nil {
    return err
}
defer app.EtcdDiscovery.RevokeLease(context.Background(), leaseID)

keepAlive, err := app.EtcdDiscovery.KeepAliveLease(ctx, leaseID)
if err != nil {
    return err
}
go func() {
    for range keepAlive {
        // channel 关闭表示 context 取消、租约丢失或 Discovery 已关闭。
    }
}()

current, found, err := app.EtcdDiscovery.GetRevision(ctx, "online/worker-a")
if err != nil {
    return err
}
expectedRevision := int64(0)
if found {
    expectedRevision = current.ModRevision
}
succeeded, err := app.EtcdDiscovery.CompareAndPut(ctx, "online/worker-a", expectedRevision, `{"addr":"127.0.0.1:9001"}`, leaseID)
if err != nil {
    return err
}
if !succeeded {
    return errors.New("online record changed concurrently")
}
```

租约 API 只接受非空相对 key，并始终写入当前 namespace 的 `/config/` 前缀。`expectedModRevision=0` 表示仅当 key 不存在时创建；CAS 冲突返回 `false, nil`。调用方应在关闭前主动 revoke 自己创建的租约，再关闭 Discovery。

注意：

- `service_addr` 必须是客户端和网关能访问到的地址，不一定等于本机监听地址。
- 对故障摘除时延敏感时可显式设置 `ttl: 5`；框架默认仍为 10 秒。
- 使用网关自动注册路由时，优先用 `EnableEtcdRegistry`，因为它会自动开启 reflection。
- `RegisterGRPCService` 要在 `Listen` 或 `Run` 前调用。
- `ConfigureGRPCServer` 必须在 `RegisterGRPCService`、`GetGRPCServer` 和 `EnableEtcdRegistry` 之前调用。

### 3. gRPC 客户端

内部服务调用优先使用 `GrpcClient`，它会通过 etcd resolver 按服务名连接后端实例。

```go
app := core.New(core.LoadConfigFile("config.yaml"))

conn, err := app.GrpcClient("user-service")
if err != nil {
    panic(err)
}
defer conn.Close()

client := pb.NewUserServiceClient(conn)
```

当某个逻辑服务要求 TLS 时，在该服务首次 dial 前配置 transport credentials。Core 在配置时保存 credentials 快照，每次新建连接再使用副本；证书、CA 和 server name 语义仍由应用负责。

```go
clientTLS := &tls.Config{
    MinVersion: tls.VersionTLS13,
    RootCAs:    rootPool,
    ServerName: "wallet.internal",
}
if err := app.ConfigureGRPCClient("wallet-service", core.GRPCClientConfig{
    TransportCredentials: credentials.NewTLS(clientTLS),
}); err != nil {
    return err
}

// 已有明确地址时不经过 etcd resolver。
conn, err := app.GrpcClientAt("wallet-service", "127.0.0.1:9001")
```

`ConfigureGRPCClient` 按逻辑服务名隔离，同一服务只允许配置一次，且必须早于 `GrpcClient`、`MustGrpcClient` 或 `GrpcClientAt` 的第一次调用。配置过 transport credentials 的服务不接受 `GrpcClient(serviceName, opts...)` 的额外不透明 dial options，以免同一连接出现两份 transport credentials；请把 transport 配置放入 `GRPCClientConfig`。未配置的服务继续使用原有 insecure 兼容路径。

如果依赖 `GrpcClient` 自动初始化 discovery，客户端读取 `etcd.endpoints` 和 `etcd.dialTimeout`；网关配置读取 `etcd.dial_timeout`。

如果启动阶段必须拿到连接，可以用 `MustGrpcClient`；它失败会 panic，适合 main 函数初始化，不适合请求处理链路。

```go
conn := app.MustGrpcClient("user-service")
client := pb.NewUserServiceClient(conn)
```

### 4. 网关启动

网关服务只需要连接 etcd 并启用 gateway。启动时会读取已有服务实例，之后继续 watch 服务上下线和路由变化。业务服务需要使用 `EnableEtcdRegistry` 注册并开启 reflection，网关才能自动发现方法。

```go
package main

import (
    "log"

    "github.com/xs23933/core/v3"
    "github.com/xs23933/core/v3/gateway"
)

func main() {
    app := core.New(core.LoadConfigFile("config.yaml"))

    if err := app.ConfigureGRPCClient("wallet-service", core.GRPCClientConfig{
        TransportCredentials: credentials.NewTLS(walletClientTLS),
    }); err != nil {
        log.Fatal(err)
    }

    if err := app.EnableEtcdDiscovery(nil); err != nil {
        log.Fatal("启用 etcd 服务发现失败:", err)
    }

    _, err := gateway.NewEtcdGateway(app)
    if err != nil {
        panic(err)
    }

app.Listen(":8080")
}
```

Gateway 的每个 `ServicePool` 使用 etcd 中的逻辑服务名选择 Core 客户端配置，因此可以只为一个 TLS 服务配置 credentials，而其他未迁移服务保持现有明文连接。配置必须早于 `gateway.NewEtcdGateway(app)`。

同一逻辑服务的多个 `service_id` 会参与轮询。`GET`/`HEAD` 遇到上游连接失败时，Gateway 最多改用另一个实例重试一次；写请求不会自动重放。gRPC 服务全部离线时，已自动生成的路由会保留并返回 `503`，实例恢复后继续使用原路由。

Gateway 首次请求优先选择 Ready 连接；没有 Ready 时只允许已核验方法的 Idle 连接由正常 RPC 唤醒，备用实例重试仍只选择 Ready，不选择 Connecting、TransientFailure 或 Shutdown。

当请求返回 `service <name> unavailable` 时，优先看 Gateway 日志中的 `state={...}` 诊断字段：

- `pool=missing` 或 `pool_instances=0`：网关当前没有可用的 gRPC proxy 连接池。
- `discovery=disabled`：网关没有启用 `EnableEtcdDiscovery`，无法 fallback 读取服务发现快照。
- `discovery_instances=0`：fallback discovery 中也没有该服务实例，通常需要检查业务服务是否仍在 `/<namespace>/services/<service_name>/` 下注册；未配置 namespace 时检查 `/services/<service_name>/`。
- `circuit_state=1`：该服务 proxy 连续失败后进入熔断冷却期。

服务注册携带每进程唯一 `generation`，同 ID/地址重启也重新 reflection。每个期望实例保留一个恢复任务，连接/reflection 超时后以 0.5 秒递增、最多 5 秒的间隔重试，熔断冷却后继续尝试；删除、换代或关闭取消任务。Registry 丢租恢复通过 CAS 防旧代覆盖，正常退出先撤注册，异常退出由 TTL 摘除。滚动升级采用方法并集，RPC descriptor 冲突返回 503，写请求不自动重放。

`GrpcClient("service-name")` 的 resolver 每连接绑定 Discovery；相同配置复用，改变 namespace/endpoints/etcd 身份返回 `ErrEtcdDiscoveryConfigurationChanged`，应新建 Core 实例迁移。直接 `etcd.NewRegistry` 默认没有 ready 字段、兼容立即可服务。

`ShutdownWithTimeout` 使用独立 deadline，先撤注册，再同时排空 HTTP/HTTP3/gRPC，最后关闭业务依赖与 Discovery；超时强制关闭 transport。信号退出默认 10 秒，可配置 `shutdown_timeout: "10s"`。重复调用共享首个退出任务，后来短期限只停止该调用等待。无 context 的旧 shutdown hook 无法强制取消，后台清理需它自行返回，但不延长调用方期限。

网关内置本地管理接口，仅允许 loopback 地址访问：

| 方法     | 路径                         | 说明           |
| -------- | ---------------------------- | -------------- |
| `GET`    | `/admin/gateway/routes`      | 路由列表       |
| `GET`    | `/admin/gateway/routes/:id`  | 路由详情       |
| `POST`   | `/admin/gateway/routes`      | 创建路由       |
| `PUT`    | `/admin/gateway/routes/:id`  | 更新或禁用路由 |
| `DELETE` | `/admin/gateway/routes/:id`  | 删除路由       |

### 5. HTTP Handler 路由自动注册

HTTP 服务可以把框架自动生成的 Handler 路由发布到 etcd，Gateway 发现后直接反向代理到该服务。该能力默认关闭，只收集嵌入 `core.Handler` 并按方法名生成的路由；`app.GET`、`app.POST` 等手写路由不会自动发布。

HTTP 服务完整 `main.go`：

```go
package main

import (
    "log"

    core "github.com/xs23933/core/v3"
)

type TaskHandler struct {
    core.Handler
}

func (h *TaskHandler) Init() {
    h.Prefix("/api/tasks")
}

func (h *TaskHandler) Get_id(c core.Ctx) error {
    return c.JSON(core.Map{"id": c.Params("id")})
}

func init() {
    // 这里只登记 Handler；此时不连接 etcd，也不发布 Gateway 路由。
    core.RegHandle(&TaskHandler{})
}

func main() {
    app := core.New(core.LoadConfigFile("config.yaml"))

    // 必须早于 Listen/Run：初始化 registry + discovery，并注册服务实例。
    if err := app.EnableEtcdRegistry(nil); err != nil {
        log.Fatal("enable etcd registry: ", err)
    }

    // 启动阶段加载 Handler、生成自动路由目录并写入 etcd。
    if err := app.Run(); err != nil {
        log.Fatal(err)
    }
}
```

开启自动发布时必须配置至少一个 `include_prefixes`。匹配按完整路径段判断，`exclude_prefixes` 优先排除：

```yaml
gateway:
  auto_http_routes:
    enabled: true
    include_prefixes:
      - /api
    exclude_prefixes:
      - /api/internal

etcd:
  namespace: xpay
  endpoints:
    - 127.0.0.1:2379
  service_name: task-service
  service_addr: 127.0.0.1:8081
  # Gateway 可访问的 HTTP origin，必须包含 http:// 或 https://。
  http_addr: http://127.0.0.1:8081
  service_id: task-service-1
  ttl: 5
```

调用顺序必须是 `core.New` → `EnableEtcdRegistry` → `Listen/Run`。具体时机如下：

1. Go `init` 阶段的 `core.RegHandle` 只登记 Handler 模块，不访问 etcd。
2. `EnableEtcdRegistry` 创建 Registry，复用相同配置的 Discovery，以每进程唯一 `generation` 和 `ready=false` 注册。Gateway 暂不选用准备态实例。
3. `Listen` / `Run` 启动准备阶段加载 Handler，方法名路由在这里生成并进入自动目录。
4. Handler、gRPC 和 TLS 准备完成后，启用自动 HTTP 发布时先写入完整目录，随后 Registry 更新 `ready=true`；成功后 server 才进入 Serve。未开启自动 HTTP 发布也使用相同 readiness 顺序。

开启自动发布但未先成功调用 `EnableEtcdRegistry`，或者目录写入 etcd 失败，`Listen` / `Run` 会返回错误并清理已启动资源。框架不会启动周期发布 worker。HTTP 自动路由保持对外路径和上游路径一致，不做参数转换，path、query 和 body 由目标 HTTP 服务处理；不支持路径改写或静态 Header 注入。

Gateway 服务完整 `main.go`：

```go
package main

import (
    "log"

    core "github.com/xs23933/core/v3"
    "github.com/xs23933/core/v3/gateway"
)

func main() {
    app := core.New(core.LoadConfigFile("config.yaml"))
    if err := app.EnableEtcdDiscovery(nil); err != nil {
        log.Fatal("enable etcd discovery: ", err)
    }
    gw, err := gateway.NewEtcdGateway(app)
    if err != nil {
        log.Fatal(err)
    }
    defer gw.Close()

    if err := app.Run(); err != nil {
        log.Fatal(err)
    }
}
```

Gateway 的 `config.yaml` 使用相同 namespace 和 endpoints，并监听 `8080`：

```yaml
listen: 8080
etcd:
  namespace: xpay
  endpoints:
    - 127.0.0.1:2379
```

启动 etcd、HTTP 服务和 Gateway 后验证：

```bash
curl http://127.0.0.1:8080/api/tasks/123
```

请求会由 Gateway 转发到 `etcd.http_addr` 对应实例的 `/api/tasks/123`。

不希望自动发布时，可以显式批量注册：

```go
if err := gateway.RegisterHTTPRoutes(app,
    &gateway.Route{Method: http.MethodGet, Path: "/api/tasks/:id"},
    &gateway.Route{Method: http.MethodPost, Path: "/api/tasks"},
); err != nil {
    return err
}
```

`RegisterHTTPRoutes` 会先校验完整批次，再逐条发布，不启动后台 worker；单条注册可使用 `RegisterHTTPRoute`。`ServiceName` 为空时使用最近一次成功的 etcd registry identity。相同 HTTP method 和规范化 path 的手动路由优先于自动路由。

### 6. gRPC 自动注册路由规则

网关通过 gRPC reflection 读取服务方法，并按方法名前缀自动生成 HTTP 路由。

只供内部服务调用的 gRPC 契约应通过完整 service 名排除，避免生成公共 HTTP 路由或与其它实例的同名契约冲突：

```yaml
gateway:
  grpc_service_excludes:
    - payment.provider.v1.PaymentProviderService
    - game.provider.v1.GameProviderService
```

该配置不关闭 reflection，仅跳过指定 service 的 HTTP 自动路由。

| gRPC 方法名        | HTTP 方法 | HTTP 路径示例              |
| ------------------ | --------- | -------------------------- |
| `PostLogin`        | `POST`    | `/v1/auth/user/login`      |
| `GetProfile`       | `GET`     | `/v1/auth/user/profile`    |
| `PutProfile`       | `PUT`     | `/v1/auth/user/profile`    |
| `DeleteSession`    | `DELETE`  | `/v1/auth/user/session`    |
| `GetUserById`      | `GET`     | `/v1/auth/user/:id`        |
| `GetOrderByUserId` | `GET`     | `/v1/order/order/:user/id` |

转换逻辑：

- proto package `v1.auth` 转为路径前缀 `/v1/auth`
- service `v1.auth.UserService` 转为 `/user`
- 方法前缀 `Post/Get/Put/Delete` 转为 HTTP method
- 方法名剩余部分按 CamelCase 拆成路径
- `By` 转为路径参数标记 `:`
- 缩写建议使用 `Id` 而不是 `ID`，避免被拆成 `/i/d`

例如：

```proto
syntax = "proto3";

package v1.auth;

service UserService {
  rpc PostLogin(LoginRequest) returns (LoginResponse);
  rpc GetUserById(GetUserRequest) returns (User);
}
```

自动生成：

```text
POST /v1/auth/user/login      -> /v1.auth.UserService/PostLogin
GET  /v1/auth/user/:id        -> /v1.auth.UserService/GetUserById
```

gRPC 自动路由采用当前已接入实例的方法并集：新增 RPC 仅选择支持实例，最后一个支持实例退出后移除该方法；全部离线时保留路由返回 `503`。同一 RPC 的输入/输出 descriptor（含嵌套消息与枚举）不一致时返回 `503`，协议一致后恢复。HTTP/manual 路由删除或禁用时撤下相应 source；manual 继续优先于 automatic。

### 7. 手动配置网关路由

除了自动注册，也可以通过管理接口写入路由配置。

```http
POST http://127.0.0.1:8080/admin/gateway/routes
Content-Type: application/json

{
  "method": "POST",
  "path": "/api/login",
  "service_name": "user-service",
  "grpc_method": "/v1.auth.UserService/PostLogin",
  "description": "manual login route"
}
```

禁用路由：

```http
PUT http://127.0.0.1:8080/admin/gateway/routes/{route_id}
Content-Type: application/json

{
  "method": "POST",
  "path": "/api/login",
  "service_name": "user-service",
  "grpc_method": "/v1.auth.UserService/PostLogin",
  "enabled": false
}
```

### 8. HTTP 到 gRPC 的请求映射

Gateway 的 gRPC 自动代理总期限由 `gateway.grpc_request_timeout` 配置（例如 `180s`），也可通过 `gateway.Config.GRPCRequestTimeout` 指定；默认及非正值保持 10s。此期限覆盖一次 RPC 及其允许的只读重试，短于它的入站 context deadline 仍优先生效。它与下游单次 HTTP timeout 不同，多步骤登录应为整个流程留出预算。配置在 Gateway 启动时读取，修改后需重建/重启；错误日志包含实际 elapsed 和配置 timeout。


网关会把 HTTP 路径参数、query 参数和 JSON body 合并为一个 JSON 对象，然后按 reflection 中的请求 message 反序列化。

```http
GET /v1/auth/user/123?expand=true
Authorization: Bearer token
X-Request-Id: req-1
```

会转成类似：

```json
{
  "id": "123",
  "expand": "true"
}
```

默认透传的 metadata：

- `authorization`
- `x-request-id`
- `x-user-id`
- `Ctx.Vars()` 中的本地变量 用于前置 middleware 处理后的后传参数 例如 jwt处理的: `Ctx.Set("user_id", "123")`

### gRPC 响应 Header 与 Cookie

服务的 gRPC Handler 可以直接调用 Core helper，Gateway 自动将声明的响应 metadata 写入 HTTP Header，不需要专用登录 adapter：

```go
func GrpcSetHeader(ctx context.Context, key string, values ...string) error
func GrpcSetCookie(ctx context.Context, name, value string, exp time.Time, path string, args ...any) error
func GrpcRemoveCookie(ctx context.Context, name, path string, dom ...string) error
func GrpcCookie(ctx context.Context, cookie *http.Cookie) error
```

上面列出的是 core 包导出签名。实际登录响应示例（token/expires 来自业务 Service）：

```go
if err := core.GrpcSetHeader(ctx, "Cache-Control", "no-store"); err != nil {
    return nil, err
}
if err := core.GrpcSetCookie(ctx, "session", token, expires, "", "httponly", true); err != nil {
    return nil, err
}
// 返回业务 proto response；浏览器通过 Set-Cookie 保存 token。
```

`GrpcSetCookie` 与 `BaseCtx.SetCookie` 使用同一套默认值和可变参数：

| 参数/属性 | 行为 |
| --- | --- |
| 空 `path` | 默认 `/` |
| `value` | `url.QueryEscape` 编码 |
| `SameSite` | 默认 `http.SameSiteLaxMode` |
| 字符串 `"httponly"` | 不区分大小写，启用 HttpOnly |
| 其他字符串参数 | 设置 Domain；重复参数以后者为准 |
| bool 参数 | 设置 Secure；重复参数以后者为准 |
| 未指定/空 Domain | 读取当前服务 Core 的 `domain` 配置 |
| 未指定 HttpOnly/Secure | 默认 false，登录场景显式传 `"httponly", true` |
| 其他类型参数 | 与 BaseCtx 一样忽略 |

例如指定域名：`core.GrpcSetCookie(ctx, "session", token, expires, "/", "example.com", "httponly", true)`。`GrpcRemoveCookie(ctx, "session", "/", "example.com")` 使用过去的 Expires 删除；与 BaseCtx 一致，删除时空 path 不补 `/`，首个可选 Domain 生效，其为空时回退配置。Path/Domain 必须与写入时匹配。

需要 MaxAge、自定义 SameSite 或 host-only cookie 时用完整属性入口；它与 `BaseCtx.Cookie` 一样不补默认值、不编码 value：

```go
if err := core.GrpcCookie(ctx, &http.Cookie{
    Name: "__Host-session", Value: token, Path: "/",
    HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
    MaxAge: 3600,
}); err != nil {
    return nil, err
}
```

helper 校验非法 header/cookie 并返回错误，不静默丢弃。header 名只接受字母、数字、`-_.`，避免生成不合法的 gRPC metadata key；`!`、`+` 等其他 HTTP token 符号不支持。必须使用当前 RPC context，并在 initial headers 发送前调用；Core 的 unary/stream interceptor 自动绑定当前应用配置，多个 Core 实例不会串用默认域名。自建 `grpc.NewServer()` 可使用 helper，但没有 Core 配置回退，需显式传 Domain 或完整 cookie。

响应协议固定为 `core-http-<小写 header 名>-bin` initial metadata，grpc-go 自动处理 wire 编码，value 不需手工 base64。`GrpcSetHeader` 的多值与重复调用都追加；Gateway 逐值追加 HTTP Header，多个 Set-Cookie 不合并。错误响应同样保留声明的 metadata，GET/HEAD 重试只使用最终尝试的响应。普通 metadata、trailer、非法值以及传输层 header（包括 Content-Type、Content-Length、Content-Encoding、Connection、TE、Trailer、Upgrade、Proxy-Authenticate、Proxy-Authorization 和 grpc-*）不桥接，JSON Content-Type 仍由 Gateway 管理。

原生客户端使用 `grpc.Header(&md)` 读取，例如 `md.Get("core-http-set-cookie-bin")`；原生 gRPC 不会自动操作浏览器 cookie。现有 `ReflectionProxy.Invoke` 保持 JSON-only 签名，新增 `InvokeWithHeaders` 返回 `(body, initialMetadata, error)`。

业务 Service 负责认证和签发会话，gRPC Handler 负责 cookie；Gateway 认证中间件仍需读取并验证后续请求的 cookie。登录等免认证路径使用应用的认证中间件及 `gateway/public_routes`；该能力不改变认证策略。登录 cookie 使用 HTTPS，跨站调用的 CORS/credentials 与 CSRF 策略由应用配置。

可复制、可编译的登录/退出响应脚手架见 [`example/grpc-cookie`](example/grpc-cookie/README.md)。

### 9. Demo 目录

仓库内置了几个最小 demo：

| 目录                  | 说明                     | 运行命令                         |
| --------------------- | ------------------------ | -------------------------------- |
| `example/restful`     | REST、模板、中间件、自动路由 | `go run ./example/restful`       |
| `example/work`        | 配置文件、数据库模型、分页查询 | `go run ./example/work`          |
| `example/websocket`   | WebSocket echo 示例       | `go run ./example/websocket`     |

REST demo 中同时展示了手写路由和自动路由：

```go
app.GET("/", func(c core.Ctx) {
    c.SendString("what happend")
})

type handler struct {
    core.Handler
}

func (handler) GetHello(c core.Ctx) {
    c.SendString("ok")
}

func (handler) GetUser_id(c core.Ctx) {
    c.SendString("id is %s", c.Params("id"))
}

func init() {
    core.RegHandle(&handler{})
}
```

生成的自动路由：

```text
GET /hello
GET /user/:id
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

### 4. 分页查询：Finds、游标分页与兼容 API

`Finds`、`FindPageBy` 和 `FindNextBy` 都会读取 `core.Map` 中的分页和筛选参数，并可以接收一个已经拼好的 `*core.DB` 作为基础查询。

常用参数：

| 参数       | 说明                              |
| ---------- | --------------------------------- |
| `p`        | 页码，默认 `1`                    |
| `l`        | 每页数量，默认 `20`               |
| `asc`      | 升序字段，如 `created_at`         |
| `desc`     | 降序字段，如 `created_at`         |
| `name`     | 内置 name 模糊查询                |
| `field IN` | IN 查询，如 `status IN: []int{}`  |
| `field >`  | 比较查询，支持 `> < >= <=`        |
| `field*`   | 包含匹配，等价于 `%value%`        |
| `^field`   | 前缀匹配，等价于 `value%`         |
| `field$`   | 后缀匹配，等价于 `%value`         |

三类入口的区别：

- `Finds`：推荐用于新代码，通过 `FindsParams` 传递 `Where`、`DB`、`Mode`、`Cursor` 和 `Extra`。默认返回带 `total` 的经典分页；`FindsModeCursor` 使用真正的 keyset cursor，不执行 `COUNT` 或 `OFFSET`。
- `FindPageBy`：返回 `Page[T]`，包含 `total` 总数；适合后台管理、需要显示总页数的列表。代价是会执行 count。
- `FindNextBy`：兼容旧代码的无 `COUNT` 页码 API，仍按 `p` 生成 `OFFSET`，不是真正的游标分页；新代码处理大列表时使用 `FindsModeCursor`。

#### Finds：推荐的新分页入口

共享 `Where` 对 `Finds` 全部模式、`FindPage` / `FindPageBy`、`FindNext` / `FindNextBy` 和 `Find` 统一校验输入：筛选字段仅接受 `[A-Za-z_][A-Za-z0-9_]*`，或一层 `table.column`，可以附加上表及 `NOTIN`、`!=` 等已支持的操作符。`asc` / `desc` / `omitFields` 必须为字符串，只接受逗号分隔的上述字段；空字符串表示不追加排序或排除字段。字段按数据库方言引用，筛选值继续绑定参数。

SQL 表达式、注释、引号、分号、非法字段和错误的排序参数类型会使整个查询返回 `core.ErrInvalidQuery`，不会静默丢弃条件或执行查询。直接调用 `Where` 时通过返回的 `tx.Error` 获取错误；其他列表接口通过 `error` 返回，可用 `errors.Is(err, core.ErrInvalidQuery)` 判断。游标模式仍忽略合法的 `p` / `asc` / `desc`，但会先拒绝非法排序输入。

兼容性：过去在 Map 中传入 `asc: "id DESC"`、函数排序或任意 SQL 筛选 key 的用法现在会被拒绝；请使用 `desc: "id"`，或把固定、可信的复杂表达式放入 DAO 构造的 GORM scope。框架只校验 SQL 标识符语法；业务仍须限制允许筛选、排序和返回的字段，并独立施加租户和权限条件。不要将请求字符串传给 GORM 的原始 SQL API，也不要将不可信输入包装成 `core.Expr`。

```go
func (dao *UsersViewDAO) List(ctx context.Context, whr *core.Map, next bool) (core.FindsResult[user.UsersView], error) {
    mode := core.FindsModePage
    if next {
        mode = core.FindsModeNext
    }

    if _, ok := (*whr)["asc"].(string); !ok {
        if _, ok := (*whr)["desc"].(string); !ok {
            (*whr)["desc"] = "created_at"
        }
    }

    tx := dao.db.WithContext(ctx).
        Model(&user.UsersView{}).
        Select("users_profiles.*, users.status").
        Joins("JOIN users ON users.id = users_profiles.user_id").
        Joins("JOIN users_identities ON users_profiles.user_id = users_identities.user_id")

    return core.Finds[user.UsersView](core.FindsParams{
        Where: whr,
        DB:    tx,
        Mode:  mode,
    })
}
```

#### FindsModeCursor：真正的 keyset cursor

游标模式只读取 `Where` 中的 `l` 和业务筛选条件，不读取 `p`、`asc` 或 `desc`。排序必须通过 `CursorSpec` 明确声明。字段仅允许安全的单段或一层限定标识符，例如 `id`、`orders.created_at`；使用一个唯一字段，或使用一个排序字段加一个唯一 tie-breaker：

```go
func (dao *OrderDAO) List(ctx context.Context, whr *core.Map, token, direction string) (core.FindsResult[model.Order], error) {
    tx := dao.db.WithContext(ctx).Model(&model.Order{})
    return core.Finds[model.Order](core.FindsParams{
        Where: whr,
        DB:    tx,
        Mode:  core.FindsModeCursor,
        Cursor: &core.CursorSpec{
            Token:     token,
            Direction: core.CursorDirection(direction), // "next" 或 "prev"；空值按 next
            Fields:    []string{"created_at", "id"},
            Desc:      true,
        },
    })
}
```

首次查询传空 token。后翻传上一响应的 `next_cursor` 和 `next`，前翻传 `prev_cursor` 和 `prev`。前翻查询会临时反转 SQL 排序并在返回前恢复规范顺序，因此调用方始终收到相同的 `created_at DESC, id DESC` 顺序。

```json
{
  "p": 1,
  "l": 20,
  "next_cursor": "eyJ2IjoxLC4uLn0",
  "has_next": true,
  "has_prev": false,
  "data": []
}
```

实现使用 `limit + 1` 判断当前导航方向是否还有数据，不执行 `COUNT` 或 `OFFSET`。token 是版本化的 URL-safe opaque 值，并绑定字段和升降序；它不是签名或授权凭据。排序字段应为非空，最后一个字段必须唯一且具备索引。常用组合是 `created_at,id` 或单独的 `id`。

需要先合并多个数据源的列表可以复用相同 token 格式：

```go
token, err := core.EncodeCursorToken([]string{"created_at", "id"}, true, row.CreatedAt, row.ID)
values, err := core.DecodeCursorToken(core.CursorSpec{
    Token: token, Fields: []string{"created_at", "id"}, Desc: true,
})
```

`DecodeCursorToken` 对版本、字段、排序和值数量进行校验，并以 `json.Number` 返回 JSON 数字，避免 64 位 ID 精度损失。聚合查询仍需自行实现各数据源的边界查询和 `limit + 1` 合并。

#### FindPageBy：带总数分页

```go
type UsersViewDAO struct {
    db *core.DB
}

func (dao *UsersViewDAO) ListPage(ctx context.Context, whr *core.Map) (core.Page[user.UsersView], error) {
    if _, ok := (*whr)["asc"].(string); !ok {
        if _, ok := (*whr)["desc"].(string); !ok {
            (*whr)["desc"] = "created_at"
        }
    }

    tx := dao.db.WithContext(ctx).
        Model(&user.UsersView{}).
        Select("users_profiles.*, users.status").
        Joins("JOIN users ON users.id = users_profiles.user_id").
        Joins("JOIN users_identities ON users_profiles.user_id = users_identities.user_id")

    res := make([]user.UsersView, 0)
    return core.FindPageBy(whr, &res, tx)
}
```

返回结构：

```json
{
  "p": 1,
  "l": 20,
  "total": 128,
  "data": []
}
```

#### FindNextBy：兼容的无 Count 页码分页

`FindNextBy` 会多查一条数据判断 `next`，并在返回前裁掉探针行。它仍使用 `p`/`OFFSET`，仅用于兼容现有调用方。

```go
func (dao *UsersViewDAO) ListNext(ctx context.Context, whr *core.Map) (core.NextPage[user.UsersView], error) {
    if _, ok := (*whr)["asc"].(string); !ok {
        if _, ok := (*whr)["desc"].(string); !ok {
            (*whr)["desc"] = "created_at"
        }
    }

    tx := dao.db.WithContext(ctx).
        Model(&user.UsersView{}).
        Select("users_profiles.*, users.status").
        Joins("JOIN users ON users.id = users_profiles.user_id").
        Joins("JOIN users_identities ON users_profiles.user_id = users_identities.user_id")

    res := make([]user.UsersView, 0)
    return core.FindNextBy(whr, &res, tx)
}
```

返回结构：

```json
{
  "p": 1,
  "l": 20,
  "next": true,
  "prev": false,
  "data": []
}
```

#### 同一个 DAO 根据参数切换分页模式

```go
func (dao *UsersViewDAO) List(ctx context.Context, whr *core.Map, page bool) (any, error) {
    _, asc := (*whr)["asc"].(string)
    _, desc := (*whr)["desc"].(string)
    if !asc && !desc {
        (*whr)["desc"] = "created_at"
    }
    if tp := whr.GetString("type"); tp != "" {
        (*whr)["type"] = constants.ParseLoginType(tp)
    }

    tx := dao.db.WithContext(ctx).
        Model(&user.UsersView{}).
        Select("users_profiles.*, users.status").
        Joins("JOIN users ON users.id = users_profiles.user_id").
        Joins("JOIN users_identities ON users_profiles.user_id = users_identities.user_id")

    res := make([]user.UsersView, 0)

    if page {
        return core.FindPageBy(whr, &res, tx)
    }

    return core.FindNextBy(whr, &res, tx)
}
```

### 5. 事件钩子

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

## Redis Cache 通用包装器

`cache` 是独立子包，用于封装“先读缓存，未命中再查 DB，并自动回填缓存”的常见模式。

导入路径：

```go
import "github.com/xs23933/core/v3/cache"
```

推荐为每类数据创建可复用 cache 实例，把 Redis 连接、前缀、TTL 和空值缓存策略集中配置一次。

```go
type UserVO struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

var userCache = cache.New(
    core.RConn("cache"),
    cache.Prefix("user:"),
    cache.TTL(10*time.Minute),
    cache.EmptyTTL(time.Minute),
    cache.Jitter(30*time.Second),
    cache.CacheNil(true),
)

func GetUser(ctx context.Context, id string) (UserVO, error) {
    var user UserVO
    err := userCache.Take(ctx, id, &user, func(ctx context.Context) (any, error) {
        return dao.GetUserByID(ctx, id)
    })
    return user, err
}
```

同一个 cache 实例可以缓存不同类型，类型由 `out` 指针决定。

```go
var order OrderVO
err := userCache.Take(ctx, "order:"+orderID, &order, func(ctx context.Context) (any, error) {
    return dao.GetOrderByID(ctx, orderID)
})
```

想保留返回值风格时，用包级泛型 helper。它不要求 `New` 绑定类型。

```go
user, err := cache.Load[UserVO](userCache, ctx, "user:"+id, func(ctx context.Context) (UserVO, error) {
    return dao.GetUserByID(ctx, id)
})

user, err = cache.Get(ctx, "user:"+id, func(ctx context.Context) (UserVO, error) {
    return dao.GetUserByID(ctx, id)
})
```

空值缓存用于防止不存在的数据持续打到 DB。默认识别 `cache.ErrNotFound`，也可以接入项目自己的 not found 错误。

```go
var userCache = cache.New(
    core.RConn("cache"),
    cache.Prefix("user:"),
    cache.CacheNil(true),
    cache.NotFound(func(err error) bool {
        return errors.Is(err, gorm.ErrRecordNotFound)
    }),
)
```

更新或删除数据后删除缓存：

```go
if err := userCache.Delete(ctx, id); err != nil {
    return err
}
```

规则：

- `Take` / `Load` 内部使用 `singleflight` 合并同进程并发 miss，避免缓存击穿。
- Redis 读失败会降级执行 loader；写缓存失败不会影响返回结果。
- TTL 可用 `Jitter` 增加随机抖动，避免大量 key 同时过期。
- 不要在每次请求中重复 `cache.New`；应创建可复用实例。

## Fetch API 客户端

可复用客户端使用 `fetch.New(baseURL).Timeout(45 * time.Second).SetProxy(proxyURL)` 配置每次请求预算，无需为超时构造 `http.Client`。非正值保持默认 30 秒，更短的父 context deadline 优先。Timeout 复制并原子替换客户端配置，保留代理、Cookie、重定向和其它设置；不修改调用方传入或已开始请求使用的客户端。

`fetch` 是独立子包，用于调用外部 HTTP API。设计接近前端 JavaScript `fetch` 的使用习惯，但保留 Go 的显式错误处理和结构体 decode。

导入路径：

```go
import "github.com/xs23933/core/v3/fetch"
```

### 1. 最简调用

包级快捷方法使用 `fetch.Default`，适合一次性调用或简单脚本。

```go
type UserVO struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

var user UserVO
res, err := fetch.Get("https://api.example.com/users/1", &user)
if err != nil {
    return err
}

token := res.Header.Get("X-Token")
_ = token
```

POST / PUT 带参数时，`params` 会按类型自动处理：

| 参数类型 | 处理方式 |
| -------- | -------- |
| `nil` | 不发送 body |
| `[]byte` | 原始 body |
| `string` | 字符串 body |
| 其它类型 | JSON body，并设置 `Content-Type: application/json; charset=utf-8` |

```go
var out UserVO

_, err := fetch.Post(
    "https://api.example.com/users",
    map[string]any{"name": "tom"},
    &out,
)

_, err = fetch.Put(
    "https://api.example.com/users/1",
    map[string]any{"name": "jerry"},
    &out,
)

_, err = fetch.Delete("https://api.example.com/users/1", nil)
```

### 2. 可复用 Client

业务服务中推荐创建可复用 client，统一配置 baseURL、公共 Header、Cookie 和 Hook。

```go
var api = fetch.New("https://api.example.com").
    Header("X-App", "core-service").
    Header("Accept", "application/json").
    UseCookie(true)

func GetUser(ctx context.Context, id string) (UserVO, error) {
    var user UserVO

    res, err := api.DoGet(ctx, "/users/"+id, &user)
    if err != nil {
        return user, err
    }

    refreshedToken := res.Header.Get("X-Token")
    _ = refreshedToken

    return user, nil
}
```

公共 Header 和单次 Header 分开：

```go
api := fetch.New("https://api.example.com").
    Header("X-App", "core-service") // 每次请求都有

var out UserVO
res, err := api.Post("/users").
    Header("X-Request-ID", "req-123"). // 只对本次请求生效
    JSON(map[string]any{"name": "tom"}).
    Result(context.Background(), &out)
```

### 3. 请求前 Hook：签名、鉴权、时间戳

`Before` 在请求发出前执行，可以读取最终 body 并修改 `*http.Request`。常用于 hash 签名、认证 Header、请求追踪。

```go
api := fetch.New("https://api.example.com").
    Before(func(ctx context.Context, req *http.Request, body []byte) error {
        sign := core.SHA256HashBytes(body)
        req.Header.Set("X-Sign", sign)
        req.Header.Set("X-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
        return nil
    })

var out UserVO
_, err := api.DoPost(context.Background(), "/users", map[string]any{"name": "tom"}, &out)
```

### 4. 响应后 Hook：解包、解密、统一 decode

`After` 在响应 body 读取完成后执行，返回值会作为最终 body 继续 decode。适合统一响应格式解包。

```go
type Envelope struct {
    Code int             `json:"code"`
    Msg  string          `json:"msg"`
    Data json.RawMessage `json:"data"`
}

api := fetch.New("https://api.example.com").
    After(func(ctx context.Context, resp *http.Response, body []byte) ([]byte, error) {
        var env Envelope
        if err := json.Unmarshal(body, &env); err != nil {
            return nil, err
        }
        if env.Code != 0 {
            return nil, fmt.Errorf("api error %d: %s", env.Code, env.Msg)
        }
        return env.Data, nil
    })

var user UserVO
_, err := api.DoGet(context.Background(), "/users/1", &user)
```

### 5. 获取响应 Header、Status、Body

使用 `Result` 或 `DoGet/DoPost/DoPut/DoDelete` 会返回 `*fetch.FetchResult`。

```go
res, err := api.Get("/session").Result(context.Background(), &out)
if err != nil {
    if ferr, ok := err.(*fetch.FetchError); ok {
        retryToken := ferr.Header.Get("X-Token")
        body := string(ferr.Body)
        _ = retryToken
        _ = body
    }
    return err
}

token := res.Header.Get("X-Token")
status := res.StatusCode
body := res.Body
```

只需要响应结果，不需要 decode 到 `out` 时可以省略第二个参数：

```go
res, err := api.Get("/session").Result(context.Background())
if err != nil {
    return err
}

token := res.Header.Get("X-Token")
rawBody := res.Body
```

### 6. 调试请求和响应

排查外部 API 问题时，可以显式开启 `Debug(true)`。每次请求结束时会打印方法、URL、最终请求 Header、请求 body、响应状态、响应 Header、响应 body 和错误信息。响应 body 如果是 `Content-Encoding: gzip` 会先解压再输出。

```go
api := fetch.New("https://api.example.com").
    Header("X-App", "core-service").
    Debug(true)

var out UserVO
_, err := api.Post("/users").
    Header("X-Request-ID", "req-123").
    JSON(map[string]any{"name": "tom"}).
    Result(context.Background(), &out)
```

调试日志会包含请求和响应 body，生产环境只应在定位问题时短期开启。

### 7. HTTP/SOCKS5 代理

`SetProxy` 支持 HTTP 和 SOCKS5 代理，账号密码会从代理 URL 中解析：

```go
api := fetch.New("https://api.example.com").
    SetProxy("http://127.0.0.1:7890")

api.SetProxy("socks5://127.0.0.1:1080")
api.SetProxy("http://user:password@127.0.0.1:7890")
api.SetProxy("socks5://user:password@127.0.0.1:1080")
```

传入空字符串会关闭代理：

```go
api.SetProxy("")
```

### 8. Cookie 开关

默认不保存 Cookie，避免隐式状态。需要模拟浏览器会话时显式启用：

```go
api := fetch.New("https://api.example.com").UseCookie(true)

// 禁用并清空 cookie jar
api.UseCookie(false)
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

app.Use(cors.New(cors.Config{
    AllowOrigins:     "http://localhost:3000",
    AllowMethods:     "GET,POST,PUT,DELETE,OPTIONS",
    AllowHeaders:     "Content-Type,Authorization",
    AllowCredentials: true,
}))
```

`cors.New` 只构造中间件；框架会让未匹配到显式路由的 OPTIONS 请求经过全局中间件，因此无需另外注册预检路由。精确 Origin 包含 scheme 和显式端口；`*.example.com` 只匹配真实子域，`https://*.example.com:8443` 还会限制 scheme 与端口。`AllowHeaders` 为空时会回显浏览器请求的 headers。安全起见，`AllowOrigins: "*"` 不能与 `AllowCredentials: true` 同时使用。

旧调用 `cors.New(app, config...)` 应迁移为 `cors.New(config...)`；过渡期可使用已废弃的 `cors.NewWithApp(app, config...)`。

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

设置 `case-sensitive: false` 可让 HTTP 路由的 ASCII 静态路径忽略大小写，例如 `/transferIn` 和 `/transferin` 访问同一接口；未配置或设置为 `true` 时保留大小写敏感行为。此配置在 `core.New` 时读取，自动 Handler、显式路由、Group、路径中间件和 `RemoveHandle` 使用相同规则。参数名、参数值、query、`c.Path()` 和原始 URL 保留大小写；非 ASCII 路径仍精确匹配。开启后仅静态路径大小写不同的同方法路由视为同一路由，后注册的 handler 覆盖前者。

| 选项            | 类型   | 默认值    | 说明             |
| --------------- | ------ | --------- | ---------------- |
| `debug`         | bool   | `false`   | 调试模式         |
| `case-sensitive` | bool | `true` | HTTP 静态路径是否区分 ASCII 大小写 |
| `listen`        | string | `":8080"` | 监听地址         |
| `network`       | string | `"tcp4"`  | 网络协议         |
| `prefork`       | bool   | `false`   | 是否启用 prefork |
| `max_body_size` | string | `"1MB"`   | 最大请求体大小   |
| `read_timeout`  | string | `"5s"`    | 读取超时         |
| `write_timeout` | string | `"10s"`   | 写入超时         |
| `trusted_proxies` | []string | `[]`   | c.RemoteIP() 可信代理 IP/CIDR；空列表默认私网/回环，非空覆盖默认规则；非法配置初始化报错 |

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
// 方式一：始终输出请求日志。
// core.New() 默认不输出，需要时显式启用。
app.Use(core.Logger())

// 方式二：按 debug 配置控制或自定义输出。
// 与上面的裸 Logger() 二选一，不要同时注册。
app.Use(core.Logger(core.LoggerConfig{
	App:    app,
	Debug:  app.Debug,
	Output: os.Stdout,
}))

// 自定义日志级别
core.Info("用户登录成功: %s", username)
core.Warn("API 调用频繁: %s", ip)
core.Erro("数据库连接失败: %v", err)
core.D("请求参数: %v", params)
```

`core.New()` 默认注册的 Recovery 会捕获普通 panic、记录堆栈并返回 500。对于 `net/http.ErrAbortHandler`（包括包装后的同一错误），Recovery 会按标准库语义静默终止当前请求，不记录 panic、不调用自定义 recovery handler，也不再尝试写入 500；这适用于反向代理或 SSE 客户端已断开连接的场景。

#### 日志轮转

框架内置日志文件轮转，配置文件方式：

```yaml
log: /var/log/myapp/app.log
log_rotate:
  - size: 300M
  - daily
  - rotate: 30
  - compress
  - delaycompress: 24h
  - missingok
  - notifempty
  - copytruncate
```

只要配置了 `log` 且 `log_rotate` 未配置、为 `null`、空字符串或空数组，框架会自动套用上面的默认轮转配置。

参数说明：

| 参数 | 含义 |
| --- | --- |
| `size: 300M` | 日志文件达到指定大小后切割，支持 `K/KB/M/MB/G/GB`，也支持纯数字字节数 |
| `daily` | 按天切割，跨日期后第一次写入会创建新的日志文件 |
| `rotate: 30` | 保留最近 30 天的切割日志，超过期限自动删除 |
| `compress` | 切割后的日志文件压缩为 `.gz` |
| `delaycompress` | 延迟压缩切割日志，默认延迟 `24h`，需配合 `compress` |
| `delaycompress: 2h` | 指定延迟压缩时长，支持 Go duration 格式，如 `30m`、`2h`、`24h` |
| `missingok` | 日志文件不存在时继续运行并重新创建，不返回错误 |
| `notifempty` | 当前日志文件为空时不切割、不压缩、不生成空的轮转文件 |
| `copytruncate` | 切割时复制当前日志再截断原文件，适合不希望替换文件句柄的部署方式 |

调用 `RotatingLogWriter.RedirectStdout` 后，框架会强制使用 copy-truncate 语义，即使配置中没有显式写 `copytruncate`。这是为了在并发 `fmt.Print*` 时保持 `os.Stdout` 指针和文件描述符稳定，避免轮转过程替换进程级全局指针产生数据竞争；未重定向 stdout 的 writer 仍按配置选择 rename 或 copy-truncate。

代码方式：

```go
w, err := core.NewRotatingLogWriter(
    "/var/log/myapp/app.log",
    "size: 300M",
    "daily",
    "rotate: 30",
    "compress",
    "delaycompress: 24h",
    "missingok",
    "notifempty",
    "copytruncate",
)
if err != nil {
    panic(err)
}
defer w.Close()

app.Use(core.Logger(core.LoggerConfig{
    App:    app,
    Debug:  app.Debug,
    Output: w,
}))
```

### 4. 性能监控

```go
import (
    "time"

    "github.com/xs23933/core/v3/middleware/metrics"
    "github.com/xs23933/core/v3/middleware/ratelimit"
)

// 启用指标收集
m, mw := metrics.New()
app.Use(mw)
metrics.Mount(app, "/metrics", m)

// 启用限流（默认内存）
app.Use(ratelimit.New(ratelimit.Config{
    Max:    300,
    Window: time.Minute,
}))

// 更平滑的用户级限流
app.Use(ratelimit.New(ratelimit.Config{
    Max:       120,
    Window:    time.Minute,
    Algorithm: ratelimit.SlidingWindow,
    KeyFunc: func(c core.Ctx) string {
        return c.GetString("user_id", c.RemoteIP().String())
    },
}))
```

## 扩展开发

### 1. 开发自定义中间件

```go
package middleware

import (
    "time"

    "github.com/xs23933/core/v3"
)

func AccessLog() core.HandlerFunc {
    return func(c core.Ctx) error {
        start := time.Now()
        err := c.Next()
        core.Info("%s %s status=%d cost=%s",
            c.Method(), c.Path(), c.GetStatus(), time.Since(start))
        return err
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
| `All`      | ALL       |

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
func (h *Handler) GetByID(c core.Ctx) error {}
// GET /:id

func (h *Handler) GetByUserid(c core.Ctx) error {}
// GET /:userid

func (h *Handler) GetByIDProfile(c core.Ctx) error {}
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
func (h *Handler) GetByID(c core.Ctx) error {
    id := c.Params("id")  // 参数名为 "id"
    // ...
}

// 路由: GET /:userid/profile
func (h *Handler) GetByUseridProfile(c core.Ctx) error {
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
func (h *UserHandler) GetByID(c core.Ctx) error {
    id := c.Params("id")
}

// GET /api/v1/users/:id/profile
func (h *UserHandler) GetByID_Profile(c core.Ctx) error {
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

## 六、命名转换规则（ToNamer）

框架内部使用 `ToNamer` 函数进行命名转换：

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
| `GetByID`           | `ByID` → `/:id`                           | `GET /:id`              |
| `Get_userId`        | `_userId` → `/:userId`                    | `GET /:userId`          |
| `GetByID_Profile`   | `_id` + `Profile` → `/:id/profile`        | `GET /:id/profile`      |
| `GetParam`          | `Param` → `/:param`                       | `GET /:param`           |
| `GetParam_Callback` | `Param` + `Callback` → `/:param/callback` | `GET /:param/callback`  |
| `GetParams`         | `Params` → `/:param?`                     | `GET /:param?`          |
| `GetParams_Detail`  | `Params` + `Detail` → `/:param?/detail`   | `GET /:param?/detail`   |
| `GetId`             | `Id` → `/id`                              | `GET /id`（不是参数！）   |
| `PostLogin`         | `Login` → `/login`                        | `POST /login`           |

---

## 七、常见错误

### 7.1 混淆大写和下划线

```go
// ❌ 错误：期望 /:id，实际得到 /id
func (h *Handler) GetId(c core.Ctx) error {}
// GET /id  （Id 是路径片段，不是参数）

// ✅ 正确：使用下划线定义参数
func (h *Handler) GetByID(c core.Ctx) error {}
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
func (h *Handler) GetByID(c core.Ctx) error {}
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
│  GetByID              →  GET /:id       (自定义参数)          │
│  GetUserid            →  GET /:userid   (自定义参数)         │
│  GetByID_Profile      →  GET /:id/profile                   │
│  GetParam             →  GET /:param     (必选关键字)        │
│  GetParam_Callback    →  GET /:param/callback               │
│  GetParams            →  GET /:param?    (可选关键字)        │
│  GetParams_Detail     →  GET /:param?/detail                │
│  GetId                →  GET /id       (不是参数！)          │
│  Post                 →  POST /                             │
│  PostLogin            →  POST /login                        │
│  PutID                →  PUT /:id                           │
│  DeleteID             →  DELETE /:id                        │
└─────────────────────────────────────────────────────────────┘

参数获取:
  - 关键字参数: c.Params("param")
  - 自定义参数: c.Params("id"), c.Params("userId") 等
```

## 版本历史

### v3.1.57 (当前版本)

- 修复 HTTP 路由树未应用 `case-sensitive: false` 的问题，支持 `/transferIn` 与 `/transferin` 兼容；默认保持大小写敏感。
- 保留参数名、参数值、query 和原始 URL，统一注册、中间件、Group 及移除行为，并补充回归测试。
- 完善 WebSocket 可运行示例，修正队列释放测试命名。

### v3.1.0

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


## 托管工作负载 mTLS

通过 `core.LoadConfigFile` 加载 YAML 或加密 `.dat` 时，`workload.state_dir`、`trust_bundle_file`、`bootstrap_token_file` 以及 `workload_issuer.state_dir`、`policy_file`、`pin_file` 的非空相对路径，在环境变量展开后按该配置文件所在目录解析。`state_dir: ./workload` 因而始终位于配置旁，不随启动 cwd 或 `-f`/`--config` 的绝对、相对写法变化。绝对路径保持原值；直接以程序构造 `Options` 或调用 `workload.Open` 时，仍由调用方确定路径基准。其它业务配置路径不受此规则影响。

Gateway 可使用 Go 标准库首次自动创建私有 CA，后端自动申请、本地保存和续期，服务端、服务间客户端和 Gateway reflection 复用本进程身份。使用前必须在首次创建 gRPC server/client 前调用 `app.EnableWorkloadIdentity()`；启用后禁止明文回退或逐目标显式 transport 冲突配置。完整配置、引导认证、独立端口、根信任更新和验收边界见 [工作负载身份说明](workload/README.md)。

后端跨服务器可预置 `workload.root_sha256`（根证书 DER SHA-256），Core 从内部签发入口 `GET /root.pem` 无凭据下载，校验指纹及本次 HTTPS 链/主机名后原子缓存，再申请身份。每次启动校验缓存，拒绝不匹配/损坏缓存的静默覆盖；默认缓存位于本进程 state_dir，不依赖 Gateway 共享文件。指纹必须由可信部署渠道预置，详见工作负载说明。

## NSQ 持久化业务重试策略

`NSQConsumerConfig.DisableMaxAttempts=true` 显式关闭 go-nsq 的次数耗尽自动 FIN，将其 `MaxAttempts` 设为 0。默认 false 保持旧行为：未指定或 0 的 `MaxAttempts` 使用 5；正值为有限次数。金融 Outbox/Inbox 等要求故障恢复的消费者应显式启用此项，避免在调用业务 Handler 之前因次数耗尽丢失重试。

该选项不改变 Handler 语义：nil 仍 FIN，error 仍 REQ。只有业务已持久化完成或持久化隔离处理后才能返回 nil；仍在处理的 Inbox 锁、取消、超时和暂态故障须返回 error。格式错误的 poison 消息也不能只靠次数耗尽丢弃，调用方须选择持久隔离及诊断或继续有界退避重试。此项不提供 DLQ、持久化 Inbox 或业务幂等；父 context 和原命令键恢复仍由调用方负责。
