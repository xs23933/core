# Gateway Hot Path Optimization Design

## Scope

Only the `gateway` package is in scope.

This work optimizes the main gateway request path for high request volume without changing public route definitions, admin APIs, automatic gRPC route naming, or the `core` router implementation.

Out of scope:

- Changes to `core` route matching, context pooling, middleware order, or handler registration.
- New gateway user-facing configuration.
- A full health-check or load-balancer redesign.
- Business service contract changes.

## Goals

- Reduce per-request allocations in HTTP and gRPC proxy selection.
- Avoid creating a new reverse proxy on every HTTP proxy request.
- Propagate client cancellation to upstream gRPC calls.
- Reduce duplicated reflection/connect work when etcd emits repeated service events.
- Keep route and instance updates copy-on-write so reads remain lock-free.
- Add tests or benchmarks that protect the hot-path behavior.

## Current Findings

HTTP proxy requests currently parse the upstream address and create `httputil.NewSingleHostReverseProxy` inside the request handler. On a busy gateway this allocates proxy objects, directors, and URL state on every request.

HTTP service selection stores instances as `map[string]map[string]string`. `getHTTPInstance` builds a fresh `[]string` from the map on each request before round-robin selection.

gRPC service selection stores instances as `map[string]*ReflectionProxy`. `ServicePool.Get` builds a fresh `[]*ReflectionProxy` from the map on each request before round-robin selection.

gRPC proxy calls create outgoing contexts from `context.Background()`, so a client disconnect does not cancel the upstream gRPC request. The call still ends at the 10 second timeout, but under load this can keep work alive longer than necessary.

etcd service watch starts a goroutine for each PUT or DELETE event. Repeated PUTs for the same instance can run concurrent reflection discovery and connection replacement for the same target.

Circuit breaker half-open failure currently increments failure counters but does not immediately reopen the breaker when the half-open trial fails.

## Design

### HTTP Instance Snapshot

Replace the HTTP instance read model with a copy-on-write snapshot:

- Keep service instances grouped by service and instance ID for update and delete semantics.
- Add a stable per-service ordered `[]string` of addresses.
- Rebuild the address slice only when an instance is added, updated, or removed.
- `getHTTPInstance` reads the stored slice and uses the existing atomic round-robin counter without allocating.

The snapshot remains immutable after storing in `atomic.Value`.

### Reusable HTTP Reverse Proxy

Build the reverse proxy once when registering an HTTP route.

The proxy director should, per request:

- Select the current service instance with `getHTTPInstance`.
- Parse or reuse enough upstream target data to set scheme and host.
- Resolve `route.UpstreamPath` using current path params.
- Apply route-level header overrides.

The handler should still return `503` when there are no instances and `502` when an instance address is invalid.

The implementation should avoid shared mutable per-request state in the proxy. Per-request values should live on the cloned request or inside the director call.

### gRPC Pool Snapshot

Extend `ServicePool` to store immutable pool state containing both:

- `byID map[string]*ReflectionProxy`
- `all []*ReflectionProxy`

`AddOrUpdateInstance`, `RemoveInstance`, and `Close` rebuild the state under the existing mutex. `Get` reads the snapshot and round-robins through `all` without creating a new slice.

Connection health behavior remains the same: prefer `connectivity.Ready`; if no ready proxy exists, return the first proxy so existing fallback semantics are preserved.

### gRPC Context Propagation

Change gRPC proxy calls to derive from the inbound request context:

- Create metadata with existing header and local variable propagation.
- Use `metadata.NewOutgoingContext(ctx.Context(), md)`.
- Keep `context.WithTimeout(..., 10*time.Second)`.

This preserves timeout behavior while allowing client disconnects and upstream middleware cancellations to stop work earlier.

### Connection Attempt Deduplication

Add a small per-instance in-flight guard in `EtcdGateway`:

- Key by `serviceName + "/" + instanceID`.
- If a connect attempt is already running for the same key, skip starting another.
- Clear the key when the connect attempt returns.

This limits repeated etcd PUT storms from creating parallel reflection discovery for the same instance.

DELETE handling should remain simple and still remove the instance promptly.

### Circuit Breaker Half-Open Failure

When a half-open trial fails, reopen the circuit immediately and reset failure accounting enough to enforce the cooldown again. Closed-state behavior keeps the existing failure threshold.

This avoids hammering a recovering service during the half-open window.

## Tests And Verification

Add focused tests in `gateway`:

- HTTP instance selection returns addresses from a stable snapshot and does not require rebuilding state per request.
- gRPC `ServicePool.Get` preserves round-robin behavior with the new snapshot state.
- gRPC proxy handler derives its upstream context from the inbound request context.
- Duplicate connect attempts for the same service instance are suppressed.
- Half-open failure reopens the circuit.

Verification commands:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -count=1
GOCACHE=/private/tmp/core-gocache go test ./... -count=1
```

The second command is broader regression coverage. If it fails outside `gateway`, inspect before changing unrelated packages.

## Risks

The main risk is changing HTTP reverse proxy behavior. The route handler must preserve existing path rewrite, route header override, and error response behavior.

The second risk is snapshot ordering. Map iteration order is not stable, so tests should assert valid round-robin behavior without relying on a specific address order unless the implementation explicitly sorts. Sorting is not required for this optimization.

The third risk is over-constraining connection retries. Deduplication must suppress only concurrent attempts for the same service instance, not future retry attempts after the current attempt finishes.
