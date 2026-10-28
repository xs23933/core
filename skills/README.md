```sh
# 配置加密密钥：core-utils.md；NSQ 认证继承：core-nsq.md。
skills/
├── core-skills.md           # 技能模块总览（功能概述/使用规范/最佳实践）
├── core-handler.md          # 创建 RESTful Handler
├── core-model.md            # 定义数据模型
├── core-service.md          # 编写业务逻辑层
├── core-middleware.md       # 开发中间件、gzip 响应压缩、monitor 进程/主机资源快照
├── core-static.md           # 静态文件与可选浏览器/CDN 长缓存
├── core-grpc.md             # gRPC 服务端（轻量 GRPCCtx / 可信 Gateway 客户端 IP / TLS/interceptor / RegisterGRPCService / EnableGRPC / EnableEtcdRegistry / 响应 Header/Cookie）
├── core-grpc-client.md      # gRPC 客户端（GrpcClient / GrpcClientAt / 每服务 TLS / etcd discovery）
├── core-gateway.md          # etcd HTTP/gRPC 网关（实例撤换连接排空 / ExactJSONBinding / core-client-ip / 自动/手动路由、负载均衡、请求 metadata / 响应 Header/Cookie）
├── core-websocket.md        # WebSocket 处理（Options/累计读限/超时/可靠入队/连接清理/按用户推送）
├── core-sse.md              # SSE 服务端推送（SSEWrite/SSESend/EventHub）
├── core-page.md             # 分页查询实现、共享 Where 字段/排序校验与 ErrInvalidQuery
├── core-utils.md            # 工具函数、Map/Array、加密与密码
├── core-redis.md            # Redis 封装（String/JSON/Hash/Set/ZSet/List/BitMap/Pipeline/Lua/PubSub/Stream）
├── core-cache.md            # Redis Cache 包装器（DB fallback、自动回填、singleflight、空值缓存）
├── core-nsq.md              # NSQ 消息队列封装（Producer/Consumer/Builder/JSON/超时处理）
├── core-logger.md           # 日志系统（D/Info/Warn/Erro/Recovery/LogMonitor）
├── core-fetch.md            # Fetch API 客户端（外部 API 调用、Header、Cookie、Hook、响应 Header）
├── core-fileupload.md       # 文件上传处理
├── core-test.md             # 测试编写
├── core-ctx.md              # Ctx 上下文使用规范（避坑指南/场景模板）
├── core-refactor.md         # 代码重构指南
├── core-gen.md              # Enum 代码生成器（YAML 配置/程序化生成）
└── core-corectl.md          # 项目脚手架工具（corectl 使用指南）

```


## 托管工作负载 mTLS

Gateway 可使用 Go 标准库首次自动创建私有 CA，后端自动申请、本地保存和续期，服务端、服务间客户端和 Gateway reflection 复用本进程身份。使用前必须在首次创建 gRPC server/client 前调用 `app.EnableWorkloadIdentity()`；启用后禁止明文回退或逐目标显式 transport 冲突配置。完整配置、引导认证、独立端口、根信任更新和验收边界见 [工作负载身份说明](../workload/README.md)。

后端跨服务器可预置 `workload.root_sha256`（根证书 DER SHA-256），Core 从内部签发入口 `GET /root.pem` 无凭据下载，校验指纹及本次 HTTPS 链/主机名后原子缓存，再申请身份。每次启动校验缓存，拒绝不匹配/损坏缓存的静默覆盖；默认缓存位于本进程 state_dir，不依赖 Gateway 共享文件。指纹必须由可信部署渠道预置，详见工作负载说明。

- [core-certificates.md](core-certificates.md)：动态精确域名 ACME、业务授权、HTTP-01、TLS 生命周期、持久存储与续期。

- 原子加密配置文件与 CurrentConfigFile、密文 CAS、跨进程锁及持久预备文件恢复：见 [core-utils.md](core-utils.md#原子加密配置文件)。

通用 `grpc.max_recv_message_bytes` 配置、默认4MiB、无效值安全回落及Gateway响应接收与ExactJSON优先级，见 [core-grpc.md](core-grpc.md#通用-grpc-接收大小) 与 [core-gateway.md](core-gateway.md#通用-grpc-接收大小)。
