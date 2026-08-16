---
name: core-fetch
description: 使用 Core Framework fetch 子包调用外部 HTTP API，支持公共/单次 Header、Cookie、HTTP/SOCKS5 代理、请求签名 Hook、响应解包 Hook 和响应 Header 提取
tags: [go, core-framework, fetch, http-client, api, hook, header, cookie, proxy]
---

# Core Fetch API 客户端技能

## 触发条件

当用户请求以下内容时激活此 Skill：

- "调用第三方 API"
- "写 HTTP client"
- "请求前签名"
- "响应后 decode / 解包"
- "获取响应 header / X-Token"
- "公共 header 和单次 header"
- "启用 / 禁用 cookie"
- "HTTP / SOCKS5 代理"

## 1. 导入路径

`fetch` 是独立子包，不在根包 `core` 下直接暴露。

```go
import "github.com/xs23933/core/v3/fetch"
```

## 2. 快捷调用

包级函数使用 `fetch.Default`，适合简单 API 调用。

```go
type UserVO struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

var out UserVO

res, err := fetch.Get("https://api.example.com/users/1", &out)
if err != nil {
    return err
}

token := res.Header.Get("X-Token")
_ = token
```

POST / PUT 支持参数：

```go
_, err := fetch.Post("https://api.example.com/users", map[string]any{
    "name": "tom",
}, &out)

_, err = fetch.Put("https://api.example.com/users/1", map[string]any{
    "name": "jerry",
}, &out)

_, err = fetch.Delete("https://api.example.com/users/1", nil)
```

参数处理规则：

| 参数类型 | 处理方式 |
| -------- | -------- |
| `nil` | 不发送 body |
| `[]byte` | 原始 body |
| `string` | 字符串 body |
| 其它类型 | JSON body，并设置 `Content-Type: application/json; charset=utf-8` |

## 3. 可复用 Client

服务代码中优先创建可复用 client，统一配置 baseURL、公共 Header、Cookie 和 Hook。

```go
var api = fetch.New("https://api.example.com").
    Header("X-App", "core-service").
    Header("Accept", "application/json").
    UseCookie(true)

func GetUser(ctx context.Context, id string) (UserVO, error) {
    var out UserVO

    res, err := api.DoGet(ctx, "/users/"+id, &out)
    if err != nil {
        return out, err
    }

    refreshedToken := res.Header.Get("X-Token")
    _ = refreshedToken

    return out, nil
}
```

## 4. Header 规则

公共 Header：

```go
api := fetch.New("https://api.example.com").
    Header("X-App", "core-service")
```

单次 Header：

```go
var out UserVO

res, err := api.Post("/users").
    Header("X-Request-ID", "req-123").
    JSON(map[string]any{"name": "tom"}).
    Result(context.Background(), &out)
```

规则：

- `Fetch.Header/Headers` 设置公共 Header，每次请求都会带上。
- `FetchRequest.Header/Headers` 设置单次 Header，只影响当前请求。
- 单次 Header 覆盖同名公共 Header。

## 5. 请求前 Hook

`Before` 在请求发送前执行，可以修改 `*http.Request`，适合 hash 签名、鉴权、trace id、时间戳。

```go
api := fetch.New("https://api.example.com").
    Before(func(ctx context.Context, req *http.Request, body []byte) error {
        req.Header.Set("X-Sign", core.SHA256HashBytes(body))
        req.Header.Set("X-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
        return nil
    })

var out UserVO
_, err := api.DoPost(context.Background(), "/users", map[string]any{"name": "tom"}, &out)
```

## 6. 响应后 Hook

`After` 在响应 body 读取后执行，返回值会作为最终 body 继续 decode。适合解密、解压、统一响应 envelope 解包。

```go
type Envelope struct {
    Code int             `json:"code"`
    Msg  string          `json:"msg"`
    Data json.RawMessage `json:"data"`
}

api := fetch.New("https://api.example.com").
    After(func(ctx context.Context, resp *http.Response, body []byte) ([]byte, error) {
        var env Envelope
        if err := json.Unmarshal(body, &env); err != nil {
            return nil, err
        }
        if env.Code != 0 {
            return nil, fmt.Errorf("api error %d: %s", env.Code, env.Msg)
        }
        return env.Data, nil
    })
```

## 7. 响应 Header / Status / Body

需要读取 `X-Token`、状态码、原始 body 时使用 `Result` 或 `DoGet/DoPost/DoPut/DoDelete` 返回值。

```go
var out UserVO

res, err := api.Get("/session").Result(context.Background(), &out)
if err != nil {
    if ferr, ok := err.(*fetch.FetchError); ok {
        retryToken := ferr.Header.Get("X-Token")
        raw := string(ferr.Body)
        _ = retryToken
        _ = raw
    }
    return err
}

token := res.Header.Get("X-Token")
status := res.StatusCode
rawBody := res.Body
```

只需要 `FetchResult`，不需要 decode 到 `out` 时可以省略第二个参数：

```go
res, err := api.Get("/session").Result(context.Background())
if err != nil {
    return err
}

token := res.Header.Get("X-Token")
rawBody := res.Body
```

## 8. Debug 调试输出

排查 API 调用时可以显式开启 `Debug(true)`。请求结束时会打印方法、URL、最终请求 Header、请求 body、响应状态、响应 Header、响应 body 和错误信息。响应 body 如果是 `Content-Encoding: gzip` 会先解压再输出。

```go
api := fetch.New("https://api.example.com").
    Header("X-App", "core-service").
    Debug(true)

var out UserVO
_, err := api.Post("/users").
    Header("X-Request-ID", "req-123").
    JSON(map[string]any{"name": "tom"}).
    Result(context.Background(), &out)
```

调试日志会包含请求和响应 body，生产环境只应在定位问题时短期开启。

## 9. HTTP/SOCKS5 代理

`SetProxy` 支持 HTTP 和 SOCKS5 代理，账号密码会从代理 URL 中解析：

```go
api := fetch.New("https://api.example.com").
    SetProxy("http://127.0.0.1:7890")

api.SetProxy("socks5://127.0.0.1:1080")
api.SetProxy("http://user:password@127.0.0.1:7890")
api.SetProxy("socks5://user:password@127.0.0.1:1080")
```

传入空字符串会关闭代理：

```go
api.SetProxy("")
```

## 10. Cookie

默认不保存 Cookie。需要会话状态时显式开启：

```go
api := fetch.New("https://api.example.com").UseCookie(true)
```

禁用并清空 cookie jar：

```go
api.UseCookie(false)
```

## 11. 生成代码规则

推荐：

- 业务服务中创建一个可复用 `fetch.New(baseURL)` client。
- 公共鉴权、App 标识、Accept 等放在公共 Header。
- 每次请求独有的 request id、trace id 放在单次 Header。
- 需要 HTTP 或 SOCKS5 代理时使用 `SetProxy("http://...")` 或 `SetProxy("socks5://...")`，账号密码写在代理 URL 中。
- 签名逻辑放在 `Before`。
- 统一响应解包放在 `After`。
- 临时排查外部 API 问题时使用 `Debug(true)`，结束后关闭。
- 需要读取 `X-Token` 时使用 `Result` 或 `DoXxx` 返回的 `FetchResult`。

禁止：

- 在业务代码中到处散落 `http.NewRequest` / `http.Client.Do`。
- 每个 API 方法重复复制签名逻辑。
- 混用公共 Header 和单次 Header，导致 token/trace 泄漏到后续请求。
- 忽略 `FetchError` 中的 Header/Body。
