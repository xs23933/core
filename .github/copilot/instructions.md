# GitHub Copilot Instructions

## Core Framework v3 开发规范

### 自动路由规则
- Handler 结构体必须嵌入 `core.Handler`
- 方法名以 HTTP 方法开头（Get/Post/Put/Delete）
- `GetByID` 生成 `/:id` 路由
- `GetParam` 生成 `/:param` 路由
- `GetParams` 生成 `/:param?` 路由
- `GetDetail__dot__html` 生成 `/detail.html` 路由
- `GetDetail__create` 生成 `/detail-create` 路由

### 代码模板参考
- Handler: `skills/core-handler.md`
- Model: `skills/core-model.md`
- Service: `skills/core-service.md`

### 禁止行为
- 下游业务使用自动 Handler；框架内部可显式注册路由
- 禁止在 Handler 中直接操作数据库
- 禁止使用 `panic(err)`
### 目录、装配与生成
- 新项目和迁移先读 `skills/core-architecture.md`，入口 `cmd/main.go → internal/app`。
- 调用 `handler/grpc → services → dao → models`；纯业务规则放 `domain`。
- app 显式装配依赖再登记 Handler，不要求 init() 隐式注册。
- HTTP context 用 `c.Ctx()`；`StdContext()` 属于 GRPCCtx。
- `corectl new` 与模板验收读 `skills/core-corectl.md`。
- `*_enums.yaml` 同目录生成与 `--check` 读 `skills/core-gen.md`。
