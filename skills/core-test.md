---
name: core-test
description: Core 框架测试编写，包括 Handler 测试、路由测试、中间件测试、Ctx 模拟
tags: [go, core-framework, testing, httptest]
---

# Core 测试编写

## 概述

Core 基于 `net/http` 标准接口，可直接用 `net/http/httptest` 测试，无需额外 mock 框架。

## 核心工具

- `core.New()` — 创建实例（无配置时用默认值）
- `httptest.NewRequest(method, url, body)` — 构造请求
- `httptest.NewRecorder()` — 捕获响应
- `app.ServeHTTP(rec, req)` — 执行请求

## Handler 测试

### 基本 GET 测试

```go
func TestGetUser(t *testing.T) {
    app := core.New()
    app.GET("/users/:id", func(c core.Ctx) error {
        return c.ToJSON(Map{"id": c.Params("id")}, nil)
    })

    req := httptest.NewRequest(http.MethodGet, "/users/123", nil)
    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status = %d, want 200", rec.Code)
    }
    // rec.Body.String() = {"id":"123","success":true}
}
```

### POST JSON 测试

```go
func TestCreateUser(t *testing.T) {
    app := core.New()
    app.POST("/users", func(c core.Ctx) error {
        var req struct {
            Name string `json:"name"`
        }
        if err := c.ReadBody(&req); err != nil {
            return err
        }
        return c.ToJSON(Map{"name": req.Name}, nil)
    })

    body := strings.NewReader(`{"name":"tom"}`)
    req := httptest.NewRequest(http.MethodPost, "/users", body)
    req.Header.Set("Content-Type", "application/json")

    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status = %d, want 200", rec.Code)
    }
}
```

### 带中间件测试

```go
func TestWithAuth(t *testing.T) {
    app := core.New()
    app.Use(func(c core.Ctx) error {
        if c.GetHeader("Authorization") == "" {
            return c.ToJSON(Map{"msg": "unauthorized"}, core.NewError(401, "unauthorized"))
        }
        return c.Next()
    })
    app.GET("/secret", func(c core.Ctx) error {
        return c.SendString("ok")
    })

    // 无 Token → 401
    req := httptest.NewRequest(http.MethodGet, "/secret", nil)
    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, req)
    if rec.Code != 401 {
        t.Fatalf("no token: status = %d, want 401", rec.Code)
    }

    // 有 Token → 200
    req = httptest.NewRequest(http.MethodGet, "/secret", nil)
    req.Header.Set("Authorization", "Bearer xxx")
    rec = httptest.NewRecorder()
    app.ServeHTTP(rec, req)
    if rec.Code != 200 {
        t.Fatalf("with token: status = %d, want 200", rec.Code)
    }
}
```

## 路由测试

### 路由注册 + 移除

```go
func TestRemoveHandle(t *testing.T) {
    app := core.New()
    app.GET("/gateway/:id", func(c core.Ctx) error {
        return c.SendString("old")
    })

    // 先验证存在
    req := httptest.NewRequest(http.MethodGet, "/gateway/1", nil)
    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, req)
    if rec.Body.String() != "old" {
        t.Fatalf("before remove: %q", rec.Body.String())
    }

    // 移除路由
    app.RemoveHandle([]string{http.MethodGet}, "/gateway/:id")

    // 验证已移除 → 404
    rec = httptest.NewRecorder()
    app.ServeHTTP(rec, req)
    if rec.Code != http.StatusNotFound {
        t.Fatalf("after remove: status = %d", rec.Code)
    }
}
```

### ALL 方法路由

```go
func TestAllMethodRoute(t *testing.T) {
    app := core.New()
    app.ALL("/ping", func(c core.Ctx) error {
        return c.SendString(c.Method())
    })

    for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
        req := httptest.NewRequest(method, "/ping", nil)
        rec := httptest.NewRecorder()
        app.ServeHTTP(rec, req)
        if rec.Body.String() != method {
            t.Fatalf("%s: got %q", method, rec.Body.String())
        }
    }
}
```

## Ctx 模拟

### AcquireCtx 直接构造

