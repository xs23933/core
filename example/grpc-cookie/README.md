# gRPC 登录响应 Cookie 脚手架

`session.go` 是可编译、可复制到 gRPC Handler 的响应适配示例，不是独立登录服务。业务 Service 先完成身份校验和会话签发，再将当前 RPC 的 `ctx`、token 和有效期传给 `LoginResponse`；退出时先撤销会话，再调用 `LogoutResponse`。按业务 proto 替换 `emptypb.Empty`。

服务保持 `RegisterGRPCService` → `EnableEtcdRegistry(nil)` → `Listen/Run` 的启动流程，Gateway 保持 `EnableEtcdDiscovery(nil)` → `NewEtcdGateway(app)` → `Listen/Run`。自动 gRPC 路由会把 helper 声明的 header/cookie 转成 HTTP 响应，不需要另写登录 adapter。

```go
// 当前 gRPC Handler 内：token/expires 来自已成功执行的业务登录。
if err := core.GrpcSetCookie(ctx, "session", token, expires, "", "httponly", true); err != nil {
    return nil, err
}

// 显式 Domain；省略时取当前服务 Core 的 domain 配置。
if err := core.GrpcSetCookie(ctx, "session", token, expires, "/", "example.com", "httponly", true); err != nil {
    return nil, err
}
```

空 path 为 `/`、SameSite 为 Lax、value 做 URL 编码；`"httponly"` 不区分大小写，其他字符串设置 Domain，bool 设置 Secure，重复 Domain/Secure 参数以后者为准。默认 Secure 和 HttpOnly 为 false，因此登录调用应显式传入。

需要 host-only、`__Host-` cookie、MaxAge 或自定义 SameSite 时使用 `core.GrpcCookie(ctx, &http.Cookie{...})`；该入口不补默认值，也不编码 value。删除时 Path/Domain 应与写入一致，`GrpcRemoveCookie` 与 `BaseCtx.RemoveCookie` 一样以过去的 Expires 删除，空 path 不自动补 `/`。

helper 必须在当前 RPC 发送 initial headers 前调用，不能使用 `context.Background()` 替代 RPC context。Core server 自动提供当前应用的默认 domain；自建 `grpc.NewServer()` 没有配置域名回退，应显式传 Domain 或完整 cookie。Gateway 只桥接 initial response metadata，错误响应也保留明确声明的 header/cookie，重试只使用最终尝试的 metadata。普通 metadata 和 trailer 不转换。

原生 gRPC 客户端可以用 `grpc.Header(&md)` 读取 `core-http-set-cookie-bin`，它收到的是原始 cookie 字符串。原生客户端不会自动保存浏览器 cookie。

登录 token 不必同时返回 JSON。Gateway 认证中间件仍需读取并验证请求 cookie；登录/刷新等入口是否免登录由应用的认证中间件和 `gateway/public_routes` 决定。HTTPS、跨站场景的 CORS/credentials 和 CSRF 策略仍由应用配置。

编译检查：`go test ./example/grpc-cookie`。
