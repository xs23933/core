# 统一响应压缩验证与性能对比

> v3.1.68 双算法入口的历史报告；Brotli、SSE 与 WebSocket 新增能力及当前测量见 [流式压缩报告](STREAMING.md)。

日期：2026-11-02。入口：`github.com/xs23933/core/v3/middleware/compress`。

```go
import "github.com/xs23933/core/v3/middleware/compress"

app.Use(compress.New())
```

以统一入口替代独立 gzip/zstd 安装即可；旧入口及其行为没有修改，无新增依赖。新入口复用两个独立 writer、流式压缩、池和退出清理，只负责协商与原文拒绝边界。无预压缩文件缓存、默认静态缓存调整或编码开始后的算法切换。

## 协商合同

- 合并多行 Accept-Encoding，以正 q 权重最高者为准，平局 zstd 优先；显式编码覆盖 `*`。
- q 按 RFC 9110 的0～1、至多三位小数解析，非法参数禁用该编码；重复同名编码取最低权重，以免 q=0 被另一条声明覆盖。
- 显式 identity 参加权重比较（未写 q 时为1），隐式 identity 只作兜底；缺失/空头保持原文，不改下游看到的请求头。
- 没有共同编码且 identity 禁止时，提前 no-store 406、不执行 handler；入口已存在非 identity 编码时透传，由提供者负责该编码的协商。
- 下游才产生的 br 等预编码无法被提前发现：若请求不接受 gzip/zstd 且禁止 identity，该路径应 Skip 或提前协商并设置编码头。不能宣称所有下游预编码都能透明透传。
- 已选编码若因 MIME、阈值、Range、no-transform、提前 Flush 等不能使用，且 identity 被禁止，在发送原文前替换为406；此时 handler 可能已执行，406不回滚业务副作用。
- 406 后向写入/复制的生产者返回内部拒绝错误停止读写，统一边界消化该策略拒绝；真实写入、Flush、Close 或并列错误继续向上传递。忽略写错误的业务代码仍需自行停止。
- HEAD、204/304 等无正文状态保留；Skip完全绕过协商。已有 Content-Encoding 不重复压缩。默认 MIME、1024字节阈值、缓存、Vary、ReaderFrom 与两算法的默认级别不变。

