# Changelog

All notable changes to AgentOven will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [0.4.0] — 2026-02-28

### 🤖 Agentic Behaviour & Sliding Context (Release Eight)

#### Agentic  Behavior
- **AgentBehavior enum** — `reactive` (single-turn) and `agentic` (multi-turn, tool use) modes
- **ReasoningStrategy enum** — `react` (Reason + Act), `plan-and-execute`, `reflexion`
- **Agent model fields** — `Behavior`, `ContextBudget`, `SummaryModel`, `ReasoningStrategy`
- **Dashboard UI** — purple-bordered Agent Behavior card in both create and edit forms with conditional agentic fields
- **Agent card badge** — amber "agentic" badge on agent list cards

#### Sliding Context Window
- **3-tier context management** — system prompt (never compressed) + summary buffer (compressed older messages) + recent window (most recent N messages)
- **Token budget estimation** — chars/4 heuristic with per-message overhead
- **Automatic summarization** — calls Model Router with SummaryModel to compress conversation history when context budget exceeded
- **Fallback truncation** — graceful degradation when no summary model available

#### Native Tool Calling
- **RouteRequest.Tools** populated with `[]ToolDefinition` for managed agents
- **ToolChoice: "auto"** — lets the model decide when to call tools
- **Prefer native ToolCalls** — `extractToolCalls()` prefers `RouteResponse.ToolCalls` over text parsing
- **FinishReason-aware** — checks `finish_reason == "tool_calls"` for reliable detection

#### Agent Delegation
- **`agentoven.delegate` virtual tool** — enables agents to invoke other agents in the same kitchen
- **Recursive execution** — managed target agents run through the same Executor
- **Kitchen-scoped discovery** — target agent lookup validates existence and ready status

#### Session Integration
- **SessionStore in Executor** — sessions loaded/created/persisted across agentic turns
- **Session-aware context** — sliding context builds from session message history
- **ExecutionTrace.SessionID** — traces linked to sessions for observability

---

## [0.3.2] — 2026-02-25

### 🔐 Pluggable Auth & Security Hardening (Release Seven)

#### Authentication Architecture (OSS)
- **AuthProvider interface** + **AuthProviderChain** in `pkg/contracts/auth.go`
- **Identity context helpers** (`SetIdentity`/`GetIdentity`) in `pkg/middleware/identity.go`
- **ProviderChain** — walks providers in priority order (internal/auth/chain.go)
- **APIKeyProvider** — wraps existing API key logic as pluggable AuthProvider
- **ServiceAccountProvider** — HMAC-SHA256 signed tokens via `X-Service-Token` header
- **AuthMiddleware** — replaces old `APIKeyAuth`, uses pluggable provider chain

#### Security Fixes
- **ISS-022** — CORS origins now env-configurable (`AGENTOVEN_CORS_ORIGINS`), no credentials with wildcard
- **ISS-023** — BakeAgent JSON decode error now checked + returns 400 for malformed JSON
- **ISS-021** — Tier enforcer uses exact path matching instead of `strings.Contains`

#### Bug Fixes
- **DeleteAgent handler** — returns 404 (not 500) when agent doesn't exist
- **Memory store DeleteAgent** — returns `ErrNotFound` for non-existent agents
- **Dashboard api.ts** — guards against `res.json()` on 204 No Content responses
- **Dashboard Agents.tsx** — catches all error types (not just `APIError`), adds toast notifications

#### Testing
- Added `handlers_test.go` — 10 unit tests for HTTP handler layer
- Added `sdk/python/tests/test_agent_crud.py` — 7 integration tests for Python SDK CRUD lifecycle

#### Enterprise (Pro)
- `getUserFromContext()` reads Identity from auth chain context
- SSO fail-open closed (ISS-019) — rejects when enabled but not configured
- RBAC context wired (per-route enforcement deferred to R8)

---

## [0.3.1] — 2026-02-24

