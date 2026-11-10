package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"go/version"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/xs23933/core/v3/gen"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

type ProjectConfig struct {
	ProjectName string
	ModulePath  string
	GoVersion   string
	CoreVersion string
	ProjectType string
	OutputDir   string
}

type TemplateFile struct{ Path, Template string }

func getProjectTemplates(cfg *ProjectConfig) ([]TemplateFile, error) {
	var files []TemplateFile
	for _, kind := range []string{"common", cfg.ProjectType} {
		prefix := "templates/" + kind + "/"
		err := fs.WalkDir(projectTemplates, strings.TrimSuffix(prefix, "/"), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			content, err := projectTemplates.ReadFile(path)
			if err != nil {
				return err
			}
			files = append(files, TemplateFile{Path: strings.TrimSuffix(strings.TrimPrefix(path, prefix), ".tmpl"), Template: string(content)})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func validateProject(cfg *ProjectConfig) error {
	if cfg == nil {
		return fmt.Errorf("project config is required")
	}
	if err := validateInput(cfg.ProjectName); err != nil {
		return err
	}
	if err := module.CheckImportPath(cfg.ModulePath); err != nil {
		return fmt.Errorf("module: %w", err)
	}
	if cfg.ProjectType != "http" && cfg.ProjectType != "grpc" {
		return fmt.Errorf("project type must be http or grpc")
	}
	if !version.IsValid("go"+cfg.GoVersion) || version.Compare("go"+cfg.GoVersion, "go1.25.0") < 0 {
		return fmt.Errorf("Go version must be a valid version >= 1.25.0")
	}
	if !semver.IsValid(cfg.CoreVersion) || semver.Canonical(cfg.CoreVersion) != cfg.CoreVersion || strings.Contains(cfg.CoreVersion, "+") || semver.Major(cfg.CoreVersion) != "v3" {
		return fmt.Errorf("Core version must be a concrete v3 semantic version")
	}
	return nil
}

func validateInput(name string) error {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(name) {
		return fmt.Errorf("project name must start with a letter or digit and contain only letters, digits, '.', '_' or '-'")
	}
	return nil
}

// CreateProject validates and renders everything before exclusively creating a directory.
func CreateProject(cfg *ProjectConfig) (err error) {
	if err := validateProject(cfg); err != nil {
		return err
	}
	templates, err := getProjectTemplates(cfg)
	if err != nil {
		return err
	}
	rendered := make(map[string][]byte)
	for _, file := range templates {
		tmpl, err := template.New(file.Path).Option("missingkey=error").Parse(file.Template)
		if err != nil {
			return fmt.Errorf("parse %s: %w", file.Path, err)
		}
		var buffer bytes.Buffer
		if err := tmpl.Execute(&buffer, cfg); err != nil {
			return fmt.Errorf("render %s: %w", file.Path, err)
		}
		code := buffer.Bytes()
		if strings.HasSuffix(file.Path, ".go") {
			code, err = format.Source(code)
			if err != nil {
				return fmt.Errorf("format %s: %w", file.Path, err)
			}
		}
		rendered[file.Path] = code
	}
	// mkdir reserves the new directory without a check/create race.
	outputDir := cfg.OutputDir
	if outputDir == "" {
		outputDir = "."
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	projectDir := filepath.Join(outputDir, cfg.ProjectName)
	if err := os.Mkdir(projectDir, 0o755); err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(projectDir))
		}
	}()
	for _, file := range templates {
		target := filepath.Join(projectDir, file.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, rendered[file.Path], 0o644); err != nil {
			return err
		}
	}
	if err := gen.GenerateEnumsInDir(projectDir, false); err != nil {
		return err
	}
	return nil
}
