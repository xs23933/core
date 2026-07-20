---
name: core-grpc-client
description: 使用 Core Framework 的 GrpcClient、GrpcClientAt 和每服务 TLS 配置调用内部 gRPC 服务
tags: [go, core-framework, grpc, client, etcd, discovery, tls]
---

# Core gRPC 客户端技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "调用内部 gRPC 服务"
- "GrpcClient 怎么用"
- "按服务名连接 gRPC"
- "MustGrpcClient"
- "etcd 服务发现客户端"
- "GrpcClientAt"
- "gRPC 客户端 TLS"

## 1. 推荐入口

Core 提供三个客户端入口：

| API | 用途 |
| --- | --- |
| `app.GrpcClient(serviceName, opts...)` | 返回 `(*grpc.ClientConn, error)`，适合业务初始化 |
| `app.MustGrpcClient(serviceName, opts...)` | 失败时 panic，适合 main 启动期硬依赖 |
| `app.GrpcClientAt(serviceName, target)` | 按明确地址连接，复用同一每服务 transport 配置 |

`GrpcClient`/`MustGrpcClient` 通过 etcd resolver 发现实例，`GrpcClientAt` 直接使用 target。未配置的逻辑服务默认使用 insecure transport credentials，以保持旧行为。

## 2. 基本调用

```go
app := core.New(core.LoadConfigFile("config.yaml"))

conn, err := app.GrpcClient("user-service")
if err != nil {
    return err
}
defer conn.Close()

client := pb.NewUserServiceClient(conn)
resp, err := client.GetProfile(ctx, &pb.GetProfileRequest{
    Id: "1001",
})
if err != nil {
    return err
}

_ = resp
```

## 3. 启动期硬依赖

如果服务启动必须依赖某个 gRPC 后端，可以在 main 初始化阶段使用 `MustGrpcClient`。

```go
conn := app.MustGrpcClient("user-service")
userClient := pb.NewUserServiceClient(conn)
```

请求处理函数里不要反复调用 `MustGrpcClient`。更好的做法是启动期创建 client，并注入到 service。

```go
type OrderService struct {
    userClient pb.UserServiceClient
}

func NewOrderService(app *core.Core) *OrderService {
    conn := app.MustGrpcClient("user-service")
    return &OrderService{
        userClient: pb.NewUserServiceClient(conn),
    }
}
```

## 4. Discovery 配置

`GrpcClient` 在 `app.EtcdDiscovery` 为空时会尝试从配置启用 discovery。

```yaml
etcd:
  namespace: xpay
  endpoints:
    - 127.0.0.1:2379
  dialTimeout: 5
```

也可以显式初始化：

```go
if err := app.EnableEtcdDiscovery(&etcd.Options{
    Namespace: "xpay",
    Endpoints: []string{"127.0.0.1:2379"},
}); err != nil {
    return err
}

conn, err := app.GrpcClient("user-service")
```

客户端 namespace 必须与目标服务注册时一致。`xpay` 客户端只发现 `/xpay/services/` 下的实例；空值继续发现 `/services/`。

## 5. 每服务 TLS 与明确地址

调用方构造标准 `credentials.TransportCredentials`，Core 不读取 CA 文件、不推断 server name，也不定义证书身份规则。

```go
clientTLS := &tls.Config{
    MinVersion: tls.VersionTLS13,
    RootCAs:    rootPool,
    ServerName: "user.internal",
}
if err := app.ConfigureGRPCClient("user-service", core.GRPCClientConfig{
    TransportCredentials: credentials.NewTLS(clientTLS),
}); err != nil {
    return err
}

conn, err := app.GrpcClientAt("user-service", "127.0.0.1:9001")
```

约束：

- `ConfigureGRPCClient` 必须早于该服务首次 `GrpcClient`、`MustGrpcClient` 或 `GrpcClientAt`，同一服务只能配置一次。
- Core 在配置时克隆 credentials 作为快照，每个新连接再克隆，避免调用方后续修改 server name 影响已配置服务。
- 已配置服务调用 `GrpcClient(serviceName, opts...)` 时不能再传旧 dial options；Core 无法检查不透明 `grpc.DialOption` 是否包含第二份 transport credentials，因此会 fail closed。
- Gateway 的 `ServicePool` 按 etcd 逻辑服务名选择配置；网关必须在 `gateway.NewEtcdGateway(app)` 前完成 TLS 服务配置。

## 6. 生成代码时避免

- 不要手动拼接 etcd resolver target；使用 `app.GrpcClient("service-name")`。
- 不要在每个请求里创建新连接；连接应复用。
- 不要在请求处理中使用 `MustGrpcClient`，避免运行时 panic。
- 不要忘记关闭长期不用的 `ClientConn`；应用级连接通常随进程生命周期释放。
- 不要把 `InsecureSkipVerify` 作为 TLS 配置的便利选项。
- 不要在该服务首次 dial 后再配置 credentials，也不要向已配置服务额外传 transport dial option。
