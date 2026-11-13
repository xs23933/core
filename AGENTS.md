# Core v3 协作入口

模块 `github.com/xs23933/core/v3`；Go 版本以 go.mod 为准。优先简洁、现有 primitive 和明确错误处理。

## 开始

1. 检查 `git status --short`，保留其它工作；查看相关近期提交。
2. 阅读 [短速查](AI_CONTEXT.md)，按 [主题索引](skills/README.md) 只选任务涉及的 skill。
3. 查看实际实现、调用方和测试后修改；代码与测试是 API 的最终依据。

已有项目的 HTTP Handler 任务，8k 本地模型使用 [Handler 任务包](context/handler-8k.md)，无需重复注入本文件和完整参考。
128k 也按任务加载；完整资料见 [参考总览](AI_CONTEXT_REFERENCE.md)，不是必读前置。

## 必守边界

- 修改框架：保持公开 API 与空配置兼容，先复现再修共享根因。内部路由与测试可显式注册路由。
- 下游业务：`handler/grpc → services → dao → models`；app 装配依赖，Handler 做协议适配，Service 控制事务并返回 DTO/VO。
- 自动 Handler 嵌入 core.Handler；请求用 Params/ReadBody/Validate/ToJSON。context 传给下游；Ctx 仅在本次请求使用。
- 输入校验、鉴权、敏感错误脱敏与错误处理必须保留；日志不记录密钥/token。密码用 bcrypt，生产配置使用独立安全密钥。
- 数据访问复用 GORM/helper，处理缓存失效；连接在装配期创建。异步任务必须检查取消、退出和资源释放。
- Gateway 任务先读当前 gateway 代码和 skills/core-gateway.md，特别是 route_state 的有效路由选择；不得靠其它项目经验推断认证或注册机制。

## 验证与交付

- 行为/兼容性修改补回归测试；并发和生命周期修改验证启动、关闭、重连。
- Go 改动：gofmt 后运行 `GOCACHE=/private/tmp/core-gocache go test ./... -count=1`。
- 所有改动运行 `git diff --check`；文档检查链接、签名和示例。任务包运行 `python3 scripts/build-handler-context.py --check`。
- 契约变化同步 README、短速查、对应 skill/参考、示例和模板；新增主题同步两个 skills 索引。文档标准见 [编写约定](skills/AUTHORING.md)。
- 完成验证后必须调用自定义 reviewer 子 agent 独立只读评审，仅含本任务；修复问题后复审。不可用时如实报告。
- 仅修改本任务文件；未经用户明确要求不提交、推送或发布。交付说明实际验证结果、兼容性与未验证范围。
