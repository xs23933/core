# Options YAML Duration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `Options.GetDuration` so YAML values such as `test.delay: 20s` are returned as `time.Duration`.

**Architecture:** Keep duration conversion in the existing `Options` typed-getter layer. `GetDuration` resolves either a direct key or a dotted nested path, then parses strings with `time.ParseDuration` while preserving an already typed `time.Duration`; lookup or conversion failures use the existing optional-default convention.

**Tech Stack:** Go 1.25, `time` standard library, `gopkg.in/yaml.v3`, Go `testing`.

## Global Constraints

- The public signature is `func (opt *Options) GetDuration(k string, def ...time.Duration) time.Duration`.
- Standard Go duration syntax such as `500ms`, `20s`, and `2m30s` is supported.
- Missing keys, invalid strings, and unsupported types return the first default or zero.
- Generic `GetAs` behavior and YAML unmarshalling behavior must not change.

---

### Task 1: Add the duration getter

**Files:**
- Create: `options_duration_test.go`
- Modify: `options.go:3-13, 154-174`

**Interfaces:**
- Consumes: YAML decoding into `Options` via `yaml.Unmarshal` and nested values represented as `Options` or `map[string]any`.
- Produces: `func (opt *Options) GetDuration(k string, def ...time.Duration) time.Duration`.

- [ ] **Step 1: Write the failing behavior test**

Create `options_duration_test.go`:

```go
package core

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestOptionsGetDuration(t *testing.T) {
	var opt Options
	if err := yaml.Unmarshal([]byte("delay: 500ms\ntest:\n  delay: 20s\n"), &opt); err != nil {
		t.Fatalf("unmarshal YAML: %v", err)
	}
	opt["typed"] = 2 * time.Minute
	opt["invalid"] = "later"
	opt["unsupported"] = 20

	tests := []struct {
		name string
		key  string
		def  []time.Duration
		want time.Duration
	}{
		{name: "top-level YAML string", key: "delay", want: 500 * time.Millisecond},
		{name: "nested YAML string", key: "test.delay", want: 20 * time.Second},
		{name: "typed duration", key: "typed", want: 2 * time.Minute},
		{name: "missing with default", key: "missing", def: []time.Duration{3 * time.Second}, want: 3 * time.Second},
		{name: "invalid with default", key: "invalid", def: []time.Duration{4 * time.Second}, want: 4 * time.Second},
		{name: "invalid without default", key: "invalid", want: 0},
		{name: "unsupported with default", key: "unsupported", def: []time.Duration{5 * time.Second}, want: 5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := opt.GetDuration(tt.key, tt.def...); got != tt.want {
				t.Fatalf("GetDuration(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
env GOCACHE=/private/tmp/core-gocache go test . -run '^TestOptionsGetDuration$'
```

Expected: build failure containing `opt.GetDuration undefined`.

- [ ] **Step 3: Add the minimal implementation**

Add `time` to the `options.go` imports and add the following methods after `GetInt64`:

```go
func (opt *Options) GetDuration(k string, def ...time.Duration) time.Duration {
	val, ok := opt.getValue(k)
	if ok {
		switch v := val.(type) {
		case time.Duration:
			return v
		case string:
			if duration, err := time.ParseDuration(v); err == nil {
				return duration
			}
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return 0
}

func (opt *Options) getValue(k string) (any, bool) {
	keys := strings.Split(k, ".")
	current := *opt
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

- [ ] **Step 4: Format and run the focused test to verify GREEN**

Run:

```bash
gofmt -w options.go options_duration_test.go
env GOCACHE=/private/tmp/core-gocache go test . -run '^TestOptionsGetDuration$'
```

Expected: `ok github.com/xs23933/core/v3`.

- [ ] **Step 5: Run regression verification**

Run:

```bash
env GOCACHE=/private/tmp/core-gocache go test ./...
git diff --check
```

Expected: all package tests pass and `git diff --check` prints no output.

- [ ] **Step 6: Commit the implementation**

```bash
git add options.go options_duration_test.go
git commit -m "feat: add options duration getter"
```
