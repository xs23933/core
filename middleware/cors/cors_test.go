package cors

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xs23933/core/v3"
)

var _ core.HandlerFunc = New()
var _ core.HandlerFunc = New(Config{AllowOrigins: "https://example.com"})

func TestMatchOriginRules(t *testing.T) {
	tests := []struct {
		name    string
		allowed string
		origin  string
		want    string
	}{
		{name: "exact", allowed: "https://example.com", origin: "https://example.com", want: "https://example.com"},
		{name: "exact host case", allowed: "https://EXAMPLE.com", origin: "https://example.com", want: "https://example.com"},
		{name: "scheme mismatch", allowed: "https://example.com", origin: "http://example.com"},
		{name: "port mismatch", allowed: "https://example.com:8443", origin: "https://example.com"},
		{name: "bare wildcard subdomain", allowed: "*.example.com", origin: "https://api.example.com:8443", want: "https://api.example.com:8443"},
		{name: "wildcard root rejected", allowed: "*.example.com", origin: "https://example.com"},
		{name: "wildcard suffix confusion", allowed: "*.example.com", origin: "https://badexample.com"},
		{name: "scheme wildcard", allowed: "https://*.example.com", origin: "http://api.example.com"},
		{name: "scheme port wildcard", allowed: "https://*.example.com:8443", origin: "https://api.example.com:8443", want: "https://api.example.com:8443"},
		{name: "scheme port wildcard mismatch", allowed: "https://*.example.com:8443", origin: "https://api.example.com"},
		{name: "malformed rule", allowed: "://bad", origin: "https://bad"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := compileConfig(Config{AllowOrigins: tt.allowed})
			if got := cfg.matchOrigin(tt.origin); got != tt.want {
				t.Fatalf("matchOrigin(%q)=%q, want %q", tt.origin, got, tt.want)
			}
		})
	}
}

func TestMatchOriginWildcardWithCredentials(t *testing.T) {
	withoutCredentials := compileConfig(Config{AllowOrigins: "*"})
	withCredentials := compileConfig(Config{AllowOrigins: "*", AllowCredentials: true})

	if got := withoutCredentials.matchOrigin("https://app.example.com"); got != "*" {
		t.Fatalf("without credentials = %q, want *", got)
	}
	if got := withCredentials.matchOrigin("https://app.example.com"); got != "" {
		t.Fatalf("with credentials = %q, want denied wildcard", got)
	}
}

func TestMatchOriginSubdomainWildcard(t *testing.T) {
	policy := compileConfig(Config{AllowOrigins: "*.example.com"})

	if got := policy.matchOrigin("https://api.example.com"); got != "https://api.example.com" {
		t.Fatalf("subdomain = %q, want allowed origin", got)
	}
	if got := policy.matchOrigin("https://badexample.com"); got != "" {
		t.Fatalf("suffix-only host matched unexpectedly: %q", got)
	}
	if got := policy.matchOrigin("https://example.com"); got != "" {
		t.Fatalf("root domain matched unexpectedly: %q", got)
	}
}

func TestMatchOriginExact(t *testing.T) {
	policy := compileConfig(Config{AllowOrigins: "https://example.com, https://app.example.com"})

	if got := policy.matchOrigin("https://app.example.com"); got != "https://app.example.com" {
		t.Fatalf("exact origin = %q, want allowed origin", got)
	}
	if got := policy.matchOrigin("http://app.example.com"); got != "" {
		t.Fatalf("scheme mismatch matched unexpectedly: %q", got)
	}
	if got := policy.matchOrigin("not an origin"); got != "" {
		t.Fatalf("invalid origin matched unexpectedly: %q", got)
	}
}

func TestPreflightRequest(t *testing.T) {
	app := core.New()
	app.Use(New(Config{
		AllowOrigins:     "https://app.example.com",
		AllowMethods:     "GET,POST,OPTIONS",
		AllowHeaders:     "Content-Type,Authorization",
		AllowCredentials: true,
		MaxAge:           "600",
	}))

	rec := performRequest(app, http.MethodOptions, "/users", map[string]string{
		core.HeaderOrigin:                      "https://app.example.com",
		core.HeaderAccessControlRequestMethod:  http.MethodPost,
		core.HeaderAccessControlRequestHeaders: "content-type, authorization",
	})

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusNoContent)
	}
	assertHeader(t, rec, core.HeaderAccessControlAllowOrigin, "https://app.example.com")
	assertHeader(t, rec, core.HeaderAccessControlAllowMethods, "GET,POST,OPTIONS")
	assertHeader(t, rec, core.HeaderAccessControlAllowHeaders, "Content-Type,Authorization")
	assertHeader(t, rec, core.HeaderAccessControlAllowCredentials, "true")
	assertHeader(t, rec, core.HeaderAccessControlMaxAge, "600")
	assertVary(t, rec, core.HeaderOrigin)
	assertVary(t, rec, core.HeaderAccessControlRequestMethod)
}

func TestPreflightRequestRejectsPolicyViolations(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{
			name: "origin",
			headers: map[string]string{
				core.HeaderOrigin:                     "https://evil.example.com",
				core.HeaderAccessControlRequestMethod: http.MethodPost,
			},
		},
		{
			name: "method",
			headers: map[string]string{
				core.HeaderOrigin:                     "https://app.example.com",
				core.HeaderAccessControlRequestMethod: http.MethodDelete,
			},
		},
		{
			name: "header",
			headers: map[string]string{
				core.HeaderOrigin:                      "https://app.example.com",
				core.HeaderAccessControlRequestMethod:  http.MethodPost,
				core.HeaderAccessControlRequestHeaders: "X-Forbidden",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := core.New()
			app.Use(New(Config{
				AllowOrigins: "https://app.example.com",
				AllowMethods: "GET,POST,OPTIONS",
				AllowHeaders: "Content-Type,Authorization",
			}))

			rec := performRequest(app, http.MethodOptions, "/users", tt.headers)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d, want %d", rec.Code, http.StatusForbidden)
			}
		})
	}
}

