# ADR-0007: Control Plane as A2A Gateway (Stable Agent URLs)

- **Date:** 2026-02-22
- **Status:** Accepted
- **Deciders:** siddartha

## Context

When an agent is baked, the Process Executor spawns a subprocess (local,
Docker, or Kubernetes) that listens on a dynamic port (e.g. `http://localhost:9100`).
The original implementation overwrote the agent's `a2a_endpoint` with this
subprocess URL, causing several problems:

1. **Unstable URLs** — the endpoint changes every time the agent is baked or
   restarted. Clients that cache the URL break.
2. **Bypasses auth / RBAC** — if a client calls `http://localhost:9100` directly,
   the control plane's authentication middleware and Pro's RBAC enforcement are
   skipped entirely.
3. **No observability** — direct calls bypass telemetry, audit logging, and rate
   limiting in the control plane.
4. **External agents exposed raw** — external agents couldn't be wrapped with
   AgentOven's auth layer; their raw endpoints would be shared with clients.
5. **Multi-tenant leakage risk** — subprocess URLs aren't kitchen-scoped, so a
   malicious client in kitchen A could call an agent in kitchen B directly.

## Decision

**The control plane is the single A2A gateway.** Agent URLs are stable and never
change. The A2A per-agent endpoint (`/agents/{name}/a2a`) is a reverse proxy that
resolves the actual backend and forwards JSON-RPC requests.

### URL Model

```
Client  ──POST──►  /agents/{name}/a2a   ──proxy──►  backend
                   ^^^^^^^^^^^^^^^^^^^              ^^^^^^^^^
                   stable, never changes            dynamic
```

| Agent Mode | Backend Resolution |
|------------|--------------------|
| `managed`  | `agent.Process.Endpoint` (subprocess on dynamic port) |
| `external` | `agent.BackendEndpoint` (user-provided URL) |

### Changes Made

1. **BakeAgent** no longer overwrites `a2a_endpoint`. The process endpoint is
   stored in `ProcessInfo.Endpoint` only.
2. **`A2AAgentEndpoint`** handler rewritten from a stub into a real reverse
   proxy: reads agent → resolves backend → relays JSON-RPC → returns response.
3. **`BackendEndpoint`** field added to the Agent model for external agents.
4. **`GetAgentCard`** always returns the stable `/agents/{name}/a2a` URL,
   never the subprocess endpoint.

### Agent Card URL

The `/.well-known/agent-card.json` and per-agent card endpoints always advertise
the control plane URL. Clients never see internal infrastructure URLs.

## Consequences

### Positive

- **Stable URLs** — agents can be restarted, rescheduled, scaled without clients
  noticing. The control plane URL is the only contract.
- **Auth enforcement** — every A2A call goes through the auth middleware chain
  (API keys, service accounts, OIDC, SAML in Pro). RBAC can be applied per-agent.
- **External agent wrapping** — registering a third-party A2A agent with
  `mode: "external"` and `backend_endpoint: "https://..."` immediately wraps it
  with AgentOven auth, observability, and rate limiting.
- **Observability** — all A2A traffic is visible in the control plane logs and
  OpenTelemetry traces.
- **Multi-tenant isolation** — the proxy validates kitchen scope before forwarding.

### Negative

- **Extra hop** — adds one HTTP hop (control plane → backend). Latency increase
  is negligible for LLM-backed agents (milliseconds vs seconds).
- **Control plane is SPOF** — if the control plane is down, no agent is reachable.
  Mitigated by standard HA patterns (replicas, load balancer).

### Neutral

- **Pro RBAC** — the proxy handler is the natural RBAC enforcement point. Pro's
  `TierEnforcer` and `RBACMiddleware` sit before `A2AAgentEndpoint` in the
  middleware chain.

## Related

- [ADR-0002](0002-pluggable-auth-provider-chain.md) — Auth provider chain
- [AUTH-PLAN.md](../AUTH-PLAN.md) — Full authentication architecture
- [ISSUES.md](../ISSUES.md) — ISS-004 (BakeAgent race condition)
