package skills
## Skill 4: core-page.md

```markdown
---
name: core-page
description: 实现 Core Framework 分页查询
tags: [go, core-framework, pagination, query]
---

# Core 分页查询技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "实现分页查询"
- "添加列表接口"
- "做滚动加载"
- "写 FindPageBy"

## 核心规范

### 1. 四种分页方式

| 方式          | 方法           | 适用场景           | 是否 Count |
| ------------- | -------------- | ------------------ | ---------- |
| 统一分页入口  | `Finds`        | 新代码、DAO 封装   | 可选       |
| Keyset 游标   | `FindsModeCursor` | 大数据列表、前后翻页 | ❌ 否    |
| 传统分页      | `FindPageBy`   | 后台管理、表格列表 | ✅ 是      |
| 兼容无 Count 页码 | `FindNextBy` | 旧移动端、旧滚动列表 | ❌ 否    |

### 2. Finds（推荐）

```go
func (dao *UserDAO) List(ctx context.Context, whr *core.Map, next bool) (core.FindsResult[model.User], error) {
    mode := core.FindsModePage
    if next {
        mode = core.FindsModeNext
    }

    tx := dao.db.WithContext(ctx).Model(&model.User{})
    return core.Finds[model.User](core.FindsParams{
        Where: whr,
        DB:    tx,
        Mode:  mode,
    })
}
```

`Finds` 内部创建结果 slice，默认使用 `FindsModePage` 返回 `total`；传 `FindsModeNext` 时保留旧的 offset/next 行为；传 `FindsModeCursor` 时使用 keyset 比较，返回 `next_cursor`、`prev_cursor`、`has_next`、`has_prev`。`Finds` 会复制传入的 `Where`，不会删除调用方 `Map` 中的分页、排序特殊 key。

### 3. FindsModeCursor（新列表推荐）

```go
result, err := core.Finds[model.Order](core.FindsParams{
    Where: &core.Map{"l": req.PageSize, "status": req.Status},
    DB:    dao.db.WithContext(ctx).Model(&model.Order{}),
    Mode:  core.FindsModeCursor,
    Cursor: &core.CursorSpec{
        Token:     req.Cursor,
        Direction: core.CursorDirection(req.CursorDirection),
        Fields:    []string{"created_at", "id"},
        Desc:      true,
    },
})
```

- 首次请求 token 留空；空 direction 按 `next`。
- 后翻使用 `NextCursor` + `CursorDirectionNext`，前翻使用 `PrevCursor` + `CursorDirectionPrev`。
- `Fields` 只允许一个唯一字段，或一个排序字段加一个唯一 tie-breaker；支持 `table.column`，不支持 SQL 表达式。
- cursor 模式忽略 `p`、`asc`、`desc`，不执行 `COUNT` 或 `OFFSET`，始终用 `limit + 1` 探测当前方向。
- token 会校验版本、字段、排序和值数量；它是 opaque 位置标记，不是签名或授权凭据。
- 聚合列表使用 `EncodeCursorToken` / `DecodeCursorToken` 复用 token 格式，但自行处理各数据源的边界查询与合并。

### 4. FindPageBy（带总数）

```go
type UserHandler struct {
    core.Handler
}

