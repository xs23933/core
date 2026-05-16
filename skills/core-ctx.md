---
name: core-ctx
description: Core Framework Ctx 上下文正确使用方法，避免与 Gin 混淆
tags: [go, core-framework, ctx, context]
---

# Core Ctx 使用规范

## ⚠️ 关键差异（AI 必读）

### 1. 参数获取：是 `Params()` 不是 `Param()`

```go
// ❌ 错误（Gin 风格）
id := c.Param("id")

// ✅ 正确（Core 风格）
id := c.Params("id")  // 注意有个 s
```

### 2. 请求体解析：用 `ReadBody()` 不是 `ShouldBindJSON()`

```go
// ❌ 错误（Gin 风格）
var user User
if err := c.ShouldBindJSON(&user); err != nil {
    return
}

// ✅ 正确（Core 风格）
var user User
if err := c.ReadBody(&user); err != nil {
    c.ToJSON(nil, err)
    return
}
```

### 3. 响应返回：用 `ToJSON()` 不是 `JSON()`

```go
// ❌ 错误（手动处理错误）
user, err := service.GetUser()
if err != nil {
    c.JSON(gin.H{"error": err.Error()})
    return
}
c.JSON(gin.H{"data": user})

// ✅ 正确（Core 自动处理）
user, err := service.GetUser()
c.ToJSON(user, err)  // 自动格式化：{"data": user, "msg": "ok"}
```

## 📚 Core 特有方法详解

### 路径参数类型转换

Core 提供了多种类型的路径参数解析，AI 应根据业务场景选择：

```go
// 字符串参数（通用）
id := c.Params("id")

// UID 类型（推荐用于 ID）
uid, err := c.ParamsUid("userId")  // 返回 uid.UID 类型

// SID 类型（短ID，适合对外暴露）
sid, err := c.ParamsSid("code")    // 返回 sid.ID 类型

// XID 类型（分布式ID）
xid, err := c.ParamsXid("traceId") // 返回 xid.ID 类型

// 整数类型
id, err := c.ParamsInt("page")     // 返回 int 类型
```

### 表单/Query 参数类型转换

```go
// Query 参数（GET 请求）
name := c.Query("name", "default")
age := c.QueryInt("age", 18)

// 表单参数（POST 表单）
email := c.FormValue("email")

// 类型化表单值
userID := c.FromValueUid("user_id")      // 解析为 uid.UID
xid := c.FromValueXid("request_id")      // 解析为 xid.ID
sid := c.GetSid("session_id")            // 解析为 sid.ID
```

### 响应方法

```go
// 标准 JSON 响应（推荐）
c.ToJSON(data, err)
// 成功：{"data": {...}, "msg": "ok", "status": true}
// 失败：{"data": null, "msg": "error message", "status": false}

// 带自定义状态码
c.ToJSONCode(data, 200, "custom message")

// 纯 JSON（不包装）
c.JSON(map[string]any{"key": "value"})

// 字符串响应
c.SendString("Hello")

// 状态码响应
c.SendStatus(404, "Not Found")
```

## 🎯 常见场景模板

### 场景 1：标准 CRUD - 获取资源

```go
// GET /api/v1/users/:id
func (h *UserHandler) Get_id(c core.Ctx) {
    // 1. 获取并转换参数
    id, err := c.ParamsUid("id")
    if err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    // 2. 调用 service
    user, err := service.GetUserByID(id)
    
    // 3. 返回响应
    c.ToJSON(user, err)
}
```

### 场景 2：创建资源 - JSON Body

```go
// POST /api/v1/users
func (h *UserHandler) Post(c core.Ctx) {
    var req dto.CreateUserRequest
    
    // 1. 解析 JSON body
    if err := c.ReadBody(&req); err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    // 2. 验证
    if err := c.Validate(&req); err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    // 3. 业务逻辑
    user, err := service.CreateUser(&req)
    c.ToJSON(user, err)
}
```

### 场景 3：列表查询 - Query 参数

