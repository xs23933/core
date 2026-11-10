---
name: core-service
description: Core 业务逻辑与 DAO 边界、context 传播、DTO/VO 和事务
tags: [go, core-framework, service, dao, context, transaction]
---

# Core Service 开发技能

## 适用范围与规则

目录与装配先读 [core-architecture.md](core-architecture.md)。

编写业务逻辑、事务、HTTP/gRPC 共享用例时使用。默认调用链为
`handler/grpc → services → dao → models`。不要为了单一实现增加 Repository/interface 层。

- Handler 解析和验证参数、鉴权后，传入 `c.Ctx()`；gRPC 直接传入 RPC context。
- Service 的公开业务方法以 `context.Context` 为首参数，不接收或保存 `core.Ctx`。
- 应用装配时通过 `app.Conn()` 注入数据库；DAO 持有实例依赖，不在请求中查找全局 `core.Conn()`。
- DAO 使用 `db.WithContext(ctx)`；事务边界由 Service 控制，将同一个 tx 传给参与事务的 DAO。
- Service 返回 DTO/VO。数据库 Model 只在 Service/DAO 内部流转，不直接作为 HTTP 响应。
- 分页复用 `core.FindPageBy` 或 `core.Finds`，大列表用 cursor，不手写 limit/offset。
- 不把数据库故障一律映射成 not found，不忽略查询、更新、提交或缓存失效错误。

## 标准用例与输出

完整实现见 [标准 Service](../example/standard/internal/services/user.go)、
[DAO](../example/standard/internal/dao/user.go) 与
[回归测试](../example/standard/internal/services/user_test.go)。

Service 构造器接收已装配的 DAO；业务方法使用 context.Context，返回本包定义的输入/结果类型。
Model → Result 转换在 Service，协议层处理 HTTP/JSON 或 protobuf，不返回数据库 Model。
只将 gorm.ErrRecordNotFound 映射为 domain 的不存在错误；其它数据库错误继续传播，
由协议层记录并返回通用内部错误。业务授权与状态规则不能只存在某个协议入口。
分页结果保留 P/L/Total/Extra，非空 Data 用 append 或长度匹配的切片转换。

## 事务与装配

事务从注入的实例数据库开启：`db.WithContext(ctx).Transaction(func(tx *core.DB) error { ... })`。
在闭包内创建 `dao.NewUserDAO(tx)` 等 DAO，全部数据库操作复用 tx；不要在 DAO
中另开事务或退回全局连接。外部 HTTP、NSQ 和跨服务 RPC 不能被本地数据库事务回滚，
必须按业务设计幂等或 Outbox。

单应用可以在 `core.New` 后装配 `dao.NewUserDAO(app.Conn())`。需要多个应用共存时，
使用 `core.RegHandleFactory(func() core.Module { return &UserHandler{} })` 创建独立 Handler，
在 `Prepare(app *core.Core) error` 中检查 `app.Conn()` 并装配 DAO/Service。必需依赖的预热也放在
Prepare，并传递 `app.Ctx`；完成后才允许发布 ready。旧 `Start` 保持异步，不能承担就绪屏障。

## 回归验证

- 取消 context 后，DAO 查询应收到取消错误。
- 同进程两个 app 的 DAO 应访问各自数据库，关闭其中一个不影响另一个。
- Service 单测覆盖业务错误映射；事务测试覆盖中途失败后整批回滚。
- 不在 Service 中处理 HTTP Header/Cookie/状态码，不把 Ctx 传入 goroutine。
