---
name: core-gateway
description: 使用 Core Framework gateway 子包接入 HTTP 或 gRPC 微服务，覆盖显式注册、自动路由、路由命名、管理接口和 metadata 透传
tags: [go, core-framework, gateway, grpc, etcd, http, reflection]
---

# Core Gateway 技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "启动网关"
- "HTTP 转 gRPC"
- "HTTP 微服务注册 gateway"
- "gateway.auto_http_routes"
- "gateway.RegisterHTTPRoute"
- "gateway.RegisterHTTPRoutes"
- "gRPC 自动路由"
- "gateway.NewEtcdGateway"
- "网关管理接口"
- "网关 metadata / x-user-id"
- "gRPC 响应 header / Set-Cookie"
- "RegisterExactJSONBinding / 精确 JSON number / 原始 body bytes"

## 1. 核心链路

网关通过 etcd 发现服务实例和路由定义：

1. HTTP 或 gRPC 业务服务调用 `EnableEtcdRegistry` 注册服务实例。
2. 网关调用 `EnableEtcdDiscovery` 连接 etcd。
3. HTTP 服务通过 `gateway.auto_http_routes` 自动发布 Handler 路由，或调用 `gateway.RegisterHTTPRoutes` 显式发布。
4. gRPC 服务自动开启 reflection，由网关按方法名生成 HTTP 路由。
5. `gateway.NewEtcdGateway(app)` 读取服务实例和路由，并代理到对应协议的上游。

### 实例生命周期与协议更新

- Core `EnableEtcdRegistry` 先发布 `ready:false` 的准备状态；监听器绑定、启动钩子和依赖准备成功后，发布 `ready:true`。Gateway 保留准备中的注册事实，但不安装 HTTP 或 gRPC 上游。历史注册没有 `ready` 时继续视为可接入。
- 每个 Registry 具有随机 `generation`。同一实例 ID、同一地址的新进程也会让 Gateway 撤换旧连接，并重新 reflection。租约变化和版本变化也触发重新核验；仅续租不产生变更事件。
- 每个期望实例只有一个持续接入任务。单次 reflection 超时、暂时不可达和服务熔断期间会退避重试，熔断冷却结束后自动探测；实例删除、被新代替换或 Gateway 关闭会取消任务。晚返回的旧连接不能覆盖新代。
- 每个进程独立 reflection 后，只有完整协议指纹相同才共享不可变 schema。请求/响应指纹覆盖传递引用的嵌套 message、enum 和 streaming 标志。请求热路径只读取预建的实例快照，不执行 reflection 或 dial。
- Gateway 首次请求优先选择 Ready 连接；没有 Ready 时只允许已核验方法的 Idle 连接由正常 RPC 唤醒，备用实例重试仍只选择 Ready，不选择 Connecting、TransientFailure 或 Shutdown。
- 滚动发布时自动路由使用已接入实例的方法并集；新增方法只选择支持它的实例。相同 RPC 的协议指纹不一致时，该 RPC 暂时返回 503，避免混用 codec；其它无冲突 RPC 继续服务。不兼容升级应使用新的 RPC/服务版本，或等待旧代全部退出。
- 删除部分实例会更新方法并集；全部实例离线保留最后的路由并返回 503。手工路由仍优先于自动路由；`gateway.grpc_service_excludes` 和 `gateway/public_routes` 的既有职责不变。
- 实例删除、`ready:false`、代次或地址替换时，旧连接立即退出选择并禁止新调用；已进入的 unary RPC 完成后才关闭连接。`ReflectionProxy.Drain()` 非阻塞返回共享完成通道，不自动强杀；`Close()` 仍用于显式最终强制清理。池保留排空连接的所有权，避免最终退出遗漏旧连接。HTTP 摘除不取消在途反代。业务服务必须在最终退出前自行停止新业务/消息领取并排空异步工作，不能把传输层在途数量当成业务已完成。

## 2. 网关启动模板

