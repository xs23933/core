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
