---
name: core-grpc-client
description: 使用 Core Framework 的 GrpcClient 和 MustGrpcClient 通过 etcd 服务发现调用内部 gRPC 服务
tags: [go, core-framework, grpc, client, etcd, discovery]
---

# Core gRPC 客户端技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "调用内部 gRPC 服务"
- "GrpcClient 怎么用"
- "按服务名连接 gRPC"
- "MustGrpcClient"
- "etcd 服务发现客户端"

## 1. 推荐入口

Core 提供两个客户端入口：

| API | 用途 |
| --- | --- |
| `app.GrpcClient(serviceName, opts...)` | 返回 `(*grpc.ClientConn, error)`，适合业务初始化 |
| `app.MustGrpcClient(serviceName, opts...)` | 失败时 panic，适合 main 启动期硬依赖 |

两者都会通过 etcd resolver 按服务名发现实例。默认会添加 insecure transport credentials。

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
  endpoints:
    - 127.0.0.1:2379
  dialTimeout: 5
```

也可以显式初始化：

```go
if err := app.EnableEtcdDiscovery(&etcd.Options{
    Endpoints: []string{"127.0.0.1:2379"},
}); err != nil {
    return err
}

conn, err := app.GrpcClient("user-service")
```

## 5. 生成代码时避免

- 不要手动拼接 etcd resolver target；使用 `app.GrpcClient("service-name")`。
- 不要在每个请求里创建新连接；连接应复用。
- 不要在请求处理中使用 `MustGrpcClient`，避免运行时 panic。
- 不要忘记关闭长期不用的 `ClientConn`；应用级连接通常随进程生命周期释放。