```go
package main

import (
    "log"

    "github.com/xs23933/core/v3"
    "github.com/xs23933/core/v3/gateway"
)

func main() {
    app := core.New(core.LoadConfigFile("config.yaml"))

    // 只为需要 TLS 的逻辑服务配置 credentials。
    if err := app.ConfigureGRPCClient("secure-service", core.GRPCClientConfig{
        TransportCredentials: credentials.NewTLS(secureServiceTLS),
    }); err != nil {
        log.Fatal(err)
    }

    if err := app.EnableEtcdDiscovery(nil); err != nil {
        log.Fatal("启用 etcd 服务发现失败:", err)
    }
    
    gw, err := gateway.NewEtcdGateway(app);
    if  err != nil {
        log.Fatal(err)
    }

    defer gw.Close()

    if err := app.Run(); err != nil {
        log.Fatal(err)
    }
}
```

配置示例：

```yaml
etcd:
  namespace: xpay
  endpoints:
    - 127.0.0.1:2379
  dial_timeout: 5
```

共享一个 etcd 集群运行多个项目时设置 `namespace`。同一项目的业务服务、内部客户端和 Gateway 必须使用相同值；`xpay` 对应 `/xpay/services/`、`/xpay/gateway/routes/` 和 `/xpay/config/`。空值保持旧版全局前缀。

Gateway 的 gRPC `ServicePool` 使用 etcd 实例所属的逻辑服务名调用 Core 客户端配置。因此可以只为部分服务调用 `ConfigureGRPCClient`，其他未配置服务保持旧的 plaintext transport。配置必须早于 `NewEtcdGateway`；错误 CA/server name 只使对应服务实例连接失败，不应改变其它服务的 transport。

`EtcdDiscovery` 还提供通用的 `GrantLease`、`KeepAliveLease`、`RevokeLease`、`GetRevision` 和 `CompareAndPut`。它们只接受相对逻辑 key，并固定写入当前 namespace 的 `/config/`；revision 0 表示 create-if-absent，CAS 冲突返回 `false, nil`。这些方法适合短期在线事实，不替代业务数据库权威状态；调用方必须消费 keepalive channel、处理关闭/租约丢失并在退出前主动 revoke。

## 3. HTTP 微服务路由注册

HTTP 服务必须先调用 `EnableEtcdRegistry` 注册实例，并通过 `etcd.http_addr` 提供 Gateway 可访问的 HTTP origin。路由可以自动发布，也可以显式注册。HTTP-only 注册不创建 gRPC server 或 reflection；Core 按实际 server 写入 `metadata.grpc_enabled=false`，Gateway 保留 HTTP 上游而跳过 gRPC 接入，能力更新为 HTTP-only 时撤销旧 gRPC 连接和自动路由。缺少能力字段的历史实例保持原接入行为。工作负载身份可只服务于出站 mTLS，无需添加入站 gRPC 端口。

### 3.1 自动发布 Handler 路由

自动发布默认关闭，只收集嵌入 `core.Handler` 后由方法名生成的路由；`app.GET`、`app.POST` 等手写路由不会进入自动目录。

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
    // 只登记 Handler，不在 init 中连接 etcd。
    core.RegHandle(&TaskHandler{})
}

func main() {
    app := core.New(core.LoadConfigFile("config.yaml"))
    // 必须在 Listen/Run 之前成功初始化 registry 和 discovery。
    if err := app.EnableEtcdRegistry(nil); err != nil {
        log.Fatal("enable etcd registry: ", err)
    }
    if err := app.Run(); err != nil {
        log.Fatal(err)
    }
}
```

```yaml
gateway:
  auto_http_routes:
    enabled: true
    # enabled=true 时至少配置一个；按完整路径段匹配。
    include_prefixes:
      - /api
    # 排除规则优先于包含规则。
    exclude_prefixes:
      - /api/internal

etcd:
  namespace: xpay
  endpoints:
    - 127.0.0.1:2379
  service_name: task-service
  service_addr: 127.0.0.1:8081
  http_addr: http://127.0.0.1:8081
  service_id: task-service-1
  ttl: 5
