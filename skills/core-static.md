---
name: core-static
description: Core Framework 静态文件服务与可选浏览器/CDN 长缓存，含 HTML 重新验证及 immutable 使用边界
tags: [go, core-framework, static, cache-control, immutable]
---

# 静态文件与长缓存

## 触发条件

- Static、StaticFS、StaticFile、StaticFileFS
- 静态文件缓存时间、Cache-Control、immutable、前端构建资源缓存

## API

```go
app.Static(relativePath, root string, config ...core.StaticConfig)
app.StaticFS(relativePath string, fs http.FileSystem, config ...core.StaticConfig)
app.StaticFile(relativePath, filename string, config ...core.StaticConfig)
app.StaticFileFS(relativePath, filename string, fs http.FileSystem, config ...core.StaticConfig)
```

每个方法返回 `core.Router`；原有不传配置的调用保持原来的缓存行为。可选配置只读取第一项，不要传入多个配置。`Static` 禁止目录列表，`StaticFS` 是否允许目录列表由传入的文件系统决定。静态目录只服务 GET/HEAD。

```go
// 启用默认缓存：7 天；HTML/index 页面 no-cache。
app.Static("/static", "./static", core.StaticConfig{})

// 仅用于内容改变就更换 URL 的构建产物。
app.Static("/assets", "./dist/assets", core.StaticConfig{
    MaxAge: 365 * 24 * time.Hour,
    Immutable: true,
})
app.StaticFile("/", "./dist/index.html", core.StaticConfig{})
```

示例中的路径属于框架静态资源注册 API，不是业务 Handler 手动路由模板。

## 缓存规则

静态处理器结束时会提交无正文响应状态，不依赖可选 StaticConfig 或 gzip；HEAD 不发送正文，目录跳转保持 301，条件请求保持 304。不传 StaticConfig 时仍不添加 Cache-Control。默认状态修复自 v3.1.62 提供；回归命令为 `go test . -run TestStaticDefaultStatuses -count=1`。

| 情况 | 框架添加的 Cache-Control |
| --- | --- |
| 不传配置 | 不添加 |
| `StaticConfig{}`，普通成功资源 | `public, max-age=604800` |
| `MaxAge: 365 * 24 * time.Hour, Immutable: true` | `public, max-age=31536000, immutable` |
| HTML/HTM 或目录默认 index 页面 | `no-cache` |
| MaxAge 为负数或小于一秒的正数 | `no-cache` |
| 已有显式 Cache-Control | 保留已有值 |
| 重定向、4xx/5xx | 不添加 |

`MaxAge` 是 `time.Duration`，不是秒数整数；0 使用 7 天默认值，其它正数按整秒输出。HTML 识别依据响应 MIME 类型、单文件实际文件名和目录 index 路径。`no-cache` 允许存储，但每次使用前必须重新验证。

缓存规则只作用于显式配置的静态挂载和静态单文件，不能按后缀给动态 API 添加 public 缓存。若静态文件受权限保护，应通过已有中间件显式设置 `private` / `no-store`（优先于 StaticConfig），或不要启用 public 缓存。

文件的 Last-Modified、If-Modified-Since、Range 继续由标准库处理。200/206/304 采用同一缓存策略；不存在的文件不获得长缓存。HTML 不自动使用 immutable；普通资源也仅在显式 `Immutable: true` 时开启，框架不猜测 hash 文件名。

可与 [gzip 中间件](core-middleware.md) 组合。图片可以长缓存，PNG/WebP/JPEG 默认不参与 gzip。需要包装响应 writer 的中间件应在写出前安装，并在退出时恢复。

## 验证

```sh
GOCACHE=/private/tmp/core-gocache go test . -run TestStaticCache -count=1
GOCACHE=/private/tmp/core-gocache go test ./middleware/gzip -count=1
```
