---
name: core-gateway
description: 使用 Core Framework gateway 子包把 etcd 中的 gRPC 服务自动映射为 HTTP 路由，覆盖自动注册、路由命名、管理接口和 metadata 透传
tags: [go, core-framework, gateway, grpc, etcd, http, reflection]
---

# Core Gateway 技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "启动网关"
- "HTTP 转 gRPC"
- "gRPC 自动路由"
- "gateway.NewEtcdGateway"
- "网关管理接口"
- "网关 metadata / x-user-id"

## 1. 核心链路

网关依赖 etcd 和 gRPC reflection：

1. 业务服务调用 `EnableEtcdRegistry` 注册服务实例，并自动开启 reflection。
2. 网关调用 `EnableEtcdDiscovery` 连接 etcd。
3. `gateway.NewEtcdGateway(app)` 读取服务列表，连接 gRPC reflection。
4. 网关按 gRPC 方法名自动生成 HTTP 路由。
5. HTTP 请求被转成 JSON message，再调用后端 gRPC 方法。

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
  endpoints:
    - 127.0.0.1:2379
  dial_timeout: 5
```

## 3. 自动路由命名

网关会把 proto package、service 和 method 合成 HTTP 路由。

| gRPC 方法名 | HTTP 方法 | HTTP 路径示例 |
| ----------- | --------- | ------------- |
| `PostLogin` | `POST` | `/v1/auth/user/login` |
| `GetProfile` | `GET` | `/v1/auth/user/profile` |
| `PutProfile` | `PUT` | `/v1/auth/user/profile` |
| `DeleteSession` | `DELETE` | `/v1/auth/user/session` |
| `GetUserById` | `GET` | `/v1/auth/user/:id` |

转换规则：

- proto package `v1.auth` 转成 `/v1/auth`。
- service `v1.auth.UserService` 去掉 `Service` 后缀，转成 `/user`。
- 方法名前缀 `Post/Get/Put/Delete` 转成 HTTP method。
- 方法名剩余部分按 CamelCase 拆路径。
- `By` 转成路径参数标记 `:`。
- 缩写使用 `Id`，避免 `ID` 被拆成 `/i/d`。

## 4. HTTP 请求映射

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

## 5. Metadata 透传

默认会透传：

- `authorization`
- `x-request-id`
- `x-user-id`
- `Ctx.Vars()` 中由前置 middleware 写入的本地变量

常见做法是在网关 HTTP middleware 中解析 JWT，并写入：

```go
c.Set("user_id", "123")
```

后端 gRPC handler 从 metadata 中读取。

## 6. 管理接口

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

## 7. 排查优先级

网关发现不到方法时按顺序检查：

1. 业务服务是否调用了 `EnableEtcdRegistry`。
2. `service_addr` 是否是网关进程可访问的地址。
3. proto service 是否已在 `Listen` 前注册。
4. 方法名是否以 `Post/Get/Put/Delete` 开头。
5. 网关是否能连接同一个 etcd endpoints。
