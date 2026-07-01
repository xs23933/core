# Gateway Hot Path Optimization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce gateway package hot-path allocation and resource retention for high-volume main gateway traffic.

**Architecture:** Keep gateway read paths lock-free by storing immutable copy-on-write snapshots for HTTP and gRPC instance selection. Reuse HTTP reverse proxy objects per route, derive upstream gRPC contexts from inbound request contexts, and suppress duplicate concurrent service connection attempts.

**Tech Stack:** Go 1.25, `net/http/httputil`, `sync/atomic`, `sync.Map`, gRPC reflection proxy code, existing `core` gateway tests.

---

## File Structure

- Modify `gateway/http_route.go`: HTTP service instance snapshot, zero-allocation address selection, reusable reverse proxy request preparation.
- Modify `gateway/pool.go`: immutable gRPC pool state containing both ID map and proxy slice.
- Modify `gateway/gateway.go`: connect attempt in-flight guard, half-open circuit failure behavior, inbound context propagation.
- Modify `gateway/gateway_test.go`: circuit breaker and connect guard tests.
- Modify `gateway/gateway_proxy_test.go`: outgoing context helper test.
- Modify `gateway/gateway_proxy_test.go` or `gateway/gateway_test.go`: allocation tests for instance selection and pool selection.
- Use existing tracked test files only because this repo ignores new `*_test.go` files.

## Task 1: HTTP Instance Snapshot

**Files:**
- Modify: `gateway/http_route.go`
- Test: `gateway/gateway_test.go`

- [ ] **Step 1: Write failing tests for HTTP snapshot selection**

Add tests to `gateway/gateway_test.go`:

```go
func TestHTTPInstanceSelectionUsesSnapshotWithoutAllocating(t *testing.T) {
	gw := &EtcdGateway{}
	gw.storeHTTPInstances(make(map[string]httpServiceInstances))
	gw.addHTTPInstance("task-service", "task-2", "127.0.0.1:8082")
	gw.addHTTPInstance("task-service", "task-1", "127.0.0.1:8081")

	first := gw.getHTTPInstance("task-service")
	second := gw.getHTTPInstance("task-service")
	if first == "" || second == "" || first == second {
		t.Fatalf("round-robin instances = %q, %q; want two non-empty different addresses", first, second)
	}

	_ = gw.getHTTPInstance("task-service")
	allocs := testing.AllocsPerRun(1000, func() {
		_ = gw.getHTTPInstance("task-service")
	})
	if allocs != 0 {
		t.Fatalf("getHTTPInstance allocations = %v, want 0", allocs)
	}
}

func TestHTTPInstanceRemovalRebuildsSnapshot(t *testing.T) {
	gw := &EtcdGateway{}
	gw.storeHTTPInstances(make(map[string]httpServiceInstances))
	gw.addHTTPInstance("task-service", "task-1", "127.0.0.1:8081")
	gw.addHTTPInstance("task-service", "task-2", "127.0.0.1:8082")

	gw.removeHTTPInstance("task-service", "task-1")

	for i := 0; i < 4; i++ {
		if got := gw.getHTTPInstance("task-service"); got != "127.0.0.1:8082" {
			t.Fatalf("instance after removal = %q, want remaining address", got)
		}
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -run 'TestHTTPInstance' -count=1
```

Expected: compile failure because `httpServiceInstances` is not defined, or allocation test fails with current map-to-slice implementation.

- [ ] **Step 3: Implement immutable HTTP snapshot**

In `gateway/http_route.go`, add `sort` import and replace the HTTP instance storage helpers with:

```go
type httpServiceInstances struct {
	byID  map[string]string
	addrs []string
}

func newHTTPServiceInstances(byID map[string]string) httpServiceInstances {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	addrs := make([]string, 0, len(ids))
	for _, id := range ids {
		addrs = append(addrs, byID[id])
	}
	return httpServiceInstances{byID: byID, addrs: addrs}
}

func (gw *EtcdGateway) loadHTTPInstances() map[string]httpServiceInstances {
	if value, ok := gw.httpInstances.Load().(map[string]httpServiceInstances); ok && value != nil {
		return value
	}
	return nil
}

func (gw *EtcdGateway) storeHTTPInstances(instances map[string]httpServiceInstances) {
	gw.httpInstances.Store(instances)
}

func copyHTTPInstances(instances map[string]httpServiceInstances) map[string]httpServiceInstances {
	next := make(map[string]httpServiceInstances, len(instances))
	for serviceName, serviceInstances := range instances {
		byID := make(map[string]string, len(serviceInstances.byID))
		for instanceID, addr := range serviceInstances.byID {
			byID[instanceID] = addr
		}
		next[serviceName] = newHTTPServiceInstances(byID)
	}
	return next
}
```

