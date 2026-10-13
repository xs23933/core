```sh
skills/
├── core-skills.md           # 技能模块总览（功能概述/使用规范/最佳实践）
├── core-handler.md          # 创建 RESTful Handler
├── core-model.md            # 定义数据模型
├── core-service.md          # 编写业务逻辑层
├── core-middleware.md       # 开发中间件
├── core-grpc.md             # gRPC 服务端（轻量 GRPCCtx / 可信 Gateway 客户端 IP / TLS/interceptor / RegisterGRPCService / EnableGRPC / EnableEtcdRegistry / 响应 Header/Cookie）
├── core-grpc-client.md      # gRPC 客户端（GrpcClient / GrpcClientAt / 每服务 TLS / etcd discovery）
├── core-gateway.md          # etcd HTTP/gRPC 网关（core-client-ip / 自动/手动路由、负载均衡、请求 metadata / 响应 Header/Cookie）
├── core-websocket.md        # WebSocket 处理（连接管理/按用户推送/广播）
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
