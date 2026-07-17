# ADR-0025: Agent as a First-Class Identity Principal

**Status:** Proposed  
**Date:** 2026-07-07  
**Authors:** AgentOven Engineering  
**Scope:** OSS + Pro

---

## Context

AgentOven's authentication layer recognises three kinds of callers today:
- `user` — OIDC/SAML or username+password (Pro)
- `service_account` — `ao_sa_*` token issued to machines / CI
- `api_key` — kitchen-wide API key or `ao_sk_*` scoped key

None of these are the right choice when **one agent needs to call another agent**. The current workaround is to create a `ServiceAccount` and embed its token in the step's `AuthKey` field or a `KitchenCredential`. This is awkward for two reasons:

1. **No identity separation.** The `ServiceAccount` is shared across the whole kitchen — there is no record in the audit trail of *which* agent initiated a call. `actor_type = "service_account"` is the same for a CI pipeline and an LLM making autonomous calls.

2. **No capability model.** A service account carries a kitchen-wide `Role`. It cannot be restricted to calling only specific agents, and it does not inherit the kitchen-level `GatewayPolicy.allowed_principals` logic that service accounts do — agents calling agents bypass the principal-check step entirely.

3. **Agent-to-Agent (A2A) recipe steps.** When the workflow engine calls `executeAgentStep`, it forwards the original caller's bearer token from the triggering API request. If the recipe is triggered by a scheduled job (no end-user token), the step call arrives with no auth. The gateway then falls back to the kitchen API key — meaning all agent-initiated calls look identical in audit logs.

4. **No rate limit context.** The per-key rate limiting described in Pro ADR-0025 works on `(kitchen, agent, callerHash)`. When callerHash derives from a kitchen API key, all internal A2A calls share the same rate-limit bucket as external API consumers.

### Identity model before this ADR

```
contracts.Identity {
    Subject  string   // hash or sub claim
    Provider string   // "apikey" | "service_account" | "oidc" | "saml"
    Role     string   // flat role
    Kitchen  string
}
```

There is no `Kind` discriminator, no way to distinguish agent callers from human callers at the type level, and no `AgentName` or `ScopedKeyID` field to enable per-principal resource-level policies.

---

## Decision

### 1. `IdentityKind` discriminator on `Identity`

Add `Kind IdentityKind` to `contracts.Identity`:

```go
// IdentityKind classifies the type of principal.
type IdentityKind string

const (
    IdentityKindUser           IdentityKind = "user"            // human via OIDC/SAML/password
    IdentityKindServiceAccount IdentityKind = "service_account" // ao_sa_* token
    IdentityKindAPIKey         IdentityKind = "api_key"         // kitchen-wide API key
    IdentityKindScopedKey      IdentityKind = "scoped_key"      // ao_sk_* scoped key
    IdentityKindAgent          IdentityKind = "agent"           // agent acting autonomously
)
```

```go
type Identity struct {
    Subject     string       `json:"subject"`
    Email       string       `json:"email,omitempty"`
    DisplayName string       `json:"display_name,omitempty"`
    Provider    string       `json:"provider"` // auth mechanism
    Kind        IdentityKind `json:"kind"`     // NEW — principal classification
    Kitchen     string       `json:"kitchen,omitempty"`
    Role        string       `json:"role"`
    Groups      []string     `json:"groups,omitempty"`
    Claims      map[string]string `json:"claims,omitempty"`
    ExpiresAt   time.Time    `json:"expires_at,omitempty"`

    // NEW — populated only for scoped_key and agent callers.
    // Used by GatewayPolicy principal check and per-key rate limiting.
    ScopedKeyID string `json:"scoped_key_id,omitempty"` // scoped key record ID
    AgentName   string `json:"agent_name,omitempty"`    // originating agent name
}
```

All existing auth providers set `Kind` on the identities they return. Existing callers that don't check `Kind` are unaffected (backward compatible).

### 2. Agent identity token — `ao_ag_*`

