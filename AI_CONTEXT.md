# [AI Context] Core Framework v3

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
- **路由歧义限制**: 同一结构位置不能使用不同参数名，也不能混用必选/可选参数；可选参数和 catch-all 必须是最后一段，且路径不得包含 `//`。违规路由会在注册时 panic。
- **Catch-all**: 具名 catch-all 如 `/*path` 用 `c.Params("path")` 读取；裸 `/*` 用 `c.Params("*")` 读取。

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

// SSE 推送
c.SSEWrite(event, data, id...)      // 写入 SSE 事件（纯文本）
c.SSESend(event, data, id...)       // 发送结构化 SSE 事件（自动 JSON 序列化）
c.SSEComment(comment)               // 发送 SSE 注释（心跳）

// 文件操作
file, _ := c.FormFile("file")
relPath, absPath, _ := c.SaveFile("file", "./uploads")
```

### 3.2 Handler 生命周期

所有自动路由 Handler 必须嵌入 `core.Handler`。框架会在注册和请求处理阶段识别这些可选方法：

| 方法 | 触发时机 | 主要用途 |
| ---- | -------- | -------- |
| `Init()` | Handler 注册/应用初始化阶段 | 设置路由前缀、初始化轻量配置 |
| `Preload(c core.Ctx) error` | 每个匹配到该 Handler 的请求进入业务方法前 | 做该 Handler 级别的认证、上下文注入、审计；必须 `return c.Next()` 才会继续 |
| `Start(app *core.Core) error` | 应用启动阶段 | 启动依赖、预热缓存、注册后台任务 |
| `Stop(app *core.Core) error` | 应用关闭阶段 | 释放资源、停止后台任务 |

推荐：

```go
type UserHandler struct {
    core.Handler
}

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
    core.Info("user handler started")
    return nil
}

func (h *UserHandler) Stop(app *core.Core) error {
    core.Info("user handler stopped")
    return nil
}
```

禁止：

* 在 `Init()` 中访问请求数据
* 在 `Preload()` 中忘记 `return c.Next()`
* 在 goroutine 中保存或复用 `core.Ctx`
* 在 `Start()` 中执行无超时的阻塞任务
* 在 `Stop()` 中 panic 或忽略释放错误

### 3.3 数据库操作 (基于 GORM)
- **连接**: `db := core.Conn()` (默认) 或 `core.Conn("log")` (多库)
- **模型**: 必须嵌入 `core.Model` (包含 `ID`, `CreatedAt`, `UpdatedAt`, `DeletedAt`)
    ```go
    type User struct {
        core.Model
        Username string `gorm:"uniqueIndex"`
    }
    ```
- **分页查询**:
    - **新代码推荐**: `core.Finds[User](core.FindsParams{Where: whr, DB: tx})`
        - 默认总数分页；`Mode: core.FindsModeNext` 切换为滚动分页。
    - **总数分页 (后台)**: `core.FindPageBy(whr, &users, tx)`
        - 支持参数: `p`(页码), `l`(数量), `desc`/`asc`(排序), `field*`(包含), `field IN`(范围)。
    - **滚动分页 (移动端)**: `core.FindNextBy(whr, &users, tx)`

- **钩子**: 支持 GORM 钩子 (`BeforeSave`, `AfterFind` 等)。

### 3.4 工具函数与通用类型

#### Map / Array

`core.Map` 和 `core.Array` 是框架内置 JSON 友好类型，支持 GORM `Value/Scan`。

```go
whr := &core.Map{
    "p":     1,
    "l":     20,
    "desc":  "created_at",
    "name*": "tom",
}

name := whr.GetString("name")
page := whr.GetInt("p", 1)
ok := whr.Contains("desc")

arr := core.ParseAndDeduplicate("a,b,a")
joined := arr.StringsJoin(",")
```

常用方法：

| 类型 | 方法 | 说明 |
| ---- | ---- | ---- |
| `Map` | `GetString/GetInt/GetBool` | 安全读取并支持默认值 |
| `Map` | `GetAs/UnmarshalTo` | 将字段或整个 map 转成结构体 |
| `Map` | `Contains` | 判断 key 是否存在 |
| `Array` | `String/StringsJoin` | 转字符串切片或拼接 |
| `Array` | `FindHandle` | 在数组内按字段查找对象 |

#### 切片工具

```go
core.Contains([]string{"a", "b"}, "a")
core.Remove([]int{1, 2, 3}, 2)
core.Unique([]string{"a", "a", "b"})
core.Filter(users, func(u User) bool { return u.Active })
```

#### 文件与路径

```go
rel, abs, err := core.MakePath("avatar.png", "./uploads")
ok := core.Exists(abs)
fs := core.Dir("./static", false) // false 禁止目录列表
```

#### 网络与请求信息

```go
ip, err := core.LocalIP()
clientIP := core.RemoteIP(req.Header, req.RemoteAddr)
info := core.ExtractClientInfo(ctx) // gRPC metadata + peer 信息
ua := core.GrpcHeader(ctx, "user-agent")
domain := core.ExtractPrimaryDomain("api.example.com")
```

#### 错误与哈希

```go
err := core.NewError(40001, "invalid token")
code, msg := err.Errors()

