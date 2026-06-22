# ADR-0021: Release 0.9.0 OSS Architecture — SSE, Skills Plugin System, Multimodal, RAG Multi-Pipeline

**Status:** Accepted  
**Date:** 2026-06-23  
**Authors:** AgentOven Engineering

---

## Context

0.8.x delivered the core agent execution loop, MCP tool gateway, recipe pipelines, A2A protocol, and the in-process observability pipeline. The OSS control plane is feature-complete for single-agent, text-in/text-out, synchronous workloads.

0.9.0 addresses four gaps that limit adoption:

1. **No streaming** — `Executor.Execute()` is synchronous; clients must wait for the full response before rendering anything. `StreamInvokeAgent` exists but only works when an external agent process is running. A2A advertises `"streaming": true` but is entirely synchronous.

2. **No pluggable skills** — agents can call external MCP tools but have no way to express "this agent knows how to search the web" in a first-class way. Adding built-in capabilities today requires modifying the control plane Go code and recompiling.

3. **Multimodal input is wired nowhere** — `ContentPart` with `image_url` is declared on `ChatMessage` but `SendSessionMessage` discards it entirely. The A2A Go handler silently drops `FilePart`/`DataPart` that the Rust crate correctly parses.

4. **RAG has a single pipeline per kitchen** — no named pipelines, no server-side chunking, client must pre-extract and pre-chunk documents.

Additionally, three high-severity correctness bugs exist in the audit trail and auth system:
- `AuditEvent` has no `ActorType` — SA vs human vs API key is indistinguishable in audit queries
- `InvokeAgent` emits no audit event — the largest action leaves no audit record
- SA revocation not enforced — `ServiceAccountProvider` validates HMAC only, never checks store

---

## Decisions

### 1. SSE Streaming — Executor-Level Callback

**Decision:** Add `StreamChunk` model and `ExecuteStream(ctx, ..., chan<- StreamChunk)` to `internal/executor/executor.go`. Emit chunk types: `thinking`, `tool_call`, `tool_result`, `delta`, `done`.

`Execute()` becomes a thin wrapper around `ExecuteStream()` that accumulates and returns — no behaviour change for existing callers.

`StreamInvokeAgent` handler gains a non-pod branch: create channel, call `ExecuteStream`, write SSE events with same guardrails as `InvokeAgent` (scoped key check, guardrails, variable sanitization).

A2A `A2AEndpoint` implements `tasks/sendSubscribe` JSON-RPC method emitting `TaskStatusUpdateEvent` and `TaskArtifactUpdateEvent` per spec. Agent card `"streaming": true` is accurate only after this ships.

New endpoint: `POST /api/v1/recipes/{name}/runs/stream` — per-step SSE events from workflow engine (`step.started`, `step.completed`, `step.failed`).

**Why not proxy-only?** The pod-proxy path already works for framework-native agents. The gap is Go-executor agents (the default for managed agents). Fixing at the executor level closes it universally.

**Key files:**
- `pkg/models/models.go` — `StreamChunk` struct
- `internal/executor/executor.go` — `ExecuteStream()`
- `internal/api/handlers/handlers.go` — `StreamInvokeAgent` non-pod branch, A2A `tasks/sendSubscribe`
- `internal/workflow/engine.go` — step-level event emission

---

### 2. Skills Plugin System — MCP + Manifest, No Go Recompile Required

**Decision:** Skills are a first-class concept distinct from Providers (LLM backends) and MCP Tools (user integrations).

#### What makes a Skill different from an MCP Tool

| | MCP Tool | Skill |
|---|---|---|
| Protocol | MCP JSON-RPC | MCP JSON-RPC + `/.well-known/skill` manifest |
| Instructions | None | `instructions` block injected into agent system prompt |
| Config UI | Manual auth config | Auto-generated from `config_schema` in manifest |
| Who registers | Kitchen operators | Kitchen admins (manually, no auto-seed) |

A skill server is any MCP server that also exposes `GET /.well-known/skill` returning:

```json
{
  "name":         "web-search",
  "version":      "1.0.0",
  "description":  "Search the web using Tavily, Brave or SerpAPI",
  "category":     "research",
  "instructions": "## Web Search\nUse the `web_search` tool when the user needs...",
  "config_schema": [
    { "key": "api_key",    "label": "API Key",      "required": true,  "secret": true },
    { "key": "provider",   "label": "Provider",     "required": false, "default": "tavily",
      "options": ["tavily", "brave", "serpapi"] },
    { "key": "max_results","label": "Max Results",  "required": false, "default": "5" }
  ]
}
```

#### Registration flow

```
Kitchen Settings → Skills → Add Skill → paste URL
→ control plane fetches /.well-known/skill → caches manifest in MCPTool record
→ UI auto-renders config fields from config_schema
→ admin fills credentials → stored as KitchenCredential: skill.<name>.<key>
→ skill appears in ingredients picker as kind: skill
```

No auto-registration on boot. Admins register explicitly.

#### Model changes

`MCPTool` gains two fields (additions only, existing fields unchanged):

