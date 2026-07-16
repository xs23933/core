# Etcd Project Namespace Design

## Goal

Allow multiple projects to share one etcd cluster without merging service instances, Gateway routes, or relative configuration keys, while preserving the existing key layout when no namespace is configured.

## Problem

Core currently registers every service under `/services/<service_name>/<instance_id>`. Discovery and Gateway both read the global `/services/` prefix, so two independently running projects that use the same service names are treated as one deployment even when their HTTP and gRPC ports differ. Gateway route definitions and relative configuration keys are also stored in global prefixes.

Changing only `service_id`, adding project metadata, or changing the Gateway route prefix does not isolate the full data flow: Gateway still watches every service instance, and current discovery does not filter metadata.

## Chosen Design

Add an optional `Namespace string` to `etcd.Options` and expose it through `etcd.namespace`. Namespace is a project/deployment ownership boundary, not part of the logical service name.

Normalize namespace values by trimming whitespace and leading/trailing `/`. The normalized namespace `xpay` produces this layout:

```text
/xpay/services/auth/auth-1
/xpay/gateway/routes/<route-id>
/xpay/config/gateway/public_routes
```

An empty or slash-only namespace preserves the current layout exactly:

```text
/services/auth/auth-1
/gateway/routes/<route-id>
/config/gateway/public_routes
```

This approach is preferred over the alternatives:

1. Separate etcd clusters provide strong isolation but add local and deployment infrastructure.
2. Prefixing every `service_name` requires business configuration changes and still does not isolate global Gateway route/config prefixes.
3. Metadata filtering leaves all consumers responsible for remembering the filter and cannot prevent route-key collisions.

## Key Construction

The etcd package owns canonical namespace/key construction. A small helper will join a normalized namespace with an absolute logical prefix without using filesystem path cleaning semantics.

`Options` will provide the canonical prefixes needed by registration and discovery:

- namespace root
- service root (`/services/` or `/<namespace>/services/`)
- service-specific prefix
- configuration root (`/config/` or `/<namespace>/config/`)

Callers must not parse keys using fixed array offsets. Service-key parsing receives the expected service root, removes it, and then reads `<service_name>/<instance_id>`.

## Configuration Flow

`Core.EnableEtcdRegistry(nil)` and `Core.EnableEtcdDiscovery(nil)` read `etcd.namespace`. Explicit non-empty `etcd.Options.Namespace` wins over configuration, matching the existing option precedence for endpoints and service fields.

`enableEtcdDiscoveryFromConf`, used by `GrpcClient`, also propagates `etcd.namespace` so internal gRPC clients resolve only the selected project.

`gateway.Config` gains `Namespace`. `NewEtcdGateway` reads `etcd.namespace` when a config is not supplied. Gateway service snapshot loading and watch operations use the namespace-specific service root.

The default Gateway route prefix is namespace-aware. An explicitly supplied `Config.RoutePrefix` remains an exact override for backward compatibility. Likewise, the optional explicit prefix passed to `RegisterHTTPRoute` remains exact; when omitted, the route publisher derives the namespace-aware default from `etcd.namespace`.

## Relative and Absolute KV Keys

Relative Discovery KV keys such as `gateway/public_routes` use the namespace-specific configuration root. Absolute keys remain exact overrides and are not automatically namespaced. This preserves current callers that intentionally address a full etcd key and lets Gateway route publication pass its already-computed absolute route prefix without double-prefixing.

## Compatibility and Validation

- Empty namespace produces byte-for-byte identical keys and prefixes.
- Leading/trailing slashes and surrounding whitespace are normalized.
- Namespace must resolve to either empty or one or more non-empty path segments; repeated separators are collapsed.
- Logical service names, gRPC targets, route paths, and protobuf descriptors are unchanged.
- Existing deployments need no configuration changes.
- Two projects sharing endpoints are isolated only when every participating service, client, and Gateway uses the same project namespace.

## Testing

Tests will pin:

1. Legacy and namespaced service keys/prefixes.
2. Namespace normalization.
3. Service-key parsing under legacy and namespaced roots.
4. Relative KV namespacing and absolute-key override behavior.
5. Registry/discovery configuration propagation.
6. Gateway service root and default route prefix selection.
7. Explicit Gateway route-prefix compatibility.
8. Existing package and repository test suites.

Verification commands:

```bash
GOCACHE=/private/tmp/core-gocache go test ./etcd ./gateway -count=1
GOCACHE=/private/tmp/core-gocache go test ./... -count=1
git diff --check
```

## Documentation

Update the main README and `skills/core-gateway.md` with matching configuration examples and a warning that all processes belonging to one project must use the same namespace.
