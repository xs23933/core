package etcd

import "testing"

func TestOptionsNamespacePrefixes(t *testing.T) {
	tests := []struct {
		name          string
		namespace     string
		serviceRoot   string
		servicePrefix string
		serviceKey    string
		configPrefix  string
	}{
		{
			name:          "legacy",
			serviceRoot:   "/services/",
			servicePrefix: "/services/auth/",
			serviceKey:    "/services/auth/auth-1",
			configPrefix:  "/config/",
		},
		{
			name:          "namespaced",
			namespace:     " /xpay//dev/ ",
			serviceRoot:   "/xpay/dev/services/",
			servicePrefix: "/xpay/dev/services/auth/",
			serviceKey:    "/xpay/dev/services/auth/auth-1",
			configPrefix:  "/xpay/dev/config/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &Options{
				Namespace:   tt.namespace,
				ServiceName: "auth",
				ServiceID:   "auth-1",
			}
			if got := opts.ServiceRoot(); got != tt.serviceRoot {
				t.Fatalf("ServiceRoot() = %q, want %q", got, tt.serviceRoot)
			}
			if got := opts.ServicePrefix(); got != tt.servicePrefix {
				t.Fatalf("ServicePrefix() = %q, want %q", got, tt.servicePrefix)
			}
			if got := opts.ServiceKey(); got != tt.serviceKey {
				t.Fatalf("ServiceKey() = %q, want %q", got, tt.serviceKey)
			}
			if got := opts.ConfigPrefix(); got != tt.configPrefix {
				t.Fatalf("ConfigPrefix() = %q, want %q", got, tt.configPrefix)
			}
		})
	}
}

func TestParseServiceKeyUsesExpectedRoot(t *testing.T) {
	tests := []struct {
		name                string
		key                 string
		root                string
		wantService, wantID string
		wantOK              bool
	}{
		{name: "legacy", key: "/services/auth/auth-1", root: "/services/", wantService: "auth", wantID: "auth-1", wantOK: true},
		{name: "namespaced", key: "/xpay/services/auth/auth-1", root: "/xpay/services/", wantService: "auth", wantID: "auth-1", wantOK: true},
		{name: "foreign root", key: "/union/services/auth/auth-1", root: "/xpay/services/"},
		{name: "missing instance", key: "/xpay/services/auth", root: "/xpay/services/"},
		{name: "extra segment", key: "/xpay/services/auth/auth-1/extra", root: "/xpay/services/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, id, ok := parseServiceKey(tt.key, tt.root)
			if service != tt.wantService || id != tt.wantID || ok != tt.wantOK {
				t.Fatalf("parseServiceKey(%q, %q) = (%q, %q, %v), want (%q, %q, %v)", tt.key, tt.root, service, id, ok, tt.wantService, tt.wantID, tt.wantOK)
			}
		})
	}
}
