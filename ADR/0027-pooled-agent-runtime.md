# ADR-0027: Pooled Agent Runtime — Request-Time Config Injection into a Per-Kitchen Warm Process Pool

- **Status:** Proposed
- **Date:** 2026-08-18
- **Author(s):** AgentOven Engineering

---

## Context

### What already exists

AgentOven already treats an agent as configuration, not as code. `Dockerfile.agent`
builds **one generic image** (`ghcr.io/agentoven/agentoven:<version>`) containing a
single file — `agentoven/control-plane/internal/process/templates/agent_runner.py`.
Every agent, regardless of persona, model, or tools, runs that same binary. Agent
identity arrives entirely through environment variables:

```
AGENT_NAME, AGENT_KITCHEN, AGENT_DESCRIPTION, AGENT_MODEL_PROVIDER,
AGENT_MODEL_NAME, AGENT_API_KEY, AGENT_API_ENDPOINT, AGENT_TOOLS_JSON,
AGENT_MAX_TURNS, AGENT_SKILLS, AGENTOVEN_CONTROL_PLANE_URL, CONTROL_PLANE_TOKEN
```

The config is therefore already decoupled from the image. What is *not* decoupled
is **when** the config is bound: `agent_runner.py` reads these variables at module
import time into module-level globals, and derives `RESOLVED_TOOLS` and `TOOL_DEFS`
from them at import time as well. Config is bound at **process start**.

Because config binds at process start, the operator
(`agentoven-pro/internal/operator/k8sclient.go`) must create **one Deployment and
one Service per agent** (`ao-<agentName>`) in the kitchen-scoped namespace
(ADR-0022). N agents in a kitchen means N Deployments, N Services, N ReplicaSets,
N pods, and N pod IPs — even when every one of them is idle.

### The problem

Four distinct costs follow from one-pod-per-agent:

1. **Infra cost.** Idle pods hold reserved CPU/memory requests continuously.
2. **Cold-start latency.** The operator already supports `replicas: 0` — the
   reconciler comment explicitly states *"replicas == 0 means the workload is
   intentionally cooled (scaled to zero) … Do NOT floor to 1."* But scaling to zero
   trades cost for a multi-second cold start on the next invocation, against a
   typical agent turn of 1–3s. Scale-to-zero fixes cost and **worsens** latency.
3. **Density / scale ceiling.** Kubernetes object count and pod IP consumption grow
   linearly with agent count. On AKS with non-overlay Azure CNI, every pod consumes a
   VNet subnet IP, making IP exhaustion — not CPU — the real ceiling.
4. **Operational surface.** Every agent is an independent rollout, health target, and
   failure domain to observe and debug.

Scale-to-zero alone addresses only (1).

### Why this is now feasible

Two existing decisions remove the usual blockers to a shared runtime:

- **ADR-0007** makes the control plane the sole A2A gateway. Callers use the stable
  `/agents/{name}/a2a` URL and `ResolveBackendEndpoint(agent)` decides the backend.
  Changing where an agent physically runs is invisible to every client.
- **ADR-0014** externalises session state into Postgres with an optional Redis cache
  (`HybridStore`). Conversation state does not live in the agent process. A shared
  runtime therefore needs **no sticky routing** — the usual killer of pooled designs
  is already solved.

Additionally, `AgentEnvironment.RecipeHash` (SHA-256 of resolved ingredients JSON)
is a ready-made cache key for a resolved agent configuration.

---

## Decision

Move the config injection point from **process start** to **request time**, and run
light agents as tasks inside a warm, shared, **per-kitchen** runtime pool.

This is a refactor of an existing config-driven runtime, not a re-architecture.

### 1. Three execution tiers

`Agent.ExecutionMode` gains a fourth value, `pooled`, alongside the existing
`local`, `docker`, and `k8s` (ADR-0017):

