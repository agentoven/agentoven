# ADR-0008: Three-Layer Product Architecture (Dashboard · Pro Dashboard · Agent Viewer)

- **Status:** Accepted
- **Date:** 2026-03-01
- **Author(s):** Siddartha Kopparapu
- **Related:** [ADR-0003](0003-open-core-two-repo-model.md) (Open-core two-repo model)

## Context

AgentOven has three distinct user personas with different access needs:

| Persona | What they need | Auth level |
|---------|---------------|------------|
| **Developer / Baker** | Build, test, deploy agents. See logs, traces, guardrails. | Full control plane access |
| **Platform Admin** | Multi-kitchen management, RBAC, compliance, cost analytics, audit trails | Authenticated admin with enterprise IdP |
| **Agent Consumer** | Run agents, view outputs, interact with recipes. No control plane knowledge. | Scoped API key tied to specific agent(s) |

Today, one React dashboard (OSS) tries to serve all three. The Pro repo has a
Streamlit app (`agentoven-pro/dashboard/app.py`, 506 lines) with compliance
pages (audit trail, cost analytics, license status, etc.) that overlaps with
what the Pro Dashboard should do.

### Problems with a Single Dashboard

1. **OSS dashboard is public** — adding enterprise features via feature flags
   means the code is visible. Coding agents (Copilot, Cursor, Codex) can trivially
   strip license checks from public code.
2. **Consumer-facing UX is fundamentally different** — agent consumers don't need
   to see provider configs, MCP tools, or trace spans. They need a simple
   chat/run/output interface.
3. **Login/auth complexity** — OSS dashboard is no-auth for local dev. Mixing auth
   flows (SSO, RBAC) into the same app creates configuration complexity.
4. **Deployment topology** — enterprises want the admin dashboard behind VPN and
   the agent viewer exposed to end users. Same app can't serve both.

## Decision

AgentOven uses a **three-layer product architecture** for user-facing surfaces:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Layer 1: OSS Dashboard                        │
│              (React 19 + Vite + Tailwind)                       │
│                                                                  │
│   Repo: ent-agent-core/control-plane/dashboard/                  │
│   Auth: None (local dev tool)                                    │
│   Users: Developers building agents locally                      │
│   Features: Agent CRUD, recipe builder, provider config,         │
│             MCP tools, traces, RAG pipelines, guardrail config   │
│   Deployment: localhost:5175 (bundled with control plane)        │
└─────────────────────────────────────────────────────────────────┘
                              │
                    imports shared components
                              │
┌─────────────────────────────────────────────────────────────────┐
│                   Layer 2: Pro Dashboard                         │
│              (React 19 + Vite + Tailwind)                       │
│                                                                  │
│   Repo: agentoven-pro/dashboard/                                 │
│   Auth: SSO/SAML/OIDC via Pro auth providers                     │
│   Users: Platform admins, ML/AI engineers                        │
│   Features: Everything in OSS Dashboard PLUS:                    │
│     - Login page + session management                            │
│     - Multi-kitchen switching                                    │
│     - RBAC-aware UI (admin vs baker vs viewer)                   │
│     - Compliance & audit trail                                   │
│     - Cost analytics & chargeback                                │
│     - License status & tier management                           │
│     - Provider quota dashboards                                  │
│     - User/team management                                       │
│   Deployment: Internal/VPN, enterprise-managed                   │
└─────────────────────────────────────────────────────────────────┘
                              │
                    calls agent APIs
                              │
