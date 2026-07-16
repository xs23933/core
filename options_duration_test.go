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
