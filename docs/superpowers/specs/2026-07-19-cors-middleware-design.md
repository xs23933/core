# CORS Middleware Optimization Design

## Goal

Refactor `middleware/cors` into a type-safe, side-effect-free middleware while preserving a clear compatibility path for callers that rely on automatic preflight route registration. Improve CORS correctness, security, request-path efficiency, documentation, and integration-test coverage without changing unrelated routing behavior.

## Public API

The recommended API becomes:

```go
app.Use(cors.New(cors.Config{
	AllowOrigins:     "https://app.example.com",
	AllowMethods:     "GET,POST,PUT,DELETE,OPTIONS",
	AllowHeaders:     "Content-Type,Authorization",
	AllowCredentials: true,
}))
```

`New(config ...Config) core.HandlerFunc` only constructs a middleware. It does not register routes or otherwise mutate `*core.Core`.

For migration, retain:

```go
app.Use(cors.NewWithApp(app, config))
```

`NewWithApp(app *core.Core, config ...Config) core.HandlerFunc` is marked deprecated. It registers the legacy catch-all OPTIONS route and returns the same middleware produced by `New`, so both entry points share one implementation and behavior.

This is a source-breaking rename for existing `cors.New(app, ...)` calls. The migration is mechanical: use `cors.New(...)` for the new router fallback behavior, or temporarily use `cors.NewWithApp(app, ...)` when legacy explicit preflight registration is required.

## Router Fallback

The current router returns 404 before global middleware executes when an OPTIONS request has no explicit route. A pure CORS middleware therefore cannot handle browser preflight requests on its own.

`Core.ServeHTTP` will add a narrow fallback:

1. Attempt normal method-and-path matching first.
2. Explicit OPTIONS routes retain priority and use the unchanged matched handler chain.
3. If and only if an OPTIONS request has no matching route, run the OPTIONS tree's global middleware followed by a terminal 404 handler.
4. A CORS middleware may terminate a valid preflight request with 204 or reject it with 403.
5. If no middleware handles the request, the terminal handler preserves the existing 404 response.

The fallback does not execute path-scoped middleware because no route path matched. CORS is expected to be installed globally. Other HTTP methods retain the current not-found path.

The router exposes `core.IsRouteFallback(c)` without extending the `Ctx` interface. CORS terminates preflight only in this fallback chain. When an explicit OPTIONS route matched, CORS may attach applicable actual-response headers but calls `c.Next()`, allowing the explicit handler to retain priority.

## Request Handling

### Preflight requests

An OPTIONS request is treated as CORS preflight only when both `Origin` and `Access-Control-Request-Method` are present. Ordinary OPTIONS requests call `c.Next()`.

For a preflight request, the middleware validates:

- the request origin against `AllowOrigins`;
- the requested method against `AllowMethods`;
- every requested header against `AllowHeaders` when an explicit header list is configured.

An invalid origin, method, or header returns 403. An allowed preflight sets the applicable CORS headers and returns 204 without invoking later handlers.

When `AllowHeaders` is empty, retain the existing compatibility behavior: reflect `Access-Control-Request-Headers` into `Access-Control-Allow-Headers`.

### Actual requests

Requests without `Origin` take a fast path directly to `c.Next()`.

For requests with an allowed origin, set `Access-Control-Allow-Origin`, optional credential and expose headers, then call `c.Next()`. Requests with a disallowed or malformed origin still reach the application but do not receive CORS allow headers. This preserves existing business-request behavior while letting the browser enforce the policy.

`AllowOrigins: "*"` with `AllowCredentials: true` remains a deny-all combination. The middleware will not silently reinterpret it as origin reflection because that would expand the current security policy.

## Configuration Compilation and Matching

Keep the existing `Config` fields and comma-separated string format:

- `AllowOrigins`
- `AllowHeaders`
- `AllowMethods`
- `AllowCredentials`
- `ExposeHeaders`
- `MaxAge`

At construction time, trim, normalize, validate, and compile configuration once:

- exact origins are stored in a map for constant-time lookup;
- wildcard subdomain rules are stored separately;
- allowed methods and headers use case-insensitive lookup sets;
- response header strings are prepared once and reused per request.

Exact origin rules compare normalized scheme, hostname, and effective explicit port. Wildcard rules have these semantics:

- `*.example.com` matches real subdomains of `example.com` under any valid origin scheme and port;
- it does not match `example.com` or `badexample.com`;
- `https://*.example.com` additionally requires HTTPS;
- `https://*.example.com:8443` additionally requires port 8443.

Malformed origin rules are retained as non-matching rules rather than causing a panic or accidentally broadening access.

The default wildcard-without-credentials case avoids URL parsing and returns `*` immediately. Other requests parse the request origin at most once.

## Response Caching

Set `Vary` only for request values that can change the generated response:

- `Origin` when the allow-origin response is origin-specific;
- `Access-Control-Request-Method` for preflight responses;
- `Access-Control-Request-Headers` when requested headers are reflected.

Use the framework's header helpers without adding duplicate `Vary` tokens. The default unrestricted `*` response does not vary by origin.

## Tests

Retain focused rule-unit tests and add end-to-end `httptest` coverage through `Core.ServeHTTP` for:

- default configuration and the no-Origin fast path;
- exact origins;
- wildcard subdomains, root-domain rejection, and suffix-confusion rejection;
- scheme and port constraints;
- wildcard plus credentials rejection;
- allowed and rejected requested methods;
- allowed and rejected requested headers;
- reflected request headers when `AllowHeaders` is empty;
- credential, expose, max-age, and `Vary` response headers;
- malformed origins and malformed configuration rules;
- ordinary non-preflight OPTIONS requests;
- unmatched OPTIONS handled by global CORS middleware;
- explicit OPTIONS route priority;
- unmatched OPTIONS remaining 404 when CORS is not installed;
- actual requests with disallowed origins still reaching the business handler without CORS allow headers;
- `NewWithApp` compatibility behavior.

Run the focused package tests first, followed by the repository verification required by `AGENTS.md`:

```bash
gofmt -w <changed-go-files>
GOCACHE=/private/tmp/core-gocache go test ./... -count=1
git diff --check
```

## Documentation and Compatibility

Update `README.md`, `AI_CONTEXT.md`, `skills/core-middleware.md`, `skills/core-skills.md`, examples, and generator documentation or templates that still use `cors.New(app)`.

Document the source migration from `cors.New(app, config...)` to `cors.New(config...)`, with `cors.NewWithApp(app, config...)` as the deprecated transition path. No configuration field is removed, and default CORS policy values remain unchanged.

The only intentional framework behavior addition is the narrowly scoped unmatched-OPTIONS global-middleware fallback. Explicit routes, non-OPTIONS not-found behavior, and applications without a preflight-handling middleware preserve their current outcomes.
