package nid

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestGenerateNID(t *testing.T) {
	g := New(Config{Start: 0})
	seen := make(map[ID]struct{})

	for range 10000 {
		id, err := g.Generate()
		if err != nil {
			t.Fatalf("generate nid: %v", err)
		}
		if !id.IsValid() {
			t.Fatalf("invalid numeric nid %d", id)
		}
		encoded := id.Encode()
		if len(encoded) != Size {
			t.Fatalf("encoded length = %d, want %d", len(encoded), Size)
		}
		decoded, err := Decode(encoded)
		if err != nil {
			t.Fatalf("decode %q: %v", encoded, err)
		}
		if decoded != id {
			t.Fatalf("decoded nid = %d, want %d", decoded, id)
		}
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate nid generated: %d", id)
		}
		seen[id] = struct{}{}
	}
}

func TestEncodeDecode(t *testing.T) {
	tests := map[ID]string{
		0:            "aaaaaa",
		1:            "aaaaab",
		35:           "aaaaa9",
		36:           "aaaaba",
		MaxValue - 1: "999999",
	}

	for id, want := range tests {
		if got := id.Encode(); got != want {
			t.Fatalf("ID(%d).Encode() = %q, want %q", id, got, want)
		}
		decoded, err := Decode(want)
		if err != nil {
			t.Fatalf("Decode(%q): %v", want, err)
		}
		if decoded != id {
			t.Fatalf("Decode(%q) = %d, want %d", want, decoded, id)
		}
	}
}

func TestParseString(t *testing.T) {
	id, err := ParseString("123")
	if err != nil {
		t.Fatalf("parse decimal nid: %v", err)
	}
	if id != 123 {
		t.Fatalf("parsed decimal nid = %d, want 123", id)
	}

	id, err = ParseString("aaaaba")
	if err != nil {
		t.Fatalf("parse encoded nid: %v", err)
	}
	if id != 36 {
		t.Fatalf("parsed encoded nid = %d, want 36", id)
	}

	for _, input := range []string{"ABC123", "abc12", "abc1234", "abc-12"} {
		if _, err := Decode(input); err == nil {
			t.Fatalf("Decode(%q) error = nil, want error", input)
		}
	}
}

func TestJSON(t *testing.T) {
	id := ID(36)
	data, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("marshal nid: %v", err)
	}
	if string(data) != `"36"` {
		t.Fatalf("json = %s, want %q", data, `"36"`)
	}

	var decoded ID
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal decimal nid: %v", err)
	}
	if decoded != id {
		t.Fatalf("decoded nid = %d, want %d", decoded, id)
	}

	if err := decoded.UnmarshalBinary([]byte("aaaaba")); err != nil {
		t.Fatalf("unmarshal binary nid: %v", err)
	}
	if decoded != id {
		t.Fatalf("binary decoded nid = %d, want %d", decoded, id)
	}

	if err := json.Unmarshal([]byte(`"2176782336"`), &decoded); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("out of range error = %v, want %v", err, ErrOutOfRange)
	}
}

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

func TestReserve(t *testing.T) {
	g := New(Config{Start: 0})
	id := ID(36)

	if !g.Reserve(id) {
		t.Fatal("first reserve failed")
	}
	if g.Reserve(id) {
		t.Fatal("second reserve succeeded, want duplicate rejection")
	}
}