Agents that need to call other agents are issued a short-lived **agent identity token** when the workflow engine spawns them:

```
Token format: ao_ag_<random>  — 32 bytes, base62 encoded
TTL: matches the recipe run timeout (or 1 hour for direct bake, whichever is shorter)
```

The token is injected as an environment variable into the spawned process:  
`AGENTOVEN_AGENT_TOKEN=ao_ag_...`

The engine also injects this token as the `Authorization: Bearer` header when calling agent steps in `executeAgentStep()` — replacing the current "forward original caller token or fall back to kitchen API key" logic.

**Token lifecycle:**
1. Engine mints the token at run start: `store.CreateAgentToken(ctx, kitchen, agentName, runID, ttl)`
2. Token is a random opaque value stored hashed (bcrypt, same as `ao_sk_*`)
3. `AgentTokenProvider` (new auth provider) recognises `ao_ag_` prefix, validates hash, resolves to an `Identity{Kind: IdentityKindAgent, AgentName: ..., Role: "agent"}`
4. Token is revoked when the run completes (store delete or a `revoked_at` timestamp)

**Model additions (`models.go`):**

```go
// AgentToken is a short-lived identity token for an agent acting as a caller.
// Issued by the workflow engine at run start; revoked on run completion.
type AgentToken struct {
    ID          string     `json:"id" db:"id"`
    TokenHash   string     `json:"-" db:"token_hash"` // bcrypt
    TokenPrefix string     `json:"token_prefix" db:"token_prefix"`
    Kitchen     string     `json:"kitchen" db:"kitchen"`
    AgentName   string     `json:"agent_name" db:"agent_name"`
    RunID       string     `json:"run_id" db:"run_id"`
    ExpiresAt   time.Time  `json:"expires_at" db:"expires_at"`
    RevokedAt   *time.Time `json:"revoked_at,omitempty" db:"revoked_at"`
    CreatedAt   time.Time  `json:"created_at" db:"created_at"`
}
```

**Store interface additions:**
```go
type AgentTokenStore interface {
    CreateAgentToken(ctx context.Context, token *models.AgentToken) error
    LookupAgentToken(ctx context.Context, tokenHash string) (*models.AgentToken, error)
    RevokeAgentToken(ctx context.Context, runID string) error // revoke all tokens for a run
}
```

### 3. Agent principal in `GatewayPolicy.allowed_principals`

`GatewayPolicy.allowed_principals` (Pro ADR-0025) is extended to support `agent:` prefixed entries:

```json
{
  "allowed_principals": [
    "sa:my-ci-pipeline",
    "agent:data-collector-agent",
    "agent:*"
  ]
}
```

The principal check in `A2AAgentEndpoint` resolves the `caller.Kind`:
- `IdentityKindServiceAccount` → check for `sa:<subject>` or `sa:*`
- `IdentityKindAgent` → check for `agent:<agentName>` or `agent:*`
- `IdentityKindScopedKey` → check for `sk:<scopedKeyID>` or `sk:*`
- Other kinds → treat as a user principal (subject claim match)

A `GatewayPolicy` with no `allowed_principals` (nil/empty) continues to allow all authenticated callers (backward compatible).

### 4. Audit trail enrichment

All handlers that emit `AuditEvent` now populate:
- `actor_type` from `identity.Kind` (already has the string values; now strongly typed)
- Two new fields:

```go
// NEW in AuditEvent:
AgentCaller string `json:"agent_caller,omitempty" db:"agent_caller"` // set when kind=agent
ScopedKeyID string `json:"scoped_key_id,omitempty" db:"scoped_key_id"` // set when kind=scoped_key
```

This makes it possible to query: "show me all invoke calls made autonomously by `data-collector-agent`" — distinguishable from calls made by a human or a service account.

### 5. `AGENTOVEN_CONTROL_PLANE_URL` and self-identification