// GET /api/v1/users
func (h *UserHandler) Get(c core.Ctx) {
    var users []model.User
    
    // 构建查询条件
    whr := &core.Map{
        "p":    c.QueryInt("page", 1),      // 页码
        "l":    c.QueryInt("size", 20),     // 每页数量
        "desc": "created_at",               // 降序排序
        // 筛选条件
        "status":   c.QueryInt("status", 0),
        "username*": c.Query("keyword"),    // 模糊查询
    }
    
    // 执行分页查询
    page, err := core.FindPageBy(whr, &users, core.Conn())
    if err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    c.ToJSON(page, nil)
}
```

### 5. FindNextBy（兼容无 Count 页码分页）

`FindNextBy` 仍从 `p` 计算 `OFFSET`，不是真正的 cursor，只用于兼容已有调用。

```go
// GET /api/v1/users/next
func (h *UserHandler) GetNext(c core.Ctx) {
    var users []model.User
    
    whr := &core.Map{
        "p":    c.QueryInt("page", 1),
        "l":    c.QueryInt("size", 20),
        "desc": "created_at",
    }
    
    // 执行滚动分页
    nextPage, err := core.FindNextBy(whr, &users, core.Conn())
    if err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    c.ToJSON(nextPage, nil)
}
```

### 6. 带复杂条件的查询

```go
// DAO 层封装
func (dao *UserDAO) ListPage(ctx context.Context, whr *core.Map) (core.Page[vo.UserVO], error) {
    // 构建基础查询
    tx := dao.db.WithContext(ctx).
        Model(&model.User{}).
        Select("users.*, profiles.nickname").
        Joins("LEFT JOIN profiles ON profiles.user_id = users.id")
    
    // 添加筛选条件
    if status := whr.GetInt("status"); status > 0 {
        tx = tx.Where("users.status = ?", status)
    }
    
    if keyword := whr.GetString("keyword"); keyword != "" {
        tx = tx.Where("users.username LIKE ? OR users.email LIKE ?", 
            "%"+keyword+"%", "%"+keyword+"%")
    }
    
    // 设置默认排序
    if _, ok := (*whr)["asc"].(string); !ok {
        if _, ok := (*whr)["desc"].(string); !ok {
            (*whr)["desc"] = "users.created_at"
        }
    }
    
    var users []model.User
    page, err := core.FindPageBy(whr, &users, tx)
    if err != nil {
        return core.Page[vo.UserVO]{}, err
    }
    
    // 转换为 VO
    result := make([]vo.UserVO, len(users))
    for i, u := range users {
        result[i] = vo.ToUserVO(&u)
    }
    
    return core.Page[vo.UserVO]{
        P:     page.P,
        L:     page.L,
        Total: page.Total,
        Data:  result,
    }, nil
}
```

### 7. 筛选参数详解

| 参数写法        | 说明           | SQL 结果                        |
| --------------- | -------------- | ------------------------------- |
| `"name": "john"` | 精确匹配       | `WHERE name = 'john'`           |
| `"name*": "john"`| 包含匹配       | `WHERE name LIKE '%john%'`      |
| `"^name": "john"`| 前缀匹配       | `WHERE name LIKE 'john%'`       |
| `"name$": "john"`| 后缀匹配       | `WHERE name LIKE '%john'`       |
| `"age >": 18`    | 大于           | `WHERE age > 18`                |
| `"age <": 30`    | 小于           | `WHERE age < 30`                |
| `"age >=": 18`   | 大于等于       | `WHERE age >= 18`               |
| `"age <=": 30`   | 小于等于       | `WHERE age <= 30`               |
| `"status IN": []int{1,2}` | IN 查询 | `WHERE status IN (1,2)`         |
| `"asc": "name"`  | 升序排序       | `ORDER BY name ASC`             |
| `"desc": "age"`  | 降序排序       | `ORDER BY age DESC`             |

### 8. 返回值结构

#### FindPageBy 返回

```json
{
  "p": 1,
  "l": 20,
  "total": 128,
  "data": [...]
}
```

#### FindNextBy 返回

```json
{
  "p": 1,
  "l": 20,
  "next": true,
  "prev": false,
  "data": [...]
}
```

#### Finds 返回

`FindsModePage` 返回：

```json
{
  "p": 1,
  "l": 20,
  "total": 128,
  "data": [...]
}
```

`FindsModeNext` 返回：

```json
{
  "p": 1,
  "l": 20,
  "next": true,
  "prev": false,
  "data": [...]
}
```

`FindsModeCursor` 返回：

```json
{
  "p": 1,
  "l": 20,
  "next_cursor": "eyJ2IjoxLC4uLn0",
  "prev_cursor": "eyJ2IjoxLC4uLn0",
  "has_next": true,
  "has_prev": true,
  "data": []
}
```

### 9. 完整 Handler 示例

```go
package handler

import (
    "github.com/xs23933/core/v3"
    "your-project/internal/model"
)

type ProductHandler struct {
    core.Handler
}

func (h *ProductHandler) Init() {
    h.Prefix("/api/v1/products")
}

// GET /api/v1/products - 后台管理分页
func (h *ProductHandler) Get(c core.Ctx) {
    var products []model.Product
    
    whr := &core.Map{
        "p":        c.QueryInt("page", 1),
        "l":        c.QueryInt("size", 20),
        "desc":     "created_at",
        "status":   c.QueryInt("status", 0),
        "name*":    c.Query("keyword"),
        "price >":  c.QueryFloat("min_price", 0),
        "price <":  c.QueryFloat("max_price", 0),
    }
    
    page, err := core.FindPageBy(whr, &products, core.Conn())
    c.ToJSON(page, err)
}

// GET /api/v1/products/scroll - 移动端滚动加载
func (h *ProductHandler) GetScroll(c core.Ctx) {
    var products []model.Product
    
    whr := &core.Map{
        "p":    c.QueryInt("page", 1),
        "l":    c.QueryInt("size", 20),
        "desc": "created_at",
    }
    
    nextPage, err := core.FindNextBy(whr, &products, core.Conn())
    if err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    c.ToJSON(nextPage, nil)
}
```

## 禁止事项

- ❌ 手动拼接 `LIMIT` 和 `OFFSET`
- ❌ 在循环中查询数据库（N+1 问题）
- ❌ 不设置分页大小上限
- ❌ 新代码继续在 DAO 中手动维护 out slice 和分页模式分支，优先用 `Finds`
- ❌ 把 `FindNextBy` / `FindsModeNext` 当作真正的 cursor
- ❌ cursor 只按非唯一字段排序

## 输出要求

生成分页代码时必须包含：

1. ✅ 新列表优先使用 `FindsModeCursor`；兼容旧代码时使用 `FindPageBy` 或 `FindNextBy`
2. ✅ 正确的参数映射
3. ✅ 排序字段设置
4. ✅ 错误处理
```
