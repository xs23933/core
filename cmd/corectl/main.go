package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

var (
	projectName string
	modulePath  string
	goVersion   string
	projectType string
	outputDir   string

	// coreVersion 通过 -ldflags 在构建时注入，默认 "latest"
	// 示例: go build -ldflags "-X main.coreVersion=v3.1.20" ./cmd/corectl/
	coreVersion = "latest"
)

func main() {
	flag.Parse()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "corectl",
	Short: "Core Framework v3 项目脚手架工具",
	Long: `corectl 是 Core Framework v3 的官方项目脚手架工具。
	
通过 corectl 可以快速创建符合 Core Framework 规范的项目骨架，
包括标准目录结构和基础代码文件，帮助开发者快速启动新项目。`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var newCmd = &cobra.Command{
	Use:   "new [project-name]",
	Short: "创建新的 Core Framework 项目",
	Long: `new 命令用于创建新的 Core Framework 项目骨架。

生成的项目遵循 Core Framework 的标准目录规范:

  HTTP 项目 (-t http, 默认):
    cmd/main.go              - 应用入口
    internal/handler/        - HTTP Handler 层
    internal/service/        - 业务逻辑层
    internal/dao/            - 数据访问层
    internal/models/          - 数据模型层
    internal/middleware/     - 中间件
    config.yaml              - 配置文件

  gRPC 项目 (-t grpc):
    cmd/main.go              - 应用入口 (gRPC + etcd)
    internal/grpc/           - gRPC Service 实现
    internal/service/        - 业务逻辑层
    internal/dao/            - 数据访问层
    internal/models/          - 数据模型层
    proto/user/v1/           - Protobuf 定义
    config.yaml              - 配置文件

示例:
  corectl new myapp -m github.com/example/myapp
  corectl new myapp -m github.com/example/myapp -t grpc
  corectl new myapp -m github.com/example/myapp --go-version 1.23
  corectl new myapp -m github.com/example/myapp -o /path/to/dir`,
	Args: cobra.ExactArgs(1),
	RunE: runNew,
}

func init() {
	newCmd.Flags().StringVarP(&modulePath, "module", "m", "", "Go module 路径，如 github.com/example/myapp（可选）默认值为项目名称")
	newCmd.Flags().StringVar(&goVersion, "go-version", runtime.Version()[2:], fmt.Sprintf("Go 版本号（默认: %s）", runtime.Version()[2:]))
	newCmd.Flags().StringVarP(&projectType, "type", "t", "http", "项目类型: http | grpc（默认: http）")
	newCmd.Flags().StringVarP(&outputDir, "output", "o", ".", "输出目录（默认: 当前目录）")

	rootCmd.AddCommand(newCmd)
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "显示 corectl 和 Core Framework 版本信息",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("corectl version: %s (Core Framework %s)\n", coreVersion, coreVersion)
	},
}

func runNew(cmd *cobra.Command, args []string) error {
	projectName = args[0]

	if err := validateInput(projectName); err != nil {
		return err
	}

	if projectType != "http" && projectType != "grpc" {
		return fmt.Errorf("invalid project type: %s (must be http or grpc)", projectType)
	}

	if modulePath == "" {
		modulePath = projectName
	}

	cfg := &ProjectConfig{
		ProjectName: projectName,
		ModulePath:  modulePath,
		GoVersion:   goVersion,
		CoreVersion: coreVersion,
		ProjectType: projectType,
		OutputDir:   outputDir,
	}

	return CreateProject(cfg)
}

// validateInput 校验用户输入的合法性
func validateInput(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("project name cannot be empty")
	}

	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("project name cannot contain path separators, use --module for module path")
	}

	return nil
}
