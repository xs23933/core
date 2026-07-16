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
