# Changelog

All notable changes to AgentOven will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