hash := core.SHA256("route-id")
```

### 3.5 加密与密码工具

`crypto.go` 提供 AES-GCM、bcrypt 和 SHA-256。生成安全相关代码时优先使用这些封装。

```go
encrypted, err := core.EncryptAES("secret")
plain, err := core.DecryptAES(encrypted)

hash, err := core.HashPassword("password")
ok := core.CheckPassword("password", hash)

idx := core.SHA256Hash("user@example.com")
```

注意：

* `EncryptAES/DecryptAES` 使用全局 `core.AESKey`，生产环境必须在启动时替换为安全的 32 字节密钥。
* 密码只使用 `HashPassword` / `CheckPassword`，不要自己保存明文或用 SHA-256 存密码。
* `SHA256Hash` 适合邮箱、手机号等索引哈希，不适合密码哈希。

### 3.6 Fetch API 客户端

外部 HTTP API 调用优先使用独立子包 `github.com/xs23933/core/v3/fetch`。

导入：

```go
import "github.com/xs23933/core/v3/fetch"
```

推荐生成可复用 client，而不是在业务函数中反复创建 `http.Client`：

```go
var api = fetch.New("https://api.example.com").
    Header("X-App", "core-service").
    UseCookie(true)
```

快捷调用：

```go
var out UserVO

res, err := fetch.Get("https://api.example.com/users/1", &out)
token := res.Header.Get("X-Token")

_, err = fetch.Post("https://api.example.com/users", map[string]any{
    "name": "tom",
}, &out)

_, err = fetch.Put("https://api.example.com/users/1", map[string]any{
    "name": "jerry",
}, &out)

_, err = fetch.Delete("https://api.example.com/users/1", nil)
```

可复用 client 调用：

```go
var out UserVO

res, err := api.DoPost(ctx, "/users", map[string]any{"name": "tom"}, &out)
if err != nil {
    return err
}

refreshedToken := res.Header.Get("X-Token")
_ = refreshedToken
```

Header 规则：

* `api.Header("X-App", "...")` 是公共 Header，每次请求都会带上。
* `api.Post("/x").Header("X-Request-ID", "...")` 是单次 Header，只对当前请求生效。
* 单次 Header 会覆盖同名公共 Header。

Hook 规则：

```go
api := fetch.New("https://api.example.com").
    Before(func(ctx context.Context, req *http.Request, body []byte) error {
        req.Header.Set("X-Sign", core.SHA256HashBytes(body))
        return nil
    }).
    After(func(ctx context.Context, resp *http.Response, body []byte) ([]byte, error) {
        // 统一解包、解密或 decode
        return body, nil
    })
```

响应结果：

* `Do(ctx, &out)` 只关心 decode。
* `Result(ctx, &out)` 返回 `*fetch.FetchResult`，可读取 `StatusCode/Header/Body`。
* 只需要 `FetchResult` 不需要 decode 时，使用 `Result(ctx)`。
* 非 2xx 返回 `*fetch.FetchError`，其中也包含 `Header/Body`，例如错误响应中的 `X-Token`。
* `Debug(true)` 会在请求结束时打印方法、URL、最终请求 Header、请求 body、响应状态、响应 Header、响应 body 和错误信息；gzip 响应 body 会先解压再输出；只在排查问题时短期开启。
* `SetProxy(...)` 支持 `http://ip:port`、`socks5://ip:port`、`http://user:password@ip:port`、`socks5://user:password@ip:port`；传入空字符串会关闭代理。

禁止：

* 不要在业务代码中散落 `http.NewRequest`、`http.Client.Do`。
* 不要把签名逻辑复制到每个 API 调用；用 `Before`。
* 不要手写重复响应解包；用 `After`。

### 3.6.1 Redis Cache

DAO 查询缓存优先使用独立子包 `github.com/xs23933/core/v3/cache`。

推荐为每类数据创建可复用实例：

