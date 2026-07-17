# ADR-0023: Pluggable Cloud Agent Deployment Backend (AgenticCore Selection)

**Status:** Proposed  
**Date:** 2026-07-07  
**Authors:** AgentOven Engineering

---

## Context

AgentOven currently runs agents in three modes (ADR-0017):

| `execution_mode` | How the agent is spawned |
|---|---|
| `local` | `os/exec` child process on the control-plane host |
| `docker` | `docker run` via Docker daemon socket |
| `k8s` | K8s `Deployment` + `Service` via the AgentOven operator (Pro) |

All three modes assume AgentOven **owns the compute**. A fourth class of user is emerging: enterprises that want to run agents on a fully-managed cloud inference plane — Azure AI Foundry hosted agents, GCP Vertex AI Agent Engine, or AWS Bedrock AgentCore — while still using AgentOven as the control plane for auth, observability, recipes, and the A2A gateway (ADR-0007).

### Validated cloud backends

**Azure AI Foundry (fully validated):**  
Foundry hosted agents are arbitrary containers deployed to a managed compute pool. An agent image built from `Dockerfile.agent` can be submitted as a Foundry deployment. Foundry returns a managed HTTPS endpoint. AgentOven sets this as `BackendEndpoint` on the agent record; all A2A traffic is proxied through the stable `/agents/{name}/a2a` URL per ADR-0007.

**GCP Vertex AI Agent Engine (fully validated):**  
Vertex AI exposes `ReasoningEngine` as a deployable unit. AgentOven agents speak A2A, and Vertex AI Agent Engine natively proxies A2A-compatible agents (`client.agent_engines.create(config={"image_spec": {...}})`). The returned resource ID maps to a stable HTTPS endpoint. AgentOven sets `BackendEndpoint` to this endpoint; no protocol change required.

**AWS Bedrock AgentCore (not yet validated):**  
AWS Bedrock AgentCore is AWS's managed agent runtime. Documentation was not publicly accessible during the drafting of this ADR. This backend is marked **`experimental`** and must be explicitly opted-in at the kitchen level. A follow-up spike is required before `stable` promotion.

### Why not just use `BackendEndpoint` directly?

`BackendEndpoint` (ADR-0007) already supports pointing an agent at an external URL. The gap is the **provisioning lifecycle**: today a human must manually deploy the container to Foundry/Vertex, copy the returned endpoint URL, and paste it into the agent form. There is no automated deploy-on-bake, no teardown, and no health-check loop.

Cloud backends also require credential management (API keys / service-principal tokens / OAuth flows) that must not be hardcoded into agent records.

---

## Decision

### 1. `BackendProvisioner` interface

Add a `BackendProvisioner` interface in `agentoven-pro/internal/deployment/provisioner.go`:

```go
type BackendProvisioner interface {
    // Deploy provisions the agent on the cloud backend and returns the
    // endpoint URL to use as BackendEndpoint. Called on Bake or Env upsert.
    Deploy(ctx context.Context, agent *models.Agent, env *models.AgentEnvironment, image string) (endpoint string, err error)

    // Teardown de-provisions the agent. Called on Cool / Delete.
    Teardown(ctx context.Context, agent *models.Agent, env *models.AgentEnvironment) error

    // HealthCheck probes the endpoint and returns nil if the agent is live.
    // Called by the health monitor goroutine every 30 seconds.
    HealthCheck(ctx context.Context, endpoint string) error
}
```

Concrete implementations:
- `FoundryProvisioner` — Azure AI Foundry hosted agent API
- `VertexProvisioner` — GCP Vertex AI Agent Engine (`ReasoningEngine` API)
- `BedrockCoreProvisioner` — AWS Bedrock AgentCore (experimental, gated)
- `NoopProvisioner` — used for all non-cloud `execution_mode` values; `Deploy` is a no-op that returns the existing `BackendEndpoint` as-is

### 2. `BackendType` field on `AgentEnvironment`

Add `BackendType string` to `models.AgentEnvironment` (Pro model):

```go
// BackendType selects the cloud provisioner for this environment.
// Valid values: "" (default, uses execution_mode), "foundry", "vertex", "bedrock_core"
// When set, overrides the agent's execution_mode for this environment only.
BackendType string `json:"backend_type,omitempty" db:"backend_type"`
```

This is environment-scoped: a single agent can be deployed to `dev` via `docker`, `staging` via `foundry`, and `prod` via `vertex` — without changing the agent definition.

### 3. Provisioner registry and selection

A `ProvisionerRegistry` in Pro resolves `BackendType → BackendProvisioner`:

```go
func NewProvisioner(backendType string, creds *models.KitchenCredential) (BackendProvisioner, error) {
    switch backendType {
    case "foundry":  return newFoundryProvisioner(creds)
    case "vertex":   return newVertexProvisioner(creds)
    case "bedrock_core":
        if !featureflag.IsEnabled("bedrock_core") {
            return nil, ErrBackendNotEnabled
        }
        return newBedrockCoreProvisioner(creds)
    case "":         return &NoopProvisioner{}, nil
    default:         return nil, fmt.Errorf("unknown backend type: %s", backendType)
    }
}
```

