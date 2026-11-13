---
name: core-handler
description: 编写下游 HTTP Handler、自动路由、参数校验、错误响应及应用注册
---

# Handler

## 适用任务

新增或修改 HTTP 接口、自动路由、Handler 生命周期。请求 API 同读 [Ctx](core-ctx.md)；
创建项目/装配读 [架构](core-architecture.md)，业务与事务读 [Service](core-service.md)。

## 必须遵守

<!-- packet:start -->
Use github.com/xs23933/core/v3. Downstream call chain: handler -> services -> dao -> models.
Embed core.Handler; Init sets Prefix; app constructs dependencies then calls core.RegHandle(handler.NewUserHandler(svc)).
This is process-global registration for one Core. For multiple Core instances read core-handler-reference.md before using RegHandleFactory/Prepare.
HTTP methods take core.Ctx and may return error. Use Params("id"), Query("q", ""), ReadBody(&req), Validate(&req).
Check parsing and validation errors. Set c.Status(400) before returning c.ToJSON(nil, core.NewError(400, "invalid request")).
ToJSON formats JSON but does not set HTTP status from an error. Map known business errors; log internal errors and return a generic 500 message.
Pass c.Ctx() into services and DB/RPC/HTTP calls. Services own transactions, business authorization and DTO results; inject dependencies once in app.
Ctx is request-scoped: extract values before asynchronous work; choose a context with the intended lifetime. Do not retain Ctx.
Routes relative to Prefix: Get -> GET /; Get_id -> GET /:id; Post -> POST /; Put_id -> PUT /:id; GetProfile -> GET /profile.
GetId is static /id. Param is required /:param; Params is optional /:param? and must be last. Keep parameter names consistent at shared route positions.
Preload must verify real credentials and return c.Next() to continue. Header presence is not authentication.
Core internals/tests may register explicit routes; downstream business handlers use automatic routes.
<!-- packet:end -->

## 最小完整示例

使用 [已编译的标准 Handler](../example/standard/internal/handler/user.go)，由
[app](../example/standard/internal/app/http.go) 创建依赖并登记；完整项目启动见
[标准示例](../example/standard/README.md)。示例中的 Service/domain 是同项目实际文件，不是未实现的占位符。
无文件读取工具时使用 [8k 包](../context/handler-8k.md)，已嵌入 Handler 源码。

## 易错边界

- ReadBody 和 Validate 分别检查错误；body/tag 规则见 Ctx。业务授权仍在 Service 保证。
- Init 做前缀等注册期配置；必需依赖预热用 Prepare。Start 异步，不是就绪屏障。
- 多实例不能共享绑定的 Handler/DAO；Prepare 失败后 Stop 也可能执行，清理允许未初始化依赖。
- 可选参数/catch-all 仅放末尾；静态路径默认区分大小写，配置与歧义合同查完整参考。
- 本文面向业务接口；ReplaceHandle、并发关闭和旧 Start/Init 顺序按需读参考，不改变其合同。

## 验证与深入

完成标准：路径和 HTTP 状态符合需求；非法输入、业务不存在和内部错误不泄露信息；context 取消向下传递。
参考 [HTTP 验收](../example/standard/internal/app/http_test.go)、[Service 测试](../example/standard/internal/services/user_test.go)。
完整路由、生命周期、ReplaceHandle、多 Core 合同见 [Handler 完整参考](core-handler-reference.md)。
真实 API： [handler.go](../handler.go)、[module.go](../module.go)、[ctx.go](../ctx.go)。