```go
IsSkill       bool           `json:"is_skill"        db:"is_skill"`
SkillManifest *SkillManifest `json:"skill_manifest"  db:"-"`  // cached, not DB-persisted
```

`SkillManifest` refreshed via `PATCH /api/v1/skills/{name}/refresh`.

New `IngredientKind`:
```go
IngredientSkill IngredientKind = "skill"
```

#### Executor integration

- `resolveSkill()` in resolver: loads manifest, resolves `KitchenCredential` values → `ResolvedSkill`
- `buildSystemPrompt()`: appends `skill.manifest.instructions` as `## Skills` block
- `buildToolDefinitions()`: injects skill's MCP `tools/list` response as additional `ToolDefinition`s
- `executeTool()`: credentials attached to outgoing MCP gateway call via existing `applyAuth()` — no new transport code

#### Built-in skill containers

Four containers ship with 0.9.0 as standalone deployable images (not embedded in the control plane):

| Image | Skill name | What it does |
|---|---|---|
| `ghcr.io/agentoven/skill-web-search:0.9.0` | `web_search` | Tavily/Brave/SerpAPI web search |
| `ghcr.io/agentoven/skill-deep-research:0.9.0` | `deep_research` | Multi-step search + LLM synthesis |
| `ghcr.io/agentoven/skill-vision:0.9.0` | `vision` | Image input formatting for vision models |
| `ghcr.io/agentoven/skill-document:0.9.0` | `document_analysis` | PDF/DOCX via OpenAI Files / Anthropic document blocks |

These live in `agentoven/skills/` (subdirectory of this repo). Each is a standalone service built and released independently of the control plane.

**Why containers, not embedded code?** Embedding requires recompiling the control plane for every skill. Containers ship on their own cadence, in any language, deployable anywhere. A community skill author needs zero knowledge of Go or the control plane internals.

**Key files:**
- `pkg/models/models.go` — `SkillManifest`, `IngredientSkill`, `MCPTool.IsSkill`
- `internal/resolver/resolver.go` — `resolveSkill()`
- `internal/executor/executor.go` — `buildSystemPrompt`, `buildToolDefinitions`, `executeTool`
- `internal/api/handlers/handlers.go` — `POST /api/v1/skills/register`, `GET /api/v1/skills`, `PATCH /api/v1/skills/{name}/refresh`
- `skills/web-search/`, `skills/deep-research/`, `skills/vision/`, `skills/document/` — new skill containers

---

### 3. Multimodal — Provider Pass-Through

**Decision:** No server-side PDF/DOCX parsing in the control plane in 0.9.0. Two sub-paths:

**Images (vision):** Wire existing `ContentParts` through the full stack:
1. `SendSessionMessage` handler: forward `content_parts` into `ChatMessage` (currently discarded)
2. LLM router: translate `ContentPart{type:"image_url"}` → provider-native multi-part content array (OpenAI `{type:"image_url", image_url:{url:...}}`, Anthropic `{type:"image", source:{...}}`)
3. Model router drivers gain `SupportsVision() bool` capability flag
4. A2A Go handler: parse `FilePart`/`DataPart` from message parts instead of silently dropping them

**Documents:** Handled entirely by the `skill-document` container via OpenAI Files API or Anthropic document blocks. No binary upload to the control plane in 0.9.0.

**Why not server-side parsing?** PDF rendering fidelity (poppler/ghostscript), DOCX complex formatting, file storage (where do uploaded files live?), and binary deps (CGO) are all out-of-scope rabbit holes for 0.9.0. Provider APIs absorb this complexity. Server-side ingestion is addressed in the RAG worker decision below.

**Key files:**
- `internal/api/handlers/handlers.go` — `SendSessionMessage` ContentParts forwarding, A2A FilePart parsing
- `internal/router/router.go` — ContentPart → provider content array translation
- `pkg/models/models.go` — `SupportsVision()` capability on driver interface

---

### 4. RAG — Named Multi-Pipeline + Named Endpoints

**Decision:** Add `name` (slug, unique per kitchen) to `RAGPipeline`. Namespace all RAG endpoints under the pipeline name.

**New endpoints (backward compatible — old endpoints remain):**
- `POST /api/v1/rag/{pipeline}/ingest` — ingest into named pipeline
- `POST /api/v1/rag/{pipeline}/query` — query named pipeline
- `GET  /api/v1/rag/pipelines` — list all configured pipelines

Each pipeline has its own strategy (`naive`, `sentence-window`, `parent-document`, `HyDE`, `agentic`), vector store backend, embedding model, and chunking config. Agent ingredients reference pipelines by name:

```yaml
ingredients:
  - kind: retriever
    name: my-product-docs   # references a named RAG pipeline
```

**Server-side file upload and ingest worker** (chunking, PDF/DOCX parsing, embedding queue) is a **Pro feature** — separate binary `cmd/ingest-worker`, separate Dockerfile, HPA-eligible. OSS continues to accept JSON text ingest only.

**Why separate process for the worker?** Embedding 1000 chunks blocks goroutines for 30-120s. PDF parsing spikes CPU/memory. Isolating these from the API server prevents P99 latency degradation and lets them scale independently.

