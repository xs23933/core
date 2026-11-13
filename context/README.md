# 按预算加载 Core 上下文

## 8k：已有项目的 HTTP Handler 任务

将 [handler-8k.md](handler-8k.md) 作为一次性上下文注入，再追加任务、当前代码与 Service 签名。
包内含核心规则和标准项目真实 Handler；不依赖模型能打开链接。
它不包含完整 app/DAO/数据库装配，不用于无资料从零搭建项目。工具可用时按包中路径继续读取；
无工具且签名缺失时，应补充必要代码，不能编造 Service API。

任务提示示例：

```text
使用已注入的 Core Handler 上下文。
任务：为已有 UserService 添加 GET /api/v1/users/:id。
当前 Handler、Service.Get 的签名与相关错误定义如下：<粘贴实际代码>
输出最小修改及实际验证结果；无法运行测试时明确说明。
```

生成包替代重复的 AGENTS/AI_CONTEXT/完整 skills 注入；适用仓库指令与运行器的系统指令仍然生效，不能省略验证或独立评审。
模型不会从 Markdown 自动识别窗口，需运行器或使用者明确选择。
本包以 6000 UTF-8 字节为维护上限，不代表 6000 tokens 或保证任何模型都能放入 8k。
使用目标 tokenizer 计算系统提示、工具定义、历史、任务包和任务的总 token；
建议总输入不超过约 4k，剩余窗口留给工具结果及输出。超预算时缩小任务或删减无关历史，保留校验与安全规则。

## 128k：按任务扩展

从 [AGENTS](../AGENTS.md)、[短速查](../AI_CONTEXT.md) 和 [主题索引](../skills/README.md) 开始，
加载相关 skill、直接调用方、实现与测试。只在任务涉及某个边界时打开完整参考，不要求全量注入。
这两种预算使用同一来源；本次不增加模型自动识别、向量检索或另一套手工文档。

## 生成与验证

```bash
python3 scripts/build-handler-context.py
python3 scripts/build-handler-context.py --check
python3 -m unittest discover -s scripts -p 'test_*context.py'
GOCACHE=/private/tmp/core-gocache go test ./example/standard/... -count=1
```

生成来源：core-handler.md 中 packet 标记间的规则和 example/standard/internal/handler/user.go。
修改这些来源后重新生成。CI 检查生成漂移与字节上限；真实示例由 Go 测试验证。
静态检查不等于模型效果评测；本次未跑本地模型对照实验。
