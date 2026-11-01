package gateway

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/xs23933/core/v3"
)

func TestGatewayProtectedIdentityMetadata(t *testing.T) {
	headers := http.Header{}
	headers.Add("User-Id", "forged-1")
	headers.Add("USER-ID", "forged-2")
	headers.Set("Core-Identity-Tenant", "forged-tenant")
	headers.Set("Authorization", "Bearer token")
	headers.Add("X-Sign", "one")
	headers.Add("X-Sign", "two")
	for _, tc := range []struct {
		name                 string
		vars                 core.Map
		wantUser, wantTenant string
	}{
		{"missing middleware", nil, "", ""},
		{"verified middleware", core.Map{"USER-ID": "verified-user", "core-identity-tenant": "verified-tenant"}, "verified-user", "verified-tenant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			md := gatewayRequestMetadata(headers, tc.vars, []string{" User-ID "})
			if got := md.Get("user-id"); len(got) > 0 && got[0] != tc.wantUser || tc.wantUser != "" && len(got) != 1 {
				t.Fatalf("identity user=%v", got)
			}
			if tc.wantUser == "" && len(md.Get("user-id")) != 0 {
				t.Fatal("forged user survived")
			}
			gotTenant := md.Get("core-identity-tenant")
			if tc.wantTenant == "" && len(gotTenant) != 0 || tc.wantTenant != "" && !reflect.DeepEqual(gotTenant, []string{tc.wantTenant}) {
				t.Fatalf("tenant=%v", gotTenant)
			}
			if !reflect.DeepEqual(md.Get("x-sign"), []string{"one", "two"}) || !reflect.DeepEqual(md.Get("authorization"), []string{"Bearer token"}) {
				t.Fatal("raw authentication/signature headers changed")
			}
		})
	}
	if headers.Get("Core-Identity-Tenant") != "forged-tenant" {
		t.Fatal("raw headers needed for signatures were mutated")
	}
	// An empty protection list preserves existing legacy custom-header contracts.
	if got := gatewayRequestMetadata(headers, nil, nil).Get("user-id"); len(got) != 2 {
		t.Fatalf("legacy custom headers changed: %v", got)
	}
}
