---
name: core-gen
description: 枚举 YAML、corectl 目录扫描与检查、coregen 单文件生成、类型与持久化兼容性
---

# 类型枚举生成

## 项目目录扫描

```sh
corectl gen enums
corectl gen enums ./internal
corectl gen enums --check
```

从指定目录递归查找 *_enums.yaml 和 enums.yaml，在 YAML 同目录生成每个类型的
`gen_<snake_case类型名>.go`。相同内容不重写；--check 不写入，缺失或过期返回非零。
跳过 .git、vendor、node_modules、符号链接与嵌套 go.mod 目录；扫描根目录必须是普通目录。
根目录可以是项目或 internal 等子目录。

```text
internal/models/
├── user_enums.yaml
└── gen_user_status.go
```

```yaml
package: models
enums:
  - name: UserStatus
    values: [none, active, disabled]
  - name: Lang
    values: [none, en, zh-CN]
    sensitive: true
```

package 必填，必须与目录现有非测试 Go 文件相符；批量模式不允许 output。
每文件一个 YAML document，未知配置字段报错。同目录多个 YAML 合并检查符号冲突。
类型名是导出的 Go 标识符；避免 UserTest、StateLinux、StateAmd64 等使输出成为测试或平台文件的名称，
此类名称报错，可用 UserTestEnum 等后缀。枚举有 1..256 个值且首项为 none；- 和 . 转换为常量名中的 _，
原 wire 值保留。转换后的常量、类型、辅助函数都不能重名；非敏感枚举也检查大小写重复。

写入前检查整个批次、手写 Go 声明、包名、目标所有权和格式。手写文件按当前默认
GOOS/GOARCH 与 build constraints 选择，忽略测试文件、点/下划线文件和忽略脚本；
只比较手写与生成声明，不校验手写文件彼此是否重复。其它构建目标仍需对应编译验证。
目标必须不存在或具有本生成器的头部及匹配的 Source 标记；不覆盖手写文件、其它 YAML
产物或符号链接。旧无 Source 标记产物需要人工确认并移除，再用扫描命令生成。
源文件改名、移除类型后遗留的产物需人工检查删除；扫描目录内有配置时会报告多余的已标记产物。
不自动清理孤立目录，不提供跨所有文件的事务或并发编辑保证；生成期间不要同时修改配置或输出。
每个变化文件用临时文件替换，磁盘故障仍可能留下已完成的部分更新，修复后重新运行。

生成类型含 Core/GORM 与 JSON 支持，放在 models 或其子包；不要放入约定无框架依赖的 domain。
产物提交版本控制，不手改，生成命令不绑定应用启动。CI 用 --check 防止 YAML 与 Go 不一致。

## 保留 coregen 单文件方式

```sh
go install ./cmd/coregen
coregen -f enums.yaml -o ./constants -p constants
```

coregen 使用标准 flag，**没有 enum 子命令**。-o/-p 覆盖 YAML 的 output/package；
未指定时输出相对当前工作目录，默认包 constants。单文件旧接口不强制 Source 标记，
但只更新带 Core 生成头的文件，拒绝覆盖手写文件。

程序化调用复用相同模板与校验：

```go
err := gen.GenerateEnums(gen.EnumConfig{
    Package: "constants",
    Output: "./constants",
    Enums: []gen.Enum{
        {Name: "State", Values: []string{"none", "active", "disabled"}},
    },
})
// 调用方必须处理 err。
```

RenderEnums 返回格式化后的文件内容，不写文件。GenerateEnumsInDir(root, check) 提供
与 corectl 相同的目录语义。旧 GenerateEnums 默认 package/output 保留；非法配置和手写覆盖
现在返回错误。不会自动补救或重排业务枚举。

## 生成合同与验收

类型为 uint8，包含 iota 常量、Mapping、FromString、String/Int、JSON、SQL Scan/Value、
GORM 类型和 Parse 辅助函数。默认 JSON 解析不区分大小写，sensitive: true 保留大小写。
首项 none 对应 0；已经持久化的值只追加，不能重排或插入，否则旧数据含义改变。

Scan 支持 int64/uint8/uint16/uint32/float64/string/[]byte；数值必须为 0..255 的整数，
拒绝负数、溢出及非有限值且不修改原值。String 越界返回空串，JSON/Text 越界返回错误。
字符串领域合法性仍由业务校验，数字范围有效不代表业务取值有效。

```sh
go test ./gen ./cmd/corectl -count=1
corectl gen enums --check
go test ./...
go vet ./...
```

生成回归编译真实产物，检查数值边界、带点 wire 值、扫描冲突、防覆盖和只读检查。