```go
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

规则：

* 简单场景可以用 `cache.Get[T](ctx, key, loader)`。
* 复杂场景用 `cache.New(...)` 复用 Redis 连接、前缀、TTL 和 singleflight。
* 同一个 `*cache.Cache` 可以通过 `Take(ctx, key, &out, loader)` 缓存不同类型。
* 想要返回值风格时，用 `cache.Load[T](typedCache, ctx, key, loader)`。
* not found 需要缓存时启用 `cache.CacheNil(true)`，默认识别 `cache.ErrNotFound`。
* 项目自己的 not found 错误用 `cache.NotFound(func(error) bool { ... })` 接入。
* 更新或删除 DB 后必须调用 `typedCache.Delete(ctx, key)` 删除缓存。
* 不要在每个请求中重复 `cache.New`。

### 3.7 中间件
```go
// 使用内置中间件
app.Use(requestid.New())
app.Use(cors.New())
app.Use(core.Logger()) // 显式启用请求日志；core.New() 默认不输出 access log

m, mw := metrics.New()
app.Use(mw)
metrics.Mount(app, "/metrics", m)

app.Use(ratelimit.New(ratelimit.Config{
    Max:    300,
    Window: time.Minute,
}))

app.Use(ratelimit.New(ratelimit.Config{
    Max:       120,
    Window:    time.Minute,
    Algorithm: ratelimit.SlidingWindow,
    KeyFunc: func(c core.Ctx) string {
        return c.GetString("user_id", c.RemoteIP().String())
    },
}))

// 自定义中间件
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

未匹配显式路由的请求会先执行全局中间件，再由框架 fallback handler 返回 404；路径级中间件不会执行。显式注册的 `core.Logger()` 因此可记录 404。`IsRouteFallback(c)` 仅用于识别未匹配的 OPTIONS 预检 fallback，不会将普通 404 标记为 OPTIONS fallback。

### 3.8 gRPC 与网关

服务端生成代码时遵循这个顺序：

```go
app := core.New(core.LoadConfigFile("config.yaml"))

// 需要 transport credentials 或 interceptor 时，先调用且只调用一次。
if err := app.ConfigureGRPCServer(core.GRPCServerConfig{
    TransportCredentials: credentials.NewTLS(serverTLS),
    UnaryInterceptors:     []grpc.UnaryServerInterceptor{identityUnary},
    StreamInterceptors:    []grpc.StreamServerInterceptor{identityStream},
}); err != nil {
    return err
}

app.RegisterGRPCService(func(s *grpc.Server) {
    pb.RegisterUserServiceServer(s, &UserService{})
})

if err := app.EnableEtcdRegistry(nil); err != nil {
    return err
}

return app.Listen(":8080")
```

规则：

* 只需要本机 gRPC 时用 `app.EnableGRPC(":9001")`。
* 需要网关自动发现时用 `app.EnableEtcdRegistry(nil)`；它会注册 etcd，并自动开启 gRPC reflection。
* `RegisterGRPCService` 必须在 `Listen` / `Run` 前调用。
* `ConfigureGRPCServer` 必须在 `GetGRPCServer`、`RegisterGRPCService` 或 `EnableEtcdRegistry` 创建 server 前调用；只允许配置一次。
* Core 始终保留默认 unary error wrapper；调用方 interceptor 按配置顺序组成 chain。
* 配置 gRPC transport credentials 时必须使用独立的 Core-managed gRPC 地址；HTTP/gRPC 共端口会以 `ErrGRPCTLSSharedAddress` fail closed。
* 证书身份、URI、吊销和授权策略属于应用，不属于 Core。
* 内部客户端用 `app.GrpcClient("user-service")` 或启动期的 `app.MustGrpcClient("user-service")`；已有明确地址时用 `app.GrpcClientAt("user-service", target)`。
* 某服务需要 TLS 时，在首次 dial 前调用一次 `ConfigureGRPCClient(serviceName, GRPCClientConfig{TransportCredentials: ...})`。Core 按服务隔离并克隆 credentials；配置服务禁止再传不透明旧 dial options。
* 网关启动用 `app.EnableEtcdDiscovery(nil)` + `gateway.NewEtcdGateway(app)`。
* Gateway 按发现到的逻辑服务名选择 `ConfigureGRPCClient` 配置；必须在 `NewEtcdGateway` 前配置。
* 同一服务的多个 `service_id` 参与轮询；`GET`/`HEAD` 连接失败只换一个实例重试一次，写请求不重放。gRPC 全部离线时保留自动路由并返回 `503`。
* HTTP Handler 自动发布默认关闭；开启 `gateway.auto_http_routes.enabled` 时必须设置 `include_prefixes`，可用 `exclude_prefixes` 排除内部路径，并配置 Gateway 可访问的 `etcd.http_addr`。
* HTTP 自动发布只收集嵌入 `core.Handler` 后按方法名生成的路由；`app.GET/POST` 等手写路由不收集。启动时同步一次完整目录，不创建周期 worker。
* HTTP 自动发布顺序固定为 `core.New` → `EnableEtcdRegistry` → `Listen/Run`；`RegHandle` 只登记模块，Handler 路由在启动加载阶段生成，随后写入 etcd。registry 未初始化或同步失败会使启动返回错误。
* HTTP 手动批量注册使用 `gateway.RegisterHTTPRoutes`，单条使用 `RegisterHTTPRoute`；HTTP 路由不支持 path rewrite 或静态 Header 注入。

