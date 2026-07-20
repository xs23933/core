# Core Framework v3 协作指南

本文件是 AI/自动化代理进入本仓库后的首要导航。它不复制完整 API 文档；具体能力与用法必须继续阅读 `AI_CONTEXT.md`、`skills/` 中对应主题文档，并以当前代码和测试为最终依据。

## 1. 项目定位

- 模块：`github.com/xs23933/core/v3`
- 语言版本：以 `go.mod` 为准（当前为 Go 1.25.0）。
- 定位：轻量级 Go Web 框架，包含自动路由、Ctx、GORM、Redis、NSQ、WebSocket、SSE、Fetch、gRPC、etcd 和 Gateway 等能力。
- 设计原则：简洁、约定优于配置、可读性优先、低依赖；避免过度抽象、过度 interface 化和 Java 风格分层。

## 2. 开始工作前

按以下顺序建立上下文：

1. 阅读本文件。
2. 阅读 `AI_CONTEXT.md`，掌握框架总览、强制约定和常用 API。
3. 阅读 `skills/README.md` 与任务对应的 `skills/core-*.md`。
4. 定位实际实现、现有测试和 `README.md` 对应章节。
5. 检查 `git status --short`，保留并避开用户已有改动。
6. 查看相关近期提交，确认当前行为及兼容边界。

文档、示例与实现不一致时，不要凭文档猜测：以当前实现和测试为准，确认差异后同步修正文档。

## 3. Skill 选择

先通过 `skills/README.md` 或 `skills/core-skills.md` 找到主题，再阅读对应文件：

| 任务 | 必读 Skill |
| --- | --- |
| Handler、自动路由、请求响应 | `core-handler.md`、`core-ctx.md` |
| Service、业务分层 | `core-service.md` |
| GORM、模型、事务、分页 | `core-model.md`、`core-page.md` |
| 中间件 | `core-middleware.md` |
| Redis 与缓存 | `core-redis.md`、`core-cache.md` |
| NSQ | `core-nsq.md` |
| 外部 HTTP API | `core-fetch.md` |
| gRPC 服务端/客户端 | `core-grpc.md`、`core-grpc-client.md` |
| etcd Gateway | `core-gateway.md` |
| WebSocket | `core-websocket.md` |
| SSE | `core-sse.md` |
| 文件上传 | `core-fileupload.md` |
| 日志 | `core-logger.md` |
| 测试 | `core-test.md` |
| 工具、加密、Map/Array | `core-utils.md` |
| Enum 生成、脚手架 | `core-gen.md`、`core-corectl.md` |
| 大范围整理或迁移 | `core-refactor.md` |

跨多个能力的修改必须阅读所有相关 Skill。例如 etcd namespace 变更通常同时涉及 Registry、Discovery、gRPC client、Gateway、配置、README 和多份 Skill。

## 4. 修改边界

本仓库既是框架实现，也是下游项目的规范来源。工作时区分两类场景：

### 4.1 修改框架自身

- 先从失败测试或可复现行为确认问题，再修改最小公共实现。
- 优先修复共享根因，不为单一调用方增加重复 helper。
- 保持公开 API 和空配置下的既有行为兼容；确需破坏兼容时必须明确说明。
- 框架内部、底层路由实现及测试可以直接使用 `app.GET`、`app.POST` 等 API；不要把下游自动路由规范误当成框架内部禁令。
- 涉及 goroutine、watcher、连接池、缓存或后台任务时，必须检查启动、退出、重连和资源释放生命周期。

### 4.2 编写下游使用示例或生成业务代码

- 默认采用 `handler -> service -> dao -> model`，Handler 只负责协议适配、参数解析、鉴权、调用 Service 和响应。
- 自动路由 Handler 必须嵌入 `core.Handler`，遵循方法名到 HTTP 路径的映射规则；动态参数使用 `_id` 或规定的 `Param/Params` 形式。
- 使用 `c.Params(...)`、`c.ReadBody(...)`、`c.Validate(...)` 和 `c.ToJSON(...)`，不要生成 Gin/Fiber/Echo 风格 API。
- Service 使用 `context.Context`，不要依赖 `core.Ctx`；事务边界放在 Service。
- 不直接向客户端暴露数据库 Model，使用 DTO/VO 表达输入输出。
- `core.Ctx` 只在当前请求生命周期有效；禁止存入全局或在 goroutine 中继续使用。异步工作先提取值并传递 `c.StdContext()` 或独立 context。

## 5. 通用编码规则

- 遵循现有包结构和命名；保持函数短小、职责单一、显式处理错误并优先提前返回。
- 不忽略错误，不使用 `panic`、`log.Fatal` 或 `fmt.Println` 处理可恢复错误。
- 日志使用框架日志 API，并避免记录密码、token、密钥和完整敏感请求体。
- 密码使用 bcrypt 封装；SHA-256 不能用于密码存储；生产环境不能使用默认 AES key。
- 数据访问优先使用 GORM 和已有查询/分页 helper，禁止拼接不可信 SQL。
- 修改数据库后同步处理相关缓存失效；不要在每个请求中重复创建 Redis、Fetch、gRPC 或 cache client。
- 不增加与现有公共 primitive 重复的一次性 helper；先搜索仓库中同类能力。
- 新行为、bugfix 和兼容边界必须有回归测试；并发或生命周期变更应包含相应测试。

## 6. 文档同步

框架公开行为发生变化时，同一任务中检查并更新：

- `README.md`：面向使用者的完整说明与示例。
- `AI_CONTEXT.md`：AI 总览、强制规则与速查内容。
- `skills/core-*.md`：对应能力的触发条件、推荐写法、禁止事项和示例。
- 相关 example、生成模板或脚手架输出。

新增能力时同步更新 `skills/README.md` 和 `skills/core-skills.md` 的索引。不要只更新其中一处，也不要在多个文件保留互相矛盾的签名、版本或默认值。

## 7. 验证要求

根据改动范围先运行最小相关测试，再运行仓库级验证：

```bash
gofmt -w <changed-go-files>
GOCACHE=/private/tmp/core-gocache go test ./... -count=1
git diff --check
```

纯文档改动至少执行：

```bash
git diff --check
```

同时检查文档引用的文件存在、命令与当前 API 相符。不要在未看到命令成功输出时声称测试通过；如果环境依赖导致无法运行，需报告具体命令、错误和未验证范围。

## 8. 交付约定

- 只修改任务需要的文件，不清理或覆盖无关的工作区改动。
- 交付时说明改了什么、为什么，以及实际执行的验证命令与结果。
- 未经用户明确要求，不提交、推送、创建分支或发布版本。
- 若修改了框架契约，明确指出兼容性影响和下游需要采取的动作。