规范参考：[RFC 9110 Accept-Encoding](https://www.rfc-editor.org/rfc/rfc9110.html#section-12.5.3)。

## 回归验证

实际执行并通过：

```sh
GOCACHE=/private/tmp/core-gocache go test ./... -count=1
GOCACHE=/private/tmp/core-gocache go test ./middleware/compress ./middleware/gzip ./middleware/zstd ./middleware/etag -count=1
GOCACHE=/private/tmp/core-gocache go test ./middleware/compress ./middleware/gzip ./middleware/zstd ./middleware/etag -race -count=1
git diff --check
```

覆盖 q 权重/通配符/多行/重复/非法值、真实 gzip/zstd 解码、配置验证、Skip、图片/SSE/no-transform/已有编码、小正文、提前 Flush、Range/HEAD/304、静态缓存、ETag 两种安装顺序的 If-None-Match、原始请求头、空源与错误、并发、静态快速复制保留、406停止消费源及真实错误保留。

## 性能方法

环境：darwin/arm64，Apple M5 Pro，Go 1.26.3。每组3次、每次500ms，表格为各项中位数；热编码器、默认级别、单请求顺序执行。输入为基准程序生成的2000条不同ID/title的JavaScript记录，共132003字节（约129KiB）。不是广泛资源语料，也不推断图片、随机数据或所有站点的压缩率。

对比旧单算法入口与新统一入口；共享同一正文、响应路径和编码器设置。先用真实缓冲响应完成解码校验，计时中使用预生成 byte 正文和计数输出器，排除字符串转换和 httptest 正文缓冲复制。输出器仍接收编码器完整输出，`wire-B/op` 指响应正文大小（不含HTTP头/协议帧）。未测网络、磁盘、TLS、HTTP2、生产吞吐或编码器峰值常驻内存，不能从本基准的 MB/s 推断下载速度。

```sh
GOCACHE=/private/tmp/core-gocache go test ./middleware/compress -run '^$' -bench 'Benchmark(Negotiation|Compression)$' -benchmem -benchtime=500ms -count=3
```

| 入口 | µs/op | 正文输出字节 | 分配 B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| gzip-direct | 637.962 | 16337 | 304 | 9 |
| gzip-unified | 671.603 | 16337 | 368 | 11 |
| zstd-direct | 136.488 | 7471 | 320 | 11 |
| zstd-unified | 138.400 | 7471 | 417 | 14 |
| identity-direct | 0.137 | 132003 | 16 | 1 |
| identity-unified | 0.242 | 132003 | 80 | 3 |

本样本 zstd 输出7471字节，相对 gzip 的16337字节再减少约54.3%；统一入口 zstd 的压缩耗时中位数约138.4µs，gzip约671.6µs。它们是本样本的压缩CPU差异，不是网络收益承诺。相对各自单算法入口，本轮统一gzip约增加5.3%耗时、64B/2次分配；统一zstd约增加1.4%耗时、约97B/3次分配。identity路径约增加105ns、64B/2次分配。机器调度、GC和sync.Pool会造成波动，这些百分比不能当成稳定开销上限。

独立协商的典型头中位数约44～108ns，0分配；缺省/空头约13ns。该解析成本不包含后续独立中间件为兼容旧API而进行的接受检测，完整入口开销以上表为准。

### 最终原始数据

```text
gzip-direct run=1: 620598 ns/op, 16337 body-B/op, 304 B/op, 9 allocs/op
gzip-direct run=2: 731897 ns/op, 16337 body-B/op, 304 B/op, 9 allocs/op
gzip-direct run=3: 637962 ns/op, 16337 body-B/op, 304 B/op, 9 allocs/op
gzip-unified run=1: 654863 ns/op, 16337 body-B/op, 368 B/op, 11 allocs/op
gzip-unified run=2: 682065 ns/op, 16337 body-B/op, 368 B/op, 11 allocs/op
gzip-unified run=3: 671603 ns/op, 16337 body-B/op, 368 B/op, 11 allocs/op
zstd-direct run=1: 136488 ns/op, 7471 body-B/op, 320 B/op, 11 allocs/op
zstd-direct run=2: 135302 ns/op, 7471 body-B/op, 320 B/op, 11 allocs/op
zstd-direct run=3: 138235 ns/op, 7471 body-B/op, 320 B/op, 11 allocs/op
zstd-unified run=1: 139557 ns/op, 7471 body-B/op, 418 B/op, 14 allocs/op
zstd-unified run=2: 138400 ns/op, 7471 body-B/op, 417 B/op, 14 allocs/op
zstd-unified run=3: 138018 ns/op, 7471 body-B/op, 417 B/op, 14 allocs/op
identity-direct run=1: 137.1 ns/op, 132003 body-B/op, 16 B/op, 1 allocs/op
identity-direct run=2: 137.2 ns/op, 132003 body-B/op, 16 B/op, 1 allocs/op
identity-direct run=3: 134.5 ns/op, 132003 body-B/op, 16 B/op, 1 allocs/op
identity-unified run=1: 261.9 ns/op, 132003 body-B/op, 80 B/op, 3 allocs/op
identity-unified run=2: 239.7 ns/op, 132003 body-B/op, 80 B/op, 3 allocs/op
identity-unified run=3: 242.4 ns/op, 132003 body-B/op, 80 B/op, 3 allocs/op

BenchmarkNegotiation/gzip,_deflate,_br,_zstd-18         	 9655641	        61.21 ns/op	       0 B/op	       0 allocs/op
BenchmarkNegotiation/gzip,_deflate,_br,_zstd-18         	 9668990	       108.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkNegotiation/gzip,_deflate,_br,_zstd-18         	 7886132	       118.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkNegotiation/zstd;q=0.5,_gzip;q=1-18            	12620869	        44.41 ns/op	       0 B/op	       0 allocs/op
BenchmarkNegotiation/zstd;q=0.5,_gzip;q=1-18            	13048796	        44.49 ns/op	       0 B/op	       0 allocs/op
BenchmarkNegotiation/zstd;q=0.5,_gzip;q=1-18            	13671097	        44.92 ns/op	       0 B/op	       0 allocs/op
BenchmarkNegotiation/#00-18                             	45225988	        12.88 ns/op	       0 B/op	       0 allocs/op
BenchmarkNegotiation/#00-18                             	46922655	        12.75 ns/op	       0 B/op	       0 allocs/op
BenchmarkNegotiation/#00-18                             	45813038	        12.49 ns/op	       0 B/op	       0 allocs/op
```

## 独立评审

reviewer 初审发现406后仍消费正文的P2：写入返回成功及ReadFrom丢弃源会延长大文件/持续流。已修复为可识别拒绝错误，并补充零读取/首次读取后停止、错误剥离不丢真实错误的回归。复审通过，无必须修正的问题；reviewer独立重跑新增专项与diff check通过。预编码早拒绝限制已在API文档中说明。

本报告记录发布前的本地验证结果；生产网络吞吐不在验证范围内。
