# [AI Context] Core Framework v3


加密配置原子编辑提供 `CurrentConfigFile`、`NewEncryptedConfigFile` 及 Snapshot / Prepare / Commit / Discard，支持密文版本 CAS、跨进程锁和重启恢复；拒绝模板、链接与损坏配置，部署目录必须可写。文件提交不代表运行时已应用，完整合同见 [原子配置文件](skills/core-utils.md#原子加密配置文件)。
v3.1.62 修复四种 Static API 的默认无正文状态提交，不依赖 StaticConfig 或 gzip；目录跳转保持 301、条件请求保持 304、HEAD 不发送正文。不传 StaticConfig 时仍不添加 Cache-Control。回归见 static_status_test.go。

v3.1.61 提供 middleware/gzip 和 StaticConfig 静态长缓存；默认压缩文本类响应，图片默认不压缩，HTML 使用 no-cache。不传 StaticConfig 保留旧缓存行为。静态方法新增可变配置参数，Ctx 新增 SetResponseWriter；已提交响应遇到 handler 错误只记录、不追加错误文本。详见 skills/core-middleware.md、skills/core-static.md。

v3.1.60 起，NSQ Consumer（含 Builder）创建时继承最近成功 InitNSQ 的 nsq.auth_secret，NSQConsumerConfig.AuthSecret 非空覆盖，CloseNSQ 清空默认凭据，读写受锁保护；见 skills/core-nsq.md。

配置 `.dat` 的可选独立密钥来自 CORE_CONFIG_KEY_FILE 指定的普通文件，恰好 32 字节原始内容，用于 --generate、LoadConfigFile 和 SaveConfigFile。显式设置后失败关闭，LoadConfigFile 使用脱敏 panic，不生成默认配置；未设置保持旧流程。不得改变业务 AESKey；密钥文件限制到 root/运行用户可读，详见 skills/core-utils.md。

带点协议值可直接进入枚举 values：ToFixed 将点及连字符转换为常量名下划线，原 wire 值保持不变；无需在业务代码追加映射或伪造生成文件。

枚举边界：生成的 Scan 支持 int64/uint8/uint16/uint32/float64/string/[]byte；数值输入必须为 0～255 的整数，拒绝负数、溢出和非有限浮点且不修改原值。EnumString 对越界索引返回空串，EnumMarshalJSON/EnumMarshalText 返回错误；EnumUnmarshalJSON 在转换前检查数值范围，并支持 256 项映射。字符串解析沿用现行合同，领域取值仍由调用方校验。修改模板后须重新生成业务枚举。

monitor 资源采集使用固定间隔缓存：必须安装 `monitor.New`，`Snapshot()`、JSON 与 Prometheus Handler 只读快照。默认常规 5 秒、FD/磁盘/cgroup 慢采集 30 秒；`Config.DiskPath` 只对指定本地目录所在文件系统执行 statfs。CPU 使用 Linux 累计进程 CPU 差分，单核 100%，以 `cpu_available` 判断有效性；`max_fds` 为软限制，`max_fds_hard` 为硬限制，`fd_available` 判断两者与计数有效性。新增 RSS、主机内存/Swap、CPU、load、PSI、磁盘容量/inode、cgroup v2 有限配额用可空值表达未知；非 Linux 不伪造系统零值。`host_id` 为内核 boot_id SHA-256，`sampled_at`/`resource_sampled_at` 表达新鲜度；跨服务投影禁止携带 `disk_path`。详见 README 与 skills/core-middleware.md。 `disk_id` 是同一主机启动身份与文件系统设备号的 SHA-256；不同磁盘分别聚合，未取得主机身份时为空，不包含监控路径。

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

HTTP 路由默认区分大小写；在配置中设置 `case-sensitive: false` 可让 ASCII 静态路径如 `/transferIn` 与 `/transferin` 兼容。配置在 `core.New` 时固定，适用于 Handler、Group、中间件、显式注册及移除；参数名和值、query 和原始 URL 不变，非 ASCII 路径精确匹配。同方法且仅静态路径大小写不同的注册以后者覆盖前者。

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
        - 默认总数分页；大列表使用 `Mode: core.FindsModeCursor` + `CursorSpec` 做真正的 keyset cursor。
        - `CursorSpec.Fields` 使用一个唯一字段，或“排序字段 + 唯一 tie-breaker”，例如 `[]string{"created_at", "id"}`；通过 `Desc` 固定方向。
    - **总数分页 (后台)**: `core.FindPageBy(whr, &users, tx)`
        - 支持参数: `p`(页码), `l`(数量), `desc`/`asc`(排序), `field*`(包含), `field IN`(范围)。
    - **兼容的无 Count 页码分页**: `core.FindNextBy(whr, &users, tx)`
        - 仍使用 `p`/`OFFSET`，不是真正的 cursor；不要用于新建的大数据列表。

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

`c.RemoteIP()` 的 `trusted_proxies` 未配置、为 null 或空列表时，默认信任 IPv4/IPv6 私网和回环（`net.IP.IsPrivate()` / `IsLoopback()`）；非空列表只信任配置的 IP/CIDR，覆盖默认范围，回环也必须显式列入。配置必须是字符串列表，非法类型、空白项或任意非法 IP/CIDR 都使整个配置失败；`New()` 沿用现有初始化错误约定直接 panic，不忽略条目、不回退私网兜底。先校验 TCP 对端，不可信则忽略所有 IP 头。XFF 从右向左使用同一可信规则检查，重复头合并，返回首个不可信 IP，全部可信时返回最左项。仅缺少 XFF 时尝试单个合法的 `X-Real-IP`；XFF 为空或检查途中遇到非法项时直接回退对端。重复或包含列表的 X-Real-IP 无效。不采信其它 IP 头、不内置信任 Cloudflare；无效对端返回 nil。包级 `core.RemoteIP(header, addr)` 不读取应用配置，使用私网/回环默认规则；`ExtractClientInfo(ctx)` 使用独立的 Gateway 信任规则，不再解析原始 XFF/X-Real-IP：仅在当前 Core 的 Gateway 信任规则通过且 `core-client-ip` 为单个合法 IP 时采信，否则返回 gRPC 对端 IP。未设置 `GRPCServerConfig.TrustGateway` 时按实际 gRPC 对端 IP 默认信任私网/回环；设置后完全使用回调结果，拒绝时不启用兜底。

Gateway 自动 HTTP→gRPC 路由保留 `core-client-ip`：过滤同名用户头，在 header/Vars 转换后用 `c.RemoteIP()` 覆盖。下游在 `ConfigureGRPCServer` 设置 `TrustGateway func(context.Context) bool`，基于 peer IP/CIDR 或已验证的 mTLS Gateway 身份确认来源，不能基于 metadata 自我声明。未设置回调默认按 gRPC 对端 IP 信任私网/回环；显式回调覆盖默认规则，拒绝时不兜底；unary/stream 均通过 `ExtractClientInfo` 获取，非法或缺失字段回退 gRPC 对端，不回退原始转发头。该行为替代旧的 gRPC XFF/X-Real-IP 推断；详见 `skills/core-grpc.md`。

`core.NewGRPCCtx(ctx)` 是 RPC 生命周期内的轻量适配对象，stream 传 stream.Context()，不改变 protobuf 签名。GetHeader/HeaderValues/RequestMetadata/Cookie 只读请求，ClientIP 复用 ExtractClientInfo；SetHeader/SetCookie/WriteCookie/RemoveCookie 复用响应桥接并返回 error；OutgoingContext(metadata.MD) 只显式构造下游 metadata，不自动透传请求或身份字段，同名 outgoing 键覆盖，保留取消/deadline。Service 继续接收 StdContext()。Cookie 读取返回 (string,error)，请求副本独立；详见 skills/core-grpc.md。

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

可复用客户端使用 `fetch.New(baseURL).Timeout(45 * time.Second).SetProxy(proxyURL)` 配置每次请求预算，无需为超时构造 `http.Client`。非正值保持默认 30 秒，更短的父 context deadline 优先。Timeout 复制并原子替换客户端配置，保留代理、Cookie、重定向和其它设置；不修改调用方传入或已开始请求使用的客户端。

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

响应压缩使用 `middleware/gzip.New(config...)`：默认 MIME 为 JSON、HTML、CSS、JS、纯文本、XML、SVG，阈值 1024 字节，级别为标准库默认值。非 nil 的 `ContentTypes` 替换默认列表，空切片禁用；`MinLength < 0` 取消阈值，`Skip` 排除请求。按 `Accept-Encoding`/q 协商，添加 Vary；跳过 HEAD/CONNECT/Upgrade/Range、204/304、SSE、已有 Content-Encoding、no-transform，默认不压缩 PNG/WebP/JPEG。只缓冲阈值前缀，达到阈值后流式发送；提前 Flush/DoWriteHeader 会保持原始编码。详见 skills/core-middleware.md。

静态长缓存通过 `app.Static(path, root, core.StaticConfig{...})` 显式开启，`StaticFS`、`StaticFile`、`StaticFileFS` 同样支持可选配置。不传配置保持旧缓存行为；传空配置默认 7 天，`MaxAge` 是 time.Duration，负数表示 no-cache；HTML/index 页面使用 no-cache，已有 Cache-Control 优先，重定向/错误及动态 API 不添加长缓存。`Immutable: true` 仅用于内容变化即更换 URL 的资源，可配合一年 MaxAge。详见 skills/core-static.md。

中间件包装响应时可用 `original := c.Response(); c.SetResponseWriter(wrapped)`，必须在返回前恢复原 writer，不得跨请求保存。Core 在响应写出后遇到 handler 错误只记录错误，不追加错误文本；未写出时保持原有错误响应。

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
* `app.EnableEtcdRegistry(nil)` 仅为已有入站 gRPC server 开启 reflection，不隐式创建 server；以每进程唯一 `generation` 和 `ready=false` 注册；启动准备和启用的自动 HTTP 目录发布完成后更新 `ready=true`。历史缺少 ready 的记录兼容可服务。
* `RegisterGRPCService` 必须在 `Listen` / `Run` 前调用。
* `ConfigureGRPCServer` 必须在 `GetGRPCServer` 或 `RegisterGRPCService` 创建 server 前调用；只允许配置一次。
* Core 始终保留默认 unary error wrapper；调用方 interceptor 按配置顺序组成 chain。
* 配置 gRPC transport credentials 时必须使用独立的 Core-managed gRPC 地址；HTTP/gRPC 共端口会以 `ErrGRPCTLSSharedAddress` fail closed。
* 证书身份、URI、吊销和授权策略属于应用，不属于 Core。
* 内部客户端用 `app.GrpcClient("user-service")` 或启动期的 `app.MustGrpcClient("user-service")`；已有明确地址时用 `app.GrpcClientAt("user-service", target)`。
* 某服务需要 TLS 时，在首次 dial 前调用一次 `ConfigureGRPCClient(serviceName, GRPCClientConfig{TransportCredentials: ...})`。Core 按服务隔离并克隆 credentials；配置服务禁止再传不透明旧 dial options。
* 网关启动用 `app.EnableEtcdDiscovery(nil)` + `gateway.NewEtcdGateway(app)`。
* v3.1.59 起提供：`gw.RegisterExactJSONBinding(gateway.ExactJSONBinding{ServiceName, FullMethod, RequestMessage, ResponseMessage, MoneyPaths, IntegerPaths, MaxRecvBytes})` 在 NewEtcdGateway 后、Run 前显式注册。仅绑定方法原 body→唯一 bytes raw_body，拒绝 query；响应 descriptor 逐次核验，指定金额 string 和整数路径以精确 JSON number 输出，其余字段及未绑定方法保持原行为；零值/空数组显式输出，成功缺失 data/指定字段或超界返回 500，非零 code 允许缺失 data。接收上限仅该调用且最多 32MiB。完整约束见 README 的精确 JSON 数字绑定小节。
* Gateway 按发现到的逻辑服务名选择 `ConfigureGRPCClient` 配置；必须在 `NewEtcdGateway` 前配置。
* Gateway 首次请求优先选择 Ready 连接；没有 Ready 时只允许已核验方法的 Idle 连接由正常 RPC 唤醒，备用实例重试仍只选择 Ready，不选择 Connecting、TransientFailure 或 Shutdown。
* Gateway 在实例删除、未就绪或换代时先摘除并禁止旧 proxy 的新调用，再等已进入的 unary RPC 完成后关闭连接；没有自动强杀期限。`ReflectionProxy.Drain()` 返回共享完成通道，显式 `Close()` 仍强制关闭。HTTP 摘除不取消在途反代；业务 owner 必须另行停止新业务/领取并排空异步任务后才进入最终进程关闭。
* 同一服务的多个 `service_id` 参与轮询；`GET`/`HEAD` 连接失败只换一个实例重试一次，写请求不重放。gRPC 全部离线时保留自动路由并返回 `503`。
* HTTP Handler 自动发布默认关闭；开启 `gateway.auto_http_routes.enabled` 时必须设置 `include_prefixes`，可用 `exclude_prefixes` 排除内部路径，并配置 Gateway 可访问的 `etcd.http_addr`。
* HTTP 自动发布只收集嵌入 `core.Handler` 后按方法名生成的路由；`app.GET/POST` 等手写路由不收集。启动时同步一次完整目录，不创建周期 worker。
* HTTP 自动发布顺序固定为 `core.New` → `EnableEtcdRegistry` → `Listen/Run`；`RegHandle` 只登记模块，Handler 路由在启动加载阶段生成，随后写入 etcd。registry 未初始化或同步失败会使启动返回错误。


服务注册携带每进程唯一 `generation`，同 ID/地址重启也重新 reflection。每个期望实例保留一个恢复任务，连接/reflection 超时后以 0.5 秒递增、最多 5 秒的间隔重试，熔断冷却后继续尝试；删除、换代或关闭取消任务。Registry 丢租恢复通过 CAS 防旧代覆盖，正常退出先撤注册，异常退出由 TTL 摘除。滚动升级采用方法并集，RPC descriptor 冲突返回 503，写请求不自动重放。

`GrpcClient("service-name")` 的 resolver 每连接绑定 Discovery；相同配置复用，改变 namespace/endpoints/etcd 身份返回 `ErrEtcdDiscoveryConfigurationChanged`，应新建 Core 实例迁移。直接 `etcd.NewRegistry` 默认没有 ready 字段、兼容立即可服务。

`ServingReady()` 仅在工作负载、传输、自动路由同步与注册就绪均成功后返回 true，开始关闭后返回 false；用于启动进度，不替代业务依赖健康检查。

`ShutdownWithTimeout` 使用独立 deadline，先撤注册，再同时排空 HTTP/HTTP3/gRPC，最后关闭业务依赖与 Discovery；超时强制关闭 transport。信号退出默认 10 秒，可配置 `shutdown_timeout: "10s"`。重复调用共享首个退出任务，后来短期限只停止该调用等待。无 context 的旧 shutdown hook 无法强制取消，后台清理需它自行返回，但不延长调用方期限。

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

### 3.8.1 gRPC 响应 Header/Cookie

在 gRPC Handler 中使用 `core.GrpcSetHeader(ctx, key, values...)`、`core.GrpcSetCookie(ctx, name, value, exp, path, args...)`、`core.GrpcRemoveCookie(ctx, name, path, dom...)` 或 `core.GrpcCookie(ctx, *http.Cookie)`；都返回 error，必须处理。`ctx` 必须是当前 RPC server context，在 initial headers 发送前调用。业务 Service 仍只处理认证与会话签发。

`GrpcSetCookie` 与 `BaseCtx.SetCookie` 共享构造规则：空 path 为 `/`，value URL 编码，SameSite=Lax；字符串 `"httponly"`（大小写无关）开启 HttpOnly，其他字符串设置 Domain，bool 设置 Secure，Domain/Secure 重复参数以后者为准，未知类型忽略；空 Domain 回退当前 Core 的 `domain`。默认 Secure/HttpOnly 为 false，登录时显式传 `"httponly", true`。Core unary/stream interceptor 提供每应用配置；自建 grpc.Server 没有配置域名回退。

`GrpcCookie` 不补默认值、不编码 value，可设置 host-only/MaxAge/SameSite；非法 cookie 返回错误。`GrpcRemoveCookie` 与 BaseCtx 一样用过去的 Expires 删除，首个可选 Domain 生效、空值回退配置，空 path 不补 `/`，必须匹配原 Path/Domain。

Gateway 仅桥接 `core-http-<小写 header 名>-bin` initial metadata，支持多值和多个 Set-Cookie；helper 重复调用追加。header 名仅接受字母、数字、`-_.`，不支持其他 HTTP token 符号。错误响应也桥接，重试只取最终尝试。普通 metadata、trailer、非法 header/value、Content-Type/Content-Length/Content-Encoding/hop-by-hop/proxy/grpc-* header 不桥接。使用标准 grpc.Header 可在原生客户端收集 metadata；它不会保存浏览器 cookie。原有 `ReflectionProxy.Invoke` 兼容，`InvokeWithHeaders` 返回 body、initial metadata、error。

```go
if err := core.GrpcSetCookie(ctx, "session", token, expires, "", "httponly", true); err != nil {
    return nil, err
}
```

认证中间件仍需验证请求 cookie，`gateway/public_routes` 只控制免认证路径；该能力不改变认证/CSRF/CORS 策略。详见 README 的 gRPC 响应 Header 与 Cookie，以及 `example/grpc-cookie/session.go`。

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
* `core.New()` 已注册 Recovery；普通 panic 会记录并返回 500
* `net/http.ErrAbortHandler` 是标准库的静默中止信号，Recovery 不记录堆栈、不调用自定义 recovery handler，也不再写 500；反向代理或 SSE 客户端断开时不要把它当作业务 panic

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

WebSocket 必须复用 `websocket.New().WithOptions(websocket.Options{...}).OnConnect(...).OnMessage(...).OnClose(...).Handler()`，不要在业务复制 upgrader 或读写循环。`OnMessage` 保持 `func(*websocket.Conn, websocket.MessageType, []byte)`。Options 支持 `Subprotocols`、`RequireSubprotocol`、`CheckOrigin`、`ReadLimit`、`ReadTimeout`、`WriteTimeout` 和 `HandshakeTimeout`。完整消息含分片受累计 ReadLimit 约束；固定协议、来源和超时需业务显式开启，零值保持原无限制默认。`TrySend([]byte) bool` 及 `TrySendWithType` 只确认入队，背压返回 false 并关闭，不能当作网络或业务 ACK。`Conn.Context()` 来自 `c.Request().Context()`，Close 时取消；Core HTTP Ctx 没有 StdContext 方法。OnClose 在读写结束后调用一次，关闭归还未发送缓冲区；回调字节异步使用必须先复制。


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
| `EnableEtcdRegistry(opts)` | 注册 etcd、启用 discovery、为已有入站 gRPC server 开启 reflection |
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

列表筛选统一经过共享 `Where` 安全校验：字段仅允许 ASCII 标识符或一层 `table.column`，以及既有的 `IN` / `NOTIN` / `!=` / `> < >= <=` / `*` / `^` / `$` 语法。`asc` / `desc` / `omitFields` 仅接受逗号分隔字段字符串（空字符串为 no-op），不接受 SQL 表达式、内嵌方向或其他类型。非法输入返回 `ErrInvalidQuery`，整条查询不执行；`Where` 保留原签名，通过 `tx.Error` 返回，列表 API 通过 `error` 返回。`FindsModeCursor` 在忽略 `asc` / `desc` 前也校验它们。

业务必须在 DAO 定义可用字段并独立施加权限/租户条件；标识符语法校验不代表字段授权。可信复杂查询放入服务端构造的 GORM scope；禁止把请求字符串作为原始 SQL 或 `core.Expr` 的表达式部分。原先 Map 中的 `asc: "id DESC"` 必须改为 `desc: "id"`。

需要总数的兼容后台管理：

```go
core.Finds[User](core.FindsParams{Where: whr, DB: tx})
```

所有大数据列表和新列表：

```go
core.Finds[User](core.FindsParams{
    Where: whr,
    DB: tx,
    Mode: core.FindsModeCursor,
    Cursor: &core.CursorSpec{
        Token: req.Cursor,
        Direction: core.CursorDirection(req.CursorDirection),
        Fields: []string{"created_at", "id"},
        Desc: true,
    },
})
```

游标模式规则：

* 请求使用 `cursor`、`cursor_direction`（`next|prev`）和 `page_size`；DAO 映射到 `CursorSpec.Token`、`Direction` 与 `Where["l"]`。
* 响应使用 `next_cursor`、`prev_cursor`、`has_next`、`has_prev`；Go 结果字段为 `NextCursor`、`PrevCursor`、`CursorHasNext`、`CursorHasPrev`。
* `Fields` 只接受一到两个安全标识符，可用 `table.column`；最后字段必须唯一，排序字段必须非空并建立匹配索引。
* `FindsModeCursor` 使用 keyset 比较和 `limit + 1`，不执行 `COUNT` 或 `OFFSET`。`Where` 中的 `p`、`asc`、`desc` 在此模式被忽略。
* token 是版本化 opaque 值，会校验字段和排序，但不是授权凭据。聚合列表使用 `EncodeCursorToken` / `DecodeCursorToken` 复用格式，自行完成跨数据源边界合并。

禁止：

* 手写 limit/offset
* 返回无限数据
* 把 `FindsModeNext` 或 `FindNextBy` 称为真正的 cursor
* 只按非唯一时间字段分页，导致同值记录丢失或重复

推荐：

* 默认分页
* 最大限制 limit
* cursor 使用 `created_at,id` 或单独唯一 `id` 的稳定排序

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

### Gateway gRPC 调用期限

Gateway 的 gRPC 自动代理总期限由 `gateway.grpc_request_timeout` 配置（例如 `180s`），也可通过 `gateway.Config.GRPCRequestTimeout` 指定；默认及非正值保持 10s。此期限覆盖一次 RPC 及其允许的只读重试，短于它的入站 context deadline 仍优先生效。它与下游单次 HTTP timeout 不同，多步骤登录应为整个流程留出预算。配置在 Gateway 启动时读取，修改后需重建/重启；错误日志包含实际 elapsed 和配置 timeout。


## 托管工作负载 mTLS

通过 `core.LoadConfigFile` 加载 YAML 或加密 `.dat` 时，`workload.state_dir`、`trust_bundle_file`、`bootstrap_token_file` 以及 `workload_issuer.state_dir`、`policy_file`、`pin_file` 的非空相对路径，在环境变量展开后按该配置文件所在目录解析。`state_dir: ./workload` 因而始终位于配置旁，不随启动 cwd 或 `-f`/`--config` 的绝对、相对写法变化。绝对路径保持原值；直接以程序构造 `Options` 或调用 `workload.Open` 时，仍由调用方确定路径基准。其它业务配置路径不受此规则影响。

Gateway 可使用 Go 标准库首次自动创建私有 CA，后端自动申请、本地保存和续期，服务端、服务间客户端和 Gateway reflection 复用本进程身份。使用前必须在首次创建 gRPC server/client 前调用 `app.EnableWorkloadIdentity()`；启用后禁止明文回退或逐目标显式 transport 冲突配置。完整配置、引导认证、独立端口、根信任更新和验收边界见 [工作负载身份说明](workload/README.md)。

EventHub 注册通道后由 Hub 独占发送和关闭，调用方不得自行关闭或复用退役通道；广播、定向发送、替换、注销和关闭共享生命周期锁，仍保持非阻塞丢弃语义。相同 ID 的旧连接注销不能关闭新连接，同一活动通道的多个 ID 共享生命周期。

后端跨服务器可预置 `workload.root_sha256`（根证书 DER SHA-256），Core 从内部签发入口 `GET /root.pem` 无凭据下载，校验指纹及本次 HTTPS 链/主机名后原子缓存，再申请身份。每次启动校验缓存，拒绝不匹配/损坏缓存的静默覆盖；默认缓存位于本进程 state_dir，不依赖 Gateway 共享文件。指纹必须由可信部署渠道预置，详见工作负载说明。

HTTP-only 应用可启用工作负载身份进行出站 mTLS，`EnableEtcdRegistry` 不因此开启入站 gRPC。实际 server 能力写入 `metadata.grpc_enabled`；显式 `false` 只排除 gRPC 接入，HTTP 路由照常发布和代理，缺字段保持旧接入方式。真正注册 gRPC 服务必须先于 registry，mTLS listener 仍须独立 `grpc.addr`，共享 HTTP 端口继续拒绝启动。

## NSQ 持久化业务重试策略

`NSQConsumerConfig.DisableMaxAttempts=true` 显式关闭 go-nsq 的次数耗尽自动 FIN，将其 `MaxAttempts` 设为 0。默认 false 保持旧行为：未指定或 0 的 `MaxAttempts` 使用 5；正值为有限次数。金融 Outbox/Inbox 等要求故障恢复的消费者应显式启用此项，避免在调用业务 Handler 之前因次数耗尽丢失重试。

该选项不改变 Handler 语义：nil 仍 FIN，error 仍 REQ。只有业务已持久化完成或持久化隔离处理后才能返回 nil；仍在处理的 Inbox 锁、取消、超时和暂态故障须返回 error。格式错误的 poison 消息也不能只靠次数耗尽丢弃，调用方须选择持久隔离及诊断或继续有界退避重试。此项不提供 DLQ、持久化 Inbox 或业务幂等；父 context 和原命令键恢复仍由调用方负责。

### Gateway gRPC 响应中的零值 code

未注册 ExactJSONBinding 的默认 Gateway 对 gRPC 响应仅补齐顶层 proto 原名为 `code`、不带 presence 的单值整数字段：零值仍输出 `code: 0`；64 位整数按 ProtoJSON 输出 `code: "0"`。非零值保持原值。未定义 code、optional/proto2/oneof、其他类型及嵌套 code 保持原序列化语义。不会补齐 error、data 或空数组，不会添加响应外层；HTTP 反向代理和普通 HTTP Handler 不受影响。已有接口若依赖零值 code 缺省，需要适配此响应变化。

## 动态精确域名证书

内置引擎预绑定回环临时端口并明确设置 solver 的 ListenHost/AltHTTPPort；内部 listener 同样检查域名成员、Remove/关闭状态和实时 Authorize，禁止自动产生无保护的公网 :80 入口。

租户域名使用独立 `certificate.New(certificate.Config{StorageDir, Email, CA, Authorize})`；必须显式授权，调用 `Ensure(ctx, hostname)` 后台申请，`Remove` 拒绝新握手和续期，`Status` 只返回非敏感状态。管理器使用专用 CertMagic HTTP-01 与持久 Storage，拒绝未知 SNI、泛域名和任意按需签发；授权检查覆盖申请、续期、TLS 和挑战。应用启动前 `app.UseCertificateManager(manager, ":80")` 接入原 HTTPS listener，listen 配置设为 :443；与旧 acme/certmagic 互斥，Core 关闭时释放资源。业务负责绑定 generation、所有权验证、域名清单同步和当前授权事实。完整合同见 [skills/core-certificates.md](skills/core-certificates.md)。

静态 CertMagic 与旧版 on-demand 的维护缓存必须返回实例自身的 Config，不能用 `NewDefault()` 后局部覆盖 Storage/Issuers；其共享缓存的维护回调会重建默认配置、丢失这些覆盖。缓存应在启动失败及 Core 关闭时停止。外部反代手动加载 PEM 时需另行重载。

通用 `grpc.max_recv_message_bytes` 支持1～64MiB，默认或无效值回落4MiB；server固定入站上限，Gateway普通反射调用控制响应接收，ExactJSON逐方法限制优先。详见 `skills/core-grpc.md` 与 `skills/core-gateway.md`。
