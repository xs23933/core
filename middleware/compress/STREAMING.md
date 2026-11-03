# Brotli、SSE 与 WebSocket 压缩验证

日期：2026-11-03。记录本次发布前的本地实现与验证结果。

## 使用

```go
import "github.com/xs23933/core/v3/middleware/compress"

// 统一协商：q 优先，同 q 顺序 zstd → br → gzip。
app.Use(compress.New(compress.Config{EnableSSE: true}))
```

独立入口为 `middleware/brotli.New()`、`middleware/gzip.New()`、`middleware/zstd.New()`，Config 均有 EnableSSE。默认 false，保持既有 SSE 不压缩；Brotli 编码名为 br。BrotliLevel/独立 Level 的0使用质量6，显式1～11。复用已经存在的 andybalholm/brotli v1.2.1，仅由间接依赖改为直接依赖。

SSE 写出前设置 Content-Type: text/event-stream 和 Cache-Control: no-cache。开启后不等待1024字节阈值，首次 Flush/DoWriteHeader 也先建立编码器。每个 SSEWrite/SSESend/SSEComment 会刷新压缩器和 HTTP 输出；仍尊重 no-transform、Range、已有编码、无正文状态和空 ContentTypes。不要同时设置 no-transform 来期望启用源站压缩。普通 HTTP 流仍使用原有阈值/提前Flush规则。

WebSocket 按用户确认采用标准浏览器兼容方案：

```go
import ws "github.com/xs23933/core/v3/websocket"

socket := ws.New().WithOptions(ws.Options{
    EnableCompression: true,
    ReadLimit: 1 << 20,
})
// 继续配置原有 OnMessage/OnClose，并使用 socket.Handler()。
```

WebSocket 是 RFC7692 permessage-deflate，不能称为 gzip/br/zstd HTTP 编码。默认不开启，只有双方扩展协商成功才压缩；客户端收到原始消息，Text/Binary 类型不变。分段写入后必须结束消息，浏览器按完整消息触发事件；实时更新应逐条发送，而不是一个无限大消息。解压后的完整消息继续受 ReadLimit 限制。统一 compress 对 Upgrade/CONNECT 直接放行，不污染握手，也不会因 HTTP identity/q 条件拒绝升级。

三个 HTTP 入口复用 internal/responsecompression 的 writer、前缀缓冲、编码器池、错误处理与 Reset/Close 生命周期，不再复制三份响应包装实现。默认关闭SSE时提前返回，避免额外MIME解析。

## 验证

实际执行通过：

```sh
GOCACHE=/private/tmp/core-gocache go test ./... -count=1
GOCACHE=/private/tmp/core-gocache go test ./middleware/compress ./middleware/brotli ./middleware/gzip ./middleware/zstd ./middleware/etag ./websocket -race -count=1
GOCACHE=/private/tmp/core-gocache go vet ./...
GOCACHE=/private/tmp/core-gocache go build ./...
go mod verify
GOCACHE=/private/tmp/core-gocache go mod tidy -diff
git diff --check
```

最初 gzip SSE 真实 HTTP 回归失败：编码为空，首次 Flush 关闭了压缩选择。修复后，该回归合并到多算法矩阵，覆盖独立/统一入口、首次直接Write/Flush/DoWriteHeader、两种ETag顺序（共36个子案例）。客户端在不允许 handler 结束的情况下逐条读出两条解压事件，证明不依赖 Close 产生消息。不是仅验证最终缓冲区能解压。

其它覆盖：默认 SSE 不压缩、no-transform、空 MIME 列表、br q/通配符/重复/非法值、静态缓存/Range/HEAD/304、编码写入/Close错误、Brotli接受但未写出正文时的handler错误、原有gzip/zstd兼容测试；WebSocket双方压缩开关组合、未协商回退、HTTP q禁止不干扰握手、文本与二进制消息分段写入echo、解压后超限拒绝且确实断连（不能以读超时当通过）。

## 性能方法

环境 darwin/arm64、Apple M5 Pro、Go1.26.3。三次500ms样本，以下各项取中位数。热编码器、默认级别；预生成正文，计时阶段写入计数输出器，排除网络、磁盘和响应缓冲复制。计时前真实编码并解压校验，计时中确认完整编码正文长度。B/op 不包含池中编码器的常驻内存，sync.Pool回收会影响测量。不得把此处MB/s当成网络下载速度。

```sh
GOCACHE=/private/tmp/core-gocache go test ./middleware/compress -run '^$' -bench 'Benchmark(Compression|SSECompression)$' -benchmem -benchtime=500ms -count=3
```

普通正文是132003字节的生成JavaScript样本（2000条不同ID/title记录）：

| 入口 | µs/op | 正文输出字节 | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| gzip-direct | 605.529 | 16337 | 336 | 9 |
| gzip-unified | 582.200 | 16337 | 400 | 11 |
| br-direct | 992.432 | 6078 | 346 | 9 |
| br-unified | 949.634 | 6078 | 578 | 11 |
| zstd-direct | 130.239 | 7471 | 352 | 11 |
| zstd-unified | 128.792 | 7471 | 449 | 14 |
| identity-direct | 0.131 | 132003 | 16 | 1 |
| identity-unified | 0.246 | 132003 | 80 | 3 |

此样本br质量6输出6078字节，比gzip的16337字节少约62.8%，比zstd的7471字节少约18.6%；br编码CPU也更高，不代表所有数据都应选br。单算法/统一入口的小幅速度反转属于运行波动，不能解释为统一入口让编码器更快。默认等级不同也是算法对比的一部分，并非相同CPU预算对比。

