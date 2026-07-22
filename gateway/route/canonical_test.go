package route

import "testing"

func TestCanonicalPathErasesOnlyParameterNames(t *testing.T) {
	got, err := CanonicalPath("/api/users/:id/orders/:orderId")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/api/users/:/orders/:"; got != want {
		t.Fatalf("CanonicalPath() = %q, want %q", got, want)
	}
}

func TestCanonicalSlotIDUsesMethodAndFullPath(t *testing.T) {
	first, err := SlotID("get", "/api/users/:id")
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := SlotID("GET", "/api/users/:userID")
	if err != nil {
		t.Fatal(err)
	}
	otherPath, err := SlotID("GET", "/admin/users/:id")
	if err != nil {
		t.Fatal(err)
	}
	if first != renamed {
		t.Fatalf("parameter names changed slot: %s != %s", first, renamed)
	}
	if first == otherPath {
		t.Fatal("different full paths share a slot")
	}
}

func TestCanonicalPathRejectsCoreInvalidDynamicSyntax(t *testing.T) {
	for _, path := range []string{
		"/api/:id?/details",
		"/api/*path/details",
		"/api/:id??",
		"/api/:id*",
		"/api/::id",
		"/api/*path*",
		"/api/*path?",
	} {
		t.Run(path, func(t *testing.T) {
			if _, err := CanonicalPath(path); err == nil {
				t.Fatalf("CanonicalPath(%q) error = nil, want Core-invalid path error", path)
			}
		})
	}
}
