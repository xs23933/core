# 标准 HTTP 业务样例

这个样例与 `corectl new` 的 HTTP Go/YAML 输出由测试比较，展示：

```text
cmd/main.go → internal/app
handler → services → dao → models
                  ↘ domain/user（纯业务错误）
```

在本仓库中运行：

```sh
cd example/standard
go run ./cmd -migrate
go run ./cmd -f config.yaml
curl 'http://localhost:8080/api/v1/users?p=1&l=20'
```

使用父级 Core module，不单独创建 go.mod。迁移显式执行，普通启动不会更改表结构。
首次列表为空；测试通过独立 SQLite 夹具插入 alice，覆盖非空列表、详情、非法参数、
不存在和 context 取消。样例没有认证，不创建默认账号或密码；生产前需落实身份和业务权限。
生产数据库使用版本化迁移，不使用示例 AutoMigrate 作为发布迁移。

```sh
# 从仓库根目录
corectl gen enums ./example/standard
corectl gen enums ./example/standard --check
go test ./example/standard/... -count=1
```

HTTP 标准 context 为 c.Ctx()。DAO 接收数据库实例，每次操作 WithContext(ctx)，
Service 输出 UserResult，HTTP 负责状态与 JSON；数据库结构不直接返回客户端。
UserStatus 带 GORM 支持，YAML 与产物放 models；持久化 iota 值只追加，不重排。

app 的装配示例面向单进程单 Core。多实例使用 RegHandleFactory/Prepare 按当前实例注入。
完整目录、迁移与生命周期约定见 [项目架构](../../skills/core-architecture.md)。
gRPC 项目由 `corectl new users -m example.com/users -t grpc` 创建，读取生成 README 的
固定 protobuf 工具与生成命令。CI 生成实际 gRPC 项目并验证 RPC 和 etcd reflection。