```

调用顺序固定为 `core.New` → `EnableEtcdRegistry` → `Listen/Run`：

1. Go `init` 中的 `RegHandle` 只登记 Handler，不访问 etcd。
2. `EnableEtcdRegistry` 初始化 registry/discovery，并立即写入服务实例。
3. `Listen` / `Run` 加载 Handler，生成方法名路由。
4. Handler、gRPC 和 TLS 准备完成后，完整目录一次写入 `/<namespace>/gateway/routes/auto_http/<owner_id>`，随后进入 HTTP Serve。

配置无效、未先成功初始化 registry/discovery 或 etcd 写入失败都会使启动返回错误并清理已启动资源；框架不启动周期发布 worker。自动 HTTP 路由的 Gateway path 与上游 path 相同，不做参数转换，path、query 和 body 由目标服务处理；不支持路径改写或静态 Header。

### 3.2 显式注册 HTTP 路由

HTTP 微服务需要分别注册服务实例和 HTTP 路由：

- `app.EnableEtcdRegistry(nil)`：把实例写入 `/<namespace>/services/<service_name>/<service_id>`，并通过租约续期；namespace 为空时仍是 `/services/...`。
- `gateway.RegisterHTTPRoutes(app, routes...)`：校验完整批次后，把手动路由写入 `/<namespace>/gateway/routes/manual/<slot>`。
- `gateway.RegisterHTTPRoute(app, route)`：单条注册的兼容封装。

必须先调用 `EnableEtcdRegistry`，因为注册接口通过 `app.EtcdDiscovery` 写入 etcd。多条公开路由优先一次调用 `RegisterHTTPRoutes`；它先校验整个批次再逐条写入，不启动后台 worker。`ServiceName` 为空时使用最近一次成功的 registry identity。

```go
package main

import (
    "log"
    "net/http"

    core "github.com/xs23933/core/v3"
    "github.com/xs23933/core/v3/gateway"
)

func main() {
    app := core.New(core.LoadConfigFile("config.yaml"))

    // 微服务内部真实路由。
    app.Get("/api/tasks/:id", func(c core.Ctx) error {
        return c.JSON(core.Map{"id": c.Params("id")})
    })

    // 注册当前服务实例，同时初始化 app.EtcdDiscovery。
    if err := app.EnableEtcdRegistry(nil); err != nil {
        log.Fatal("注册服务实例失败:", err)
    }

    // 注册 Gateway 对外路由；HTTP 上游使用相同路径。
    if err := gateway.RegisterHTTPRoutes(app,
        &gateway.Route{Method: http.MethodGet, Path: "/api/tasks/:id"},
    ); err != nil {
        log.Fatal("注册 HTTP 路由失败:", err)
    }

    if err := app.Run(); err != nil {
        log.Fatal(err)
    }
}
```

微服务配置：

```yaml
listen: 8081

etcd:
  namespace: xpay
  endpoints:
    - 127.0.0.1:2379
  service_name: task-service
  service_addr: 127.0.0.1:8081
  http_addr: http://127.0.0.1:8081
  service_id: task-service-1
  ttl: 5
  version: 1.0.0
```

字段说明：

| 字段 | 说明 |
| ---- | ---- |
| `Method` | Gateway 对外接受的 HTTP 方法 |
| `Path` | Gateway 对外路径 |
| `UpstreamPath` | HTTP 路由必须为空或与 `Path` 相同；不支持路径改写 |
| `ServiceName` | 对应 etcd 服务名；为空时读取 `etcd.service_name` |

`etcd.http_addr` 必须是 Gateway 进程可访问的完整 HTTP origin，包含 `http://` 或 `https://`，且不能包含 path、query 或 fragment。在容器或跨主机部署中，不要填写只对微服务自身有效的 `127.0.0.1`。

相同 HTTP method 和规范化 path 的手动路由优先于自动路由；两个不同自动来源声明不同目标时会停止激活该 slot，并在管理接口中显示冲突。

