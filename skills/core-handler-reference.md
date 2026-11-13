# Core Handler 开发技能

> 按需完整参考；日常任务从 [core-handler.md](core-handler.md) 开始。API/合同调整需同步本参考与短规则。


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

// 在 internal/app 中装配 Service 后登记：
// core.RegHandle(handler.NewXxxHandler(svc))
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

Core 默认注册 `GET /health`，返回 `204 No Content`。业务程序需要自定义健康检查时，
可以显式注册同一路径，业务 handler 会覆盖默认实现。

同一路由结构位置必须使用相同的参数名和必选/可选类型。可选参数只能位于路径末尾；
自动路由命名不得生成空 segment（`//`）或非末尾 catch-all，否则框架会在注册时 panic。

### 3. 前缀设置

HTTP 路由默认区分大小写。配置 `case-sensitive: false`（在 `core.New` 时读取）可使 ASCII 静态路径 `/transferIn` 与 `/transferin` 兼容，自动 Handler、显式路由、Group、路径中间件及移除均生效。参数名、参数值、query 和原始路径保持大小写；非 ASCII 路径仍精确匹配。开启后同方法且仅静态路径大小写不同的注册以后者覆盖前者，业务应避免重复定义。

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
| `Prepare(app *core.Core) error` | Init/路由建立后同步执行，完成前不发布 ready | 注入实例依赖、连接检查、必需预热 |
| `Start(app *core.Core) error` | 有 Prepare 的模块在全部 Prepare 成功后异步执行；旧模块保留 Start 先启动、Init 随后执行 | 后台任务，不作为就绪屏障 |
| `Stop(app *core.Core) error` | 应用关闭阶段 | 释放资源、停止后台任务 |

```go
func (h *UserHandler) Init() {
    h.Prefix("/api/v1/users")
}

func (h *UserHandler) Preload(c core.Ctx) error {
    // 如需鉴权，在此调用真实身份验证器；Header 非空不代表认证成功。
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
- 必需依赖和预热放在 `Prepare()`，失败返回 error；使用 `app.Ctx` 传播取消并为外部调用设置超时。
- `Start()` 保留旧的异步行为；常驻任务必须响应 `app.Ctx`，不阻塞就绪但也不保证已初始化。
- `Stop()` 用于清理资源，不要 panic。

### 5. 标准模板与装配

完整、可运行模板见 [标准 Handler](../example/standard/internal/handler/user.go)，
[app 装配](../example/standard/internal/app/http.go)。目录约定见
[core-architecture.md](core-architecture.md)。

Handler 接收构造好的 Service，app 统一 core.RegHandle；不要求 init() 隐式注册。
参数使用 Params/ReadBody/Validate。HTTP context 通过 c.Ctx() 传入 Service，
业务方法不得接收 core.Ctx。格式非法在入口拒绝，业务规则由 Service 再保证。
HTTP 状态显式调用 c.Status(code)，然后 c.ToJSON(data, err) 生成响应体；
ToJSON(error) 不自动设置对应 HTTP 状态。内部故障只返回通用错误。

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
if err := c.ReadBody(&req); err != nil {
    c.Status(400)
    return c.ToJSON(nil, core.NewError(400, "invalid request"))
}

// 请求头
token := c.GetHeader("Authorization")

// 表单
email := c.FormValue("email")
```

### 8. 响应输出速查

