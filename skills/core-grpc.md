---
name: core-grpc
description: 使用 Core Framework 开发 gRPC 服务端，覆盖 service 注册、TLS/拦截器配置、EnableGRPC、EnableEtcdRegistry、同端口分流和 reflection 规则
tags: [go, core-framework, grpc, protobuf, tls, interceptor, etcd, reflection]
---

# Core gRPC 服务端技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "添加 gRPC 服务"
- "gRPC 服务写 cookie / 响应 header / metadata"
- "配置 gRPC mTLS 或 interceptor"
- "注册 proto service"
- "EnableGRPC 怎么用"
- "gRPC 和 HTTP 共用端口"
- "服务注册到 etcd"
- "网关发现不到 gRPC 方法"

## 1. 基本规则

Core 的 gRPC 服务端入口：

| API | 用途 |
| --- | --- |
| `app.RegisterGRPCService(func(*grpc.Server))` | 注册 protobuf 生成的 service |
| `app.GetGRPCServer()` | 获取底层 `*grpc.Server`，适合封装到构造函数里 |
| `app.ConfigureGRPCServer(core.GRPCServerConfig)` | server 创建前配置 transport credentials 与 unary/stream interceptor |
| `app.EnableGRPC(addr)` | 启动 gRPC server |
| `app.EnableEtcdRegistry(opts)` | 注册 etcd、启用 discovery、开启 reflection |

顺序要求：

1. 创建 `app`。
2. 需要 TLS/interceptor 时调用一次 `ConfigureGRPCServer`。
3. 注册 gRPC service。
4. 调用 `EnableGRPC` 或 `EnableEtcdRegistry`。
5. 调用 `Listen` 或 `Run`。

## 2. 独立 gRPC 端口

HTTP 和 gRPC 使用不同端口时，`Listen` 负责 HTTP，`EnableGRPC` 负责 gRPC。

```go
package main

import (
    "context"

    "github.com/xs23933/core/v3"
    "google.golang.org/grpc"

    pb "your-project/proto/user/v1"
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

## 3. TLS、mTLS 与 interceptor

调用方构造标准 gRPC transport credentials，并在任何 service 注册或 server 获取之前配置。Core 保留默认 unary error wrapper，调用方 unary/stream interceptor 按声明顺序执行。

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
    return err
}
app.EnableGRPC(":9001")
app.RegisterGRPCService(registerServices)
```

配置只能成功一次，且必须早于 `GetGRPCServer`、`RegisterGRPCService` 和 `EnableEtcdRegistry`。证书身份、URI、吊销和授权策略由应用实现；Core 不推断业务身份。

## 4. HTTP 和 gRPC 共用端口

共用端口时，`EnableGRPC` 和 `Listen` 使用同一个地址。框架会按 HTTP/2 与 `Content-Type: application/grpc` 分流。

```go
app.RegisterGRPCService(func(s *grpc.Server) {
    pb.RegisterUserServiceServer(s, &UserService{})
})

app.EnableGRPC(":8080")
app.Listen(":8080")
```

配置 transport credentials 后禁止共端口；Core 会返回 `ErrGRPCTLSSharedAddress`。需要 mTLS 时使用独立但仍由 Core 托管的 gRPC 地址。

对应客户端需要 TLS 时，在首次 dial 前使用 `ConfigureGRPCClient(serviceName, GRPCClientConfig{TransportCredentials: ...})`；服务发现与明确 target 分别使用 `GrpcClient` 和 `GrpcClientAt`。Core 只托管 transport，证书信任与身份策略仍属于应用。

## 5. 注册到 etcd 并支持网关

需要被网关自动发现时，优先使用 `EnableEtcdRegistry`。它会创建/复用 gRPC server、注册服务实例、开启 discovery，并自动注册 gRPC reflection。

Core 初次注册携带 `ready=false`；`Listen` / `Run` 完成 Handler、gRPC、TLS 和路由目录准备后发布 `ready=true`。准备失败会清理注册；Gateway 和 gRPC resolver 不选择未就绪实例。直接使用 `etcd.NewRegistry` 的旧调用方默认不写 ready，兼容旧就绪语义；可设置 `Options.RegistrationReady` 并调用 `Registry.SetReady(ctx, ready)`。

每个 Registry 自动生成唯一 `generation`，租约恢复保留该代标识和 readiness。恢复使用 etcd CAS，已被后继代替换的旧进程停止重注册；新 Registry 初次注册仍允许同固定 key 接管。并存副本使用不同 `service_id`。注销先取消并等待续租/恢复任务停止，再撤销自身租约，避免晚写或删除后继注册；注册和 readiness 写入均有截止时间。

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

配置示例：

```yaml
etcd:
  # 同一项目的服务、客户端和 Gateway 使用相同值
  namespace: xpay
  endpoints:
    - 127.0.0.1:2379
  service_name: user-service
  service_addr: 127.0.0.1:8080
  service_id: user-service-1
  ttl: 10
  version: 1.0.0
```