同一 `service_name` 下不同 `service_id` 会参与轮询。`GET`/`HEAD` 遇到连接失败时最多换一个实例重试一次；写请求不自动重放。对故障摘除时延敏感时可显式设置 `ttl: 5`，框架默认仍为 10 秒。

## 4. gRPC 自动注册

gRPC 微服务不需要调用 `RegisterHTTPRoute`。它只需在 `Listen` / `Run` 前注册 protobuf service 并调用 `EnableEtcdRegistry`，Gateway 会通过 reflection 自动生成路由。

gRPC 服务全部离线时，Gateway 保留已自动生成的路由并返回 `503`；任一实例恢复并重新注册后继续使用原路由。

内部 gRPC 契约不得自动暴露为 HTTP。Gateway 配置 `gateway.grpc_service_excludes`，使用完整 service 名精确排除：

```yaml
gateway:
  # Gateway 在读取和 Base64 编码前允许的请求体上限；默认 8 MiB。
  max_request_body_bytes: 8388608
  grpc_service_excludes:
    - payment.provider.v1.PaymentProviderService
    - game.provider.v1.GameProviderService
```

排除只影响 HTTP 自动路由，不关闭 reflection，也不影响内部 gRPC 客户端按服务发现调用。

### 4.1 自动路由命名

网关会把 proto package、service 和 method 合成 HTTP 路由。
如果前缀未包含 Post/Get/Put/Delete，HTTP method 默认为 POST。
服务名去除 `Service` 后缀，转为全小写。会自动映射为路径前缀, 例如 `UserService` → `/user`。
方法中尽量就别包含服务名前缀,否则会出现路径名多余重复: 

```proto
正确示例
service UserService {
    rpc GetInfo(GetUserInfoRequest) returns (GetUserInfoResponse); // GET /user/info
}

错误示例
service UserService {
    rpc GetUserInfo(GetUserInfoRequest) returns (GetUserInfoResponse); // GET /user/user/info
}
```



| gRPC 方法名 | HTTP 方法 | HTTP 路径示例 |
| ----------- | --------- | ------------- |
| `PostLogin` | `POST` | `/v1/auth/user/login` |
| `GetProfile` | `GET` | `/v1/auth/user/profile` |
| `PutProfile` | `PUT` | `/v1/auth/user/profile` |
| `DeleteSession` | `DELETE` | `/v1/auth/user/session` |
| `GetUserById` | `GET` | `/v1/auth/user/:id` |
| `PostReward_Claims` | `POST` | `/v1/vip/reward-claims` |
| `PostBenefitConfigs` | `POST` | `/v1/vip/benefit/configs` |

### 完整转换算法

按以下步骤依次处理，每一步的输入是上一步的输出：

1. **提取 HTTP 方法**：检查方法名是否以 `Post`/`Get`/`Put`/`Delete` 开头，若是则剥离此前缀作为 HTTP method，剩余部分进入下一步。若不匹配任何前缀，HTTP method 默认为 `POST`。

2. **转换 proto package 为路径前缀**：将 proto package 中的 `.` 替换为 `/`，例如 `v1.auth` → `/v1/auth`。

3. **转换 service 名为路径段**：取 service 全名（如 `v1.auth.UserService`）的最后一段，去掉 `Service` 后缀并转为全小写，例如 → `/user`。

4. **转换方法名剩余部分为路径段**（核心规则）：
   - **CamelCase 边界**：每个大写字母（首字符除外）前插入 `/`，并将该大写字母转为小写。例如 `BenefitConfigs` → `benefit/configs`。
   - **下划线 `_`**：一律转换为连字符 `-`，其后紧跟的大写字母同时转为小写。例如 `Reward_Claims` → `reward-claims`。
   - **注意**：下划线 **不会** 被转换为路径分隔符 `/`。`_` → `-`，不是 `_` → `/`。

5. **处理 `By` 关键字**：将步骤 4 结果中的 `by`（及其后可能存在的 `/`）替换为 `:`，形成路径参数标记。例如 `user/by/id` → `user/:id`。