```go
// GET /api/v1/users
func (h *UserHandler) Get(c core.Ctx) {
    // 获取分页参数
    page := c.QueryInt("page", 1)
    size := c.QueryInt("size", 20)
    keyword := c.Query("keyword")
    
    // 业务逻辑
    users, total, err := service.GetUserList(page, size, keyword)
    
    c.ToJSON(core.Map{
        "list":  users,
        "total": total,
        "page":  page,
        "size":  size,
    }, err)
}
```

### 场景 4：文件上传

```go
// POST /api/v1/upload
func (h *UploadHandler) Post(c core.Ctx) {
    // 单文件上传
    relpath, abspath, err := c.SaveFile("file", "./uploads")
    if err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    c.ToJSON(map[string]string{
        "path": relpath,
    }, nil)
}
```

### 场景 5：请求级别变量存储

```go
// 中间件中存储用户信息
func AuthMiddleware(next core.HandlerFunc) core.HandlerFunc {
    return func(c core.Ctx) error {
        userID := validateToken(c.GetHeader("Authorization"))
        
        // 存储到上下文
        c.Set("user_id", userID)
        c.Locals("user", user)  // 另一种方式
        
        return next(c)
    }
}

// Handler 中获取
func (h *UserHandler) GetProfile(c core.Ctx) {
    // 获取存储的值
    userID := c.GetString("user_id")
    user := c.Locals("user")
    
    // 类型安全的获取
    uid := c.GetSid("user_id")
    
    c.ToJSON(user, nil)
}
```

## 🔧 方法对照表（AI 自查用）

| 操作 | ❌ 错误（Gin/其他） | ✅ 正确（Core） |
|-----|-------------------|----------------|
| 路径参数 | `c.Param("id")` | `c.Params("id")` |
| Query 参数 | `c.Query("name")` | `c.Query("name")` ✅ 相同 |
| Query 整数 | `c.DefaultQuery("age", 18)` | `c.QueryInt("age", 18)` |
| JSON 绑定 | `c.ShouldBindJSON(&user)` | `c.ReadBody(&user)` |
| 表单绑定 | `c.Bind(&form)` | `c.ReadBody(&form)` |
| 验证 | `c.ShouldBind(&form)` | `c.Validate(&form)` |
| 响应 JSON | `c.JSON(200, user)` | `c.ToJSON(user, err)` |
| 响应错误 | `c.JSON(400, gin.H{"error": err})` | `c.ToJSON(nil, err)` |
| 设置变量 | `c.Set("key", val)` | `c.Set("key", val)` ✅ 相同 |
| 获取变量 | `c.GetString("key")` | `c.GetString("key")` ✅ 相同 |
| 请求 ID | `c.GetHeader("X-Request-Id")` | `c.GetHeader("X-Request-Id")` ✅ 相同 |

## 🚫 禁止事项（AI 特别注意）

1. **禁止使用 `c.Param()`**（Gin 风格），必须用 `c.Params()`
2. **禁止使用 `c.ShouldBindJSON()`**，必须用 `c.ReadBody()`
3. **禁止手动处理错误响应**，使用 `c.ToJSON(data, err)` 统一处理
4. **禁止忽略 `ParamsUid` 等方法的错误返回**
5. **禁止在 goroutine 中直接使用 `c`**，需要先提取数据

## ✅ 正确示例总结

```go
// 完整的 Handler 示例
type OrderHandler struct {
    core.Handler
}

func (h *OrderHandler) Init() {
    h.Prefix("/api/v1/orders")
}

// GET /api/v1/orders/:id
func (h *OrderHandler) Get_id(c core.Ctx) {
    // 1. 解析参数（注意是 Params 不是 Param）
    id, err := c.ParamsSid("id")
    if err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    // 2. 调用 service
    order, err := service.GetOrder(id)
    
    // 3. 统一响应
    c.ToJSON(order, err)
}

// POST /api/v1/orders
func (h *OrderHandler) Post(c core.Ctx) {
    var req dto.CreateOrderRequest
    
    // 4. 解析 body（注意是 ReadBody）
    if err := c.ReadBody(&req); err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    // 5. 验证
    if err := c.Validate(&req); err != nil {
        c.ToJSON(nil, err)
        return
    }
    
    // 6. 业务逻辑
    order, err := service.CreateOrder(&req)
    c.ToJSON(order, err)
}
```