### 4. Credential binding

Cloud credentials are stored in the existing `kitchen_credentials` table (Pro) keyed by `(kitchen_id, provider_name)`. The provisioner resolves credentials from the store at deploy time — never from agent fields.

Credential types per backend:
- `foundry`: `azure_client_id`, `azure_client_secret`, `azure_tenant_id`, `foundry_project_endpoint`
- `vertex`: GCP service-account JSON or workload-identity annotation  
- `bedrock_core`: `aws_access_key_id`, `aws_secret_access_key`, `aws_region`

No credentials are stored on `Agent` or `AgentEnvironment` records.

### 5. Bake flow integration

When `AgentEnvironment.BackendType != ""`:

1. **Bake** → `provisioner.Deploy()` is called after image build. Returned endpoint is written to `agent.BackendEndpoint` and `agent.EnvEndpoints[env.Slug]`.
2. **Cool / Delete** → `provisioner.Teardown()` is called to de-provision.
3. **Health monitor** → Pro's existing health-check goroutine calls `provisioner.HealthCheck()` every 30 seconds. On repeated failure, agent status transitions to `burnt` with `tags.error` set.

### 6. Observability callback (ADR-0017 pattern)

The `AGENTOVEN_CONTROL_PLANE_URL` environment variable is injected into the deployed container at provisioning time (Foundry env vars, Vertex env spec, Bedrock env). Regardless of which cloud owns the compute, traces/metrics callback to AgentOven's OTEL pipeline. This is the same pattern already established by ADR-0017 for local and docker modes.

### 7. UI exposure (Pro dashboard)

`BakeModal` in `Agents.tsx` gains a "Cloud Backend" dropdown visible only when the kitchen has at least one cloud credential configured. Options: `(default)`, `Azure Foundry`, `GCP Vertex`, `AWS Bedrock (experimental)`.

The dropdown sets `backend_type` on the `AgentEnvironment` upsert payload.

---

## Consequences

**Positive:**
- Enterprises can target Foundry/Vertex/Bedrock without leaving the AgentOven control plane.
- `BackendProvisioner` is open for community plugins — third-party backends can implement the interface and register at startup.
- The A2A gateway (ADR-0007) remains the stable URL surface; cloud-backend URLs are an implementation detail hidden from recipe authors and integrators.
- Credential management is centralised in `kitchen_credentials`; no secret sprawl into agent records.

**Negative / Risks:**
- Cloud round-trips in the bake path add latency (minutes). Bake must become asynchronous for cloud backends (already planned for K8s mode).
- Bedrock AgentCore is unvalidated. `bedrock_core` backend ships behind a feature flag and is excluded from SLA guarantees until validated.
- Teardown is best-effort on Cool/Delete. A cloud-side orphan-resource audit job (outside scope of this ADR) should be added in a follow-up.

## Implementation plan

| Phase | Scope | Target |
|---|---|---|
| P0 | `BackendProvisioner` interface + `NoopProvisioner` + wire into bake/cool | 0.9.x Pro |
| P0 | `FoundryProvisioner` (Azure — validated) | 0.9.x Pro |
| P0 | Postgres migration: `backend_type` column on `agent_environments` | 0.9.x Pro |
| P0 | `BakeModal` backend-type dropdown in Agents.tsx | 0.9.x Pro |
| P1 | `VertexProvisioner` (GCP — validated) | 0.9.x Pro |
| P2 | `BedrockCoreProvisioner` (AWS — experimental, gated) | post-validation |
| P3 | Cloud orphan-resource cleanup job | TBD |

## Key files

| File | Change |
|---|---|
| `agentoven-pro/internal/deployment/provisioner.go` | New — `BackendProvisioner` interface + registry |
| `agentoven-pro/internal/deployment/foundry.go` | New — `FoundryProvisioner` |
| `agentoven-pro/internal/deployment/vertex.go` | New — `VertexProvisioner` |
| `agentoven-pro/internal/deployment/bedrock.go` | New — `BedrockCoreProvisioner` (experimental) |
| `agentoven/control-plane/pkg/models/models.go` | `BackendType string` on `AgentEnvironment` |
| `agentoven-pro/internal/store/postgres.go` | Migration + `backend_type` in queries |
| `agentoven-pro/internal/environment/handlers.go` | Invoke provisioner on bake/cool/delete |
| `agentoven-pro/dashboard/src/pages/Agents.tsx` | `BakeModal` backend-type dropdown |

## Related ADRs

- ADR-0007: Control Plane as A2A Gateway — stable URL surface preserved
- ADR-0014: Pluggable Scheduler Dispatcher — same extensibility pattern applied here
- ADR-0017: Framework-Native Managed Agents — `AGENTOVEN_CONTROL_PLANE_URL` callback pattern
