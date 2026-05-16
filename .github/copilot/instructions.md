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
- 禁止手动注册路由
- 禁止在 Handler 中直接操作数据库
- 禁止使用 `panic(err)`