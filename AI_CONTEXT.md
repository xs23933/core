# Core v3：任务速查

本文件是入口；[完整参考](AI_CONTEXT_REFERENCE.md) 按主题查阅，不要求全文加载。
8k 的 HTTP 任务直接使用 [自包含任务包](context/handler-8k.md)；加载方法见 [上下文说明](context/README.md)。

## 下游业务默认写法

- 装配：`cmd/main.go → internal/app`；调用：`handler/grpc → services → dao → models`。
- Handler 嵌入 `core.Handler`，`Init()` 设置 `h.Prefix("/api/v1/users")`。
- 业务方法如 `func (h *UserHandler) Get_id(c core.Ctx) error`，路由为 `GET /api/v1/users/:id`。
- app 先创建 DAO/Service，再 `core.RegHandle(handler.NewUserHandler(svc))`。这是进程级登记，适合单 Core；多实例用 RegHandleFactory + Prepare，见 Handler skill。
- `c.Params("id")` 取路径；`c.Query("q", "")` 取 query；`c.ReadBody(&req)` 后 `c.Validate(&req)`，两步都检查错误。
- `result, err := h.service.Get(c.Ctx(), c.Params("id"))`，将 context 传给 DAO/RPC/HTTP。
- `return c.ToJSON(result, err)` 写统一 JSON；它不会根据 err 自动设置 HTTP 状态。错误分支先 `c.Status(code)`，内部错误记录日志后只返回通用信息。
- Service 接收 context.Context，控制事务、授权和业务规则；DAO 使用注入的 DB.WithContext(ctx)，结果转换为 DTO/VO。
- Ctx 在请求结束后会复用；异步任务先提取值，按任务生命周期选择 context。HTTP Ctx 的标准 context 是 Ctx()/Context()。

## 任务导航

| 任务 | 读取 |
| --- | --- |
| HTTP/自动路由 | [Handler](skills/core-handler.md)、[Ctx](skills/core-ctx.md) |
| 目录/装配/迁移 | [架构](skills/core-architecture.md) |
| 业务/事务 | [Service](skills/core-service.md)、[模型](skills/core-model.md) |
| 分页/条件 | [分页](skills/core-page.md) |
| 测试 | [测试](skills/core-test.md)、[标准 HTTP 验收](example/standard/internal/app/http_test.go) |
| 其它能力 | [主题索引](skills/README.md) |

框架内部及底层测试可显式注册路由；下游业务默认使用自动 Handler。
实现与文档冲突时先读当前实现、测试，再同步文档，不猜测 API。
