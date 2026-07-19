# CORS Middleware Optimization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn CORS into a side-effect-free, type-safe middleware with correct and efficient origin/preflight validation, a deprecated compatibility entry point, and a narrowly scoped unmatched-OPTIONS router fallback.

**Architecture:** Compile `cors.Config` once into immutable lookup structures used by one middleware handler. Extend `Core.ServeHTTP` only for unmatched OPTIONS requests so global middleware can handle preflight while a terminal handler preserves 404. Keep legacy route mutation isolated in `NewWithApp` and make all request behavior share the same compiled implementation.

**Tech Stack:** Go 1.25, `net/http`, `net/url`, Core v3 router and middleware APIs, `httptest`, standard `testing`.

## Global Constraints

- Preserve all existing `cors.Config` fields and default values.
- `cors.New(config ...Config)` must not mutate `*core.Core`.
- `cors.NewWithApp(app *core.Core, config ...Config)` is deprecated and is the only compatibility API that registers `OPTIONS /*`.
- Explicit OPTIONS routes have priority over fallback middleware.
- Applications without a preflight-handling middleware retain 404 for unmatched OPTIONS.
- `AllowOrigins: "*"` with `AllowCredentials: true` remains deny-all.
- Do not add dependencies.
- Do not commit, stage, push, or create a branch unless the user explicitly authorizes it.
- Use `GOCACHE=/private/tmp/core-gocache` for Go tests.

## File Map

- Modify `route.go`: add the unmatched-OPTIONS global-middleware fallback and terminal 404 handler.
- Create `route_options_test.go`: lock fallback, explicit-route priority, and unchanged 404 behavior at router level.
- Modify `middleware/cors/cors.go`: expose the new API, compatibility API, compiled configuration, matching, and request handling.
- Modify `middleware/cors/cors_test.go`: unit tests for configuration/rules and integration tests through `Core.ServeHTTP`.
- Modify `README.md`: replace old calls and document configuration, wildcard semantics, and migration.
- Modify `AI_CONTEXT.md`: update the CORS quick-reference API.
- Modify `skills/core-middleware.md`: update middleware guidance and preflight behavior.
- Modify `skills/core-skills.md`: update indexed examples and CORS summary.
- Modify `skills/core-test.md`: update the CORS test example.
- Modify `skills/core-corectl.md`: update generated-code examples.
- Modify `example/restful/main.go`: use the new API.

---

### Task 1: Unmatched OPTIONS Router Fallback

**Files:**
- Modify: `route.go:134-165`
- Create: `route_options_test.go`

**Interfaces:**
- Consumes: `Core.Use(fn ...any) Router`, `RouteNode.middlewares`, `Ctx.Next() error`, `Ctx.SendStatus(code int, msg ...string) error`.
- Produces: unmatched OPTIONS requests run the OPTIONS root's global middleware followed by a 404 handler; all other matching behavior is unchanged.

- [ ] **Step 1: Write failing router tests**

Create tests equivalent to:

```go
package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnmatchedOptionsRunsGlobalMiddleware(t *testing.T) {
	app := New()
	called := false
	app.Use(func(c Ctx) error {
		called = true
		return c.SendStatus(StatusNoContent)
	})

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/users", nil))

	if !called || rec.Code != http.StatusNoContent {
		t.Fatalf("called=%v status=%d, want true/%d", called, rec.Code, http.StatusNoContent)
	}
}

func TestUnmatchedOptionsWithoutHandlerRemainsNotFound(t *testing.T) {
	app := New()
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/users", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestExplicitOptionsRouteHasPriority(t *testing.T) {
	app := New()
	app.Use(func(c Ctx) error { return c.SendStatus(StatusNoContent) })
	app.OPTIONS("/users", func(c Ctx) error {
		return c.SendStatus(StatusAccepted)
	})

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/users", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusAccepted)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify the new fallback test fails**

Run:

```bash
GOCACHE=/private/tmp/core-gocache go test . -run 'Test(UnmatchedOptions|ExplicitOptions)' -count=1
```

Expected: `TestUnmatchedOptionsRunsGlobalMiddleware` fails with `called=false` and status 404; the other assertions describe existing behavior.

- [ ] **Step 3: Implement the narrow fallback**

Refactor the failed-match branch in `Core.ServeHTTP` to use a helper with this contract:

```go
func (app *Core) serveUnmatchedOptions(c Ctx, root *RouteNode) bool {
	if c.Method() != MethodOptions || len(root.middlewares) == 0 {
		return false
	}

	handlers := make(HandlerFuncs, 0, len(root.middlewares)+1)
	handlers = append(handlers, root.middlewares...)
	handlers = append(handlers, func(c Ctx) error {
		return c.SendStatus(StatusNotFound, ErrNotFound.Error())
	})

	baseCtx, ok := c.(*BaseCtx)
	if !ok {
		return false
	}
	baseCtx.handlers = handlers
	baseCtx.indexHandler = -1
	return true
}
```

Use the same existing error-to-response handling after either a normal match or this fallback. Do not run path-scoped middleware and do not change non-OPTIONS branches.

- [ ] **Step 4: Run router tests**

Run:

```bash
gofmt -w route.go route_options_test.go
GOCACHE=/private/tmp/core-gocache go test . -run 'Test(UnmatchedOptions|ExplicitOptions)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Review checkpoint**