┌─────────────────────────────────────────────────────────────────┐
│               Layer 3: Agent Viewer (Streamlit)                  │
│              (Streamlit + auto-generated from agent metadata)    │
│                                                                  │
│   Repo: ent-agent-core/control-plane/viewer/ (new, OSS)         │
│   Auth: Scoped API keys (per-agent, per-consumer)                │
│   Users: End users consuming agents, business stakeholders       │
│   Features:                                                      │
│     - Chat interface for conversational agents                   │
│     - Form-based input for structured agents                     │
│     - Output rendering (markdown, JSON, tables, charts)          │
│     - Recipe run visualization (DAG progress)                    │
│     - Shareable URLs (agent_key + viewer link)                   │
│     - Usage tracking tied to scoped key                          │
│     - No control plane concepts exposed                          │
│   Deployment: Public-facing, shareable link                      │
│   Analogy: HuggingFace Spaces for your private agents           │
└─────────────────────────────────────────────────────────────────┘
```

### Why Streamlit for the Agent Viewer?

1. **Auto-generation** — Streamlit apps can be generated from agent metadata
   (input schema, output format, description). No frontend engineering needed.
2. **Low barrier** — data scientists and ML engineers already know Streamlit.
   They can customize the viewer without learning React.
3. **Embeddable** — Streamlit apps can be embedded in iframes, shared via URLs,
   and deployed as standalone containers.
4. **Rapid iteration** — Python-native, hot-reloads. Perfect for consumer-facing
   UX that changes per agent.

### Why NOT Feature Flags in OSS Dashboard?

This decision was carefully evaluated. See "Alternatives Considered" below.

The critical insight: **OSS code is public**. Modern coding agents (Copilot,
Cursor, Codex) can read the entire codebase and trivially strip license checks,
feature flags, or conditional imports. The two-repo model (ADR-0003) ensures Pro
code is **invisible** to OSS users, not just gated.

### Scoped API Keys (New Concept)

The Agent Viewer introduces **scoped API keys** — API keys that grant access to
specific agents only, with usage quotas and traceability:

```go
type ScopedAPIKey struct {
    ID          string    `json:"id"`
    Key         string    `json:"key"`          // hashed, never stored plain
    Kitchen     string    `json:"kitchen"`
    AgentNames  []string  `json:"agent_names"`  // which agents this key can access
    MaxCalls    int       `json:"max_calls"`     // 0 = unlimited
    CallCount   int       `json:"call_count"`    // current usage
    ExpiresAt   time.Time `json:"expires_at"`    // optional expiry
    CreatedBy   string    `json:"created_by"`    // who issued this key
    Label       string    `json:"label"`         // "marketing-team", "client-demo"
}
```

This enables:
- **Traceability** — every agent call via viewer is tied to a key and creator
- **Quota management** — demo keys with 100-call limits
- **Revocation** — revoke a consumer's access without affecting other users
- **Cost attribution** — usage tracked per key for chargeback

## Consequences

### Positive

- **Clear separation of concerns** — each layer serves one persona with focused UX
- **Business model alignment** — OSS (free) → Pro Dashboard (paid) → Agent Viewer
  (drives adoption, creates lock-in via consumer dependence)
- **Security** — Pro code stays in private repo. Scoped keys limit blast radius.
- **Deployment flexibility** — enterprises can deploy each layer independently
  (admin on VPN, viewer public, OSS for dev laptops)

### Negative

- **Three codebases to maintain** — more surface area. Mitigated by:
  OSS dashboard is baseline, Pro dashboard extends it, viewer is simple Streamlit.
- **Shared component divergence** — OSS and Pro dashboards may diverge. Mitigated
  by Pro importing OSS components (same pattern as Go backend).
- **Streamlit limitations** — not suitable for complex admin UIs (no built-in auth,
  limited routing). That's fine — admin UIs are React.

### Neutral

- **Current Streamlit compliance pages** (audit trail, cost analytics, license
  status) should migrate to the Pro React Dashboard. The Streamlit app should be
  rewritten as the Agent Viewer.
- **Pro Dashboard shares the same React stack** as OSS. The Pro repo can copy the
  OSS dashboard as a starting point, then add enterprise pages.

## Alternatives Considered

### 1. Feature flags in OSS Dashboard

**Rejected.** OSS code is public on GitHub. Feature flags (even server-side) are
visible in source. Coding agents can strip them. The two-repo model is intentional
business protection, not just code organization.

### 2. Single dashboard with role-based page visibility

**Rejected.** Still requires all code in one repo. The consumer-facing UX is
fundamentally different from admin UX — different layout, different auth model,
different deployment topology.

### 3. React for Agent Viewer instead of Streamlit

**Partially rejected.** React would work but requires frontend engineering for
every agent. Streamlit enables auto-generation from agent metadata, which is the
key value prop. A React viewer could be offered as a Pro option later.

### 4. Gradio instead of Streamlit

**Deferred.** Gradio is HuggingFace's framework and has better ML-specific
components. Worth evaluating but Streamlit has broader adoption and simpler
embedding story. Could support both in the agent viewer framework.

## Migration Path

1. **Current Streamlit app** (`agentoven-pro/dashboard/app.py`) → pages move to
   Pro React Dashboard (compliance, audit, cost, license)
2. **New Streamlit viewer** (`ent-agent-core/control-plane/viewer/`) → auto-generated
   from agent metadata, chat/run/output interface
3. **Pro React Dashboard** (`agentoven-pro/dashboard/`) → new React app, imports
   OSS dashboard components, adds login + enterprise pages
4. **Scoped API keys** → new store interface, CRUD API, auth provider
