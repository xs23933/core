---
name: core-ctx
description: HTTP Ctx 参数解析、请求体校验、JSON/状态响应及请求生命周期
---

# HTTP Ctx

## 适用任务

读取路径/query/body、校验输入、返回 HTTP 响应或传播请求 context；自动路由配合 [Handler](core-handler.md)。

## 必须遵守

| 操作 | Core API | 完成条件 |
| --- | --- | --- |
| 路径 | c.Params("id") | 按业务规则验证格式 |
| 数字路径 | c.ParamsInt("id") | 检查返回的 error |
| query | c.Query("q", "") | 严格整数用 strconv.Atoi 并检查错误 |
| body | c.ReadBody(&req) | 错误时返回 400，不继续业务 |
| 校验 | c.Validate(&req) | 使用 validate 标签，检查 error |
| JSON | return c.ToJSON(data, err) | 业务错误先设置 HTTP 状态并脱敏 |
| 状态 | c.Status(http.StatusBadRequest) | 在写出响应之前调用 |
| context | c.Ctx() 或 c.Context() | 传给 Service 与下游操作 |

ToJSON 不从 error 自动推导 HTTP 状态。Ctx 只在当前请求有效，不能保留给 goroutine；
先提取值并选择符合异步任务生命周期的 context。StdContext 属于 GRPCCtx，不是 HTTP Ctx。

## 最小示例

以下为 Handler 方法片段；完整 imports/装配见 [标准 Handler](../example/standard/internal/handler/user.go)。

```go
func (h *UserHandler) Post(c core.Ctx) error {
    var req struct {
        Name string `json:"name" validate:"required"`
    }
    if err := c.ReadBody(&req); err != nil {
        c.Status(400)
        return c.ToJSON(nil, core.NewError(400, "invalid request"))
    }
    if err := c.Validate(&req); err != nil {
        c.Status(400)
        return c.ToJSON(nil, core.NewError(400, "invalid name"))
    }
    // 此片段仅演示解析与校验；实际创建需调用 Service 并映射业务错误。
    return c.ToJSON(req.Name, nil)
}
```

## 易错边界

QueryInt 的默认值不能替代严格格式校验。写出响应后无法再改状态；写出后返回错误不会追加错误文本。
超时是协作式取消，需要下游响应 context；异步保存 Ctx 会越过复用边界。
文件上传、cookie、writer 包装、超时细节见 [完整 Ctx 参考](core-ctx-reference.md)。

## 验证与深入

检查非法 JSON、缺必填字段、非法数字、状态码与取消传播。实现见 [ctx.go](../ctx.go)，
集成样例见 [HTTP 测试](../example/standard/internal/app/http_test.go)。
