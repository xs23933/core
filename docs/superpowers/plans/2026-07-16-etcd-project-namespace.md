# Etcd Project Namespace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Isolate services, Gateway routes, and relative configuration keys for multiple projects sharing one etcd cluster.

**Architecture:** `etcd.Options` owns normalized namespace-aware roots and key construction. Registry, Discovery, Core configuration, and Gateway consume those helpers so the selected namespace propagates end to end; explicit absolute KV keys and explicit route prefixes remain exact compatibility overrides.

**Tech Stack:** Go, etcd client v3, gRPC resolver, Core Gateway, Go testing

## Global Constraints

- Empty namespace preserves `/services/`, `/gateway/routes/`, and `/config/` exactly.
- Normalize whitespace, leading/trailing slashes, and repeated separators.
- Do not change logical service names, gRPC targets, HTTP paths, or protobuf descriptors.
- Explicit absolute KV keys and explicit Gateway route prefixes remain exact overrides.
- Use `GOCACHE=/private/tmp/core-gocache` for verification.

---

### Task 1: Canonical namespace and etcd keys

**Files:**
- Create: `etcd/options_test.go`
- Modify: `etcd/options.go`
- Modify: `etcd/kv.go`
- Modify: `etcd/kv_test.go`
- Modify: `etcd/discovery.go`

**Interfaces:**
- Produces: `Options.Namespace string`
- Produces: `(*Options).ServiceRoot()`, `ServicePrefix()`, `ServiceKey()`, `ConfigPrefix()`
- Produces: `NamespacePrefix(namespace, logicalPrefix string) string`
- Produces: `parseServiceKey(key, serviceRoot string) (serviceName, instanceID string, ok bool)`

- [ ] **Step 1: Write failing namespace key tests**

Create a table test with these cases:

```go
tests := []struct {
    namespace, serviceRoot, servicePrefix, serviceKey, configPrefix string
}{
    {"", "/services/", "/services/auth/", "/services/auth/auth-1", "/config/"},
    {" /xpay//dev/ ", "/xpay/dev/services/", "/xpay/dev/services/auth/", "/xpay/dev/services/auth/auth-1", "/xpay/dev/config/"},
}
```

Extend `TestKVKey` so `Discovery{opts: &Options{Namespace: "xpay"}}` maps relative `gateway/public_routes` to `/xpay/config/gateway/public_routes`, while `/custom/key` remains exact.

- [ ] **Step 2: Verify RED**

Run:

```bash
GOCACHE=/private/tmp/core-gocache go test ./etcd -run 'TestOptionsNamespacePrefixes|TestKVKey' -count=1
```

Expected: compile/test failure because namespace fields and helpers do not exist.

- [ ] **Step 3: Implement canonical key construction**

Add `Namespace` and a helper that splits namespace on `/`, discards empty segments, and prepends the normalized segments to a logical absolute prefix. Build service/config roots from it. Replace package-global `kvKey` usage with `d.kvKey`, using `d.opts.ConfigPrefix()` for relative keys and preserving absolute keys. Use `d.opts.ConfigPrefix()` when trimming `WatchKV` callbacks.

- [ ] **Step 4: Verify GREEN**

Run `GOCACHE=/private/tmp/core-gocache go test ./etcd -count=1`.

Expected: PASS.

- [ ] **Step 5: Write failing service parser tests**

Assert legacy and namespaced keys parse under their expected root, while foreign roots and missing service/instance segments are rejected:

```go
parseServiceKey("/services/auth/auth-1", "/services/")
parseServiceKey("/xpay/services/auth/auth-1", "/xpay/services/")
```

Run `GOCACHE=/private/tmp/core-gocache go test ./etcd -run TestParseServiceKey -count=1` and confirm RED.

- [ ] **Step 6: Make Discovery use canonical roots**

Use `d.opts.ServiceRoot()` for parsing and derive the watched service prefix from a copied `Options` value with the requested service name. This prevents a Discovery from accepting keys outside its namespace.

- [ ] **Step 7: Verify and commit**

Run:

```bash
GOCACHE=/private/tmp/core-gocache go test ./etcd -count=1
git diff --check
git add etcd/options.go etcd/options_test.go etcd/kv.go etcd/kv_test.go etcd/discovery.go
git commit -m "feat: namespace etcd service and config keys"
```

Expected: tests pass, whitespace check exits 0, and commit succeeds.

### Task 2: Propagate namespace through Core and Gateway