| Tier | When | Isolation |
|---|---|---|
| `pooled` | Light, bursty, `runtime == "agentoven"` agents | Task-level, in-process |
| `k8s` | Heavy, long-running, or noisy agents; all non-`agentoven` runtimes | Pod-level (unchanged) |
| `local` / `docker` | Development | Unchanged |

`pooled` is **opt-in and explicit per agent**. The system never silently promotes or
demotes an agent between tiers.

### 2. Pooling applies only to `runtime == "agentoven"`

`models.Agent.Runtime` distinguishes the built-in Go/Python executor (`agentoven`)
from framework-native agents (`langchain`, `langgraph`, `crewai`, `custom`), which
own their own loop via `Entrypoint` (e.g. `python myagent.py`) and carry arbitrary
dependencies. Those agents **cannot** be config-injected and remain on dedicated pods
permanently. Pooling is a **second execution mode, not a replacement**.

### 3. The pool is per-kitchen, never global

One pooled Deployment + Service per kitchen namespace, reconciled by the operator.

A global pool is explicitly rejected. `EnsureNamespace(ctx, ns, kitchenID)` (ADR-0022)
is a real Kubernetes tenancy boundary, and `AGENT_API_KEY` is today scoped to a single
agent's pod. A cross-tenant pool would hold many tenants' provider keys and MCP
credentials inside one process, so a single successful prompt injection could exfiltrate
across tenants — directly undercutting the compliance posture of Pro ADR-0031
(immutable audit trail) and Pro ADR-0030 (authority delegation).

### 4. `agent_runner.py` gains a `RunContext`

Module-level globals are refactored into a per-request `RunContext` carrying the agent
identity, model config, resolved tools, tool definitions, max turns, and skills. The
file already has `_REQUEST_CONTEXT = threading.local()` — currently used only to carry
per-request SSL context (`_set_request_ssl_context` / `_restore_request_ssl_context` /
`_urlopen`). That mechanism is extended to carry the full `RunContext`.

A new endpoint `POST /run` accepts `{agent_config, message, session_id, run_id}`. The
existing env-var boot path is retained as the default so every dedicated pod is
untouched by this change. Resolved configs are cached by `RecipeHash`.

### 5. Dispatch through the existing gateway

`ResolveBackendEndpoint` gains a `pooled` branch returning the kitchen pool endpoint,
and `proxyA2ARequest` injects the resolved agent config into the request body for
pooled agents. Environment-scoped routing (`resolveEnvBackendEndpoint`, and
`AgentEnvironment` provider/tool overrides and guardrail policy) continues to apply.

If the pool is unavailable or rejects the run as over capacity, dispatch **falls back**
to the agent's dedicated-pod path.

### 6. In-pool isolation is first-class, not deferred

Losing per-agent CPU/memory isolation is not acceptable. Because the pod boundary no
longer separates pooled agents, the following are **part of this decision, not follow-up
work**:

| Control | Purpose |
|---|---|
| Per-run timeout with hard cancellation | Nothing exists today beyond `MAX_TURNS` |
| Per-agent concurrency cap inside the pool | One agent cannot starve its neighbours |
| Per-agent circuit breaker | Repeated failures trip that agent only, never the pool |
| Per-run credential scoping | Only the invoked agent's credentials enter `RunContext`; the pool never holds a kitchen's full credential set in one scope |
| Per-run wall-clock + in-process CPU sampling | Replaces the pod-level CPU/memory attribution that pooling removes |

Autoscaling moves from per-agent `replicas` to an HPA on the pool driven by concurrent
runs.

---

## Consequences

### Easier

- Idle cost for a kitchen becomes a function of pool size, not agent count.
- Warm-process dispatch removes cold start for pooled agents, so scale-to-zero economics
  become achievable *without* the latency penalty.
- Kubernetes object count and pod IP consumption decouple from agent count.
- One rollout, one health target, and one log stream per kitchen instead of per agent.
- Adding an agent becomes a database write plus a config resolution, not a Deployment
  rollout.

### Harder