6. **拼接最终路径**：`{package路径}/{service名}/{方法路径}`。

### 命名约定

- 方法名中的 `_` 用于连接语义相关的词，对应 HTTP 路径中的 `-`（连字符），**不对应** `/`（路径分隔符）。
- 需要路径分隔符时，使用 CamelCase 大写字母边界。
- 缩写使用 `Id` 而非 `ID`，避免 `ID` 被按 CamelCase 拆成 `/i/d`。

### 常见错误示例

| 错误写法 | 错误生成的路径 | 正确写法 | 正确路径 | 错误原因 |
| -------- | -------------- | -------- | -------- | -------- |
| `rpc PostReward_Claims(...)` | `/reward/claims` | 同上 | `/reward-claims` | 将 `_` 误当作路径分隔符 `/`，应转换为 `-` |
| `rpc PostBenefitConfigs(...)` | `/configs/benefit` | 同上 | `/benefit/configs` | CamelCase 拆分后顺序颠倒，`Benefit` 在前、`Configs` 在后 |
| `rpc GetUserByID(...)` | `/user/:id` | `rpc GetUserById(...)` | `/user/:id` | 使用 `ID` 全大写会被拆成 `/i/d`，应使用 `Id` |

## 5. HTTP 请求映射

Gateway 的 gRPC 自动代理总期限由 `gateway.grpc_request_timeout` 配置（例如 `180s`），也可通过 `gateway.Config.GRPCRequestTimeout` 指定；默认及非正值保持 10s。此期限覆盖一次 RPC 及其允许的只读重试，短于它的入站 context deadline 仍优先生效。它与下游单次 HTTP timeout 不同，多步骤登录应为整个流程留出预算。配置在 Gateway 启动时读取，修改后需重建/重启；错误日志包含实际 elapsed 和配置 timeout。


网关会合并三类输入：

- 路径参数：`/v1/auth/user/:id`
- query 参数：`?expand=true`
- JSON body：POST / PUT / DELETE 的请求体

合并优先级固定为 `query < JSON body < path params`，真实路径参数最后覆盖同名 query/body 字段，禁止客户端伪造路由分派字段。非 GET 请求体在解析或写入 `raw_body` 前受 `gateway.max_request_body_bytes` 限制，未配置或配置为非正数时使用 8 MiB 默认值，超限返回 `413 Request Entity Too Large`。

示例：

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

## 6. Metadata 透传

默认会把 HTTP 请求 Header 透传到 gRPC metadata，metadata key 使用小写 header 名。
同名 Header 的多个值会全部保留。HTTP/2 或 gRPC 传输层禁止的 Header
（如 `connection`、`content-length`、`content-type`）不会写入 metadata。

同时会透传 `Ctx.Vars()` 中由前置 middleware 写入的本地变量。

对于声明了 `raw_body` / `headers` 字段的 gRPC 请求，Gateway 会额外把 HTTP 原始请求体写入
`raw_body`，并把 HTTP Header 写入 `headers` map，便于支付回调等场景在后端验签。

常见做法是在网关 HTTP middleware 中解析 JWT，并写入：

```go
c.Set("user_id", "123")
```

后端 gRPC handler 从 metadata 中读取。

### Gateway 保留客户端 IP

`core-client-ip`（`core.GatewayClientIPMetadata`）由 Gateway 使用 `c.RemoteIP()` 生成。客户端同名头被过滤，并在 header/Vars 转换后覆盖成单值，不能由 middleware 变量覆盖。无效连接对端不发送该字段，RPC 重试使用同一值。HTTP 原始 headers/body 仍是不可信请求数据。

