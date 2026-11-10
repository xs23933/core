---
name: core-architecture
description: Core 下游项目目录、应用装配、domain 与 models 边界、HTTP/gRPC 共享业务及旧项目迁移
---

# 项目架构与装配

创建业务项目、调整目录、装配依赖或迁移旧项目时先读本文件，再读相关能力 Skill。
这些是下游项目约定；Core 框架本身保留现有包结构和公开 API。

## 目录与职责

```text
project/
├── cmd/main.go                # 命令行参数、调用 app.Run、退出码
├── internal/
│   ├── app/                   # 配置、Core、依赖装配、协议注册、清理
│   ├── domain/user/           # 纯业务错误、状态规则和值对象
│   ├── services/user.go        # 用例、业务授权、事务；context.Context
│   ├── dao/user.go             # 注入数据库；WithContext(ctx) 查询
│   ├── models/
│   │   ├── user.go            # 数据表结构、GORM 标签
│   │   ├── user_enums.yaml
│   │   └── gen_user_status.go # 带 Core/GORM 支持的生成枚举
│   ├── handler/user.go         # HTTP 参数、身份提取、响应与错误映射
│   └── grpc/user.go            # protobuf/metadata、RPC 错误映射
├── proto/user/v1/user.proto    # gRPC 项目
├── gen/user/v1/                # protobuf 生成产物
├── config.yaml
├── README.md
└── go.mod
```

HTTP/gRPC 目录按项目类型生成，middleware、迁移等有用途时再加。
业务包统一使用 services、models；文件按业务命名。测试与源文件同包。
数据库模型与领域对象不必逐字段复制；domain 只容纳真实业务语义，不能依赖
Core、GORM 或协议。现有枚举生成器带 Core/GORM 支持，应放在 models 或其子包。

调用方向为 `handler/grpc → services → dao → models`，services 使用 domain 的规则和错误。
app 是统一装配入口；handler/grpc 构造器接受已创建的 Service，不在内部创建 DAO。
DAO 构造器只保存依赖，不迁移数据库、不访问全局连接。避免为单实现增加 Repository 接口。

请求结构放在协议入口；协议无关的用例参数与结果就近放在 services。共享业务状态与规则
放在 domain。DTO/VO 描述职责，不要求额外的 dto/vo 目录。数据库 Model 不直接返回客户端。

## 启动与资源

1. main 解析配置路径与显式迁移参数，调用 app.Run；错误返回到 main 处理退出码。
2. app 加载配置、创建 Core，检查必需依赖，用 engine.Conn() 构造 DAO/Service。
3. HTTP 在 app 中通过 core.RegHandle 注册自动 Handler；gRPC 先配置 transport/interceptor，
   再 RegisterGRPCService，最后按配置 EnableEtcdRegistry(nil)。
4. engine.Run 复用 Core 生命周期；初始化失败也调用 ShutdownWithTimeout 清理已创建资源。

HTTP context 用 c.Ctx()（或 c.Context()）；GRPCCtx 的 StdContext() 属于另一种对象。
必需预热用 Prepare，后台任务用 Start 并响应 app.Ctx，关闭用 Stop；不新增生命周期框架。
普通启动不修改表结构。脚手架的 `-migrate` 只用于本地演示库，生产使用审核后的版本化迁移。

RegHandle 是进程级登记，生成样例面向单进程单 Core。多 Core 用 RegHandleFactory，
在 Prepare 中按当前 app 装配依赖；工厂不能捕获首个 app 的 DAO/Service 供所有实例复用。
不要把全局注册改称实例注册，也不要在重复调用 Run 后复用已绑定 Handler。

事务在 Service 开启，闭包内 DAO 复用同一 tx；HTTP、RPC、NSQ 不能由本地事务回滚。
协议层检查格式和身份，Service/Domain 保证业务授权、状态和金额规则，入口之间不能互相代替。
业务错误由 domain 定义，HTTP 映射状态和 JSON，gRPC 映射 codes；未知错误只向客户端给出通用信息。

## 样例、生成与迁移

可运行的 HTTP 参考为 [example/standard](../example/standard/README.md)，模板为
[corectl templates](../cmd/corectl/templates)。协议或业务样例变更时同步模板和实际生成验收。
WebSocket、SSE、底层路由等能力演示不必扩展成完整业务项目。

新项目用 corectl new；已有项目不自动移动文件：

1. 将 main 的装配移入 internal/app，保留启动参数与配置路径。
2. 统一 service→services、model→models，修改 import，保留数据库表名和迁移历史。
3. 将协议转换与业务规则分离，保持接口路径、字段、错误码和授权语义。
4. 提取真实领域规则；无需创建与 Model 同字段的空领域对象。
5. 将启动迁移改为显式步骤，验证调用方后再切换部署入口。

目录变化不是数据库迁移。每次迁移检查已有 API、认证、事务、资源关闭及生成结果；
不要在一次框架升级中强制搬迁所有下游项目。
