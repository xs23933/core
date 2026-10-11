# v2 共享 Where SQL 注入整改

日期：2026-10-11。发布版本：v2.0.64。

本次回移 v3.1.48 的共享查询安全修复，保持 v2 现有 API，不引入 v3 的 Finds 或游标功能。发布以远端 v2.0.63 为基线，未包含本地 v2 原有的未发布文档提交。

## 风险与复现

原 `Where` 将 `asc` / `desc` 作为原始排序 SQL，将带操作符筛选 key 的字段部分直接拼入 SQL。若请求可以控制这些输入，就存在 SQL 注入风险。普通字符串值原本已参数绑定，继续保留。

本地 SQLite 回归测试在修复前失败：`TestListQueriesRejectUnsafeInputBeforeSQL/FindPage/like` 中的 `Map{"name LIKE '%' OR name*": "absent"}` 返回 `nil` error，未拦截恶意 SQL 结构。该检查验证框架查询构造器，不代表下游公网接口已验证可被利用。

## 已落实的整改

- 所有筛选字段在删除控制参数或忽略空值前校验。非法条件使整个查询失败，避免静默丢弃后扩大查询。
- 字段只允许 ASCII 标识符或一层 `table.column`，保留已支持操作符。
- `asc` / `desc` / `omitFields` 只接受逗号分隔字段字符串；空字符串为 no-op，表达式、注释、分号、内嵌方向及错误类型报错。
- 排序使用结构化 GORM clause，动态筛选字段按数据库方言引用，值保持参数绑定。
- 统一返回 `ErrInvalidQuery`；`Where` 原签名不变，通过 `tx.Error` 返回。拒绝时不污染可复用 DB/scope。
- 回归测试覆盖 24 类非法输入 × 4 个入口（`Where`、`FindPage`、`FindNext`、`Find`），共 96 个拒绝场景，并断言 SQL 不执行。
- 补齐合法筛选/排序、字面攻击字符串、分页/字段排除、共享 DB 错误隔离、可信 scope/Expr、MySQL/PostgreSQL DryRun 测试。
- 同步 README 的 API 契约和兼容迁移说明。

## 兼容边界

Map 中的任意 SQL 片段会被拒绝。例如 `asc: "id DESC"` 改为 `desc: "id"`，合法多字段排序可写 `desc: "created_at,id"`。复杂 SQL 放在可信的 DAO GORM scope 中，禁止拼接不可信请求字符串。

标识符校验不等于业务字段授权；下游仍须维护字段白名单、租户和权限条件。调用方必须检查 `error` 或 `tx.Error`，不得在失败后改用无过滤基础 DB 查询。分页上限、LIKE 通配符策略及下游权限不在本次范围内。

## 验证命令

```bash
GOCACHE=/private/tmp/core-gocache go test . -run 'Test(ListQueries|Where)' -count=1
GOCACHE=/private/tmp/core-gocache go test ./... -count=1
git diff --check
```

SQLite 测试执行真实内存数据库查询；MySQL/PostgreSQL 仅验证 DryRun SQL 及绑定参数，不作为真实数据库集成验证。

本次定向测试、全量 `go test ./... -count=1` 与 `git diff --check` 均通过。仓库原有忽略规则包含 `*_test.go`，本次回归测试文件显式纳入版本控制。
