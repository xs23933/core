package sid

import (
	"encoding/json"
	"testing"
)

func TestGenSID(t *testing.T) {
	sid, err := New(1)
	if err != nil {
		t.Fatalf("new sid generator: %v", err)
	}

	var prev ID
	for range 100 {
		id, err := sid.Generate()
		if err != nil {
			t.Fatalf("generate sid: %v", err)
		}
		if id == 0 {
			t.Fatal("generated zero sid")
		}
		if prev != 0 && id <= prev {
			t.Fatalf("generated sid %v after %v, want increasing ids", id, prev)
		}

		str := id.Encode()
		nid, err := Decode(str)
		if err != nil {
			t.Fatalf("decode %q: %v", str, err)
		}
		if nid != id {
			t.Fatalf("decoded sid = %v, want %v", nid, id)
		}
		prev = id
	}
}

func TestIDCompatibility(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  ID
	}{
		{name: "int", input: int(123), want: 123},
		{name: "uint64", input: uint64(123), want: 123},
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

func TestRejectUint64Overflow(t *testing.T) {
	var id ID
	if err := id.Scan(uint64(^uint64(0))); err == nil {
		t.Fatal("scan overflowing uint64 error = nil, want error")
	}
}
