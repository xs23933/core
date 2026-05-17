package xid

import (
	"encoding/json"
	"testing"
)

func TestIDCompatibility(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  ID
	}{
		{name: "int", input: int(123), want: 123},
		{name: "string", input: "123", want: 123},
		{name: "float64", input: float64(123), want: 123},
		{name: "float string", input: "123.0", want: 123},
		{name: "bytes", input: []byte("123"), want: 123},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var id ID
			if err := id.Scan(tt.input); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if id != tt.want {
				t.Fatalf("id = %d, want %d", id, tt.want)
			}
		})
	}
}

func TestUnmarshalJSONCompatibility(t *testing.T) {
	for _, raw := range []string{`123`, `"123"`, `123.0`, `"123.0"`} {
		var id ID
		if err := json.Unmarshal([]byte(raw), &id); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if id != 123 {
			t.Fatalf("unmarshal %s = %d, want 123", raw, id)
		}
	}
}

func TestRejectNonIntegerFloat(t *testing.T) {
	var id ID
	if err := id.Scan(float64(123.4)); err == nil {
		t.Fatal("scan non-integer float error = nil, want error")
	}
	if _, err := ParseString("123.4"); err == nil {
		t.Fatal("parse non-integer float string error = nil, want error")
	}
}
