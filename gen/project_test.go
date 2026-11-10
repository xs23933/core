package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putConfig(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

const statusConfig = "package: fixture\nenums:\n  - name: State\n    values: [none, active]\n"

func TestDirectoryEnumsCheckAndIdempotence(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "internal", "user_enums.yaml")
	putConfig(t, cfg, statusConfig)
	if err := GenerateEnumsInDir(root, true); err == nil {
		t.Fatal("check accepted missing output")
	}
	output := filepath.Join(filepath.Dir(cfg), "gen_state.go")
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("check wrote output")
	}
	if err := GenerateEnumsInDir(root, false); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(output)
	if err := GenerateEnumsInDir(root, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(output)
	if !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged output rewritten")
	}
	if err := GenerateEnumsInDir(root, true); err != nil {
		t.Fatal(err)
	}
	putConfig(t, cfg, strings.Replace(statusConfig, "active]", "active, disabled]", 1))
	before, _ := os.ReadFile(output)
	if err := GenerateEnumsInDir(root, true); err == nil {
		t.Fatal("check accepted outdated output")
	}
	unchanged, _ := os.ReadFile(output)
	if !bytes.Equal(before, unchanged) {
		t.Fatal("check changed output")
	}
}

func TestDirectoryEnumsPreflight(t *testing.T) {
	for _, tc := range []struct{ name, second, path string }{
		{"duplicate output", statusConfig, "a/other_enums.yaml"},
		{"constant collision", "package: fixture\nenums:\n - name: Other\n   values: [none, a-b, a.b]\n", "b/enums.yaml"},
		{"unknown field", statusConfig + "typo: true\n", "b/enums.yaml"},
		{"package conflict", strings.Replace(statusConfig, "fixture", "different", 1), "a/other_enums.yaml"},
		{"output forbidden", statusConfig + "output: ../outside\n", "b/enums.yaml"},
		{"second document", statusConfig + "---\npackage: fixture\n", "b/enums.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			putConfig(t, filepath.Join(root, "a/enums.yaml"), statusConfig)
			putConfig(t, filepath.Join(root, tc.path), tc.second)
			if err := GenerateEnumsInDir(root, false); err == nil {
				t.Fatal("expected preflight failure")
			}
			if _, err := os.Stat(filepath.Join(root, "a/gen_state.go")); !os.IsNotExist(err) {
				t.Fatal("partial batch written")
			}
		})
	}
}

func TestDirectoryEnumsProtectsFiles(t *testing.T) {
	for _, tc := range []struct{ name, file, content string }{
		{"manual target", "gen_state.go", "package fixture\n"},
		{"manual declaration", "state.go", "package fixture\ntype State int\n"},
		{"package mismatch", "user.go", "package other\n"},
		{"other owner", "gen_state.go", generatedHeader + "// Source: \"other_enums.yaml\"\npackage fixture\n"},
		{"stale output", "gen_old.go", generatedHeader + "// Source: \"enums.yaml\"\npackage fixture\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			putConfig(t, filepath.Join(root, "enums.yaml"), statusConfig)
			target := filepath.Join(root, tc.file)
			putConfig(t, target, tc.content)
			if err := GenerateEnumsInDir(root, false); err == nil {
				t.Fatal("expected conflict")
			}
			data, _ := os.ReadFile(target)
			if string(data) != tc.content {
				t.Fatal("file changed")
			}
		})
	}
}

func TestDirectoryEnumsSkipsDependenciesAndLinks(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"vendor", ".git", "node_modules", "nested"} {
		putConfig(t, filepath.Join(root, dir, "bad_enums.yaml"), "invalid yaml: [")
	}
	putConfig(t, filepath.Join(root, "nested/go.mod"), "module nested\n")
	outside := t.TempDir()
	putConfig(t, filepath.Join(outside, "enums.yaml"), statusConfig)
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := GenerateEnumsInDir(root, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "gen_state.go")); !os.IsNotExist(err) {
		t.Fatal("followed directory link")
	}
	putConfig(t, filepath.Join(root, "enums.yaml"), statusConfig)
	target := filepath.Join(root, "gen_state.go")
	victim := filepath.Join(outside, "victim.go")
	putConfig(t, victim, "safe")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}
	if err := GenerateEnumsInDir(root, false); err == nil {
		t.Fatal("accepted output link")
	}
	data, _ := os.ReadFile(victim)
	if string(data) != "safe" {
		t.Fatal("changed symlink target")
	}
}

func TestRenderEnumsValidation(t *testing.T) {
	for _, cfg := range []EnumConfig{
		{Package: "bad-name", Enums: []Enum{{Name: "State", Values: []string{"none"}}}},
		{Enums: []Enum{{Name: "../Escape", Values: []string{"none"}}}},
		{Enums: []Enum{{Name: "State", Values: []string{"active"}}}},
		{Enums: []Enum{{Name: "State", Values: append([]string{"none"}, make([]string, 256)...)}}},
		{Enums: []Enum{{Name: "State", Values: []string{"none", "a-b", "a.b"}}}},
	} {
		if _, err := RenderEnums(cfg); err == nil {
			t.Fatalf("accepted %#v", cfg)
		}
	}
}

func TestDirectoryEnumsRespectsGoFileSelection(t *testing.T) {
	root := t.TempDir()
	putConfig(t, filepath.Join(root, "enums.yaml"), statusConfig)
	putConfig(t, filepath.Join(root, "helper_linux.go"), "package fixture\nfunc platform() {}\n")
	putConfig(t, filepath.Join(root, "helper_darwin.go"), "package fixture\nfunc platform() {}\n")
	putConfig(t, filepath.Join(root, "generate.go"), "//go:build ignore\n\npackage main\nfunc main() {}\n")
	putConfig(t, filepath.Join(root, "_ignored.go"), "not Go source")
	putConfig(t, filepath.Join(root, ".ignored.go"), "not Go source")
	if err := GenerateEnumsInDir(root, false); err != nil {
		t.Fatal(err)
	}
	if err := GenerateEnumsInDir(root, true); err != nil {
		t.Fatal(err)
	}
	putConfig(t, filepath.Join(root, "methods.go"), "package fixture\nfunc (s State) String() string { return \"custom\" }\n")
	if err := GenerateEnumsInDir(root, false); err == nil {
		t.Fatal("accepted duplicate generated method")
	}
}

func TestEnumOutputMustBePortable(t *testing.T) {
	for _, name := range []string{"UserTest", "StateLinux", "StateAmd64", "StateLinuxAmd64"} {
		if _, err := RenderEnums(EnumConfig{Enums: []Enum{{Name: name, Values: []string{"none", "active"}}}}); err == nil {
			t.Fatalf("accepted constrained output name %s", name)
		}
	}
	if _, err := RenderEnums(EnumConfig{Enums: []Enum{{Name: "StateLinuxEnum", Values: []string{"none", "active"}}}}); err != nil {
		t.Fatal(err)
	}
}
