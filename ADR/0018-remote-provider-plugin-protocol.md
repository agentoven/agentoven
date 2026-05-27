# ADR-0018: Remote Provider Plugin Protocol (gRPC)

- **Date:** 2026-05-18
- **Status:** Accepted
- **Deciders:** siddartha
- **Relates to:** [ADR-0004](0004-provider-first-embedding-architecture.md), [ADR-0007](0007-control-plane-as-a2a-gateway.md), [ADR-0012](0012-agent-packaging-distribution.md)

## Context

All model provider drivers (`ProviderDriver` interface in `router/router.go`) are
compiled into the control-plane binary at build time. Adding a new provider requires
a fork or a PR to the core repo, which creates a barrier for third-party integrations
and makes the Pro/OSS split harder to maintain as the driver list grows.

We want third parties (and enterprise customers) to ship provider drivers independently,
written in any language, without rebuilding the control plane. The existing Go interface
already defines the right contract:

```go
type ProviderDriver interface {
    Kind() string
    Call(ctx, provider, req) → response
    HealthCheck(ctx, provider) error
}
// Optional capability interfaces
StreamingProviderDriver  // stream chunks
EmbeddingCapableDriver   // text embeddings
ModelDiscoveryDriver     // list available models
```

The challenge is lifecycle and observability: if the control plane does not own the
provider server process it cannot proactively detect failures, cannot enforce graceful
drains during upgrades, and cannot inject secrets without a separate secret-management
surface. External-only (dial-on-demand) pushes this complexity onto the operator and
creates a reactive rather than proactive failure model.

## Decision

### Protocol

Provider plugins communicate with the control plane over **gRPC** using a canonical
`ProviderService` (defined in `pkg/provider/v1/provider.proto`). The control plane
is always the gRPC *client*; provider servers implement the service. Protocol Buffers
provide a strongly-typed, language-agnostic contract that enables SDKs in Go, Python,
TypeScript, and any other gRPC-supported language.

### Three Deployment Modes

| Mode | Who spawns | Transport | Secrets | When to use |
|------|-----------|-----------|---------|-------------|
| **managed** (default) | control plane | Unix socket | env vars at spawn | local, Docker, dev |
| **supervised-external** | external / K8s operator | TCP + mTLS | K8s secrets / Vault | cloud deployments |
| **k8s-operator** | operator deploys `Deployment`+`Service` | TCP + mTLS | K8s secrets | managed K8s clusters |

**Managed mode** mirrors how agent subprocesses work today (`LocalExecutor`): the
control plane spawns the provider binary, owns the PID, injects secrets as environment
variables, and communicates over a Unix-domain socket on a path it controls. No
network port is opened; no auth is needed on the socket itself.

**Supervised-external mode** is the escape hatch for remote or K8s deployments where
the control plane cannot spawn processes directly. The control plane does *not* manage
the lifecycle but runs a `ProviderWatcher` goroutine per external provider that:
1. Probes the gRPC `HealthCheck` RPC every 10 seconds.
2. Emits `agentoven.provider.health` OTel metric (`0.0` = healthy, `1.0` = degraded,
   `2.0` = unhealthy) so Prometheus/Grafana alerts can fire proactively.
3. Applies a circuit-breaker: after 3 consecutive failures the provider is marked
   `DEGRADED`; after 5 it is `UNHEALTHY` and calls are rejected immediately (no
   wasted timeout on a dead backend).
4. Resets to `HEALTHY` on the first successful probe after recovery.

The transport uses mTLS: the control plane acts as CA and issues a short-lived leaf
certificate to each supervised-external provider at registration time. Providers
present this certificate on every connection; the control plane validates it against
its own CA. This replaces any per-call auth overhead.

**K8s-operator mode** is supervised-external where the operator writes the endpoint
back to the provider CR (`status.grpcAddr`) after the pod becomes Ready. The control
plane reads this field and initialises a `ProviderWatcher` automatically.

### Capabilities Negotiation

On first connection (after `Configure`), the control plane calls `Capabilities()` and
caches the result:

```
capabilities {
  kind: "openrouter"
  version: "1.2.0"
  features: [STREAMING, EMBEDDINGS, MODEL_DISCOVERY]
  supported_models: ["gpt-4o", "claude-3-5-sonnet", ...]
  cost_table: { "gpt-4o": { input_per_1k: 0.0025, output_per_1k: 0.01 } }
}
```