func TestPreflightRequestReflectsHeaders(t *testing.T) {
	app := core.New()
	app.Use(New(Config{
		AllowOrigins: "https://app.example.com",
		AllowMethods: "POST,OPTIONS",
	}))

	rec := performRequest(app, http.MethodOptions, "/users", map[string]string{
		core.HeaderOrigin:                      "https://app.example.com",
		core.HeaderAccessControlRequestMethod:  http.MethodPost,
		core.HeaderAccessControlRequestHeaders: "X-Trace-ID, Content-Type",
	})

	assertHeader(t, rec, core.HeaderAccessControlAllowHeaders, "X-Trace-ID, Content-Type")
	assertVary(t, rec, core.HeaderAccessControlRequestHeaders)
}

func TestPreflightWildcardDoesNotVaryByOrigin(t *testing.T) {
	app := core.New()
	app.Use(New())

	rec := performRequest(app, http.MethodOptions, "/users", map[string]string{
		core.HeaderOrigin:                     "https://app.example.com",
		core.HeaderAccessControlRequestMethod: http.MethodGet,
	})

	assertHeader(t, rec, core.HeaderAccessControlAllowOrigin, "*")
	assertNotVary(t, rec, core.HeaderOrigin)
}

func TestPreflightWildcardWithCredentialsIsRejected(t *testing.T) {
	app := core.New()
	app.Use(New(Config{AllowOrigins: "*", AllowCredentials: true}))

	rec := performRequest(app, http.MethodOptions, "/users", map[string]string{
		core.HeaderOrigin:                     "https://app.example.com",
		core.HeaderAccessControlRequestMethod: http.MethodGet,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestOrdinaryOptionsRequestFallsThrough(t *testing.T) {
	app := core.New()
	app.Use(New())

	rec := performRequest(app, http.MethodOptions, "/users", map[string]string{
		core.HeaderOrigin: "https://app.example.com",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestActualRequestCORSHeaders(t *testing.T) {
	app := core.New()
	app.Use(New(Config{
		AllowOrigins:     "https://app.example.com",
		AllowCredentials: true,
		ExposeHeaders:    "X-Request-ID",
	}))
	app.GET("/users", func(c core.Ctx) error { return c.SendString("ok") })

	rec := performRequest(app, http.MethodGet, "/users", map[string]string{
		core.HeaderOrigin: "https://app.example.com",
	})

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("status/body=%d/%q, want 200/ok", rec.Code, rec.Body.String())
	}
	assertHeader(t, rec, core.HeaderAccessControlAllowOrigin, "https://app.example.com")
	assertHeader(t, rec, core.HeaderAccessControlAllowCredentials, "true")
	assertHeader(t, rec, core.HeaderAccessControlExposeHeaders, "X-Request-ID")
	assertVary(t, rec, core.HeaderOrigin)
}

func TestActualRequestWithDeniedOriginReachesHandler(t *testing.T) {
	app := core.New()
	app.Use(New(Config{AllowOrigins: "https://app.example.com"}))
	app.GET("/users", func(c core.Ctx) error { return c.SendString("ok") })

	rec := performRequest(app, http.MethodGet, "/users", map[string]string{
		core.HeaderOrigin: "https://evil.example.com",
	})

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("status/body=%d/%q, want 200/ok", rec.Code, rec.Body.String())
	}
	assertHeader(t, rec, core.HeaderAccessControlAllowOrigin, "")
}

func TestExplicitOptionsRouteHasPriorityOverCORSFallback(t *testing.T) {
	app := core.New()
	app.Use(New())
	app.OPTIONS("/users", func(c core.Ctx) error {
		c.Status(http.StatusAccepted)
		c.Response().DoWriteHeader()
		return nil
	})

	rec := performRequest(app, http.MethodOptions, "/users", map[string]string{
		core.HeaderOrigin:                     "https://app.example.com",
		core.HeaderAccessControlRequestMethod: http.MethodGet,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusAccepted)
	}
}

func TestNewWithAppHandlesLegacyPreflight(t *testing.T) {
	app := core.New()
	app.Use(NewWithApp(app, Config{
		AllowOrigins: "https://app.example.com",
		AllowMethods: "GET,OPTIONS",
	}))

	rec := performRequest(app, http.MethodOptions, "/users", map[string]string{
		core.HeaderOrigin:                     "https://app.example.com",
		core.HeaderAccessControlRequestMethod: http.MethodGet,
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusNoContent)
	}
}

func performRequest(app *core.Core, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

func assertHeader(t *testing.T, rec *httptest.ResponseRecorder, key, want string) {
	t.Helper()
	if got := rec.Header().Get(key); got != want {
		t.Fatalf("header %s=%q, want %q", key, got, want)
	}
}

func assertVary(t *testing.T, rec *httptest.ResponseRecorder, field string) {
	t.Helper()
	if !hasHeaderToken(rec.Header().Values(core.HeaderVary), field) {
		t.Fatalf("Vary=%q does not include %q", rec.Header().Values(core.HeaderVary), field)
	}
}

func assertNotVary(t *testing.T, rec *httptest.ResponseRecorder, field string) {
	t.Helper()
	if hasHeaderToken(rec.Header().Values(core.HeaderVary), field) {
		t.Fatalf("Vary=%q unexpectedly includes %q", rec.Header().Values(core.HeaderVary), field)
	}
}

func hasHeaderToken(values []string, want string) bool {
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), want) {
				return true
			}
		}
	}
	return false
}