下游 Core 通过 `GRPCServerConfig.TrustGateway(ctx)` 验证实际 Gateway 对端或 mTLS 身份；未设置回调时默认信任实际对端的私网/回环 IP；显式回调覆盖默认规则，返回 false 时绝不兜底。`ExtractClientInfo` 仅对确认后的单值合法 IP 采信，其余情况返回 gRPC 对端，绝不回退原始 XFF/X-Real-IP。自动注册与 reflection 不代表调用方认证。部署需同时升级 Gateway/下游；公网 Gateway 或严格部署需配置服务端信任，示例见 [core-grpc.md](core-grpc.md#gateway-客户端-ip-的可信传递)。

### 6.1 gRPC 响应 Header 与 Cookie

业务 gRPC Handler 使用 `core.GrpcSetHeader(ctx, key, values...)`、`core.GrpcSetCookie(ctx, name, value, exp, path, args...)`、`core.GrpcRemoveCookie(ctx, name, path, dom...)` 或 `core.GrpcCookie(ctx, *http.Cookie)`（均返回 error），自动路由即支持写入浏览器 cookie；无需按业务解析 token JSON 或增加登录 adapter。

Gateway 通过 `ReflectionProxy.InvokeWithHeaders` 收集 initial response metadata，仅转换 `core-http-<小写 header 名>-bin`，header 名须符合字母、数字、`-_.` 的 metadata 字符集。多值与多个 Set-Cookie 逐条追加；成功/错误响应都转换，GET/HEAD 重试只转换最终尝试。普通 metadata、trailer、非法 header/value、Content-Type、Content-Length、Content-Encoding、hop-by-hop/proxy/grpc-* header 不转换，JSON 响应类型仍由 Gateway 管理。现有 `Invoke` JSON-only API 保持兼容。

`GrpcSetCookie` 默认值/可变参数与 BaseCtx.SetCookie 一致，Domain 默认取当前服务 Core 配置；与 Gateway 的 domain 配置无关。详细参数规则见 [core-grpc.md](core-grpc.md)。Gateway 认证 middleware 仍需读取验证请求 cookie，登录入口等免认证配置仍由 `gateway/public_routes` 管理；该能力不改变认证策略。

## 7. 管理接口

网关内置本地管理接口，仅允许 loopback 地址访问：

| 方法 | 路径 | 说明 |
| ---- | ---- | ---- |
| `GET` | `/admin/gateway/routes` | 路由列表 |
| `GET` | `/admin/gateway/routes/:id` | 路由详情 |
| `POST` | `/admin/gateway/routes` | 创建路由 |
| `PUT` | `/admin/gateway/routes/:id` | 更新或禁用路由 |
| `DELETE` | `/admin/gateway/routes/:id` | 删除路由 |

手动路由示例：

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

## 8. 排查优先级

网关发现不到方法时按顺序检查：

1. 业务服务是否调用了 `EnableEtcdRegistry`。
2. HTTP 自动发布是否启用了 `gateway.auto_http_routes.enabled`，并配置了命中实际路径的 `include_prefixes`；显式注册是否调用了 `RegisterHTTPRoutes` / `RegisterHTTPRoute`。
3. 路由的 `ServiceName` 是否与 `etcd.service_name` 一致。
4. `etcd.http_addr` 是否是网关进程可访问且不带 path 的完整 HTTP origin。
5. proto service 是否已在 `Listen` 前注册。
6. gRPC 方法名是否以 `Post/Get/Put/Delete` 开头。
7. 网关与业务服务是否连接同一组 etcd endpoints。

请求返回 `service <name> unavailable` 时，查看同一条错误日志中的 `state={...}`：

- `pool=missing` 或 `pool_instances=0` 表示 Gateway 当前没有该服务的可用 proxy 池。
- `discovery=disabled` 表示 Gateway 没有 fallback 服务发现快照。
- `discovery_instances=0` 表示 fallback discovery 也没有该服务实例，继续检查 `/<namespace>/services/<service_name>/` 注册；未配置 namespace 时检查 `/services/<service_name>/`。
- `circuit_state=1` 表示该服务连接连续失败后处于熔断冷却期。

休眠恢复或网络抖动后，重点匹配 `etcd service watch error`、`etcd service watch stopped unexpectedly`、`watch: <service>/<id> deregistered`、`watch: <service>/<id> registered`。只看到注销没有看到重新注册，优先排查 Gateway watch 或业务服务租约续期；重新注册存在但仍不可用，继续看连接实例失败日志。


## 托管工作负载 mTLS

通过 `core.LoadConfigFile` 加载 YAML 或加密 `.dat` 时，`workload.state_dir`、`trust_bundle_file`、`bootstrap_token_file` 以及 `workload_issuer.state_dir`、`policy_file`、`pin_file` 的非空相对路径，在环境变量展开后按该配置文件所在目录解析。`state_dir: ./workload` 因而始终位于配置旁，不随启动 cwd 或 `-f`/`--config` 的绝对、相对写法变化。绝对路径保持原值；直接以程序构造 `Options` 或调用 `workload.Open` 时，仍由调用方确定路径基准。其它业务配置路径不受此规则影响。

Gateway 可使用 Go 标准库首次自动创建私有 CA，后端自动申请、本地保存和续期，服务端、服务间客户端和 Gateway reflection 复用本进程身份。使用前必须在首次创建 gRPC server/client 前调用 `app.EnableWorkloadIdentity()`；启用后禁止明文回退或逐目标显式 transport 冲突配置。完整配置、引导认证、独立端口、根信任更新和验收边界见 [工作负载身份说明](../workload/README.md)。

后端跨服务器可预置 `workload.root_sha256`（根证书 DER SHA-256），Core 从内部签发入口 `GET /root.pem` 无凭据下载，校验指纹及本次 HTTPS 链/主机名后原子缓存，再申请身份。每次启动校验缓存，拒绝不匹配/损坏缓存的静默覆盖；默认缓存位于本进程 state_dir，不依赖 Gateway 共享文件。指纹必须由可信部署渠道预置，详见工作负载说明。

### Gateway gRPC 响应中的零值 code

未注册 ExactJSONBinding 的默认 Gateway 对 gRPC 响应仅补齐顶层 proto 原名为 `code`、不带 presence 的单值整数字段：零值仍输出 `code: 0`；64 位整数按 ProtoJSON 输出 `code: "0"`。非零值保持原值。未定义 code、optional/proto2/oneof、其他类型及嵌套 code 保持原序列化语义。不会补齐 error、data 或空数组，不会添加响应外层；HTTP 反向代理和普通 HTTP Handler 不受影响。已有接口若依赖零值 code 缺省，需要适配此响应变化。


## 精确 JSON 数字绑定（v3.1.59 起）

自 v3.1.59 起提供 `(*EtcdGateway).RegisterExactJSONBinding(ExactJSONBinding) error`。NewEtcdGateway 成功后、Run 前显式注册。配置字段为 `ServiceName`、`FullMethod`、`RequestMessage`、`ResponseMessage`、`MoneyPaths`、`IntegerPaths`、`MaxRecvBytes`；完整示例与权威契约见 [README 精确绑定小节](../README.md#gateway-精确-json-数字绑定v3159-起)。

- 请求 descriptor 只准单一 bytes raw_body。原 HTTP body 直接写入，不经 sonic/map/ProtoJSON 请求转换，不与 query/path/body 合并；query 返回 400，认证头沿用 metadata，单值校验由服务负责。
- 每次 RPC 前核验当前 descriptor；响应 code 为 int32、data 为 message。路径用原 proto 字段名和重复 message 的 [*]，金额仅 string，整数为 string 或 signed 64 位字段。
- 仅声明路径转换为精确 number；金额最多 8 位，缩放后 signed int64；整数 signed int64，无 float 或任意 raw JSON 出口。其它字符串/ID 不变。
- 绑定响应保留普通默认值/空数组。成功缺 data、指定叶子缺失/null、错误类型或溢出返回 500；非零业务 code 可无 data，提供 data 时仍须完整校验。
- 每绑定接收上限必须为 1..32MiB，只在该次调用覆盖，默认方法不变。重复注册拒绝、注册时复制切片，并发安全；写请求不重放。
- 不把该能力当成鉴权、服务暴露白名单、业务 JSON 校验或已发布 Core 的现有能力。
