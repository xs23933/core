# Options Shared Lookup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace duplicated `Options` dotted-path traversal with one shared raw-value lookup while preserving public getter behavior.

**Architecture:** `Options.lookup` owns direct and dotted raw-value traversal through `Options` and `map[string]any`. Typed getters and compatibility `GetPathXxx` methods retain responsibility for their own conversions and defaults, so sharing lookup does not erase the existing `GetBool` versus `GetPathBool` conversion distinction.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3`, Go `testing`.

## Global Constraints

- Keep all existing exported method signatures.
- Preserve existing conversion and default behavior, including `GetPathBool` and dotted `GetBool` accepting string `"1"` and integer values while direct-key `GetBool` does not broaden.
- Add dotted-path support to `GetInt64`.
- Keep `GetDuration("test.delay") == 20 * time.Second`.

---

### Task 1: Introduce and adopt shared lookup

**Files:**
- Create: `options_lookup_test.go`
- Modify: `options.go`
- Test: `options_duration_test.go`, `options_lookup_test.go`

**Interfaces:**
- Consumes: `Options` and `map[string]any` nested configuration values.
- Produces: unexported `func (opt Options) lookup(path string) (any, bool)` and unchanged exported typed-getter APIs.

- [ ] **Step 1: Add regression and new-behavior tests**

Create `options_lookup_test.go`:

```go
package core

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOptionsTypedGettersUseNestedLookup(t *testing.T) {
	var opt Options
	if err := yaml.Unmarshal([]byte("test:\n  name: worker\n  count: 20\n  enabled: true\n  tags: [a, b]\n  int64: 922337203685477580\n"), &opt); err != nil {
		t.Fatalf("unmarshal YAML: %v", err)
	}

	if got := opt.GetString("test.name"); got != "worker" {
		t.Fatalf("GetString() = %q, want worker", got)
	}
	if got := opt.GetInt("test.count"); got != 20 {
		t.Fatalf("GetInt() = %d, want 20", got)
	}
	if got := opt.GetInt64("test.int64"); got != 922337203685477580 {
		t.Fatalf("GetInt64() = %d, want 922337203685477580", got)
	}
	if got := opt.GetBool("test.enabled"); !got {
		t.Fatal("GetBool() = false, want true")
	}
	if got := opt.GetStrings("test.tags"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("GetStrings() = %#v, want [a b]", got)
	}
}

func TestOptionsLookupSupportsMapAndPreservesPathBoolConversions(t *testing.T) {
	opt := Options{
		"one": "1",
		"nested": map[string]any{
			"name":    "worker",
			"one":     "1",
			"integer": 1,
		},
	}

	if got := opt.GetString("nested.name"); got != "worker" {
		t.Fatalf("GetString() = %q, want worker", got)
	}
	if got := opt.GetPathBool("nested.one"); !got {
		t.Fatal("GetPathBool(string 1) = false, want true")
	}
	if got := opt.GetPathBool("nested.integer"); !got {
		t.Fatal("GetPathBool(integer 1) = false, want true")
	}
	if got := opt.GetBool("nested.one"); !got {
		t.Fatal("GetBool(dotted string 1) = false, want preserved true behavior")
	}
	if got := opt.GetBool("one"); got {
		t.Fatal("GetBool(direct string 1) = true, want preserved false behavior")
	}
}

func TestOptionsPathGettersPreserveDefaults(t *testing.T) {
	opt := Options{}
	if got := opt.GetPathString("missing.value", "fallback"); got != "fallback" {
		t.Fatalf("GetPathString() = %q, want fallback", got)
	}
	if got := opt.GetPathInt("missing.value", 3); got != 3 {
		t.Fatalf("GetPathInt() = %d, want 3", got)
	}
	if got := opt.GetPathBool("missing.value", true); !got {
		t.Fatal("GetPathBool() = false, want default true")
	}
	if got := opt.GetPathStrings("missing.value", []string{"fallback"}); !reflect.DeepEqual(got, []string{"fallback"}) {
		t.Fatalf("GetPathStrings() = %#v, want [fallback]", got)
	}
}
```

- [ ] **Step 2: Verify RED for new dotted `GetInt64` behavior**

Run:

```bash
env GOCACHE=/private/tmp/core-gocache go test . -run '^TestOptions(TypedGettersUseNestedLookup|LookupSupportsMapAndPreservesPathBoolConversions|PathGettersPreserveDefaults)$'
```

Expected: failure showing `GetInt64() = 0, want 922337203685477580`.

- [ ] **Step 3: Replace duration-only lookup and duplicated traversal**

In `options.go`, rename and generalize `getValue`:

```go
func (opt Options) lookup(path string) (any, bool) {
	keys := strings.Split(path, ".")
	current := opt
	for i, key := range keys {
		val, ok := current[key]
		if !ok {
			return nil, false
		}
		if i == len(keys)-1 {
			return val, true
		}
		switch next := val.(type) {
		case Options:
			current = next
		case map[string]any:
			current = Options(next)
		default:
			return nil, false
		}
	}
	return nil, false
}
```

In `GetString`, `GetStrings`, `GetInt`, `GetInt64`, and `GetDuration`, replace the current raw lookup expression with the exact shared form below while leaving the conversion block and fallback that follow it unchanged:

```go
if val, ok := (*opt).lookup(k); ok && val != nil {
```

For `GetDuration`, whose local `val` is used after lookup, use:

```go
val, ok := (*opt).lookup(k)
```

Keep the existing dotted-path branch at the start of `GetBool`:

```go
if strings.Contains(k, ".") {
	return opt.GetPathBool(k, def...)
}
```

Then replace its direct map lookup with:

```go
val, ok := (*opt).lookup(k)
```

This preserves direct-key and dotted-path boolean conversion behavior while removing duplicated traversal.

Replace the bodies of `GetPathString`, `GetPathInt`, and `GetPathStrings` with delegation because their conversions already match:

```go
func (opt *Options) GetPathString(path string, def ...string) string {
	return opt.GetString(path, def...)
}

func (opt *Options) GetPathInt(path string, def ...int) int {
	return opt.GetInt(path, def...)
}

func (opt *Options) GetPathStrings(path string, def ...[]string) []string {
	return opt.GetStrings(path, def...)
}
```

Replace `GetPathBool` with shared lookup plus its existing broader conversion rules:

```go
func (opt *Options) GetPathBool(path string, def ...bool) bool {
	if val, ok := (*opt).lookup(path); ok {
		switch v := val.(type) {
		case string:
			return v == "true" || v == "1"
		case bool:
			return v
		case int:
			return v != 0
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return false
}
```

Delete `getPathStringsRecursive` after `GetPathStrings` no longer calls it.

- [ ] **Step 4: Format and verify focused GREEN**

Run:

```bash
gofmt -w options.go options_lookup_test.go
env GOCACHE=/private/tmp/core-gocache go test . -run '^TestOptions(GetDuration|TypedGettersUseNestedLookup|LookupSupportsMapAndPreservesPathBoolConversions|PathGettersPreserveDefaults)$'
```

Expected: `ok github.com/xs23933/core/v3`.

- [ ] **Step 5: Run full regression verification**

Run:

```bash
env GOCACHE=/private/tmp/core-gocache go test ./...
git diff --check
```

Expected: all packages pass and `git diff --check` prints no output.

- [ ] **Step 6: Commit**

```bash
git add options.go options_lookup_test.go docs/superpowers/specs/2026-07-16-options-lookup-refactor-design.md
git commit -m "refactor: share options path lookup"
```
