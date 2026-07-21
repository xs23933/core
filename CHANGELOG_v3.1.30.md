# Core Framework v3.1.30 更新说明

## 概述

v3.1.30 是一个重大功能更新，主要引入了 **SSE（Server-Sent Events）服务端推送** 和 **压缩基数树路由** 两大核心能力，同时新增 6 个内置中间件，并修复了路由中间件重复执行的关键 Bug。

---

## 新功能

### SSE 服务端推送

Ctx 层新增三个 SSE 核心方法，EventHub 封装完整的连接管理和广播能力。

**Ctx 方法：**
- `c.SSEWrite(event, data, id...)` — 写入底层 SSE 事件，支持 `id`/`event`/`data` 字段和多行数据自动格式化
- `c.SSESend(event, data, id...)` — 发送结构化 SSE 事件，自动 JSON 序列化
- `c.SSEComment(comment)` — 发送 SSE 注释（心跳保活）

**EventHub 新特性：**
- `EventHub.Get()` 重写为统一 Stream 循环，合并之前双 Stream 调用的设计
- 连接建立自动发送 `connected` 确认事件
- 15 秒心跳注释，自动设置 `X-Accel-Buffering: no` 禁用 Nginx 缓冲
- `EventData.ID` 字段输出支持（客户端断线重连时发送 `Last-Event-ID`）
- 多行 data 按 SSE 规范每行添加 `data: ` 前缀

```go
hub := core.NewEventHub()
defer hub.Close()
app.GET("/events", hub.Get)
app.POST("/events/push", hub.PostData)
```

### 压缩基数树路由

RouteNode 重写为压缩基数树（Compressed Radix Tree），实现路径压缩和高效前缀匹配。

**新特性：**
- 节点 `path` 字段可跨多个路径段（如 `"api/v1/users/"`）
- 静态子节点使用 `indices` 字节索引 + `children` 切片，按字典序二分查找
- 支持 `:param`（参数）、`:param?`（可选参数）、`*wildcard`（通配符）三种动态节点
- `addRoute()` 支持路径前缀分裂（split），自动合并公共前缀
- 参数提取使用按需分配的 map，减少不必要的内存分配

**与旧版对比：**
| 特性 | v3.1.29（旧） | v3.1.30（新） |
| ---- | ----------- | ----------- |
| 数据结构 | 逐段 trie（每段一个节点） | 压缩基数树（跨段前缀） |
| 静态子节点查找 | map[string]*RouteNode | indices 二分查找 |
| 路径压缩 | 不支持 | 支持（O(k) 前缀匹配） |
| 内存布局 | 每个 segment 独立 node | 合并连续静态段 |

### 新增中间件

| 中间件 | 包路径 | 功能 |
| ------ | ------ | ---- |
| BodyLimit | `middleware/bodylimit` | 限制请求体大小 |
| ETag | `middleware/etag` | 自动生成 ETag，支持 304 Not Modified |
| Monitor | `middleware/monitor` | 性能监控面板 |
| OpenTelemetry | `middleware/otel` | OpenTelemetry 链路追踪集成 |
| Security | `middleware/security` | 安全响应头（CSP/X-Frame-Options 等） |
| Timeout | `middleware/timeout` | 请求超时控制 |

### 其他新增
- Gateway 文档（`skills/core-gateway.md`）
- `Model.BeforeDelete` 生命周期钩子

---

## Bug 修复

### 路由中间件重复执行（严重）

**问题**：`route_node.go` 中 `match()` 和 `matchPath()` 各自追加了一次 `root.middlewares`，导致全局中间件（Logger、Recovery、metrics、ratelimit 等）在 handler chain 中出现两次，每个请求被处理两遍。

**影响**：
- 请求日志重复输出
- Metrics 计数器双倍计数
- RateLimit 实际限流阈值减半（Max=2 时仅允许 1 次请求）
- 所有全局中间件产生双倍开销

**修复**：删除 `match()` 中多余的 `append(chain, n.middlewares...)`，统一由 `matchPath()` 处理。

### EventData 格式化修复
- `EventData.String()` 补全 `ID` 字段输出
- 多行 data 按 SSE 规范每行添加 `data: ` 前缀
- `ToString()` 新增 `uint`/`int8~64`/`float32/64` 类型支持

---

## 测试

### 新增测试
- **SSE 测试**（23 个）：覆盖 SSEWrite/SSESend/SSEComment 格式、EventData 序列化、EventHub 连接/广播/定向推送/断开/心跳/多事件
- **路由边缘测试**（19 个）：单/多参数、特殊字符、方法分离、静态优先、通配符、深层嵌套、中文
- **路由基准测试**（9 个）：静态路由、深度压缩、参数路由、通配符、混合路由表、分配次数
- **路由压力测试**：大规模路由表（1000 条）性能验证
- **并发安全测试**：Model、Utils、WebSocket Conn
- **中间件测试**：Metrics 压力测试、RateLimit 并发/滑动窗口

---

## 文档

- `skills/core-sse.md`：SSE 完整使用文档（Ctx 方法、EventHub、手动流式处理、注意事项）
- `skills/core-gateway.md`：Gateway 网关文档
- `skills/core-middleware.md`：新增中间件文档
- `README.md`：新增 SSE 章节（使用示例、前端连接、编程式推送）
- `AI_CONTEXT.md`：SSE 加入长连接规范 + Ctx 快速参考
- `AGENTS.md`：SSE 加入 Skill 表

---

## 兼容性

- **向后兼容**：所有公开 API 保持不变，空配置下行为一致
- **中间件行为变更**：修复了中间件重复执行 Bug，之前依赖该隐形重复行为的代码可能需要调整（如限流阈值可能需要从实际需求的 2 倍调回正常值）

---

## 贡献者

- 路由压缩基数树实现
- SSE 全套功能设计与实现
- 6 个新中间件开发
- 全面测试覆盖与文档同步