SSE 每流32个不同sequence的小JSON事件，原文共2902字节；初始刷新头，每事件都Flush，最后Close。数值是每条完整流（32事件）而非单事件延迟：

| 编码 | µs/stream | 正文输出字节 | B/stream | allocs/stream |
| --- | ---: | ---: | ---: | ---: |
| br | 149.766 | 427 | 448 | 12 |
| gzip | 57.333 | 466 | 448 | 12 |
| zstd | 52.769 | 493 | 464 | 14 |

此样本br相对gzip节约约8.4%正文，但每流压缩CPU更高；频繁Flush会牺牲压缩率。Header/协议帧不计入正文长度。本测试不测浏览器收到事件的延迟、并发吞吐、WebSocket压缩比或Cloudflare链路，不能承诺CF不缓冲。

## 独立评审

reviewer 初审/复审通过，无必须修正问题。已落实默认关闭SSE时的快速返回与首次DoWriteHeader覆盖建议；reviewer独立重跑相关包、SSE/WebSocket race专项与diff check通过。旧v3.1.68性能报告保留为历史资料，当前能力以本文为准。

## 原始测量数据

```text
BenchmarkCompression/gzip-direct run=1: 593789 ns/op, 16337 body bytes, 336 B/op, 9 allocs/op
BenchmarkCompression/gzip-direct run=2: 605529 ns/op, 16337 body bytes, 336 B/op, 9 allocs/op
BenchmarkCompression/gzip-direct run=3: 609081 ns/op, 16337 body bytes, 336 B/op, 9 allocs/op
BenchmarkCompression/gzip-unified run=1: 583570 ns/op, 16337 body bytes, 400 B/op, 11 allocs/op
BenchmarkCompression/gzip-unified run=2: 569160 ns/op, 16337 body bytes, 400 B/op, 11 allocs/op
BenchmarkCompression/gzip-unified run=3: 582200 ns/op, 16337 body bytes, 400 B/op, 11 allocs/op
BenchmarkCompression/br-direct run=1: 1.00168e+06 ns/op, 6078 body bytes, 507 B/op, 9 allocs/op
BenchmarkCompression/br-direct run=2: 970838 ns/op, 6078 body bytes, 346 B/op, 9 allocs/op
BenchmarkCompression/br-direct run=3: 992432 ns/op, 6078 body bytes, 337 B/op, 9 allocs/op
BenchmarkCompression/br-unified run=1: 949634 ns/op, 6078 body bytes, 589 B/op, 11 allocs/op
BenchmarkCompression/br-unified run=2: 942945 ns/op, 6078 body bytes, 578 B/op, 11 allocs/op
BenchmarkCompression/br-unified run=3: 976870 ns/op, 6078 body bytes, 426 B/op, 11 allocs/op
BenchmarkCompression/zstd-direct run=1: 151632 ns/op, 7471 body bytes, 352 B/op, 11 allocs/op
BenchmarkCompression/zstd-direct run=2: 130239 ns/op, 7471 body bytes, 352 B/op, 11 allocs/op
BenchmarkCompression/zstd-direct run=3: 128206 ns/op, 7471 body bytes, 353 B/op, 11 allocs/op
BenchmarkCompression/zstd-unified run=1: 126509 ns/op, 7471 body bytes, 449 B/op, 14 allocs/op
BenchmarkCompression/zstd-unified run=2: 128792 ns/op, 7471 body bytes, 449 B/op, 14 allocs/op
BenchmarkCompression/zstd-unified run=3: 130761 ns/op, 7471 body bytes, 449 B/op, 14 allocs/op
BenchmarkCompression/identity-direct run=1: 131.4 ns/op, 132003 body bytes, 16 B/op, 1 allocs/op
BenchmarkCompression/identity-direct run=2: 132.1 ns/op, 132003 body bytes, 16 B/op, 1 allocs/op
BenchmarkCompression/identity-direct run=3: 131.3 ns/op, 132003 body bytes, 16 B/op, 1 allocs/op
BenchmarkCompression/identity-unified run=1: 243.5 ns/op, 132003 body bytes, 80 B/op, 3 allocs/op
BenchmarkCompression/identity-unified run=2: 246 ns/op, 132003 body bytes, 80 B/op, 3 allocs/op
BenchmarkCompression/identity-unified run=3: 247.7 ns/op, 132003 body bytes, 80 B/op, 3 allocs/op
BenchmarkSSECompression/br run=1: 149636 ns/op, 427 body bytes, 448 B/op, 12 allocs/op
BenchmarkSSECompression/br run=2: 150322 ns/op, 427 body bytes, 451 B/op, 12 allocs/op
BenchmarkSSECompression/br run=3: 149766 ns/op, 427 body bytes, 448 B/op, 12 allocs/op
BenchmarkSSECompression/gzip run=1: 56790 ns/op, 466 body bytes, 530 B/op, 12 allocs/op
BenchmarkSSECompression/gzip run=2: 57333 ns/op, 466 body bytes, 448 B/op, 12 allocs/op
BenchmarkSSECompression/gzip run=3: 57635 ns/op, 466 body bytes, 448 B/op, 12 allocs/op
BenchmarkSSECompression/zstd run=1: 52769 ns/op, 493 body bytes, 464 B/op, 14 allocs/op
BenchmarkSSECompression/zstd run=2: 52543 ns/op, 493 body bytes, 2319 B/op, 14 allocs/op
BenchmarkSSECompression/zstd run=3: 53140 ns/op, 493 body bytes, 464 B/op, 14 allocs/op
```