ADR-0017 injects `AGENTOVEN_CONTROL_PLANE_URL` into spawned processes. This ADR adds `AGENTOVEN_AGENT_TOKEN` alongside it. Framework-native agents (LangChain, LangGraph, etc.) that call other agents using the AgentOven Python/TS SDK automatically pick up both env vars — they call out to the control plane using their agent identity, without the operator needing to provision a service account manually.

---

## Consequences

**Positive:**
- Audit logs distinguish human invocations from autonomous agent invocations unambiguously.
- Per-agent rate limiting (Pro ADR-0025 `allowed_principals`) can now gate agent-to-agent calls with a separate bucket from human callers.
- No service-account credential management required for recipe steps — the engine mints tokens automatically.
- `agent:*` in `allowed_principals` lets operators whitelist all agent callers globally with one rule; `agent:foo` scopes it to a specific agent.

**Negative / Risks:**
- Short-lived token store requires the OSS MemoryStore to hold `AgentToken` records. On restart, in-flight runs will have their agent tokens invalidated — they'll need to be re-baked. This is acceptable (same behaviour as the existing in-memory gate channels).
- Agent token revocation on run completion requires the engine's run-terminal-state transition to call `RevokeAgentToken`. If the control plane crashes between run terminal and revoke, the token survives until `ExpiresAt`. Mitigated by short TTL.

## Implementation plan

| Phase | Scope | Target |
|---|---|---|
| P0 | `IdentityKind` on `Identity` struct; backfill all auth providers | 0.9.x OSS + Pro |
| P0 | `AgentToken` model + store interface + MemoryStore impl | 0.9.x OSS |
| P0 | `AgentTokenProvider` auth provider; register in both OSS and Pro | 0.9.x OSS + Pro |
| P0 | Engine: mint token at run start, inject as `AGENTOVEN_AGENT_TOKEN`, use in `executeAgentStep` | 0.9.x OSS |
| P0 | Engine: revoke tokens on run terminal state | 0.9.x OSS |
| P1 | Pro Postgres migration: `agent_tokens` table | 0.9.x Pro |
| P1 | `GatewayPolicy.allowed_principals` `agent:` prefix support | 0.9.x Pro |
| P1 | Audit trail `agent_caller` + `scoped_key_id` fields | 0.9.x Pro |
| P2 | Dashboard: show `agent_caller` in Audit Trail page | TBD |

## Key files

| File | Change |
|---|---|
| `agentoven/control-plane/pkg/contracts/auth.go` | `IdentityKind`, `Kind`/`AgentName`/`ScopedKeyID` on `Identity` |
| `agentoven/control-plane/pkg/models/models.go` | `AgentToken` struct |
| `agentoven/control-plane/internal/store/store.go` | `AgentTokenStore` interface |
| `agentoven/control-plane/internal/store/memory.go` | `AgentToken` in-memory impl |
| `agentoven/control-plane/internal/providers/agenttoken/provider.go` | New — `AgentTokenProvider` |
| `agentoven/control-plane/internal/workflow/engine.go` | Mint/revoke token; inject `AGENTOVEN_AGENT_TOKEN`; use in `executeAgentStep` |
| `agentoven/control-plane/internal/api/handlers/handlers.go` | Audit `agent_caller` + `scoped_key_id` on invoke/A2A events |
| `agentoven-pro/internal/store/postgres.go` | `agent_tokens` table migration |
| `agentoven-pro/internal/gateway/policy.go` | `agent:` principal prefix in allowed_principals check |

## Related ADRs

- ADR-0002: Pluggable Auth Provider Chain — `AgentTokenProvider` registers as a new link
- ADR-0007: Control Plane as A2A Gateway — `agent:` principal check in gateway handler
- ADR-0017: Framework-Native Managed Agents — `AGENTOVEN_AGENT_TOKEN` alongside existing `AGENTOVEN_CONTROL_PLANE_URL`
- Pro ADR-0025: Per-Agent Gateway Policy — `allowed_principals` extended here
- Pro ADR-0026: Dashboard RBAC & OIDC — `IdentityKind` makes `actor_type` strongly typed
