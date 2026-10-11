# Finds 列表查询 SQL 注入检查与整改

检查日期：2026-10-11。范围：本仓库共享 `Where`、`Finds` 全部模式及旧列表/分页接口；未对下游业务的公网入口做渗透测试。

## 检查结论

原实现存在 SQL 注入风险，条件是调用方允许不可信输入控制筛选 key 或排序参数：

| 输入位置 | 原实现 | 本次处理 |
| --- | --- | --- |
| `asc` / `desc` | 字符串作为原始 SQL 传入 `Order` | 校验字段列表，使用 `clause.OrderByColumn` 引用字段 |
| `IN` / `NOTIN` / LIKE 筛选 key | 字段部分直接拼接 SQL | 先校验完整 key 语法，再按数据库方言引用字段 |
| 比较 / 不等于筛选 key | 手工拼反引号，未校验字段 | 同一套校验和数据库方言引用 |
| 普通等值 key | GORM 会引用字段，但框架未限制 key | 统一校验，非法字段在 SQL 执行前报错 |
| 普通字符串值 | 通过 `?` 参数绑定 | 保持参数绑定，不按 SQL 关键词过滤正常值 |
| 游标字段和边界 | 已校验字段并绑定边界值 | 复用公共标识符引用；补齐普通筛选及遗留排序参数校验 |
| `omitFields` | 未校验类型，非字符串会 panic | 校验类型及字段列表，返回统一错误 |

修复前，本地 SQLite 三条记录上的以下输入会返回全部三条，本应搜索 `absent` 的条件被改变：

```go
Map{"name LIKE '%' OR name*": "absent"}
// WHERE name LIKE '%' OR name like ?
Map{"id IN (1,2,3) OR id IN": []int{999}}
// WHERE id IN (1,2,3) OR id in (?)
```

`TestListQueriesRejectUnsafeInputBeforeSQL/Finds-page/like` 在修复前失败：返回 `nil` error，未拦截。修复后要求返回 `ErrInvalidQuery`，且 SELECT/COUNT 均不执行。该证据证明共享构造器风险，不代表所有下游 HTTP 接口都可被攻击；实际暴露取决于请求映射方式。

## 整改计划及落实

- [x] 共享 `Where` 在删除控制参数、忽略空值及构造条件前校验所有 key；非法条件使整个查询失败，避免删除条件后扩大查询。
- [x] 筛选字段仅允许 `[A-Za-z_][A-Za-z0-9_]*` 或一层 `table.column`；保留 `IN`、`NOTIN`、`!=`、`> < >= <=`、`*`、`^`、`$` 已有语法。
- [x] `asc` / `desc` / `omitFields` 仅允许逗号分隔字段字符串；空字符串为 no-op，错误类型、SQL 表达式、注释、分号、内嵌排序方向等返回 `ErrInvalidQuery`。
- [x] 排序使用结构化 GORM clause；所有动态操作符筛选字段按数据库方言引用，值保持参数绑定。
- [x] 保持 `Where` 原签名，通过 `tx.Error` 返回错误；`Finds`、`FindPage`、`FindPageBy`、`FindNext`、`FindNextBy`、`Find` 传播错误。拒绝时不污染可复用的共享 DB/scope。
- [x] 游标模式忽略合法 `asc` / `desc` 前先校验，避免不同模式有不同的非法输入处理；合法排序仍由 `CursorSpec` 决定。
- [x] 增加全入口拒绝矩阵、合法筛选/排序、字面攻击字符串、可信 scope/Expr、分页/字段排除、DB 错误隔离及 MySQL/PostgreSQL DryRun 回归测试。
- [x] 同步 README、AI_CONTEXT、model/page skills 及 skill 索引。

## 兼容与使用边界

原先放在 Map 里的任意 SQL 片段会被拒绝。例如 `asc: "id DESC"` 应改为 `desc: "id"`。合法多字段排序可使用 `desc: "created_at,id"`；混合方向通过分别设置 `asc` 和 `desc` 或可信 GORM scope 完成。

调用方使用 `errors.Is(err, core.ErrInvalidQuery)` 识别输入错误；直接使用 `Where` 时检查返回的 `tx.Error`。不得忽略错误后换用未过滤的基础 DB 重试。

框架只提供 SQL 语法边界，不负责业务字段授权。下游 DAO 仍须定义可筛选、排序及返回的字段，并独立施加租户/权限条件。调用方自行构造的 GORM SQL scope 或 `core.Expr` 是可信代码边界，不能包含未绑定的请求字符串。分页大小上限、LIKE 通配符策略和下游字段授权不在本次 SQL 注入修复范围内。

## 验证

```bash
GOCACHE=/private/tmp/core-gocache go test . -run 'Test(ListQueries|Where|Finds|FindNextBy|.*Cursor)' -count=1
GOCACHE=/private/tmp/core-gocache go test ./... -count=1
git diff --check
```

SQLite 测试执行真实内存数据库查询；MySQL/PostgreSQL 测试只检查 DryRun SQL 和绑定参数，不作为真实数据库集成验证。全量测试需要允许在本机临时端口监听的环境。

本次执行结果：定向测试通过，包含 24 类非法输入 × 9 个入口的 216 个拒绝场景；全量测试最初受沙箱端口监听限制，在允许本机监听后重跑通过；`git diff --check` 通过。
