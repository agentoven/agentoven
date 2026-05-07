# ADR-0015: Agent Orchestrator — CRD-Based Lifecycle on Kubernetes

- **Status:** Accepted
- **Date:** 2026-05-05
- **Relates to:** [ADR-0012](0012-agent-packaging-distribution.md) (packaging), [ADR-0013](0013-agent-to-ui-protocol.md) (UI protocol)

## Context

The OSS `process.Manager` handles agent lifecycle on a single host (local process or Docker container). When AgentOven is deployed on Kubernetes, operators want declarative, reconciled agent deployments with autoscaling, rolling updates, and health-driven restart — none of which a hand-written process manager provides.

We also need a language-agnostic way to describe an "agent deployment" so that Pro features (environment promotion, version tracking, session routing) can attach metadata without coupling to Kubernetes concepts in the OSS layer.

## Decision

### `AgentDeployment` CRD

API group `agentoven.io/v1alpha1`, Kind `AgentDeployment`:

```yaml
spec:
  agentName: my-agent
  kitchenID: acme-corp
  image: ghcr.io/acme/my-agent:v1.2.3
  replicas: 2
  env: []
  resources: {}
  autoscaling:
    enabled: false
    minReplicas: 1
    maxReplicas: 10
    targetCPUUtilizationPercentage: 70
status:
  phase: Running|Pending|Failed|Terminating
  readyReplicas: 2
  version: v1.2.3
  conditions: []
```

### Operator (`cmd/operator/main.go`)

- Built with `controller-runtime` (sigs.k8s.io/controller-runtime).
- Reconciler creates/updates a `apps/v1 Deployment` + `v1 Service` (ClusterIP) with owner references.
- When `autoscaling.enabled`, creates/updates `autoscaling/v2 HPA`.
- Port `:8083` — `/healthz` liveness, `/readyz` readiness.
- `--leader-elect` flag enabled; uses `agentoven-operator-lease` Lease resource.

### Execution Mode Routing

| Mode | Handled by |
|------|-----------|
| `local` | `process.Manager` (unchanged) |
| `docker` | `process.Manager` Docker backend (unchanged) |
| `kubernetes` | `AgentDeployment` CR → operator reconciles |

The API server creates `AgentDeployment` records in Postgres; when mode=`kubernetes` it also creates the CR in the cluster via `k8s.io/client-go`.

### Standalone Scheduler Binary

`cmd/scheduler/main.go` contains the background scheduler loop separated from the API server binary, enabling independent scaling and deployment as a single-replica pod (see Pro ADR-0015 for production deployment details).

## Consequences

- **+** Kubernetes users get first-class declarative agent lifecycle.
- **+** Promotion/rollback can be expressed as CR spec updates.
- **+** HPA enables cost-efficient autoscaling.
- **–** Adds `controller-runtime`, `k8s.io/client-go`, `k8s.io/api` as new Go deps.
- **–** Operator pod requires `ClusterRole` with `deployments`, `services`, `hpa` verbs — must be documented.
- **Known limitation**: Multi-replica scheduler requires `pg_try_advisory_lock` for distributed locking; out of scope for v1.
