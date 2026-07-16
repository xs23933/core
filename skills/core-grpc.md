---
name: core-grpc
description: 使用 Core Framework 开发 gRPC 服务端，覆盖 service 注册、EnableGRPC、EnableEtcdRegistry、同端口分流和 reflection 规则
tags: [go, core-framework, grpc, protobuf, etcd, reflection]
---

# Core gRPC 服务端技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "添加 gRPC 服务"
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
| `app.EnableGRPC(addr)` | 启动 gRPC server |
| `app.EnableEtcdRegistry(opts)` | 注册 etcd、启用 discovery、开启 reflection |

顺序要求：

1. 创建 `app`。
2. 注册 gRPC service。
3. 调用 `EnableGRPC` 或 `EnableEtcdRegistry`。
4. 调用 `Listen` 或 `Run`。

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

## 3. HTTP 和 gRPC 共用端口

共用端口时，`EnableGRPC` 和 `Listen` 使用同一个地址。框架会按 HTTP/2 与 `Content-Type: application/grpc` 分流。

```go
app.RegisterGRPCService(func(s *grpc.Server) {
    pb.RegisterUserServiceServer(s, &UserService{})
})

app.EnableGRPC(":8080")
app.Listen(":8080")
```

## 4. 注册到 etcd 并支持网关

需要被网关自动发现时，优先使用 `EnableEtcdRegistry`。它会创建/复用 gRPC server、注册服务实例、开启 discovery，并自动注册 gRPC reflection。

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

## 5. 封装注册函数

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

## 6. 生成代码时避免

- 不要在 `Listen` 之后再注册 gRPC service。
- 不要手动维护重复的 reflection 注册；网关场景使用 `EnableEtcdRegistry`。
- 不要把 proto service 实现直接写进 HTTP handler；建议 service 层复用业务逻辑，HTTP handler 和 gRPC handler 只做协议适配。
- 不要把 `service_addr` 写成其它进程无法访问的地址。
