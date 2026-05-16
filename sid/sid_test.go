package sid

import "testing"

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