`namespace: xpay` 会把服务注册到 `/xpay/services/<service_name>/`。不配置时继续使用 `/services/<service_name>/`，与现有部署兼容。

## 6. 封装注册函数

项目中通常把注册逻辑封装到 handler 或 module 初始化函数里。

```go
type AuthHandler struct {
    pb.UnimplementedAuthServiceServer
    userService *service.UserService
}

func NewAuthHandler(app *core.Core, userService *service.UserService) {
    pb.RegisterAuthServiceServer(app.GetGRPCServer(), &AuthHandler{
        userService: userService,
    })
}
```

注意：封装函数内部可以调用 `app.GetGRPCServer()`，但必须保证封装函数在 `Listen` / `Run` 前执行。

## 7. 生成代码时避免

- 不要在 `Listen` 之后再注册 gRPC service。
- 不要在 server 已创建后配置 TLS/interceptor，也不要把 gRPC transport credentials 用于 HTTP 共端口 handler。
- 不要手动维护重复的 reflection 注册；网关场景使用 `EnableEtcdRegistry`。
- 不要把 proto service 实现直接写进 HTTP handler；建议 service 层复用业务逻辑，HTTP handler 和 gRPC handler 只做协议适配。
- 不要把 `service_addr` 写成其它进程无法访问的地址。


服务注册携带每进程唯一 `generation`，同 ID/地址重启也重新 reflection。每个期望实例保留一个恢复任务，连接/reflection 超时后以 0.5 秒递增、最多 5 秒的间隔重试，熔断冷却后继续尝试；删除、换代或关闭取消任务。Registry 丢租恢复通过 CAS 防旧代覆盖，正常退出先撤注册，异常退出由 TTL 摘除。滚动升级采用方法并集，RPC descriptor 冲突返回 503，写请求不自动重放。

`GrpcClient("service-name")` 的 resolver 每连接绑定 Discovery；相同配置复用，改变 namespace/endpoints/etcd 身份返回 `ErrEtcdDiscoveryConfigurationChanged`，应新建 Core 实例迁移。直接 `etcd.NewRegistry` 默认没有 ready 字段、兼容立即可服务。

`ShutdownWithTimeout` 使用独立 deadline，先撤注册，再同时排空 HTTP/HTTP3/gRPC，最后关闭业务依赖与 Discovery；超时强制关闭 transport。信号退出默认 10 秒，可配置 `shutdown_timeout: "10s"`。重复调用共享首个退出任务，后来短期限只停止该调用等待。无 context 的旧 shutdown hook 无法强制取消，后台清理需它自行返回，但不延长调用方期限。

## 8. 响应 Header/Cookie 脚手架

优先用 Core helper，不在业务 Service 中保存 HTTP Ctx，也不为自动 gRPC 路由另写 cookie adapter：

```go
if err := core.GrpcSetHeader(ctx, "Cache-Control", "no-store"); err != nil {
    return nil, err
}
if err := core.GrpcSetCookie(ctx, "session", token, expires, "", "httponly", true); err != nil {
    return nil, err
}
// 退出：业务撤销会话后调用；原路径为 /。
if err := core.GrpcRemoveCookie(ctx, "session", "/"); err != nil {
    return nil, err
}
```

`GrpcSetCookie(ctx, name, value, exp, path, args...)` 复用 BaseCtx 的默认值/变参：path 空为 `/`、value URL 编码、SameSite=Lax；`"httponly"` 不区分大小写，其他 string 设置 Domain，bool 设置 Secure，重复 Domain/Secure 取最后值，未知类型忽略。Domain 空时回退当前 Core `domain`，默认 HttpOnly/Secure 为 false。Core unary/stream interceptor 注入当前应用配置；原生 grpc.Server 无域名配置回退，显式传 Domain 或完整 cookie。

`GrpcRemoveCookie(ctx, name, path, dom...)` 使用过去 Expires，空 path 不补 `/`，第一个 Domain 生效、空值回退配置。`GrpcCookie(ctx, *http.Cookie)` 不补默认值、不编码，可配置 host-only、MaxAge、SameSite。helper 都返回 error，非法 cookie/header 必须处理；不要用 context.Background 替代当前 RPC ctx，也不要在 initial headers 发送后调用。

响应 metadata 协议为 `core-http-<小写 header 名>-bin`，grpc-go 处理 wire 编码。header 名仅接受字母、数字、`-_.`，其他 HTTP token 符号返回错误，避免非法 metadata key。Gateway 只桥接这些 initial headers，包括 RPC 错误；多值、重复调用和多个 Set-Cookie 追加，重试只取最终尝试。普通 metadata 和 trailer 不转发，Content-Type、Content-Length、Content-Encoding、hop-by-hop/proxy/grpc-* 等保留 header 禁止设置。原生客户端用 grpc.Header 收集，不会自动保存 cookie。

认证、CSRF、CORS 策略由应用负责。完整属性与原生读取示例见 [README](../README.md)，可复制响应代码见 [example/grpc-cookie/session.go](../example/grpc-cookie/session.go)。