### 🖥️ CLI Overhaul — Full Control Plane Parity

The CLI has been completely rewritten to cover all ~90 control plane API endpoints. Previously 19 subcommands with many placeholders, now **55+ subcommands** across 13 command groups — all wired to real API calls.

#### New Command Groups

- **`agentoven provider`** — 7 subcommands: list, add, get, update, remove, test, discover
- **`agentoven tool`** — 5 subcommands: list, add, get, update, remove (with `--schema-file` support)
- **`agentoven prompt`** — 7 subcommands: list, add, get, update, remove, validate, versions
- **`agentoven session`** — 6 subcommands: list, create, get, delete, send, chat (interactive REPL)
- **`agentoven kitchen`** — 4 subcommands: list, get, settings, update-settings
- **`agentoven rag`** — 2 subcommands: query (5 strategies), ingest (with progress bar)

#### Enhanced Commands

- **`agentoven agent`** — expanded from 8 to **15 subcommands**: added update, delete, recook, invoke, config, card, versions. Register now supports dual-mode (TOML config file **or** direct CLI flags with `--mode`, `--model-provider`, `--guardrail`, etc.)
- **`agentoven recipe`** — expanded from 4 to **7 subcommands**: added get, delete, approve. All handlers now call real API endpoints.
- **`agentoven trace`** — expanded from 3 to **4 subcommands**: added audit. All handlers now call real API endpoints with structured table output.

#### Core SDK Updates

- **`AgentMode`** enum — `Managed` (AgentOven executor) vs `External` (A2A proxy)
- **`Guardrail`** struct — kind, stage, config fields for the guardrails engine
- **12 new Agent fields** — model_provider, model_name, backup_provider, backup_model, system_prompt, max_turns, skills, guardrails, a2a_endpoint, mode, etc.
- **4 new IngredientKind variants** — Observability, Embedding, VectorStore, Retriever
- **`AgentOvenClient`** — expanded from ~19 to **59 HTTP client methods** covering all control plane endpoints

### 📦 Version Bumps

| Package | Previous | Now |
|---------|----------|-----|
| `a2a-ao` (crate) | 0.3.0 | 0.3.1 |
| `agentoven-core` (crate) | 0.3.0 | 0.3.1 |
| `agentoven-cli` (crate) | 0.3.0 | 0.3.1 |
| `agentoven` (PyPI) | 0.3.0 | 0.3.1 |
| `@agentoven/sdk` (npm) | 0.3.0 | 0.3.1 |

---

## [0.3.0] — 2026-02-22

### 🧠 Model Catalog & Intelligence

- **Live Model Catalog** — enriched capability database auto-populated from LiteLLM model metadata + provider discovery. Lists context windows, costs, supported features (tools, vision, streaming, JSON mode, thinking).
- **ModelDiscoveryDriver interface** — providers can implement `DiscoverModels()` to list available models at runtime. OpenAI and Ollama discovery drivers ship built-in.
- **LiteLLM Proxy Driver** — first-class `litellm` provider kind. Point AgentOven at any LiteLLM proxy and get unified routing across 100+ LLM providers.
- **Catalog API endpoints** — `GET /api/v1/catalog`, `GET /api/v1/catalog/{model_id}`, `POST /api/v1/catalog/refresh`, `POST /api/v1/catalog/discover/{provider}`, `GET /api/v1/catalog/discovery-drivers`.

### 🔄 Session Management

- **Multi-turn sessions** — create sessions per agent with conversation memory, token/cost tracking, and configurable max turns.
- **Session API endpoints** — `POST /agents/{name}/sessions`, `GET /agents/{name}/sessions`, `GET /agents/{name}/sessions/{id}`, `DELETE /agents/{name}/sessions/{id}`, `POST /agents/{name}/sessions/{id}/messages`.
- **SessionStore interface** — pluggable storage with in-memory implementation shipping by default.

### 🤖 Agent Enhancements