Update `addHTTPInstance`, `removeHTTPInstance`, and `getHTTPInstance` to rebuild the per-service snapshot only on mutation and read `serviceInstances.addrs` in `getHTTPInstance`.

- [ ] **Step 4: Run tests and verify they pass**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -run 'TestHTTPInstance' -count=1
```

Expected: PASS.

## Task 2: gRPC Pool Snapshot

**Files:**
- Modify: `gateway/pool.go`
- Test: `gateway/gateway_test.go`

- [ ] **Step 1: Write failing tests for pool snapshot reads**

Add to `gateway/gateway_test.go`:

```go
func TestServicePoolGetUsesSnapshotWithoutAllocating(t *testing.T) {
	pool := NewServicePool(nil, "billing-service")
	pool.storeState(servicePoolState{
		byID: map[string]*ReflectionProxy{
			"billing-1": {addr: "127.0.0.1:9001"},
		},
		all: []*ReflectionProxy{{addr: "127.0.0.1:9001"}},
	})

	if got := pool.Get(); got == nil || got.addr != "127.0.0.1:9001" {
		t.Fatalf("pool.Get() = %#v, want billing proxy", got)
	}

	allocs := testing.AllocsPerRun(1000, func() {
		_ = pool.Get()
	})
	if allocs != 0 {
		t.Fatalf("ServicePool.Get allocations = %v, want 0", allocs)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -run TestServicePoolGetUsesSnapshotWithoutAllocating -count=1
```

Expected: compile failure because `servicePoolState` and `storeState` do not exist.

- [ ] **Step 3: Implement pool state snapshot**

In `gateway/pool.go`, add:

```go
type servicePoolState struct {
	byID map[string]*ReflectionProxy
	all  []*ReflectionProxy
}
```

Change `ServicePool.instances` to `state atomic.Value`, initialize with `servicePoolState{byID: make(map[string]*ReflectionProxy)}`, and add:

```go
func (p *ServicePool) loadState() servicePoolState {
	if state, ok := p.state.Load().(servicePoolState); ok && state.byID != nil {
		return state
	}
	return servicePoolState{byID: make(map[string]*ReflectionProxy)}
}

func (p *ServicePool) storeState(state servicePoolState) {
	p.state.Store(state)
}

func copyServicePoolState(state servicePoolState, extra int) servicePoolState {
	nextByID := make(map[string]*ReflectionProxy, len(state.byID)+extra)
	nextAll := make([]*ReflectionProxy, 0, len(state.byID)+extra)
	for id, proxy := range state.byID {
		nextByID[id] = proxy
	}
	for _, proxy := range nextByID {
		nextAll = append(nextAll, proxy)
	}
	return servicePoolState{byID: nextByID, all: nextAll}
}
```

Update `AddOrUpdateInstance`, `RemoveInstance`, `Get`, `Size`, and `Close` to read and store `servicePoolState`.

- [ ] **Step 4: Run test and verify it passes**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -run TestServicePoolGetUsesSnapshotWithoutAllocating -count=1
```

Expected: PASS.

## Task 3: gRPC Context Propagation

**Files:**
- Modify: `gateway/gateway.go`
- Test: `gateway/gateway_proxy_test.go`

- [ ] **Step 1: Write failing context propagation test**

Add to `gateway/gateway_proxy_test.go`:

```go
func TestGatewayOutgoingContextInheritsInboundCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	md := metadata.Pairs("x-request-id", "req-1")

	out := gatewayOutgoingContext(parent, md)
	cancel()

	select {
	case <-out.Done():
	case <-time.After(time.Second):
		t.Fatal("outgoing context was not canceled when inbound context was canceled")
	}
	if got := metadata.ValueFromIncomingContext(out, "x-request-id"); len(got) != 0 {
		t.Fatalf("incoming metadata = %#v, want none on outgoing context", got)
	}
	if got, ok := metadata.FromOutgoingContext(out); !ok || len(got.Get("x-request-id")) != 1 {
		t.Fatalf("outgoing metadata = %#v, want x-request-id", got)
	}
}
```

Add imports for `time` and `google.golang.org/grpc/metadata` if missing.

- [ ] **Step 2: Run test and verify it fails**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -run TestGatewayOutgoingContextInheritsInboundCancellation -count=1
```

Expected: compile failure because `gatewayOutgoingContext` does not exist.

- [ ] **Step 3: Implement context helper and use it**

In `gateway/gateway.go`, add:

```go
func gatewayOutgoingContext(parent context.Context, md metadata.MD) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return metadata.NewOutgoingContext(parent, md)
}
```

Change:

```go
grpcCtx := metadata.NewOutgoingContext(context.Background(), md)
```

to:

```go
grpcCtx := gatewayOutgoingContext(ctx.Context(), md)
```

- [ ] **Step 4: Run test and verify it passes**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -run TestGatewayOutgoingContextInheritsInboundCancellation -count=1
```

Expected: PASS.

## Task 4: Connect Guard And Circuit Breaker

**Files:**
- Modify: `gateway/gateway.go`
- Test: `gateway/gateway_test.go`

- [ ] **Step 1: Write failing tests**

Add to `gateway/gateway_test.go`:

```go
func TestConnectInstanceGuardSuppressesDuplicateInFlightAttempts(t *testing.T) {
	gw := &EtcdGateway{}

	key, ok := gw.beginConnectInstance("billing-service", "billing-1")
	if !ok {
		t.Fatal("first connect attempt should start")
	}
	if _, ok := gw.beginConnectInstance("billing-service", "billing-1"); ok {
		t.Fatal("duplicate in-flight connect attempt should be suppressed")
	}

	gw.finishConnectInstance(key)
	if _, ok := gw.beginConnectInstance("billing-service", "billing-1"); !ok {
		t.Fatal("connect attempt should be allowed after previous attempt finishes")
	}
}

func TestCircuitBreakerHalfOpenFailureReopensCircuit(t *testing.T) {
	gw := &EtcdGateway{}
	gw.circuitStates.Store("billing-service", &CircuitBreaker{state: circuitHalfOpen})

	gw.circuitBreakerRecordFailure("billing-service")

	value, ok := gw.circuitStates.Load("billing-service")
	if !ok {
		t.Fatal("missing circuit breaker")
	}
	cb := value.(*CircuitBreaker)
	if cb.state != circuitOpen {
		t.Fatalf("circuit state = %d, want open", cb.state)
	}
	if gw.circuitBreakerCheck("billing-service") {
		t.Fatal("open circuit should reject requests during cooldown")
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -run 'TestConnectInstanceGuard|TestCircuitBreakerHalfOpenFailure' -count=1
```

Expected: compile failure for missing guard methods and/or half-open assertion failure.

- [ ] **Step 3: Implement guard and half-open failure behavior**

In `gateway/gateway.go`, add field to `EtcdGateway`:

```go
connectInFlight sync.Map // map[string]struct{}
```

Add:

```go
func connectInstanceKey(serviceName, instanceID string) string {
	return serviceName + "/" + instanceID
}

func (gw *EtcdGateway) beginConnectInstance(serviceName, instanceID string) (string, bool) {
	key := connectInstanceKey(serviceName, instanceID)
	if _, loaded := gw.connectInFlight.LoadOrStore(key, struct{}{}); loaded {
		return key, false
	}
	return key, true
}

func (gw *EtcdGateway) finishConnectInstance(key string) {
	if key != "" {
		gw.connectInFlight.Delete(key)
	}
}
```

Change watch PUT handling from:

```go
go gw.connectInstance(serviceName, info.ID, info.Addr)
```

to:

```go
if key, ok := gw.beginConnectInstance(serviceName, info.ID); ok {
	go func() {
		defer gw.finishConnectInstance(key)
		gw.connectInstance(serviceName, info.ID, info.Addr)
	}()
}
```

In `circuitBreakerRecordFailure`, add a half-open branch before the closed threshold branch:

```go
if cb.state == circuitHalfOpen {
	cb.state = circuitOpen
	cb.failCount = circuitFailThreshold
	core.D("[Gateway] circuit breaker for %s reopened after half-open failure", serviceName)
	return
}
```

- [ ] **Step 4: Run tests and verify they pass**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -run 'TestConnectInstanceGuard|TestCircuitBreakerHalfOpenFailure' -count=1
```

Expected: PASS.

## Task 5: Reusable HTTP Reverse Proxy

**Files:**
- Modify: `gateway/http_route.go`
- Test: `gateway/gateway_test.go`

- [ ] **Step 1: Write failing HTTP proxy behavior test**

Add to `gateway/gateway_test.go`:

```go
func TestPrepareHTTPProxyRequestRewritesTargetPathAndHeaders(t *testing.T) {
	target, err := parseHTTPServiceURL("http://127.0.0.1:8081/base")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/42?debug=1", nil)
	route := &Route{
		UpstreamPath: "/tasks/:id",
		Headers:      map[string]string{"X-Gateway": "core"},
	}

	prepareHTTPProxyRequest(req, target, route, map[string]string{"id": "42"})

	if req.URL.Scheme != "http" || req.URL.Host != "127.0.0.1:8081" {
		t.Fatalf("target = %s://%s, want http://127.0.0.1:8081", req.URL.Scheme, req.URL.Host)
	}
	if req.URL.Path != "/base/tasks/42" {
		t.Fatalf("path = %q, want /base/tasks/42", req.URL.Path)
	}
	if req.URL.RawQuery != "debug=1" {
		t.Fatalf("query = %q, want debug=1", req.URL.RawQuery)
	}
	if got := req.Header.Get("X-Gateway"); got != "core" {
		t.Fatalf("X-Gateway = %q, want core", got)
	}
}
```

Ensure `gateway/gateway_test.go` imports `net/http/httptest` if not already present.

- [ ] **Step 2: Run test and verify it fails**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -run TestPrepareHTTPProxyRequestRewritesTargetPathAndHeaders -count=1
```

Expected: compile failure because `prepareHTTPProxyRequest` does not exist.

- [ ] **Step 3: Implement reusable proxy helper and route handler**

In `gateway/http_route.go`, add helpers:

```go
func prepareHTTPProxyRequest(req *http.Request, target *url.URL, route *Route, params map[string]string) {
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = joinHTTPProxyPath(target.Path, resolveHTTPUpstreamPath(route.UpstreamPath, req.URL.Path, params))
	req.URL.RawPath = ""
	if target.RawQuery == "" || req.URL.RawQuery == "" {
		req.URL.RawQuery = target.RawQuery + req.URL.RawQuery
	} else {
		req.URL.RawQuery = target.RawQuery + "&" + req.URL.RawQuery
	}
	for key, value := range route.Headers {
		req.Header.Set(key, value)
	}
}

func joinHTTPProxyPath(basePath, reqPath string) string {
	if basePath == "" || basePath == "/" {
		return normalizeRoutePath(reqPath)
	}
	if reqPath == "" || reqPath == "/" {
		return normalizeRoutePath(basePath)
	}
	return strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(reqPath, "/")
}
```

Change `createHTTPProxyHandler` so `proxy := &httputil.ReverseProxy{Director: func(*http.Request) {}, ErrorHandler: ...}` is created outside the returned closure. Inside the closure, keep selecting the current address and parsing it, clone the request, call `prepareHTTPProxyRequest`, and call `proxy.ServeHTTP`.

- [ ] **Step 4: Run test and verify it passes**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -run TestPrepareHTTPProxyRequestRewritesTargetPathAndHeaders -count=1
```

Expected: PASS.

## Task 6: Gateway Package Regression

**Files:**
- Modify: any files from previous tasks if tests reveal regressions.

- [ ] **Step 1: Run all gateway tests**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./gateway -count=1
```

Expected: PASS.

- [ ] **Step 2: Run broader repository tests**

Run:

```sh
GOCACHE=/private/tmp/core-gocache go test ./... -count=1
```

Expected: PASS. If a failure is unrelated to gateway changes, inspect and report it without modifying unrelated packages.

- [ ] **Step 3: Inspect diff**

Run:

```sh
git diff -- gateway docs/superpowers/plans/2026-07-01-gateway-hot-path-optimization.md
```

Expected: diff only changes gateway package and the implementation plan.

- [ ] **Step 4: Commit implementation**

Run:

```sh
git add gateway docs/superpowers/plans/2026-07-01-gateway-hot-path-optimization.md
git commit -m "perf: optimize gateway hot path"
```

Expected: commit contains only gateway implementation, tests, and the plan.

## Self-Review

Spec coverage:

- HTTP proxy reuse: Task 5.
- HTTP instance snapshot: Task 1.
- gRPC pool snapshot: Task 2.
- gRPC inbound context propagation: Task 3.
- Duplicate connect suppression: Task 4.
- Half-open circuit failure: Task 4.
- Gateway verification: Task 6.

Placeholder scan:

- No TBD, TODO, FIXME, or "implement later" entries are intentionally left in this plan.

Type consistency:

- `httpServiceInstances`, `servicePoolState`, `gatewayOutgoingContext`, `beginConnectInstance`, `finishConnectInstance`, and `prepareHTTPProxyRequest` are introduced before use in production code.
