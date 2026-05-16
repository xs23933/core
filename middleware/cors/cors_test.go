package cors

import "testing"

func TestMatchOriginWildcardWithCredentials(t *testing.T) {
	rules := parseOrigins("*")

	if got := matchOrigin("https://app.example.com", rules, false); got != "*" {
		t.Fatalf("without credentials = %q, want *", got)
	}
	if got := matchOrigin("https://app.example.com", rules, true); got != "" {
		t.Fatalf("with credentials = %q, want denied wildcard", got)
	}
}

func TestMatchOriginSubdomainWildcard(t *testing.T) {
	rules := parseOrigins("*.example.com")

	if got := matchOrigin("https://api.example.com", rules, true); got != "https://api.example.com" {
		t.Fatalf("subdomain = %q, want allowed origin", got)
	}
	if got := matchOrigin("https://badexample.com", rules, true); got != "" {
		t.Fatalf("suffix-only host matched unexpectedly: %q", got)
	}
	if got := matchOrigin("https://example.com", rules, true); got != "" {
		t.Fatalf("root domain matched unexpectedly: %q", got)
	}
}

func TestMatchOriginExact(t *testing.T) {
	rules := parseOrigins("https://example.com, https://app.example.com")

	if got := matchOrigin("https://app.example.com", rules, false); got != "https://app.example.com" {
		t.Fatalf("exact origin = %q, want allowed origin", got)
	}
	if got := matchOrigin("http://app.example.com", rules, false); got != "" {
		t.Fatalf("scheme mismatch matched unexpectedly: %q", got)
	}
	if got := matchOrigin("not an origin", rules, false); got != "" {
		t.Fatalf("invalid origin matched unexpectedly: %q", got)
	}
}