Inspect `git diff -- route.go route_options_test.go` and confirm that the fallback is restricted to unmatched OPTIONS, uses only root global middleware, and ends in the existing 404 response.

### Task 2: Compile CORS Policy and Match Origins

**Files:**
- Modify: `middleware/cors/cors.go`
- Modify: `middleware/cors/cors_test.go`

**Interfaces:**
- Consumes: the existing public `Config` fields.
- Produces: `New(config ...Config) core.HandlerFunc`, `NewWithApp(app *core.Core, config ...Config) core.HandlerFunc`, internal `compileConfig(Config) compiledConfig`, and `compiledConfig.matchOrigin(string) string`.

- [ ] **Step 1: Expand failing rule and API tests**

Add table-driven tests that assert:

```go
func TestMatchOriginRules(t *testing.T) {
	tests := []struct {
		name    string
		allowed string
		origin  string
		want    string
	}{
		{"exact", "https://example.com", "https://example.com", "https://example.com"},
		{"exact host case", "https://EXAMPLE.com", "https://example.com", "https://example.com"},
		{"scheme mismatch", "https://example.com", "http://example.com", ""},
		{"port mismatch", "https://example.com:8443", "https://example.com", ""},
		{"bare wildcard subdomain", "*.example.com", "https://api.example.com:8443", "https://api.example.com:8443"},
		{"wildcard root rejected", "*.example.com", "https://example.com", ""},
		{"wildcard suffix confusion", "*.example.com", "https://badexample.com", ""},
		{"scheme wildcard", "https://*.example.com", "http://api.example.com", ""},
		{"scheme port wildcard", "https://*.example.com:8443", "https://api.example.com:8443", "https://api.example.com:8443"},
		{"scheme port wildcard mismatch", "https://*.example.com:8443", "https://api.example.com", ""},
		{"malformed rule", "://bad", "https://bad", ""},
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
```

Also add compile-time usage assertions in tests:

```go
var _ core.HandlerFunc = New()
var _ core.HandlerFunc = New(Config{AllowOrigins: "https://example.com"})
```

- [ ] **Step 2: Run the focused tests and verify compilation fails**

Run:

```bash
GOCACHE=/private/tmp/core-gocache go test ./middleware/cors -run 'TestMatchOriginRules' -count=1
```

Expected: FAIL because the new typed `New` signature and compiled policy do not exist.

- [ ] **Step 3: Implement immutable compiled configuration**

Keep `Config` public and introduce focused internal types:

```go
type compiledConfig struct {
	allowAny        bool
	allowCredentials bool
	exactOrigins    map[string]struct{}
	wildcardOrigins []originRule
	allowMethods    map[string]struct{}
	allowHeaders    map[string]struct{}
	allowMethodsRaw string
	allowHeadersRaw string
	exposeHeaders   string
	maxAge          string
}

type originRule struct {
	scheme string
	host   string
	port   string
}
```

Implement `compileConfig`, `parseOriginRule`, `normalizeOrigin`, and `matchOrigin` so configuration parsing happens only in `New`. Normalize schemes and hostnames to lowercase, preserve explicit ports, require a strict subdomain boundary, and make malformed rules non-matching. Keep the `*` plus credentials deny-all rule.

- [ ] **Step 4: Implement the typed API and deprecated compatibility shell**

Use these public signatures:

```go
func New(config ...Config) core.HandlerFunc

// NewWithApp constructs CORS middleware and registers a legacy catch-all
// preflight route.
// Deprecated: use app.Use(cors.New(config...)).
func NewWithApp(app *core.Core, config ...Config) core.HandlerFunc
```