This eliminates the "returns UNIMPLEMENTED if optional method not supported" anti-pattern.
The control plane only calls optional RPCs (`Stream`, `Embed`, `ListModels`) if the
corresponding feature flag is present.

### Secret Injection via `Configure` RPC

Secrets are transmitted **once at registration**, not on every `Call`. The `Configure`
RPC carries a `map<string, string>` of key-value pairs (API keys, base URLs, etc.).
The provider server stores them in memory; subsequent `CallRequest` messages reference
only the `provider_id` UUID — no credentials in the hot path. For managed mode the
control plane also injects the same values as environment variables (belt-and-suspenders
for providers that read from env directly).

### Provider Manifest

Each provider ships an `agentoven-provider.yaml` manifest:

```yaml
apiVersion: agentoven.io/v1
kind: ProviderManifest
metadata:
  name: openrouter
  version: 1.2.0
  author: "Acme Corp"
  description: "OpenRouter multi-model gateway"
spec:
  driverKind: openrouter
  capabilities:
    - streaming
    - model_discovery
  transport:
    mode: managed                     # managed | supervised-external | k8s-operator
    binary: ./bin/openrouter-provider # managed mode: path to executable
    grpcAddr: ""                      # supervised-external: host:port
  secrets:
    - OPENROUTER_API_KEY              # env var names; values from AgentOven secret store
  costTable:
    gpt-4o:
      inputPer1kTokens: 0.0025
      outputPer1kTokens: 0.0100
```

### Registration Flow

```
CLI: agentoven provider deploy ./openrouter-provider/
 │
 ├─ 1. Validate manifest (agentoven-provider.yaml)
 ├─ 2. Upload binary / image to control plane (managed) or record grpcAddr (external)
 ├─ 3. Control plane calls Configure(provider_id, secrets_map)
 ├─ 4. Control plane calls Capabilities() → caches features + cost table
 ├─ 5. Provider registered; router.RegisterRemoteDriver(shim) wires it into call path
 └─ 6. ProviderWatcher goroutine started (all modes)
```

### Router Integration

The existing `RegisterDriver(ProviderDriver)` in `router.go` gains a companion:

```go
func (r *Router) RegisterRemoteDriver(addr string, transport TransportMode) error
```

Internally this creates a `RemoteProviderShim` that implements `ProviderDriver` (and
optionally `StreamingProviderDriver`, `EmbeddingCapableDriver`, `ModelDiscoveryDriver`
based on capabilities) by delegating over gRPC. From the router's perspective a remote
provider is indistinguishable from a compiled-in one.

## Consequences

- **Easier:** Third parties can write providers in any language. No fork needed. Secrets
  are not exposed on the call hot path. Circuit breaker gives proactive failure detection
  in all modes.
- **Harder:** gRPC adds a dependency (`google.golang.org/grpc`). Managed mode adds
  process supervision logic similar to `LocalExecutor` but lighter (provider servers
  are long-lived, unlike agent processes that come and go). mTLS certificate issuance
  in supervised-external mode requires a lightweight CA in the control plane.
- **Security:** Unix socket in managed mode is unexposed. mTLS in external mode prevents
  impersonation. `Configure` secrets are transmitted in-memory only after TLS handshake.
  Provider servers MUST NOT log the `configure.config` map.
- **Versioning:** Semantic versioning in the manifest. Multiple provider versions can
  be registered simultaneously; the router selects by `spec.driverVersion` in the
  provider config. Proto package uses `v1`; breaking changes bump to `v2`.

## Alternatives Considered

1. **Go `plugin.so` dynamic linking** — Rejected: requires identical Go version, OS,
   and build flags. Not language-agnostic. Hard to sandbox.
2. **External-only (dial on demand, no lifecycle ownership)** — Rejected: reactive
   failure model only; control plane has no visibility until a call fails; version drift
   risk; separate ops surface.
3. **WASM plugins** — Rejected: WASM runtimes (wazero) add significant binary size and
   memory overhead. Streaming responses via WASM are complex. May reconsider for
   untrusted third-party plugins in a future sandbox ADR.
4. **HTTP/JSON REST protocol** — Rejected: gRPC gives streaming for free (needed for
   `StreamCall`), strongly-typed schemas, and better performance at high throughput.
   Provider authors can still use HTTP internally when calling the upstream AI API;
   the gRPC boundary is only between the provider server and the control plane.