**Files:**
- Modify: `grpc.go`
- Modify: `grpc_client.go`
- Modify: `gateway/gateway.go`
- Modify: `gateway/http_route.go`
- Modify: `gateway/gateway_test.go`
- Modify or create: root configuration helper tests

**Interfaces:**
- Consumes: `etcd.Options.Namespace`, `ServiceRoot`, and `etcd.NamespacePrefix`
- Produces: `gateway.Config.Namespace string`
- Produces: Gateway helpers for service root, default route prefix, config defaults, and root-aware key parsing

- [ ] **Step 1: Write failing Gateway tests**

Assert:

```go
defaultRoutePrefix("") == "/gateway/routes/"
defaultRoutePrefix("xpay") == "/xpay/gateway/routes/"
serviceRoot("xpay") == "/xpay/services/"
parseServiceKey("/xpay/services/auth/auth-1", "/xpay/services/") == ("auth", "auth-1", true)
```

Also test that namespace derives a route prefix only when `RoutePrefix` is empty and `/custom/routes/` remains exact.

- [ ] **Step 2: Verify RED**

Run:

```bash
GOCACHE=/private/tmp/core-gocache go test ./gateway -run 'TestDefaultRoutePrefix|TestGatewayServiceRoot|TestParseServiceKey|TestGatewayPrefixDefaults' -count=1
```

Expected: compile failure because helpers and `Config.Namespace` are missing.

- [ ] **Step 3: Implement Gateway namespace selection**

Add `Config.Namespace`, read `etcd.namespace` when no explicit config is supplied, derive the default route prefix only when it is empty, and store the canonical service root on `EtcdGateway`. Use that root for initial `Get`, `Watch`, and service-key parsing. Include the actual watched prefix in logs.

- [ ] **Step 4: Verify Gateway GREEN**

Run `GOCACHE=/private/tmp/core-gocache go test ./gateway -count=1`.

Expected: PASS.

- [ ] **Step 5: Write failing route/config propagation tests**

Test pure option-default helpers rather than opening etcd connections. Pin that Registry, Discovery, and `enableEtcdDiscoveryFromConf` propagate `etcd.namespace` when explicit options do not provide it. Pin that `RegisterHTTPRoute` derives `/xpay/gateway/routes/` by default and keeps an explicit `/custom/routes/` unchanged.

- [ ] **Step 6: Propagate namespace through Core**

Read `etcd.namespace` in registry and discovery option defaulting. When Registry enables Discovery, pass namespace-compatible options instead of constructing unrelated global discovery options. Include namespace in `enableEtcdDiscoveryFromConf`. Change `RegisterHTTPRoute` to use the namespace-aware default route prefix.

- [ ] **Step 7: Verify and commit**

Run:

```bash
GOCACHE=/private/tmp/core-gocache go test ./gateway ./... -run 'Test.*Namespace|Test.*Prefix|TestParseServiceKey' -count=1
git diff --check
git add grpc.go grpc_client.go gateway/gateway.go gateway/http_route.go gateway/gateway_test.go '*_test.go'
git commit -m "feat: isolate gateway discovery by namespace"
```

Expected: tests pass, whitespace check exits 0, and commit succeeds.

### Task 3: Documentation and repository verification

**Files:**
- Modify: `README.md`
- Modify: `skills/core-gateway.md`
- Modify: `skills/core-grpc-client.md`
- Modify: `skills/core-grpc.md`

**Interfaces:**
- Consumes: final `etcd.namespace` behavior
- Produces: user-facing configuration and migration guidance

- [ ] **Step 1: Update documentation**

Add the optional configuration and resulting prefixes:

```yaml
etcd:
  namespace: xpay
  endpoints:
    - 127.0.0.1:2379
```

State that every service, client, and Gateway in one project must use the same namespace and empty namespace preserves existing behavior.

- [ ] **Step 2: Check consistency**

Run:

```bash
rg -n "namespace|/services/|/gateway/routes/|/config/" README.md skills/core-gateway.md skills/core-grpc.md skills/core-grpc-client.md
git diff --check
```

Expected: all documents use the same key layout and the whitespace check exits 0.

- [ ] **Step 3: Run complete verification**

Run:

```bash
GOCACHE=/private/tmp/core-gocache go test ./etcd ./gateway -count=1
GOCACHE=/private/tmp/core-gocache go test ./... -count=1
git diff --check
```

Expected: all tests pass and the whitespace check exits 0.

- [ ] **Step 4: Commit documentation**

Run:

```bash
git add README.md skills/core-gateway.md skills/core-grpc.md skills/core-grpc-client.md
git commit -m "docs: explain etcd project namespaces"
```

Expected: commit succeeds.
