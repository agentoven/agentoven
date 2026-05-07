# ADR-0016: External Agent Registration and Traceability

- **Status:** Accepted
- **Date:** 2026-05-05
- **Relates to:** [ADR-0007](0007-control-plane-as-a2a-gateway.md) (A2A gateway), [ADR-0012](0012-agent-packaging-distribution.md) (packaging), [ADR-0013](0013-agent-to-ui-protocol.md) (UI protocol)

## Context

AgentOven manages agents it directly deploys (local, Docker, Kubernetes). However two agent classes exist outside this model:

1. **PicoClaw agents**: IoT/edge agents (Rust, C, MicroPython) that run on constrained hardware. They cannot be managed by the operator. They register via a lightweight heartbeat protocol.
2. **External A2A agents**: LangChain, Semantic Kernel, AutoGen, or any A2A-compatible agent running in external infrastructure. They want to participate in AO kitchens (be callable, be traced) without being managed by AO.

These agents must be registered, proxied, and traced so that cross-agent call chains and session threads are visible in the dashboard.

## Decision

### Registration (no new table)

`POST /api/v1/agents/external/register` creates or upserts an `Agent` record in the existing `agents` table with:
- `mode = "external"` (new enum value alongside `local`, `docker`, `kubernetes`)
- `endpoint_url`: the agent's A2A endpoint
- `external_trace_id`: opaque reference for correlating external telemetry

No separate `external_agents` table. Mode discrimination is sufficient.

### Proxy

External agents are proxied via the existing A2A gateway at `/agents/{name}/a2a`. No new infrastructure required.

### Heartbeat (PicoClaw)

`POST /api/v1/agents/{name}/heartbeat` — authenticated with an agent-scoped API key. Payload: `{ version, ip, resources: { cpu_pct, mem_free_kb } }`. Updates `last_seen_at` and `status` fields.

### Traceability

All trace spans emitted by/to external agents include:
- `external_trace_id` field linking to the external system's trace ID.
- `agent_mode` tag so dashboards can distinguish managed vs. external.

`GET /api/v1/kitchens/{k}/traces/matrix` returns the Traceability Matrix: for each (agent, environment) cell, the latest trace summary, test results, and promotion status.

### ExternalTraceID Flow

```
External Agent → AO Gateway → AO Trace record (external_trace_id = X-External-Trace-ID header)
AO Trace record links to: KitchenID, RecipeName, Environment, AgentName
```

## Consequences

- **+** No schema migration beyond adding `mode` and `external_trace_id` columns.
- **+** PicoClaw devices need only POST a heartbeat; no full A2A stack required.
- **+** Traceability Matrix gives a full cross-environment, cross-agent view.
- **–** External agents self-report their trace ID; AO cannot verify it matches external telemetry.
- **–** Heartbeat is best-effort; stale edge agents show `status=offline` after `last_seen_at + 90s`.