- The pod is no longer the blast radius. A crash, memory leak, or unbounded tool loop in
  one pooled agent is a neighbour's problem, mitigated only by the section-6 controls.
- Per-agent CPU/memory attribution must be reconstructed in-process rather than read from
  the kubelet.
- Two dispatch paths and two runtime code paths (`/run` and env-var boot) must be kept
  behaviour-identical, and tested as such.
- Debugging shifts from "exec into the agent's pod" to correlating runs by `run_id` inside
  a shared process.
- A pool restart affects every pooled agent in the kitchen simultaneously.

### Sequencing note

Pro ADR-0030 (Agent Authority Delegation and Capability Grants) brokers all tool calls
through the control plane and removes raw tool endpoints and credentials from agent pods.
Landing that before — or together with — the isolation phase largely dissolves the
credential-scoping risk, because the pool would then hold no tool credentials at all.
This sequencing is strongly recommended.

### Verification

1. **Isolation** — an agent with an infinite tool loop trips only its own circuit breaker;
   co-resident agents keep serving.
2. **Tenancy** — a pooled run for kitchen X cannot read kitchen Y's credentials; exactly
   one pool pod exists per kitchen namespace.
3. **Latency** — p50/p95 for pooled vs dedicated-pod vs cold-started (`0→1`) agents. Pooled
   must beat cold start decisively or the premise fails.
4. **Density** — Kubernetes object count and pod IP consumption before/after for a kitchen
   with N agents.
5. **Compatibility** — `langchain`/`crewai`/`custom` agents run unchanged on dedicated pods;
   environment-scoped routing and guardrail policies still apply to pooled agents.
6. **Fallback** — killing the pool degrades pooled agents to dedicated pods with no
   client-visible error.

---

## Alternatives Considered

### Scale to zero only

Already supported by the operator. Rejected as a complete answer: it addresses idle cost
but makes cold-start latency *worse*, and does nothing for object/IP density or operational
surface. It remains complementary — dedicated-tier agents should still cool to zero.

### Pod per invocation

Rejected. Pod scheduling and image pull take seconds against a 1–3s agent turn, so this is
strictly worse than the status quo on latency while adding churn to the API server.

### One global runtime pool

Rejected on tenancy grounds — see decision 3.

### Automatic tier selection

Rejected for the initial implementation. Implicit promotion and demotion of a production
agent between isolation models is surprising and hard to reason about during an incident.
Heavy-agent detection instead **recommends** promotion to a dedicated pod and surfaces it in
the dashboard; a human decides.

---

## Open Questions

- Is the pooled tier OSS or Enterprise-gated (ADR-0003 open-core split)? The runtime change
  is in OSS (`agent_runner.py`); the pool workload and HPA are in Pro (operator).
- Confirm the AKS CNI mode (overlay vs non-overlay) to establish whether pod IPs are in fact
  the density ceiling for the deployed topology.

---

## Related ADRs

- ADR-0007: Control Plane as A2A Gateway — pooled dispatch is a new `ResolveBackendEndpoint` branch
- ADR-0015: Agent Orchestrator K8s CRD — pool workload reconciliation
- ADR-0017: Framework-Native Managed Agents — defines the `runtime` values that are *not* poolable
- ADR-0023: Pluggable Cloud Agent Deployment Backend — an orthogonal fifth backend class; both extend `ExecutionMode`
- ADR-0024: Advanced Recipe Flow and HPA — pool HPA replaces per-agent HPA fields for pooled agents
- ADR-0025: Agent as Identity Principal — per-run agent tokens are the natural carrier for per-run credential scoping
- Pro ADR-0014: Hybrid Redis/Postgres Sessions — the reason no sticky routing is required
- Pro ADR-0018: Kubernetes/Helm Infra Separation — pool chart values
- Pro ADR-0022: Kitchen Namespace Isolation — the tenancy boundary the pool must not cross
- Pro ADR-0030: Agent Authority Delegation and Capability Grants — removes tool credentials from the pool
