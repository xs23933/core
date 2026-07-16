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
- "gateway.RegisterHTTPRoute"
- "gRPC 自动路由"
- "gateway.NewEtcdGateway"
- "网关管理接口"
- "网关 metadata / x-user-id"

## 1. 核心链路

网关通过 etcd 发现服务实例和路由定义：

1. HTTP 或 gRPC 业务服务调用 `EnableEtcdRegistry` 注册服务实例。
2. 网关调用 `EnableEtcdDiscovery` 连接 etcd。
3. HTTP 服务调用 `gateway.RegisterHTTPRoute` 显式发布路由定义。
4. gRPC 服务自动开启 reflection，由网关按方法名生成 HTTP 路由。
5. `gateway.NewEtcdGateway(app)` 读取服务实例和路由，并代理到对应协议的上游。

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

## 3. HTTP 微服务显式注册

HTTP 微服务需要分别注册服务实例和 HTTP 路由：

- `app.EnableEtcdRegistry(nil)`：把实例写入 `/<namespace>/services/<service_name>/<service_id>`，并通过租约续期；namespace 为空时仍是 `/services/...`。
- `gateway.RegisterHTTPRoute(app, route)`：把路由写入 `/<namespace>/gateway/routes/<route_id>`；namespace 为空时仍是 `/gateway/routes/...`。

必须先调用 `EnableEtcdRegistry`，因为 `RegisterHTTPRoute` 通过 `app.EtcdDiscovery` 写入 etcd。一个服务有多条公开路由时，逐条调用 `RegisterHTTPRoute`。

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
    app.Get("/tasks/:id", func(c core.Ctx) error {
        return c.JSON(core.Map{"id": c.Params("id")})
    })

    // 注册当前服务实例，同时初始化 app.EtcdDiscovery。
    if err := app.EnableEtcdRegistry(nil); err != nil {
        log.Fatal("注册服务实例失败:", err)
    }

    // 注册 Gateway 对外路由；ServiceName 为空时读取 etcd.service_name。
    if err := gateway.RegisterHTTPRoute(app, &gateway.Route{
        Method:       http.MethodGet,
        Path:         "/api/tasks/:id",
        UpstreamPath: "/tasks/:id",
        Description:  "查询任务",
    }); err != nil {
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
  service_id: task-service-1
  ttl: 10
  version: 1.0.0
```

字段说明：

| 字段 | 说明 |
| ---- | ---- |
| `Method` | Gateway 对外接受的 HTTP 方法 |
| `Path` | Gateway 对外路径 |
| `UpstreamPath` | HTTP 微服务内部真实路径；为空时使用 `Path` |
| `ServiceName` | 对应 etcd 服务名；为空时读取 `etcd.service_name` |
| `Headers` | Gateway 转发到上游时附加或覆盖的请求头 |

`service_addr` 必须是 Gateway 进程可访问的地址。在容器或跨主机部署中，不要填写只对微服务自身有效的 `127.0.0.1`。

gRPC 微服务不需要调用 `RegisterHTTPRoute`。它只需在 `Listen` / `Run` 前注册 protobuf service 并调用 `EnableEtcdRegistry`，Gateway 会通过 reflection 自动生成路由。

内部 gRPC 契约不得自动暴露为 HTTP。Gateway 配置 `gateway.grpc_service_excludes`，使用完整 service 名精确排除：

```yaml
gateway:
  grpc_service_excludes:
    - payment.provider.v1.PaymentProviderService
    - game.provider.v1.GameProviderService
```

排除只影响 HTTP 自动路由，不关闭 reflection，也不影响内部 gRPC 客户端按服务发现调用。

## 4. 自动路由命名

网关会把 proto package、service 和 method 合成 HTTP 路由。

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

网关会合并三类输入：

- 路径参数：`/v1/auth/user/:id`
- query 参数：`?expand=true`
- JSON body：POST / PUT / DELETE 的请求体

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
2. HTTP 服务是否在 `EnableEtcdRegistry` 之后调用了 `RegisterHTTPRoute`。
3. 路由的 `ServiceName` 是否与 `etcd.service_name` 一致。
4. `service_addr` 是否是网关进程可访问的地址。
5. proto service 是否已在 `Listen` 前注册。
6. gRPC 方法名是否以 `Post/Get/Put/Delete` 开头。
7. 网关与业务服务是否连接同一组 etcd endpoints。

请求返回 `service <name> unavailable` 时，查看同一条错误日志中的 `state={...}`：

- `pool=missing` 或 `pool_instances=0` 表示 Gateway 当前没有该服务的可用 proxy 池。
- `discovery=disabled` 表示 Gateway 没有 fallback 服务发现快照。
- `discovery_instances=0` 表示 fallback discovery 也没有该服务实例，继续检查 `/<namespace>/services/<service_name>/` 注册；未配置 namespace 时检查 `/services/<service_name>/`。
- `circuit_state=1` 表示该服务连接连续失败后处于熔断冷却期。

休眠恢复或网络抖动后，重点匹配 `etcd service watch error`、`etcd service watch stopped unexpectedly`、`watch: <service>/<id> deregistered`、`watch: <service>/<id> registered`。只看到注销没有看到重新注册，优先排查 Gateway watch 或业务服务租约续期；重新注册存在但仍不可用，继续看连接实例失败日志。
