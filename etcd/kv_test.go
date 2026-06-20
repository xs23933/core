package etcd

import "testing"

func TestKVKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "relative config key", key: "gateway/public_routes", want: "/config/gateway/public_routes"},
		{name: "absolute route key", key: "/gateway/routes/route-id", want: "/gateway/routes/route-id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kvKey(tt.key); got != tt.want {
				t.Fatalf("kvKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}
