---
name: core-corectl
description: 用 corectl 创建 HTTP/gRPC 项目、选择版本、维护模板和验收生成结果
---

# corectl 项目脚手架

项目结构与依赖约定见 [core-architecture.md](core-architecture.md)。枚举扫描见
[core-gen.md](core-gen.md)。

## 创建项目

```sh
go install ./cmd/corectl
corectl new myapp -m example.com/myapp
corectl new users -m example.com/users -t grpc
```

| 参数 | 默认值 | 含义 |
|---|---|---|
| 项目名 | 必填 | 字母/数字开头，其后可含字母、数字、点、下划线、连字符 |
| -m / --module | 项目名 | Go import 路径 |
| -t / --type | http | http 或 grpc |
| --go-version | 1.25.0 | 合法 Go 版本，至少 1.25.0 |
| --core-version | v3.1.71 | 明确的 Core v3 语义版本；需满足该版本自身的 Go 要求 |
| -o / --output | . | 输出父目录 |

发布构建可通过 `-ldflags '-X main.coreVersion=v3.x.y'` 绑定模板测试过的 Core 版本。
不会把 latest 写入 go.mod，也不会自动下载或选择最新依赖。指定其它 Core 版本时由调用方
确认版本存在、Go 要求与 API 兼容性；模板验收覆盖绑定基线。

生成器先校验并渲染全部模板、格式化 Go，再独占创建新目录。已有目录一律拒绝，不提供
覆盖开关。创建失败清理本次创建的项目目录；不修改已有项目。生成过程不联网、不运行项目。
模板用标准库 embed 打包，位于 cmd/corectl/templates/{common,http,grpc}。

## 生成项目后

阅读生成的 README。HTTP 项目：

```sh
cd myapp
go mod tidy
go run ./cmd -migrate
go run ./cmd -f config.yaml
curl 'http://localhost:8080/api/v1/users?p=1&l=20'
go test ./...
```

默认提供无认证的用户查询演示，首次列表为空；不会创建默认账号或密码。
迁移仅由 -migrate 显式执行。上线前配置身份验证、业务权限和版本化数据库迁移。
cmd/main.go 只解析参数和处理退出，internal/app 创建依赖并注册协议。

HTTP 列表 GET /api/v1/users，详情 GET /api/v1/users/:id；p/l 为页码和大小，大小限制 1..100。
Service 返回独立结果，领域错误由协议层映射，所有 DAO 操作传播 context。

## gRPC 项目

生成的 README 提供 protoc 命令：proto/user/v1/user.proto → gen/user/v1/*.go，go_package
包含完整 module 路径。安装 protoc；固定 Go 插件为 protoc-gen-go v1.36.11、
protoc-gen-go-grpc v1.6.1。先生成 protobuf，再执行 go mod tidy、迁移和启动；
生成器不会在生成项目时隐式执行外部工具。提交 protobuf 产物后普通构建不依赖 protoc。

默认 HTTP/gRPC 共用 :8080，etcd.enabled 为 false，可以独立运行。
启用 etcd 时先注册 gRPC service，再 EnableEtcdRegistry(nil)，保证能力元数据和 reflection。
Gateway 可通过服务发现/reflection 自动生成路由；public_routes 是跳过登录认证的列表，
不是路由发布白名单。业务授权仍需在可信身份与下游服务落实。

## 修改模板后的验收

```sh
GOCACHE=/private/tmp/core-gocache go test ./gen ./cmd/corectl ./example/standard/... -count=1
go vet ./...
git diff --check
```

TestGeneratedProjects 在临时 module 中生成真实 HTTP/gRPC 项目，执行 protobuf 生成、
go mod tidy、测试、vet 和构建；源码验证用 replace 指向当前 Core。gRPC 测试使用本地
etcd 验证 reflection。工具不齐时仅该 gRPC 子测试跳过，CI 必须先安装工具，不能把跳过称为通过。
CI 还在无 replace 的项目验证绑定发布版本，防止本地源码掩盖发布兼容问题。
标准 HTTP 样例与模板由测试比较，业务测试覆盖非空列表、非法参数、不存在及取消 context。

本期提供 new 和 gen enums；不提供 add resource、数据库反向生成或自动修改已有业务文件。