- **A2A Agent Card** — `GET /agents/{name}/card` returns an A2A-compatible agent card with capabilities, skills, and supported input/output content types.
- **Semver agent versioning** — agents now track semantic versions. Re-cook bumps the patch version and preserves history.
- **Re-cook lifecycle** — edit a ready agent → re-cook → auto-bump version → re-validate → re-bake.
- **Rewarm lifecycle** — cooled agents can be rewarmed back to ready status.
- **Integration panel (Dashboard)** — the "Invoke" button on agent cards is replaced with an "Integrate" button showing curl, CLI, and Python SDK commands for all 3 endpoints (test, invoke, sessions).

### 🔐 Pluggable Authentication (Release 7)

- **AuthProvider interface** — extensible authentication with `AuthProviderChain` that walks providers in priority order.
- **APIKeyProvider** — wraps existing API key auth as a pluggable provider.
- **ServiceAccountProvider** — HMAC-SHA256 signed tokens via `X-Service-Token` header for agent-to-agent and CI/CD auth.
- **AuthMiddleware** — replaces legacy `APIKeyAuth`, supports public path exemptions, `AGENTOVEN_REQUIRE_AUTH` env var.

### 🛡️ Security Fixes

- **ISS-022**: CORS origins now configurable via `AGENTOVEN_CORS_ORIGINS` (was hardcoded `*` with credentials).
- **ISS-023**: BakeAgent JSON decode errors now return 400 (was silently ignored).
- **ISS-021**: Tier enforcer uses exact path matching (was substring `strings.Contains`).

### 🖥️ Dashboard

- **Model Catalog page** — browse all discovered models with provider filter, capability badges, cost display.
- **Embeddings / VectorStore health fix** — health endpoints now return 200 with per-driver status (was 503 crashing dashboard).
- **Agent card improvements** — mode badge (managed/external), version display, re-cook and rewarm buttons.

### 🏗️ Infrastructure

- **Enriched RouteRequest** — supports `response_format`, `top_p`, `frequency_penalty`, `presence_penalty`, `stop`, `seed`, `user`, `tools`, `tool_choice`.
- **Enriched ChatMessage** — supports `tool_calls`, `tool_call_id`, `name` fields for full tool-calling flows.
- **RouteResponse** — includes `usage` (prompt/completion/total tokens, estimated cost), `model`, `provider`, `trace_id`, `finish_reason`.
- Dashboard built into Go binary — `agentoven-server` serves SPA from embedded `dashboard/dist/`.

### 📦 Version Bumps

| Package | Previous | Now |
|---------|----------|-----|
| `a2a-ao` (crate) | 0.2.3 | 0.3.0 |
| `agentoven-core` (crate) | 0.2.3 | 0.3.0 |
| `agentoven-cli` (crate) | 0.2.3 | 0.3.0 |
| `agentoven` (PyPI) | 0.1.0 | 0.3.0 |
| `@agentoven/sdk` (npm) | 0.1.0 | 0.3.0 |
| Dashboard | 0.2.3 | 0.3.0 |
| Control Plane (Go) | 0.2.2 | 0.3.0 |

---

## [0.2.3] — 2026-01-15

### Added
- PicoClaw IoT integration (A2A adapter, heartbeat monitor, chat gateway manager)
- Docker multi-platform images → GHCR
- npm publish pipeline (napi-rs 6-target matrix)
- Custom domains: agentoven.dev + docs.agentoven.dev

## [0.2.0] — 2025-12-01

### Added
- Initial release with A2A + MCP protocol support
- Rust CLI (13 commands), Go control plane, Python SDK, TypeScript SDK
- Model Router (OpenAI, Azure OpenAI, Anthropic, Ollama)
- MCP Gateway (JSON-RPC 2.0, SSE)
- Workflow Engine (DAG, human gates, retries)
- RAG pipeline (5 strategies), embeddings, vector stores
- Prompt management, notifications, retention engine
- Dashboard (12 pages)