`NewWithApp` must compile once, register an OPTIONS handler backed by the same compiled middleware behavior, and return that same handler. Do not duplicate policy logic.

- [ ] **Step 5: Run origin and package tests**

Run:

```bash
gofmt -w middleware/cors/cors.go middleware/cors/cors_test.go
GOCACHE=/private/tmp/core-gocache go test ./middleware/cors -count=1
```

Expected: PASS.

- [ ] **Step 6: Review checkpoint**

Inspect `git diff -- middleware/cors/cors.go middleware/cors/cors_test.go`. Confirm all configuration parsing is construction-time, request matching performs at most one URL parse, and no new exported configuration surface was added.

### Task 3: Correct Preflight and Actual-Request Behavior

**Files:**
- Modify: `middleware/cors/cors.go`
- Modify: `middleware/cors/cors_test.go`

**Interfaces:**
- Consumes: `compiledConfig` and router fallback from Tasks 1-2.
- Produces: one handler that validates preflight method/headers, emits correct headers and `Vary`, and preserves actual-request flow.

- [ ] **Step 1: Add failing end-to-end request tests**

Build a small test helper:

```go
func performRequest(app *core.Core, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}
```

Add table-driven integration cases covering these exact outcomes:

- allowed preflight returns 204 and allow-origin/method/header/max-age headers;
- disallowed origin, requested method, or requested header returns 403;
- empty `AllowHeaders` reflects requested headers and varies by `Access-Control-Request-Headers`;
- preflight varies by `Access-Control-Request-Method` and by `Origin` for origin-specific policies;
- ordinary OPTIONS without `Access-Control-Request-Method` reaches the 404 terminal handler;
- default `*` response omits `Vary: Origin`;
- allowed actual request reaches a registered handler and exposes configured headers;
- disallowed actual request reaches the handler without `Access-Control-Allow-Origin`;
- credential configuration emits `Access-Control-Allow-Credentials: true`;
- wildcard plus credentials returns 403 for preflight;
- an explicit OPTIONS route returns its own status instead of the fallback CORS response;
- `NewWithApp` returns 204 for a valid legacy preflight.

Use `http.Header.Values("Vary")` or a token helper rather than comparing one serialized `Vary` string, because the framework may append header lines.

- [ ] **Step 2: Run integration tests and verify behavior failures**

Run:

```bash
GOCACHE=/private/tmp/core-gocache go test ./middleware/cors -run 'Test(Preflight|ActualRequest|NewWithApp)' -count=1
```

Expected: FAIL on method/header validation and `Vary` behavior until request handling is implemented.

- [ ] **Step 3: Implement preflight detection and validation**

Use this control flow:

```go
if c.Method() == core.MethodOptions {
	origin := c.GetHeader(core.HeaderOrigin)
	requestMethod := c.GetHeader(core.HeaderAccessControlRequestMethod)
	if origin == "" || requestMethod == "" {
		return c.Next()
	}
	return policy.handlePreflight(c, origin, requestMethod)
}

origin := c.GetHeader(core.HeaderOrigin)
if origin == "" {
	return c.Next()
}
return policy.handleActual(c, origin)
```

In `handlePreflight`, validate origin first, uppercase and validate the requested method, split requested headers by comma, trim them, and compare lowercase names against the configured set. Return 403 on the first failed policy check and 204 after setting response headers.

- [ ] **Step 4: Implement exact response and Vary rules**

Set:

- `Access-Control-Allow-Origin` for allowed requests;
- `Access-Control-Allow-Methods` on preflight;
- `Access-Control-Allow-Headers` when configured or reflected;
- `Access-Control-Allow-Credentials: true` only when enabled;
- `Access-Control-Expose-Headers` only on actual responses;
- `Access-Control-Max-Age` only on preflight.

Add `Vary: Origin` only for origin-specific results, always add `Vary: Access-Control-Request-Method` for handled preflight, and add `Vary: Access-Control-Request-Headers` only when reflecting requested headers. Add a small internal helper that checks existing comma-separated `Vary` tokens case-insensitively before calling `c.Vary(field)`.

- [ ] **Step 5: Run CORS and router tests**

Run:

```bash
gofmt -w middleware/cors/cors.go middleware/cors/cors_test.go
GOCACHE=/private/tmp/core-gocache go test ./middleware/cors . -count=1
```

Expected: PASS.

- [ ] **Step 6: Review checkpoint**