网关路由命名：

| gRPC 方法名 | HTTP 路由 |
| ----------- | --------- |
| `PostLogin` | `POST /v1/auth/user/login` |
| `GetUserById` | `GET /v1/auth/user/:id` |
| `PutProfile` | `PUT /v1/auth/user/profile` |
| `DeleteSession` | `DELETE /v1/auth/user/session` |

禁止：

* 业务代码手写重复的服务发现逻辑。
* 网关场景只调用 `EnableGRPC` 却忘记 reflection。
* 在请求处理函数里反复创建 gRPC client。

需要有期限的 etcd KV 时，使用 `EtcdDiscovery.GrantLease`、`KeepAliveLease`、`RevokeLease`、`GetRevision` 和 `CompareAndPut`。这些 API 只接受相对 key，并放入当前 namespace 的 `/config/` 前缀；revision 0 是 create-if-absent，CAS 冲突返回 `false, nil`。调用方负责消费 keepalive channel、识别租约丢失并在关闭前主动 revoke。

### 3.9 事务规范

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

### 3.10 错误处理规范

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

### 3.11 日志规范

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
* `RotatingLogWriter.RedirectStdout` 后轮转固定使用 copy-truncate，保持 `os.Stdout` 指针/文件描述符稳定；不要在业务代码中自行替换全局 `os.Stdout`

推荐：

```go
core.Logger.Error("create user failed", err)
```

---

### 3.12 Context 使用规范

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

### 3.13 长连接规范

Core 支持：

* SSE（Server-Sent Events）
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

### 3.14 gRPC 规范

Core 的 gRPC 能力由以下入口组成：

| 入口 | 用途 |
| ---- | ---- |
| `RegisterGRPCService(func(*grpc.Server))` | 注册 protobuf 生成的 service |
| `ConfigureGRPCServer(config)` | 在 server 创建前配置 transport credentials 与 unary/stream interceptor |
| `ConfigureGRPCClient(serviceName, config)` | 在指定服务首次 dial 前配置 transport credentials |
| `EnableGRPC(addr)` | 启动 gRPC server，可与 HTTP 共用端口 |
| `EnableEtcdRegistry(opts)` | 注册 etcd、启用 discovery、开启 reflection |
| `GrpcClient(serviceName)` | 基于 etcd resolver 创建客户端连接 |
| `GrpcClientAt(serviceName, target)` | 使用同一每服务 transport 配置连接明确 target |
| `gateway.NewEtcdGateway(app)` | 自动发现服务并生成 HTTP -> gRPC 路由 |

服务端模板：

```go
app.RegisterGRPCService(func(s *grpc.Server) {
    pb.RegisterOrderServiceServer(s, &OrderService{})
})

if err := app.EnableEtcdRegistry(nil); err != nil {
    return err
}
```

客户端模板：

```go
conn, err := app.GrpcClient("order-service")
if err != nil {
    return err
}
client := pb.NewOrderServiceClient(conn)
```

TLS 客户端模板：

```go
if err := app.ConfigureGRPCClient("order-service", core.GRPCClientConfig{
    TransportCredentials: credentials.NewTLS(clientTLS),
}); err != nil {
    return err
}
conn, err := app.GrpcClientAt("order-service", "127.0.0.1:9001")
```

方法命名必须使用 `Post/Get/Put/Delete` 前缀。缩写写成 `Id`，不要写成 `ID`，否则会被拆成 `/i/d`。

---

### 3.15 数据库规范

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

### 3.16 分页规范

后台管理：

```go
core.Finds[User](core.FindsParams{Where: whr, DB: tx})
```

移动端：

```go
core.Finds[User](core.FindsParams{Where: whr, DB: tx, Mode: core.FindsModeNext})
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
