package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xs23933/core/v3/gen"
)

func config(t *testing.T) ProjectConfig {
	t.Helper()
	return ProjectConfig{ProjectName: "demo", ModulePath: "example.com/demo", GoVersion: "1.25.0", CoreVersion: "v3.1.71", ProjectType: "http", OutputDir: t.TempDir()}
}

func TestCreateProjectRejectsInvalidInputBeforeWriting(t *testing.T) {
	for _, change := range []func(*ProjectConfig){
		func(c *ProjectConfig) { c.ProjectName = ".." },
		func(c *ProjectConfig) { c.ModulePath = "bad\nmodule" },
		func(c *ProjectConfig) { c.GoVersion = "1.24.0" },
		func(c *ProjectConfig) { c.GoVersion = "latest" },
		func(c *ProjectConfig) { c.CoreVersion = "latest" },
		func(c *ProjectConfig) { c.CoreVersion = "v3.1" },
		func(c *ProjectConfig) { c.ProjectType = "unknown" },
	} {
		cfg := config(t)
		change(&cfg)
		if err := CreateProject(&cfg); err == nil {
			t.Fatalf("accepted %+v", cfg)
		}
		entries, _ := os.ReadDir(cfg.OutputDir)
		if len(entries) != 0 {
			t.Fatal("invalid input wrote files")
		}
	}
}

func TestCreateProjectExclusiveAndComplete(t *testing.T) {
	cfg := config(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- CreateProject(&cfg) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful creations: %d", successes)
	}
	root := filepath.Join(cfg.OutputDir, cfg.ProjectName)
	if err := gen.GenerateEnumsInDir(root, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"cmd/main.go", "internal/app/app.go", "internal/app/http.go", "internal/domain/user/errors.go", "internal/services/user.go", "internal/models/gen_user_status.go", "README.md", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := filepath.Join(root, "mine.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CreateProject(&cfg); err == nil {
		t.Fatal("accepted existing project")
	}
	data, _ := os.ReadFile(sentinel)
	if string(data) != "keep" {
		t.Fatal("existing content changed")
	}
}

func TestEnumCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "status_enums.yaml"), []byte("package: demo\nenums:\n - name: State\n   values: [none, active]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		cmd := newRootCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	if err := run("gen", "enums", root, "--check"); err == nil {
		t.Fatal("check accepted missing output")
	}
	if err := run("gen", "enums", root); err != nil {
		t.Fatal(err)
	}
	if err := run("gen", "enums", "--check", root); err != nil {
		t.Fatal(err)
	}
}

func TestStandardExampleMatchesTemplates(t *testing.T) {
	cfg := config(t)
	cfg.ModulePath = "github.com/xs23933/core/v3/example/standard"
	if err := CreateProject(&cfg); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(cfg.OutputDir, cfg.ProjectName)
	if err := filepath.WalkDir(generated, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || (!strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".yaml")) {
			return nil
		}
		rel, err := filepath.Rel(generated, path)
		if err != nil {
			return err
		}
		want, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		actual, err := os.ReadFile(filepath.Join("../../example/standard", rel))
		if err != nil {
			return err
		}
		if !bytes.Equal(want, actual) {
			return fmt.Errorf("standard example differs from template: %s", rel)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