**Key files (OSS scope):**
- `pkg/models/models.go` — `RAGPipeline.Name` field
- `internal/api/handlers/rag_handlers.go` — named pipeline endpoints
- `internal/rag/pipeline.go` — pipeline registry by name
- `internal/api/router.go` — new route registration

---

### 5. Audit Trail + Service Account Correctness

**Decision:** Fix four high-severity issues before any 0.9.0 feature work ships.

**`AuditEvent` changes:**
```go
ActorType string `json:"actor_type" db:"actor_type"` // "user"|"service_account"|"api_key"|"scoped_key"
ActorName string `json:"actor_name" db:"actor_name"` // human-readable: SA name, key prefix, user email
```
All three emit helpers (`emitGuardrailAuditEnv`, `emitEnvA2AAudit`, `emitThinkingAudit`) updated to populate `ActorType`, `ActorName`, `IP` (`r.RemoteAddr` stripped + `X-Forwarded-For` with `AGENTOVEN_TRUSTED_PROXIES`), and `UserAgent`.

`InvokeAgent` gains audit event emission: `action: "agent.invoke"`, `resource: agentName`, `details: {trace_id}`.

**SA revocation:**
`ServiceAccountProvider` injected with store reference. After HMAC validates, calls `store.GetServiceAccountByTokenPrefix()` — returns 401 if `Revoked == true`.

**`ServiceAccount` additions:**
```go
OwnerUserID string `json:"owner_user_id" db:"owner_user_id"`
OwnerEmail  string `json:"owner_email"   db:"owner_email"`
Type        string `json:"type"          db:"type"` // "system" | "user"
```
User-scoped SAs: role cannot exceed owner's role at creation time. All SA audit events set `UserEmail` to owner's email.

**RBAC middleware:**
New `RequireRole(roles ...string)` in `internal/api/middleware/rbac.go`. Applied to router in two modes controlled by `AGENTOVEN_RBAC_MODE`:
- `audit` (default for 0.9.0): logs violation, does not block — zero breaking change
- `enforce`: returns 403 on role mismatch

Role hierarchy: `viewer < baker < chef < admin`. Bearer JWT role claim or SA token role from store record.

Table-driven `TestRoleEnforcement` added covering all protected routes × all roles.

**Key files:**
- `pkg/models/models.go` — `AuditEvent`, `ServiceAccount` changes
- `internal/api/handlers/handlers.go` — emit helpers, `InvokeAgent` audit, `CreateServiceAccount`
- `internal/auth/service_account.go` — store injection, revocation check
- `internal/api/middleware/rbac.go` — new file
- `internal/api/router.go` — middleware application

---

## Phasing

| Phase | Deliverable | OSS / Pro |
|---|---|---|
| **0.9.0-alpha** | Audit `ActorType`/`ActorName`/IP/UA, `InvokeAgent` audit event, SA revocation store-check, SA `OwnerUserID`/`Type` | Both |
| **0.9.0-beta1** | `StreamChunk`, `ExecuteStream`, `StreamInvokeAgent` Go path, A2A `tasks/sendSubscribe`, recipe SSE endpoint | OSS |
| **0.9.0-beta2** | Shared memory + durable sessions | Pro only |
| **0.9.0-beta3** | Skills plugin system + 4 built-in containers + Kitchen Skills tab | OSS |
| **0.9.0-rc** | `ContentParts` wiring, LLM router vision translation, A2A `FilePart` fix, named RAG pipelines | OSS |
| **0.9.0-rc2** | Ingest worker binary + upload endpoint + job queue + HPA chart | Pro |
| **0.9.0** | RBAC middleware (`audit` mode default) + role tests + dashboard (skills tab, multi-pipeline RAG, streaming UI) | Both |

---

## Consequences

- **Easier:** Third parties build skills in any language without touching the control plane. Agents stream partial output to clients. Multiple RAG pipelines with different strategies per kitchen. Audit trail is complete and queryable by actor type.
- **Harder:** Skills introduce a registration/config surface that admins must manage. `/.well-known/skill` needs a versioning story (`min_agentoven_version`) before a public marketplace opens.
- **Breaking:** RBAC middleware ships in `audit` mode — zero breaking change for `baker` API key users today. `enforce` mode in 0.9.1 after one release cycle.
- **Risk:** If A2A spec evolves `tasks/sendSubscribe` before 0.9.0 ships, minor adjustment needed. Mitigated — subscribe is in the current A2A spec draft.

## Alternatives Considered

1. **Skills as compiled Go plugins (`.so`)** — rejected. Requires CGO, same Go version, recompile per skill, no language choice for skill authors.
2. **Embed PDF/DOCX parser in control plane** — rejected for 0.9.0. Adds CGO or large pure-Go deps, increases binary size, mixes ingestion with API serving.
3. **Single RAG pipeline only** — rejected. Different document collections need different strategies; forcing one pipeline per kitchen is a hard adoption blocker.
4. **Hard-enforce RBAC immediately** — rejected. Default `baker` role currently has write access in most deployments; hard enforcement is a silent breaking change.
5. **Auto-seed built-in skills on boot** — rejected. Creates surprise registrations in air-gapped deployments and couples control plane startup to network availability of skill containers.