```go
// 标准 JSON 响应 (自动处理错误)
return c.ToJSON(data, err)

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
2. ✅ 在 internal/app 装配 Service 并登记自动 Handler
3. ✅ 方法按命名规范命名
4. ✅ 使用 `c.ToJSON()` 返回响应
5. ✅ 错误处理完整


## 动态路由原子替换（框架内部）

`(*Core).ReplaceHandle(methods []string, oldURI, uri string, handler any, middleware ...HandlerFunc) Router` 是框架内部、动态路由管理使用的增量 API，现有 AddHandle/RemoveHandle 和 Router 接口不变。旧路径不存在时仍注册新路径；支持 ALL、现有大小写配置、handler 适配和声明的 route middleware，不用于替换 USE 中间件。不复制旧 route middleware，新 handler 与 middleware 组成新的链；路径级和全局 middleware 继续由树匹配。每个 method 独立原子替换，不承诺多个 method 或多个 slot 同时切换。参数名可在无共享参数歧义时改变，原有路由格式/参数歧义校验继续生效；非法注册沿用现有程序错误 panic 合同。业务示例仍优先使用自动 Handler 路由。


## 多 Core 实例

旧 `RegHandle(pointer)` 保持单应用兼容，同一对象绑定另一 app 会返回启动错误。
使用 `RegHandleFactory(func() core.Module { return &UserHandler{} })` 为每个 app 构造独立同类型对象。
工厂登记时调用一次、首个对象用于首个 app；后续 app 再调用。不要在工厂连接外部资源。
在 Prepare 中通过 `app.Conn()/app.RConn()` 装配 DAO/Service，实现 Prepare 的模块在全部 Prepare 成功后才异步启动 Start；未实现 Prepare 的旧模块保留 Start 在 Init 前异步启动的顺序。
Prepare 失败时 Stop 仍可能被调用，必须允许依赖尚未初始化。


### 旧 Handler 启动顺序兼容回归

启动顺序兼容边界：未实现 `Prepare` 的旧 Handler 在绑定当前 Core 后，先调度异步 `Start`，再执行 `Init` 并建立路由，因此 `Init` 可以等待自身 `Start` 发出的信号。`Start` 可以一直运行到 `app.Ctx` 取消，框架不会等待它返回才发布自动 HTTP 目录或 ready。实现 `Prepare` 的 Handler 先执行 `Init`/路由建立，全部 Prepare 成功后才调度 Start；给旧 Handler 新增 Prepare 时，应把必需准备逻辑放入 Prepare，并移除 Init 对 Start 的等待。每个模块的异步调度顺序不代表全部 Start 已执行或业务后台任务已准备完成。

回归覆盖见 `module_startup_compat_test.go`：旧 Handler 与 Prepare Handler 的两种装载顺序、准备失败时取消已启动旧模块、启动/停止只执行一次；Init 等待有时间上界，使顺序回归直接失败而不拖住全仓测试。自动目录发布边界继续由 `TestAutomaticHTTPRouteStartupSyncRunsAfterModuleLoading` 验证。


### 启动与关闭并发的资源边界

并发启动/关闭合同：Shutdown 开始后不再接纳新模块启动或发布 ready。框架先取消 app.Ctx，等待正在执行的 Init、Prepare 和启动发布步骤退出，再调用已加载模块的 Stop；Stop 可发出常驻 Start 的退出信号，框架等待这些 Start 返回后才最终关闭自己拥有的数据库、Redis 等依赖。不会把整个 ErrGroup（含停机观察协程）作为等待对象，避免自等待。ShutdownWithTimeout 的调用方期限仍独立生效；忽略取消或不退出的用户钩子/Start 会推迟后台清理，不能强制释放它们仍可能使用的资源。启动钩子应通过返回错误触发退出，不应同步等待自身的 Shutdown 完成。

Gateway 初始化只执行一次；Close 与任务接纳、请求回退注册共享关闭边界，Close 返回后不允许回退路径重新填入期望实例或连接池。Close 仍清理已拥有的 watcher、连接及客户端，API 签名不变。gRPC 在 server 尚未创建时触发的强制停止不会耗尽未来 server 的一次性停止标记，关闭路径以受锁保护的 server 快照执行停止。


### 有限 ErrGroup 与 HTTP/3 关闭边界

框架后台任务仍加入原 ErrGroup，保留 `ErrGroup().SetLimit(n)` 的有限并发语义：容量满时等待已有任务释放位置，等待不持应用锁，并可被 app.Ctx 或关闭标志打断。模块 Start、停机观察协程、gRPC Serve 和 HTTP/3 回调均使用这一可取消接纳边界，避免“启动等入组、停机等启动、Start 等 Stop”的循环。gRPC 已打开但未获接纳的 listener 会立即关闭。

HTTP/3 回调以同一应用锁检查关闭状态并发布 server 所有权；Shutdown/强制关闭读取受锁保护的快照。迟到回调不发布新 server，并关闭传入的 UDP listener；已经发布的 server 会被正常排空或强制关闭。该规则不增加公开 API 或改变默认无限并发配置。