```go
func TestRemoteIP(t *testing.T) {
    app := core.New()

    ctx := app.AcquireCtx(nil, &http.Request{
        Header: http.Header{
            "X-Real-Ip":       []string{"192.168.1.2"},
            "X-Forwarded-For": []string{"127.0.0.1"},
        },
    })

    ip := ctx.RemoteIP()
    if !ip.Equal(net.ParseIP("192.168.1.2")) {
        t.Fatalf("remote ip = %v, want 192.168.1.2", ip)
    }

    app.ReleaseCtx(ctx)
}
```

### 文件上传测试

```go
func TestFileUpload(t *testing.T) {
    app := core.New()
    app.POST("/upload", func(c core.Ctx) error {
        rel, abs, err := c.SaveFile("file", "/uploads", t.TempDir())
        if err != nil {
            return err
        }
        return c.ToJSON(Map{"path": rel, "abs": abs}, nil)
    })

    // 构造 multipart 表单
    var buf bytes.Buffer
    writer := multipart.NewWriter(&buf)
    part, _ := writer.CreateFormFile("file", "test.txt")
    part.Write([]byte("hello"))
    writer.Close()

    req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
    req.Header.Set("Content-Type", writer.FormDataContentType())

    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, req)

    if rec.Code != 200 {
        t.Fatalf("status = %d, want 200", rec.Code)
    }
}
```

## 表驱动测试

```go
func TestParams(t *testing.T) {
    app := core.New()
    app.GET("/items/:id/comments/:cid", func(c core.Ctx) error {
        return c.SendString(c.Params("id") + ":" + c.Params("cid"))
    })

    tests := []struct {
        url  string
        want string
    }{
        {"/items/1/comments/2", "1:2"},
        {"/items/abc/comments/xyz", "abc:xyz"},
    }

    for _, tt := range tests {
        req := httptest.NewRequest(http.MethodGet, tt.url, nil)
        rec := httptest.NewRecorder()
        app.ServeHTTP(rec, req)
        if rec.Body.String() != tt.want {
            t.Errorf("%s: got %q, want %q", tt.url, rec.Body.String(), tt.want)
        }
    }
}
```

## 验证器测试

```go
func TestValidate(t *testing.T) {
    app := core.New()
    app.POST("/users", func(c core.Ctx) error {
        var req struct {
            Name  string `validate:"required"`
            Email string `validate:"required,email"`
        }
        if err := c.ReadBody(&req); err != nil {
            return err
        }
        if err := c.Validate(&req); err != nil {
            return c.ToJSON(nil, core.NewError(400, err.Error()))
        }
        return c.ToJSON(Map{"name": req.Name}, nil)
    })

    // 缺少必填字段 → 400
    body := strings.NewReader(`{"name":""}`)
    req := httptest.NewRequest(http.MethodPost, "/users", body)
    req.Header.Set("Content-Type", "application/json")
    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, req)
    if rec.Code != 400 {
        t.Fatalf("validation should fail: status = %d", rec.Code)
    }
}
```

## 中间件测试

```go
func TestCORSMiddleware(t *testing.T) {
    app := core.New()
    app.Use(cors.New())

    app.GET("/api/data", func(c core.Ctx) error {
        return c.SendString("ok")
    })

    // OPTIONS 预检
    req := httptest.NewRequest(http.MethodOptions, "/api/data", nil)
    req.Header.Set("Origin", "https://example.com")
    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, req)

    if rec.Header().Get("Access-Control-Allow-Origin") == "" {
        t.Fatal("CORS header missing")
    }
}
```

## 测试技巧

1. **用 `t.TempDir()`** 做文件上传测试的根目录，测试结束自动清理
2. **用 `app.ReleaseCtx(ctx)`** 归还 Ctx 到对象池，避免泄漏
3. **`core.New()` 无参数** 时用默认配置（debug=true, listen=8080），适合单元测试
4. **`httptest.NewRecorder()`** 的 `rec.Code`/`rec.Body`/`rec.Header()` 直接断言响应
5. **JSON 响应断言**可用 `sonic.Unmarshal(rec.Body.Bytes(), &result)` 解析
6. **大文件测试**用 `io.LimitReader` 控制大小，避免内存溢出
7. **并发测试**用 `t.Run` 并行 + `sync.WaitGroup`，注意 `app` 不要并发写路由