Confirm via `git diff` that an invalid actual request is not blocked, a rejected preflight cannot reach application handlers, ordinary OPTIONS calls `Next`, and explicit OPTIONS routes remain higher priority.

### Task 4: Migrate Documentation and Examples

**Files:**
- Modify: `README.md`
- Modify: `AI_CONTEXT.md`
- Modify: `skills/core-middleware.md`
- Modify: `skills/core-skills.md`
- Modify: `skills/core-test.md`
- Modify: `skills/core-corectl.md`
- Modify: `example/restful/main.go`

**Interfaces:**
- Consumes: `New(config ...Config)` and deprecated `NewWithApp(app, config ...Config)`.
- Produces: repository guidance consistently recommending the new API and explaining migration and wildcard behavior.

- [ ] **Step 1: Replace current recommended calls**

Replace every repository-owned example of:

```go
app.Use(cors.New(app))
app.Use(cors.New(app, cors.Config{...}))
```

with:

```go
app.Use(cors.New())
app.Use(cors.New(cors.Config{...}))
```

Run:

```bash
rg -n 'cors\.New\(app' README.md AI_CONTEXT.md skills example
```

Expected: no matches except a clearly labeled migration example showing the old form as text.

- [ ] **Step 2: Document policy and migration**

Add concise documentation stating:

```text
- New is a pure middleware constructor and handles preflight through the framework's unmatched-OPTIONS fallback.
- Exact origins include scheme and explicit port.
- *.example.com matches subdomains only; a scheme/port-qualified wildcard constrains those components.
- An empty AllowHeaders reflects Access-Control-Request-Headers.
- Wildcard origin cannot be combined with credentials in this implementation.
- Existing cors.New(app, ...) callers migrate to cors.New(...); NewWithApp is a deprecated transition API.
```

Keep the full example in `README.md`, the short API rule in `AI_CONTEXT.md`, and operational guidance in `skills/core-middleware.md`. Update indexed or generated examples without duplicating long explanations.

- [ ] **Step 3: Verify examples compile and references are consistent**

Run:

```bash
rg -n 'cors\.New|NewWithApp|AllowOrigins|AllowHeaders' README.md AI_CONTEXT.md skills example middleware/cors
GOCACHE=/private/tmp/core-gocache go test ./example/restful -run '^$' -count=1
git diff --check
```

Expected: the example package compiles, all recommended calls use `cors.New(config...)`, legacy usage is labeled deprecated, and `git diff --check` reports no errors.

- [ ] **Step 4: Review checkpoint**

Compare documentation against `middleware/cors/cors.go` and verify signatures, defaults, wildcard semantics, response behavior, and migration instructions agree exactly.

### Task 5: Repository Verification

**Files:**
- Verify all files changed in Tasks 1-4.

**Interfaces:**
- Consumes: completed router, CORS, tests, docs, and examples.
- Produces: evidence that the change is formatted, tested repository-wide, and whitespace-clean.

- [ ] **Step 1: Format all changed Go files**

Run:

```bash
gofmt -w route.go route_options_test.go middleware/cors/cors.go middleware/cors/cors_test.go example/restful/main.go
```

Expected: command exits 0.

- [ ] **Step 2: Run focused tests without cache reuse**

Run:

```bash
GOCACHE=/private/tmp/core-gocache go test ./middleware/cors . -count=1
```

Expected: PASS.

- [ ] **Step 3: Run repository tests without cache reuse**

Run:

```bash
GOCACHE=/private/tmp/core-gocache go test ./... -count=1
```

Expected: PASS. If an environment dependency prevents completion, record the exact failing package and error and do not claim repository-wide success.

- [ ] **Step 4: Run final diff checks**

Run:

```bash
git diff --check
git status --short
git diff --stat
```

Expected: no whitespace errors; status contains only task files plus any pre-existing user changes identified before implementation.

- [ ] **Step 5: Final compatibility review**

Confirm all of the following from code and test evidence:

```text
[ ] New has the typed side-effect-free signature.
[ ] NewWithApp is deprecated and shares the same policy implementation.
[ ] Explicit OPTIONS routes win.
[ ] Unhandled unmatched OPTIONS remains 404.
[ ] Non-OPTIONS not-found behavior is unchanged.
[ ] Preflight validates origin, method, and configured headers.
[ ] Actual denied origins still reach the handler without CORS allow headers.
[ ] Config defaults and fields are unchanged.
[ ] Documentation and examples use the new API.
```

Do not create a commit unless the user separately authorizes it.
