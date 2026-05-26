package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

// ProjectConfig 项目生成配置
type ProjectConfig struct {
	ProjectName string // 项目名称（用于目录名）
	ModulePath  string // Go module 路径，如 github.com/example/myapp
	GoVersion   string // Go 版本号
	CoreVersion string // Core Framework 版本号，如 v3.1.18
	ProjectType string // 项目类型: http | grpc
	OutputDir   string // 输出目录
}

// TemplateFile 定义需要生成的文件模板
type TemplateFile struct {
	Path     string // 相对于项目根目录的输出路径
	Template string // 模板内容
}

// getProjectTemplates 返回项目所需的全部文件模板
func getProjectTemplates(cfg *ProjectConfig) []TemplateFile {
	common := []TemplateFile{
		{Path: "go.mod", Template: goModTemplate},
		{Path: "internal/models/user.go", Template: userModelTemplate},
		{Path: "internal/dao/user.go", Template: userDaoTemplate},
		{Path: "internal/service/user.go", Template: userServiceTemplate},
	}

	if cfg.ProjectType == "grpc" {
		return append(common,
			TemplateFile{Path: "cmd/main.go", Template: mainGoGrpcTemplate},
			TemplateFile{Path: "config.yaml", Template: configYamlGrpcTemplate},
			TemplateFile{Path: "proto/user/v1/user.proto", Template: protoTemplate},
			TemplateFile{Path: "internal/grpc/user.go", Template: grpcUserTemplate},
		)
	}
	return append(common,
		TemplateFile{Path: "cmd/main.go", Template: mainGoTemplate},
		TemplateFile{Path: "config.yaml", Template: configYamlTemplate},
		TemplateFile{Path: "internal/handler/handler.go", Template: handlerGoTemplate},
		TemplateFile{Path: "internal/middleware/auth.go", Template: middlewareAuthTemplate},
	)
}

// CreateProject 根据配置创建完整的项目目录和文件
func CreateProject(cfg *ProjectConfig) error {
	projectDir := filepath.Join(cfg.OutputDir, cfg.ProjectName)

	// 检查目标目录是否已存在
	if _, err := os.Stat(projectDir); err == nil {
		return fmt.Errorf("directory %s already exists", projectDir)
	}

	fmt.Printf("Creating project %s ...\n", cfg.ProjectName)
	fmt.Printf("  Module:      %s\n", cfg.ModulePath)
	fmt.Printf("  Type:        %s\n", cfg.ProjectType)
	fmt.Printf("  Core:        %s\n", cfg.CoreVersion)
	fmt.Printf("  Output:      %s\n\n", projectDir)

	templates := getProjectTemplates(cfg)

	created := 0
	for _, f := range templates {
		targetPath := filepath.Join(projectDir, f.Path)
		if err := renderTemplate(cfg, f.Template, targetPath); err != nil {
			return fmt.Errorf("render %s: %w", f.Path, err)
		}
		created++
		fmt.Printf("  CREATED  %s\n", f.Path)
	}

	fmt.Printf("\nDone. %d files created in %s\n", created, projectDir)
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  cd %s\n", cfg.ProjectName)
	fmt.Printf("  go mod tidy\n")
	fmt.Printf("  go run cmd/main.go\n")

	return nil
}

// renderTemplate 解析模板并写入目标文件
func renderTemplate(cfg *ProjectConfig, tmplStr, targetPath string) error {
	tmpl, err := template.New("").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	f, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, cfg); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}

	return nil
}
