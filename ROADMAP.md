# AgentOven Roadmap

> Last updated: 03 July 2026
>
> This document tracks the complete feature roadmap for AgentOven — the open-source,
> framework-agnostic enterprise agent control plane built on A2A and MCP protocols.
>
> Architecture rules:
> - **OSS** = lean, stable, in-memory store only, 4 community providers (OpenAI, Azure OpenAI, Anthropic, Ollama)
> - **Pro** = PostgreSQL, cloud provider drivers (Bedrock, Foundry, Vertex), RBAC, SSO, audit, federation

---

## Current Delivery Projection (July 2026)

This section reflects what is actively delivered and what is next, so execution is clear across OSS and Pro tracks.

### Completed in current cycle

| Stream | Delivered | Evidence |
|--------|-----------|----------|
| Runtime reliability | Framework-native A2A TLS propagation fixed (provider CA bundle + tls_skip_verify now forwarded and honored in runner) | `control-plane/internal/api/handlers/handlers.go`, `control-plane/internal/process/templates/agent_runner.py` |
| Pro data compatibility | `agents.behavior` migration added to self-heal older Postgres deployments | `agentoven-pro/internal/store/postgres.go` |
| Release consistency | 0.8.6 image tags aligned across Pro server, operator, scheduler, dashboard, and runtime values | `agentoven-pro/charts/*/values.yaml` |
| Image publishing | Multi-arch images pushed to GHCR and client ACR for 0.8.6 and latest | Release/publish runbook execution |

### In progress now

| Stream | Current focus | Target outcome |
|--------|---------------|----------------|
| Security audit readiness | Auth-free cyber audit workflow (Trivy/Syft/Grype/Gitleaks) and report packaging | Repeatable release evidence package per version |
| AKS rollout verification | Validate runtime pull path and A2A execution with new image set | No pull backoff and green recipe runs |

### Next up (near-term)

| Priority | Item | Success criteria |
|----------|------|------------------|
| P1 | CI security gate for image and IaC scanning | Pipeline fails on unapproved high/critical findings |
| P1 | Complete Pro release checklist automation | One command to build/push/scan/verify all Pro artifacts |
| P2 | R8 scale and federation closeout | Helm and cross-org federation milestones moved to done |

---

## ✅ Release One — Published Packages (Complete)

| Deliverable | Status |
|-------------|--------|
| `a2a-ao` crate on crates.io (v0.2.3) | ✅ |
| `agentoven-core` crate on crates.io (v0.2.3) | ✅ |
| `agentoven-cli` crate on crates.io (v0.2.3) | ✅ |
| `agentoven` Python SDK on PyPI (v0.1.0) | ✅ |
| Landing page deployed (Azure SWA) | ✅ |
| Docs site deployed (Azure SWA) | ✅ |

---

## ✅ Release Two — Control Plane Core (Complete)

| Deliverable | Status |
|-------------|--------|
| PostgreSQL store (pgx/v5) — full CRUD for 9 entity types + migrations | ✅ |
| In-memory store fallback — concurrency-safe, JSON snapshot persistence | ✅ |
| Model Router — OpenAI, Azure OpenAI, Anthropic, Ollama; 4 strategies | ✅ |
| MCP Gateway — JSON-RPC 2.0, HTTP/SSE transports, tool CRUD, per-kitchen isolation | ✅ |
| Workflow Engine — DAG execution, human gates, retries, fan-out/fan-in | ✅ |
| Agent Executor — agentic loop (prompt→model→tool→loop) for managed agents | ✅ |
| Ingredient Resolver — validates/resolves agent ingredients at bake time | ✅ |
| Notification Service — event dispatch to MCP tools with "notify" capability | ✅ |
| Store interface extracted (9 sub-interfaces in `internal/store/`) | ✅ |
| Service interfaces extracted (`pkg/contracts/`) | ✅ |
| Plan/PlanLimits model on Kitchen for tier gating | ✅ |
| 20+ API handlers (store-based, all v1 + MCP + A2A routes) | ✅ |
| Dashboard UI (React 19 + Vite 7 + Tailwind CSS) — 8 pages | ✅ |
| Prompt Validator (OSS — basic structure/size checks) | ✅ |
| Prompt Validator (Pro — injection detection, model checks, deny-lists, LLM-judge) | ✅ |
| Enterprise repo (`agentoven-pro`) initialized with tier/auth/provider scaffolding | ✅ |

---

## ✅ Release Three — Stability & Enterprise Foundation (Complete)

The focus is **hardening the OSS core** and **wiring the enterprise plumbing** so Pro can ship real value.

### OSS — Hardening (Items 1–5)

| # | Item | Description | Status |
|---|------|-------------|--------|
| 1 | **In-memory store: Close() flush** | `Close()` drains save channel, stops background goroutines, forces final snapshot write. | ✅ Done |
| 2 | **In-memory store: Trace TTL eviction** | Background goroutine evicts traces older than configurable TTL (default 7 days, `AGENTOVEN_TRACE_TTL` env). | ✅ Done |
| 3 | **In-memory store: Pagination** | `ListAgents`, `ListRecipes`, etc. return all items. Accept `ListFilter` (limit/offset) in list operations for API pagination. | ⬜ Deferred to R4 |
| 4 | **ProviderDriver registry** | `map[string]ProviderDriver` registry with `RegisterDriver()` on ModelRouter. 4 built-in drivers (openai, azure-openai, anthropic, ollama). | ✅ Done |
| 5 | **Agent versioning** | Version tracking in memory store (append-only history). `ListAgentVersions`, `GetAgentVersion` wired in handlers + store interface. | ✅ Done |

### OSS — Protocol Fixes (Items 6–7)

| # | Item | Description | Status |
|---|------|-------------|--------|
| 6 | **Fix A2A Gateway** | `tasks/send` now resolves ingredients and invokes agent executor asynchronously. Records execution traces. | ✅ Done |
| 7 | **Expose ModelRouter in Server** | `Server.Router` field exposes `*ModelRouter`. Added `NewWithStore()`, `NewWithStoreAndConfig()` constructors for Pro. | ✅ Done |

### Pro — Enterprise Wiring (Items 8–12)

| # | Item | Description | Status |
|---|------|-------------|--------|
| 8 | **Wire Pro PG store** | `server.NewWithStore(ctx, pgStore)` constructor. Pro conditionally uses PG when `DATABASE_URL` is set. | ✅ Done |
| 9 | **Wire ProviderDriver registration** | `srv.Router.RegisterDriver()` calls for Bedrock, Foundry, Vertex in Pro main.go. | ✅ Done |
| 10 | **Cloud provider auth — Bedrock** | AWS SigV4 signing implemented (stdlib-only, `awsauth/sigv4.go`). Credential chain: config → env vars. | ✅ Done |
| 11 | **Cloud provider auth — Foundry** | Azure AD token acquisition via managed identity (IMDS) or client credentials flow (`azureauth/token.go`). Token caching with auto-refresh. | ✅ Done |
| 12 | **Cloud provider auth — Vertex** | Google ADC via metadata server or service account JSON key (`gcpauth/token.go`). JWT-based token exchange, caching. | ✅ Done |

### Pro — Tier & License (Items 13–14)

| # | Item | Description | Status |
|---|------|-------------|--------|
| 13 | **TierEnforcer wired** | Already wired in Pro main.go — enforces per-entity quotas (agents, providers, tools, prompts, recipes). Active when license is valid. | ✅ Done |
| 14 | **License validation enhanced** | Supports both HMAC (dev) and RSA public key (production) via `AGENTOVEN_LICENSE_PUBLIC_KEY`. Offline verification, expiry checks. | ✅ Done |

### Pro — Audit & Auth (Items 15–16)

| # | Item | Description | Status |
|---|------|-------------|--------|
| 15 | **Audit logging wired** | `AuditLogger.Middleware()` captures POST/PUT/PATCH/DELETE operations with status codes. Wired in Pro main.go. | ✅ Done |
| 16 | **API key auth (OSS)** | Opt-in API key middleware (`AGENTOVEN_API_KEYS` env). Supports Bearer token, X-API-Key header, query param. Public paths exempted. Runtime add/remove. | ✅ Done |

### CI/CD & Infrastructure (Items 17–19)

| # | Item | Description | Status |
|---|------|-------------|--------|
| 17 | **GitHub Actions CI** | Comprehensive CI workflow exists (Rust, Go, integration, Docker, SDK). | ✅ Already existed |
| 18 | **Docker image** | Multi-stage Dockerfile updated (Go 1.24, Alpine 3.21, dashboard build stage). | ✅ Done |
| 19 | **Unit tests** | 25 tests across 3 packages: store (13), router (6), middleware (6). All passing. | ✅ Done |

---

## ✅ Release Four — Enterprise Compliance & Integrations (Complete)

The focus of Release Four is **regulated-industry readiness** (MFG, Healthcare, Banking) and **ecosystem integrations**.

### Wave 1 — Foundation (Audit, Durable Gates, RBAC Expansion)

| # | Item | Description | Repo | Status |
|---|------|-------------|------|--------|
| 20 | **Audit Store** | Full audit event CRUD — `CreateAuditEvent`, `ListAuditEvents`, `CountAuditEvents`, `DeleteAuditEvent` in store interface. Memory + PG implementations. | Both | ✅ |
| 21 | **Durable Human Gates** | Store-backed approval polling with SLA timeout (`MaxGateWaitMinutes`). `ApprovalRecord` with metadata, approver info, regulation tags. | Both | ✅ |
| 22 | **Expanded RBAC** | 6 roles (admin, chef, baker, auditor, finance, viewer) × 34 permissions. Granular per-entity access control. | Pro | ✅ |
| 23 | **Trace Aggregation** | `TraceAggregation` model with daily cost, token usage summaries. Cost analytics queries across kitchen. | Both | ✅ |
| 24 | **Notification Channels** | `NotificationChannel` CRUD in store (6 kinds: webhook, slack, teams, discord, email, zapier). Channel subscription to event types. | Both | ✅ |
| 25 | **Extended PlanLimits** | `RequireThinkingAudit`, `MaxGateWaitMinutes`, `MaxNotificationChannels`, `MaxOutputRetentionDays`, `MaxAuditRetentionDays`. | OSS | ✅ |
| 26 | **API Endpoints** | 11 new handlers: approvals (list/approve/reject), channels (CRUD), audit (list/count), stream (SSE). | OSS | ✅ |

### Wave 2 — Streaming & Thinking Blocks

| # | Item | Description | Repo | Status |
|---|------|-------------|------|--------|
| 27 | **StreamingProviderDriver** | `StreamingProviderDriver` interface with `RouteStream()` returning `StreamChunk` channel. | OSS | ✅ |
| 28 | **Thinking Block Capture** | `ThinkingBlock` model (Content, TokenCount, Model, Provider, Timestamp). Captured from OpenAI reasoning + Anthropic extended thinking. | OSS | ✅ |
| 29 | **SSE Streaming Endpoint** | `GET /api/v1/stream` with `text/event-stream` response. Token-by-token streaming from model router. | OSS | ✅ |
| 30 | **RequireThinkingAudit** | `KitchenSettings.RequireThinkingAudit` forces thinking/reasoning on all LLM calls. Responses without thinking blocks flagged. | OSS | ✅ |

### Wave 3 — Integrations (Notification Channels, LangChain, LangFuse)

| # | Item | Description | Repo | Status |
|---|------|-------------|------|--------|
| 31 | **ChannelDriver Interface** | `contracts.ChannelDriver` (public) with `Kind()` + `Send()`. Pluggable driver registry in notification service. | OSS | ✅ |
| 32 | **WebhookChannelDriver** | Built-in webhook driver with HMAC-SHA256 signing, custom headers (X-AgentOven-Event, Signature, Kitchen). 3 retries. | OSS | ✅ |
| 33 | **Concurrent DispatchAll** | `DispatchAll()` sends to both MCP tools AND registered channel drivers concurrently (goroutines + WaitGroup). | OSS | ✅ |
| 34 | **Slack Driver** | Slack Incoming Webhooks with Block Kit formatting (header, section fields, context footer). Optional channel override. | Pro | ✅ |
| 35 | **Teams Driver** | Microsoft Teams Adaptive Cards with TextBlock + FactSet layout. | Pro | ✅ |
| 36 | **Discord Driver** | Discord webhook embeds with color-coded event types and structured fields. | Pro | ✅ |
| 37 | **Email Driver** | SMTP-based notifications. Config: smtp_host/port/username/password, from_address, to_addresses. Plain text body. | Pro | ✅ |
| 38 | **Zapier Driver** | Full event JSON POST to Zapier webhook. Enables automation with 5000+ app integrations. | Pro | ✅ |
| 39 | **LangChain Adapter** | `GET /langchain/tools` + `POST /langchain/invoke` — exposes ready agents as LangChain-compatible tool schemas. Proxies to A2A endpoints. | OSS | ✅ |
| 40 | **LangFuse Bridge** | Bidirectional trace exchange. `POST /langfuse/export` (push to LangFuse), `POST /langfuse/ingest` (import LangFuse-format traces). | OSS | ✅ |

### Wave 4 — Compliance Dashboard & Retention

| # | Item | Description | Repo | Status |
|---|------|-------------|------|--------|
| 41 | **Streamlit Compliance Dashboard** | 6-page dashboard: Audit Trail, Thinking Audit, Cost Analytics, Agent Outputs, Approvals, System Health. Plotly charts, CSV export. | Pro | ✅ |
| 42 | **Retention Janitor** | Background goroutine purges expired traces + audit events. Per-kitchen overrides via `KitchenSettings.MaxOutputRetentionDays` / `MaxAuditRetentionDays`. Runs every 6 hours. | OSS | ✅ |
| 43 | **DeleteTrace / DeleteAuditEvent** | Store interface methods for data purging. Implemented in memory (OSS) and PostgreSQL (Pro). | Both | ✅ |
| 44 | **Server.Shutdown()** | Graceful shutdown method that cancels retention janitor + flushes telemetry. | OSS | ✅ |
| 44a | **ArchiveDriver Interface** | `contracts.ArchiveDriver` — pluggable archive backend with `Kind()`, `ArchiveTraces()`, `ArchiveAuditEvents()`, `HealthCheck()`. | OSS | ✅ |
| 44b | **Archive Models** | `ArchiveMode` (none/archive-and-purge/archive-only/purge-only), `ArchivePolicy` (per-kitchen config), `ArchiveRecord` (compliance audit trail). | OSS | ✅ |
| 44c | **Archive-before-Purge Janitor** | Janitor rewritten with fail-safe archive-before-purge: policy resolution, batched writes (5000/batch), driver registry. | OSS | ✅ |
| 44d | **LocalFileArchiver** | OSS default archive driver — JSONL + optional gzip to `~/.agentoven/archive/{kitchen}/{kind}/`. | OSS | ✅ |
| 44e | **Cloud Archive Drivers** | S3Archiver (SigV4 + KMS), AzureBlobArchiver (SharedKey + blob tiers), GCSArchiver (OAuth2 + CMEK). Year/month/day path partitioning. | Pro | ✅ |
| 44f | **Archive Wiring** | LocalFileArchiver auto-registered in OSS server. S3/Azure/GCS registered via env vars in Pro. `AGENTOVEN_ARCHIVE_BACKEND` sets default. | Both | ✅ |

---

## ✅ Release Five — RAG & Intelligence (Complete)

The focus is making AgentOven a **first-class RAG platform** — embedding drivers, vector stores, retrieval pipelines, and evaluation — all open-source.

### Wave 1 — Embedding Drivers & Vector Stores

| # | Item | Description | Repo | Status |
|---|------|-------------|------|--------|
| 45 | **EmbeddingDriver interface** | `contracts.EmbeddingDriver` — `Kind()`, `Embed()`, `Dimensions()`, `MaxBatchSize()`, `HealthCheck()`. Pluggable driver registry. | OSS | ✅ |
| 46 | **OpenAI Embeddings** | `text-embedding-3-small` (1536d), `text-embedding-3-large` (3072d), `text-embedding-ada-002` (1536d). Configurable endpoint + batch size. | OSS | ✅ |
| 47 | **Ollama Embeddings** | `nomic-embed-text` (768d), `mxbai-embed-large` (1024d), `all-minilm` (384d). Local-first, zero-cost embeddings. | OSS | ✅ |
| 48 | **VectorStoreDriver interface** | `contracts.VectorStoreDriver` — `Kind()`, `Upsert()`, `Search()`, `Delete()`, `Count()`, `HealthCheck()`. Registry pattern. | OSS | ✅ |
| 49 | **Embedded Vector Store** | In-memory brute-force cosine similarity. 50K max vectors (configurable), 90% capacity warning. Zero external deps. | OSS | ✅ |
| 50 | **pgvector Store** | PostgreSQL + pgvector extension driver. Auto-migration (`ao_vectors` table), cosine distance `<=>` operator. BYO PG via `AGENTOVEN_PGVECTOR_URL`. | OSS | ✅ |
| 51 | **VectorDocStore** | Store interface for vector document CRUD — `Upsert`, `Search`, `Delete`, `Count`, `ListNamespaces`. Memory + PG implementations. | OSS | ✅ |

### Wave 2 — RAG Pipeline & Retrieval Strategies

| # | Item | Description | Repo | Status |
|---|------|-------------|------|--------|
| 52 | **Recursive Text Chunker** | Configurable `ChunkSize` / `ChunkOverlap` / `Separator` with passthrough mode. Recursive splitting with separator hierarchy. | OSS | ✅ |
| 53 | **Document Ingester** | Chunk → embed (batched) → upsert pipeline. Accepts `RawDocument` array, returns `RAGIngestResult` (docs processed, chunks, vectors). | OSS | ✅ |
| 54 | **Naive RAG** | Direct embed → cosine search → return top-K. Baseline retrieval strategy. | OSS | ✅ |
| 55 | **Sentence Window RAG** | Expand context around matched chunks for richer context. | OSS | ✅ |
| 56 | **Parent Document RAG** | Return full parent document when child chunks match. | OSS | ✅ |
| 57 | **HyDE RAG** | Hypothetical Document Embedding — LLM generates hypothetical answer → embed → search. Requires ModelRouterService. | OSS | ✅ |
| 58 | **Agentic RAG** | Query decomposition → sub-queries → parallel search → merge → re-rank. Multi-step reasoning. Requires ModelRouterService. | OSS | ✅ |

### Wave 3 — RAGAS Evaluation & MCP Integration

| # | Item | Description | Repo | Status |
|---|------|-------------|------|--------|
| 59 | **RAGAS Sidecar** | FastAPI Python server wrapping RAGAS library. 7 metrics (faithfulness, answer relevancy, context precision/recall, etc.). Port 8400. | OSS | ✅ |
| 60 | **RAGAS Dockerfile** | Python 3.11-slim, ragas + fastapi + uvicorn + datasets. Ready for ACR push. | OSS | ✅ |
| 61 | **RAGAS MCP Tool** | Go client for RAGAS sidecar + MCP tool definition + auto-registration via `TryRegisterMCPTool()`. | OSS | ✅ |
| 62 | **RAG API Endpoints** | `POST /rag/query`, `POST /rag/ingest`, `GET /embeddings`, `POST /embeddings/{driver}/embed`, health checks. 9 new handlers. | OSS | ✅ |
| 63 | **Server Wiring** | Auto-register embedding drivers (OpenAI/Ollama via env vars), vector stores (embedded always + pgvector via env), RAG pipeline in `buildServer()`. | OSS | ✅ |

### Wave 4 — Models & Dashboard

| # | Item | Description | Repo | Status |
|---|------|-------------|------|--------|
| 64 | **Ingredient Model Extensions** | 3 new IngredientKind constants (`embedding`, `vectorstore`, `retriever`), `ResolvedEmbedding`, `ResolvedVectorStore`, `ResolvedRetriever`. | OSS | ✅ |
| 65 | **RAG Strategy Model** | `RAGStrategy` (5 constants), `RAGQueryRequest/Result`, `RAGIngestRequest/Result`, `VectorDoc`, `SearchResult`. | OSS | ✅ |
| 66 | **DataConnector Model** | `DataConnectorKind` (5 types: snowflake, databricks, s3, postgresql, http), `DataConnectorConfig`, store interfaces. | OSS | ✅ |
| 67 | **PlanLimits Extensions** | `MaxEmbeddingProviders`, `MaxVectorStores`, `DataConnectors`, `RAGTemplates`, `AgentMonitor` fields. | OSS | ✅ |
| 68 | **Dashboard — Embeddings Page** | List registered drivers, health status indicators, driver info cards. | OSS | ✅ |
| 69 | **Dashboard — Vector Stores Page** | List backends (embedded/pgvector), health checks, built-in vs external badges. | OSS | ✅ |
| 70 | **Dashboard — RAG Pipelines Page** | Query tab (strategy selector, top-K, namespace), Ingest tab (paste text, namespace). Live results. | OSS | ✅ |
| 71 | **Dashboard — Connectors Page** | Available connector types (OSS/Pro badges), active connectors table. | OSS | ✅ |

---

## ✅ Release 5.5 — Pro Intelligence (Complete)

Enterprise RAG add-ons — standalone monitoring, data lake connectors, and managed evaluations.

| # | Item | Description | Repo | Status |
|---|------|-------------|------|--------|
| 72 | **Agent Monitor** | Standalone A2A agent that polls traces, extracts Q&A pairs, runs RAGAS evaluation, posts audit events. Configurable poll interval. | Pro | ✅ |
| 73 | **ConnectorRegistry** | Thread-safe data connector registry with `Register()`, `Get()`, `List()`, `HealthCheckAll()`. | Pro | ✅ |
| 74 | **Snowflake Connector** | SnowflakeDriver — account/warehouse/database/schema/role configuration. `DataConnectorDriver` implementation (stub, pending gosnowflake). | Pro | ✅ |
| 75 | **Databricks Connector** | DatabricksDriver — workspace URL, catalog/schema, token auth. `DataConnectorDriver` implementation (stub, pending REST API). | Pro | ✅ |
| 76 | **S3/ADLS/GCS Connector** | S3Driver — supports S3, ADLS (Azure Blob), and GCS via endpoint + bucket configuration. `DataConnectorDriver` implementation (stub, pending AWS SDK). | Pro | ✅ |

---

## 📋 Release Six — Developer Experience

| # | Item | Description | Repo |
|---|------|-------------|------|
| 77 | **Prompt Studio** | Versioned prompt management UI — create, edit, test prompts with variable substitution and side-by-side comparison. | OSS |
| 78 | **Agent playground** | Interactive agent testing UI — send messages, view traces, inspect tool calls in real time. | OSS |
| 79 | **Agent health dashboard** | Real-time agent status, invocation counts, error rates, latency percentiles. | OSS |
| 80 | **Publish @agentoven/sdk** | Publish the TypeScript SDK to npm. Currently built via napi-rs but not published. | OSS |
| 81 | **Connect custom domains** | Wire agentoven.dev and docs.agentoven.dev to Azure SWA deployments. | Infra |
| 82 | **In-memory store pagination** | `ListAgents`, `ListRecipes`, etc. accept `ListFilter` (limit/offset) for API pagination. | OSS |
| 99 | **Multimodal harness (text, image, PDF, voice, video)** | Typed `ContentPart` media (`MediaRef`), blob store with provider file-ID reuse, per-provider mapping with capability gating (OpenAI, Anthropic, Gemini, Ollama), in-process PDF text fallback, A2A file parts, RAG PDF ingest, and cascaded STT→agent→TTS voice on `/invoke`. In code: `pkg/blobstore`, `pkg/audio`, `pkg/documents`, `internal/router/media.go`. Media can also be uploaded once (`POST /api/v1/blobs`, multipart or raw; `GET`/`DELETE /api/v1/blobs/{ref}`) and named by `blob_ref` in `/invoke`, `/invoke/stream`, session messages and A2A file parts; a blob belongs to the kitchen that uploaded it and another kitchen gets 404. Size is capped by `AGENTOVEN_BLOB_MAX_BYTES` (default 25 MB) and a blob nobody reads or writes for `AGENTOVEN_BLOB_TTL_DAYS` (default 7) is swept (`blobstore.StartJanitorFromEnv`, reusable by Pro). Inline bytes in a message are moved to the blob store, so sessions, the run journal and traces hold a `blob_ref`, never the bytes. PDFs ride the OpenAI and Anthropic Files APIs and Gemini media over `AGENTOVEN_GEMINI_FILES_MIN_BYTES` (default 4 MB) rides the Gemini Files API (uploaded once, URI reused for 46 hours), each falling back to inline bytes on failure. A refused modality is a 422 naming it, not a silent drop; LiteLLM, OpenRouter and Ollama pass images for models the catalog does not know unless `config.modalities.image.enabled` is false. An A2A `tasks/send` with `configuration.voice_output` returns the spoken reply as an audio FilePart artifact. Verified end to end (2026-11-02) against provider test doubles for OpenAI, Anthropic and Gemini wire formats, including a serverless run with a read-only home directory and only `/tmp` writable; **not yet verified against live provider APIs** (real vision, real Whisper/TTS, real Files API uploads, Gemini video processing). Not built: Gemini native `audio` is passed to the model but never transcribed, and a file produced by a tool cannot yet be returned as an artifact. | OSS |
| 100 | **Realtime voice SDKs and providers** | Python (`client.realtime(...)`, `pip install "agentoven[realtime]"`) and TypeScript (`connectRealtime`, `ProClient.realtime`) clients for the Pro realtime WebSocket. Providers: OpenAI, Gemini Live, and LiteLLM (via its OpenAI-compatible `/v1/realtime`); OpenRouter has no realtime API and is refused with guidance, but serves cascaded voice. Realtime and speech are optional capabilities of the provider drivers (`realtime.Driver`, `audio.EngineProvider`), and an agent has exactly the modalities of its own provider, set in the provider's `config.modalities` (image, pdf, video, audio, realtime, web; each with `enabled` and, for audio/realtime, model names). `web` is the one modality that is off until switched on: Gemini's built-in Google Search grounding for chat and Live (billed per grounded query; Gemini cannot combine it with function tools, so an agent with tools skips it). Voice never borrows another provider's speech. Browser clients use a short-lived token (Pro: `POST /api/v1/realtime/{agent}/token`, then the `agentoven-token.<token>` WebSocket subprotocol); calls are saved as traces and spoken input is guardrailed. The OSS relay, both drivers and both SDK clients are covered by tests against a fake upstream (`pkg/realtime`, Pro `internal/realtime`, SDK suites); a live OpenAI Realtime or Gemini Live session, and the TypeScript end-to-end test (`realtime.e2e.test.ts`, needs credentials), have **not** been run. | OSS |
| 101 | **Skills CLI** | `agentoven skills list/get/install/refresh/remove/approve/reject` over the OSS skills API, plus `stage`/`analyze` for Pro's governed flow (a 404 on the Pro routes prints a clear "requires AgentOven Pro" message). `install` takes a git URL (cloned by the server, `--ref`), a local directory (read by the CLI and sent inline; symlinks and `.git` skipped, server size limits enforced locally) or `--server-path` for a directory on the control plane; it prints the verification verdict and reasoning and exits 2 for `needs_review`, 3 for `rejected`. `analyze` discloses which provider receives the bundle and takes an optional `--provider` pin. `@owner/slug` references are refused with an explanation: the server has no skills registry to resolve them against. | OSS |
| 102 | **Hosted MCP servers as skills** | The MCP gateway is a real MCP client (official Go SDK, streamable HTTP, one session per call). A skill whose `mcp_tools` entry sets `transport: mcp` is asked what the server offers and every tool is registered for its agents, named `<skill>_<tool>` (letters, digits, `_` and `-` only, which every model provider accepts); the API key stays a kitchen credential sent as a Bearer token. Example: `examples/skills/firecrawl/SKILL.md` (Firecrawl). Also fixes `auth_type`/`auth_header`/`credential_ref` being dropped from a SKILL.md and `allowed-tools` written as a string being rejected: all 589 skills in Anthropic's and OpenAI's public repos now parse. | OSS |
| 103 | **Plugin import** | `POST /api/v1/skills/import` takes a Claude Code or Codex plugin from a git location the caller names (https only): its skills and its remote MCP servers go through the same review as `register`, the parts an agent cannot use (commands, agents, hooks, apps, local stdio servers) are reported as skipped, and `dry_run` previews without a model call. Both vendors' plugin manifests are read (`pkg/skills.ParsePlugin`), a plugin's choices are planned by `PlanImport`, git locations must be `https://`, paths and symlinks cannot leave the checkout, and `allowed-tools` may be a list or a string. All 589 skills in Anthropic's and OpenAI's public repositories parse. CLI: `agentoven skills import <git-url>`; dashboard: Skills → Import plugin. Browsing catalogs of plugins, with approved sources and an allowlist, is AgentOven Pro. | OSS |
| 104 | **Tool authentication by reference** | A tool stores the name of a credential, not its value (`pkg/toolauth`); the gateway reads it on each call, so a rotation reaches the next call with nothing re-registered, and a tool's authentication is never returned by the API (list, get, register, update and bulk responses are redacted). A tool registered by a skill is served only while that skill is accepted, checked on every `tools/call` and `tools/list`. The A2A gateway requires an identity to run an agent, as `/invoke` does; only the agent cards are public. Encrypting credentials at rest, rotation and approval authorities are AgentOven Pro. | OSS |
| 105 | **Knowledge ingredient seam** | OSS hooks so Pro's knowledge engine (`agentoven-knowledge`: hybrid retrieval, ACLs, memory, eval) can be an agent ingredient: `knowledge` kind, `ResolvedKnowledge`, resolver and executor hooks, caller principal reaching the executor (ADR-0028). Planned; tracked in `agentoven-knowledge/ROADMAP.md` (R18). | OSS |
| 106 | **Guardrails and A2A on every runtime** | The platform boundary enforces guardrails, never the agent's process. A framework-native agent's streamed replies (`/invoke/stream`, A2A `message/stream`) are held back and released only once the output rules pass (an agent with no output rule still streams as it arrives); its session messages reach its process with the session as `thread_id` instead of falling through to the raw model; the per-agent A2A gateway checks the message text going in and the reply coming back, not the JSON-RPC envelope; a resumed run's answer is checked. `framework_native_test.go` asserts all of it for LangChain, LangGraph and Deep Agents. 2026-11-02: the delegation boundary too (`executor.DelegationBoundary`, installed by `Handlers.InstallDelegationBoundary`): an agent that delegates to another reaches it through the target's input guardrails (workspace rules included, fail closed) and gets its answer through the target's output guardrails, and a framework-native target is reached through its process instead of being answered by the Go loop from the raw model (`delegation_boundary_test.go`). Delegation to an external A2A agent is still refused. | OSS |
| 107 | **Per-run budgets** | `RunBudget` (cost, tokens, active time, turns) on an agent, tightened per request and per subagent, never loosened except by a person at a pause. The executor checks it before each model call (projected cost, `max_tokens` clamped to what is left), warns at 50% and 80%, and at the cap checkpoints and pauses for a decision: 402 with the dimension and numbers, resumed with an `extend` (de-duplicated by ID, one resume wins). A subagent draws from a share reserved on its parent. A dollar cap on a model with no known price is refused (422); a token cap is always exact. Spend that used to go uncounted (the paused turn, a delegated child, the summarizer) is counted. Audit events: `run.budget_paused`, `run.budget_extended`. | OSS |
| 108 | **Journal v2** | Delta records instead of the whole history per turn (a run's file grows with the run, not its square), the model's reply journaled before its tools run and each tool result as it lands, a clean checkpoint on a crash mid-turn (a non-idempotent call that may have run is reported as "outcome unknown", never run twice), the run's input recorded so a crash before turn 1 resumes, torn last lines tolerated, a sidecar index, and a janitor (`AGENTOVEN_JOURNAL_RETENTION_DAYS`). Schema 1 files still replay; schema 2 files cannot be resumed by an older binary. 2026-11-02: the storage is a seam (`RecordLog`, `LogJournal`: the same replay over any append-only store) so Pro can keep the journal in PostgreSQL for several replicas; the file journal stays the default and is unchanged. Tested with an in-memory log shared by two journals (no duplicated history, one winner per resume). | OSS |
| 109 | **Background tasks** | `POST /api/v1/tasks` runs an agent without the request that started it: 202, then `GET /tasks/{id}`, an SSE event stream that resumes from `Last-Event-ID`, `cancel`, and `resume` (budget extension, tool approval or crash recovery). Per-kitchen and global concurrency with a bounded queue (429 + `Retry-After`); every task gets default caps on spend, tokens, time and turns. Output guardrails apply to the stream and the result; scoped keys and kitchens are enforced; framework-native agents are refused (their spend cannot be capped). A2A `tasks/send` with `configuration.blocking=false` returns a task; `tasks/get` and `tasks/cancel` read it. Recovery is manual by default, `AGENTOVEN_TASK_RECOVERY=auto` resumes with backoff, at most 3 times. Single-node by default. 2026-11-02: `TaskLeases` (`SetLeases`, an in-memory `MemoryLeases`) make replicas that share a journal agree on who runs each task: a lease before it runs, renewed while it does (a replica that cannot renew stops the task before another can take it), a task on another replica shows as `running` with its `owner` and cannot be resumed or recovered elsewhere, a cancel reaches the owner, and `StartAdoption` finds the tasks a dead replica left. `Handlers.SetJournal`, `SetTaskLeases` and `SetTaskPauseHook` are the seams; the PostgreSQL implementations are Pro (item 114). Resuming a pause at a budget outside the task (the kitchen's) no longer demands an extension. CLI `agentoven task start|list|get|events|cancel|resume`, Python `TaskMethods` and TypeScript `Tasks` over the routes (a decision on a paused task can be signed). A task's event stream and metadata stay in the memory of the replica that runs it. | OSS |
| 110 | **Long-run context hygiene** | For background tasks: tool results cut to 32KB (head and tail), history compaction at 80% of the context budget that never separates a tool call from its results and is charged to the run, a pinned `agentoven_notes` memory (goal, plan, progress, decisions) that survives compaction and restarts, a pause when the same call repeats (warned at half the limit; a person can send the run on with guidance or end it), and bounded retries of transient model errors. | OSS |
| 111 | **Dynamic subagents (seam)** | `contracts.SubagentRuntime`: an installed runtime offers the `task` and `tasks` tools, and the executor runs what it approves, bound by the parent's budget, guardrails and tool limits (a subset of the parent's tools, never one needing approval; one level deep; clean context). The community edition installs none and keeps single-hop `agentoven_delegate`. | OSS |
| 112 | **Deep Agents runtime** | `deepagents` runtime beside LangChain and LangGraph: SDK adapter (`DeepAgentsAdapter`, `build_deep_agent`), dashboard and CLI support, the K8s entrypoint honoured (ADR-0017), example and guide. Also fixes the SDK hosting contract for every framework (`/health`, `/invoke/stream`, `/a2a`, the pod token, `AGENT_PORT`, `AGENT_READY`), the session forwarded as `thread_id` on invoke and stream, and a double `Wait` in the local process executor. Tools and subagents inside a hosted process run in the user's process and are not governed. | OSS |
| 113 | **Spend gate hook** | `ModelRouter.SetSpendGate`: a gate consulted before every model call and told about every one that completes. A refusal (`SpendRefusedError`) pauses the run like its own budget (scope `kitchen`). The per-kitchen ledger that implements it is Pro (item 114; item 83 is the same work from the product side). 2026-11-02: `SpendReserver` lets a gate reserve each call's worst case and have the router settle or cancel exactly that reservation (so parallel callers cannot oversubscribe), `SpendQuery.EstimatedInputTokens` sizes it, and the router's streaming path is gated too. A gate with only Allow/Record still works. | OSS |
| 114 | **Dynamic subagent runtime** | The Pro runtime behind item 111: per-call, per-run and per-kitchen fan-out limits, optional approval of every spawn, `subagent.spawned`/`subagent.completed` audit events tying child to parent. Raising a run's budget needs the `budget:extend` permission. Scenario runs take a per-episode budget and a whole-run cost cap. 2026-11-02: also the kitchen budget behind the spend gate (item 113; daily, weekly, monthly or lifetime, per kitchen or provider, reserved per call in PostgreSQL so parallel callers and replicas cannot oversubscribe, 402-style refusal, warnings at 80%/100% as audit events, read with `cost:read`, set with `budget:extend`), the PostgreSQL journal and task leases (opt in with `AGENTOVEN_JOURNAL_BACKEND=postgres`), signed webhooks with backoff and audit on task pauses and budget events, and, in the executor, a delegation boundary (a delegation is held to the target's input and output guardrails, and a framework-native target is reached through its process). Not built: alert sinks other than one global webhook, per-kitchen webhook configuration, the `throttle` action, delegation to external A2A agents, dashboard surfaces. | Pro |
| 115 | **Metered model gateway** | ADR-0011: OpenAI-compatible `POST /v1/chat/completions` over the model router (streaming, tools, `provider/model` names). A hosted agent process is handed a credential that opens only the gateway, never the provider's key; each of its model calls is counted against the run the control plane opened for it and refused with a 402 `budget_exceeded` at the cap. A stopped hosted call is not resumable (a process cannot be checkpointed from outside); background tasks stay on AgentOven's own loop. | Pro |
| 116 | **Agentic bot runtime (seam)** | ADR-0029: runtime `agenticbot`, `Agent.Bot` (`BotConfig`), `contracts.BotControl` (`Prepare`/`Invoked`/`Stopped`; the community edition refuses), `SandboxProfile` applied by the docker and k8s executors (read-only rootfs, caps dropped, non-root, limits, runtime class, egress NetworkPolicy), no general control-plane token and no provider key in a bot, run registered before every invoke, 30m invoke timeout. A2A and delegation register bot runs, `ToolRunner` seam for kitchen tools; the Pro-operator k8s path and CLI exposure are pending | OSS |
| 117 | **Agentic bots (computer-use agents)** | The Pro manager behind item 116 with the sandbox agent in the `ao-bots` repo: per-action gate, hash-chained run journal, approvals, injection screening, egress proxy, kill switch, dashboard. See agentoven-pro ADR-0037 | Pro |

---

## 📋 Release Seven — Enterprise Differentiation

| # | Item | Description | Repo |
|---|------|-------------|------|
| 83 | **Cost budget enforcement** | Per-kitchen monthly spend limits. Router rejects requests when budget exhausted. Dashboard shows burn rate and projections. | Pro |
| 84 | **Prompt approval workflow** | Multi-step approval for prompt changes — submit → review → approve → deploy. Role-based (Baker submits, Chef approves). | Pro |
| 85 | **SSO/SAML full implementation** | Complete SAML 2.0 flow — SP metadata, assertion parsing, session management, role mapping from IdP attributes. | Pro |
| 86 | **RBAC enforcement** | Role-based access control on all API endpoints. Roles: Admin, Chef (approve), Baker (create), Viewer (read-only). | Pro |
| 87 | **Enhanced observability** | 400-day trace retention, advanced analytics (cost breakdown by agent/model/kitchen), exportable reports. | Pro |
| 88 | **Evaluation framework** | LLM-as-judge evaluation pipelines — define criteria, run evals against agent outputs, track scores over time. | Pro |

---

## 📋 Release Eight — Scale & Federation

| # | Item | Description | Repo |
|---|------|-------------|------|
| 89 | **Helm chart** | Kubernetes deployment chart — control plane, PostgreSQL, Redis (for distributed locks), Ingress, HPA. | Pro |
| 90 | **Cross-org agent federation** | Agents from different organizations can discover and invoke each other via A2A, with trust boundaries and access policies. | Pro |
| 91 | **Distributed workflow engine** | Recipe execution across multiple nodes with job queue (Redis/NATS) for horizontal scaling. | Pro |
| 92 | **Multi-region deployment** | Control plane replication across Azure regions with Cosmos DB as the geo-distributed store. | Pro |
| 93 | **Lean Go agent-runner binary** | Standalone Go binary (`cmd/agent-runner`) embedding the executor/router/MCP-gateway stack, serving the same `/invoke`, `/invoke/stream`, `/a2a`, `/health` contract `agent_runner.py` does today — so `proxyToProcess`/`proxyToProcessStream`/A2A proxying need no changes. Replaces the Python runner as the image `internal/process/docker.go`/`k8s.go` deploy (`agentoven/agent-runner:latest` is referenced today but has no Dockerfile in this repo yet), so a single-agent pod no longer loads or depends on the full control plane. Planned after the core agent-harness work (durability, streaming, HITL, subagents, reactive memory, per-tool-call guardrails) lands. | OSS |
| 94 | **Native Lambda/Azure Functions handlers** | Deploy the same agent-runner binary as a real AWS Lambda function (via `aws-lambda-go`) and an Azure Function (via Azure's Go custom-handler/worker SDK) — native per-cloud entrypoints, not the zero-dependency sidecar/web-adapter approach, by explicit choice. Depends on item 93 existing first. | OSS |

---

## 🔮 Long-term Vision

| # | Item | Description | Repo |
|---|------|-------------|------|
| 95 | **Agent marketplace** | Marketplace for agent templates, recipes, and tool integrations. Curated by community, vetted by AgentOven team. | OSS + Pro |
| 96 | **Compliance certifications** | SOC2, HIPAA, FedRAMP, GxP compliance for Pro/Enterprise tier. | Pro |
| 97 | **Custom routing strategies** | Plugin system for user-defined routing strategies (e.g., A/B test, shadow traffic, canary). | Pro |
| 98 | **SageMaker driver** | AWS SageMaker endpoints as a model provider for enterprise. | Pro |
| 125 | **Native tool transport and tool-level approval (seams for tool packs)** | ADR-0031: transport `native` run by an installed `contracts.NativeToolRunner` (the community edition has none), `contracts.ToolApprovalPolicy` asked by the executor on every call beside `Agent.ApprovalTools` (so a tool is held for approval for every agent, read per call), the gateway refusing a gated tool unless the call passed the executor's gate (`contracts.WithToolApproved`, JSON-RPC `-32004`), and skill approval keeping the tools a skill registered itself. `mcpgw/native_test.go`, `executor/tool_policy_test.go`. The packs, importer and executors are Pro item 126 | OSS |
| 126 | **Tool packs and the OpenAPI importer** | Installable governed tools for enterprise systems: read-only PostgreSQL, email and Slack, any REST API from an OpenAPI 3 document; write tools wait for approval, credentials are `vault://` references, SSRF-safe egress, skill approval holds a pack. Curated vendor packs on a manifest format: ServiceNow and Salesforce (agentoven-pro ADR-0061, [format](docs/curated-tool-pack-format.md), [ServiceNow](docs/tool-pack-servicenow.md), [Salesforce](docs/tool-pack-salesforce.md); tested against fakes only). See agentoven-pro ADR-0048 and [docs/tool-packs.md](docs/tool-packs.md) | Pro |
| 118 | **Guardrail actions and validated detectors** | ADR-0032: a rule has an action (`block` by default, so nothing existing changes; `redact` to `[IBAN]`-style placeholders, `mask`, `warn`, `require_approval` on a tool call through the existing approval pause). Detectors with check digits (Luhn cards, IBAN mod 97, Aadhaar Verhoeff, PAN, UK NINO and NHS number, SSN rules, DE/FR/IT/BE/PL VAT, ES DNI, IPs, phone, AWS/GitHub/Slack/JWT/PEM/API-key shapes) with an allowlist; RE2 regex rules with a label, bounded in size and count; `custom` rules through `contracts.CustomGuardrailEvaluator` (Pro: signed webhook). New hops: retrieved chunks (stage `retrieval`, `all`) before they enter the prompt, redaction of tool arguments and results, delegation and A2A bodies. Redacted text is what the trace, the session and the audit event keep; surfaces that cannot rewrite (held-back streams, bots, voice) refuse instead. `POST /api/v1/guardrails/test` shows what a rule would do to a text. `guardrail_actions_test.go`, `guardrail_hops_test.go`, `engine_test.go`, `detectors_test.go`. | OSS |
| 127 | **Route guard seam and kitchen-scoped ids** | ADR-0033: `Handlers.RouteGuard` (an edition with roles maps every route to a permission; Pro's route table, 459 rules, deny by default), `Handlers.ProviderRepoint` (a provider's credential is pinned to its host; `403 provider_endpoint_pinned`), provider clients refuse cross-host redirects, recipe runs, gates, approvals and sessions answer 404 for another kitchen's id, LangFuse/LangChain/PicoClaw use the authenticated kitchen, A2A bodies are bounded and guardrails judge data parts and files. Tested by a walker that fails for an unclassified by-id route. Amended 2026-11-03: `/mcp*` is no longer a public path (it follows the auth flag; Pro demands an identity through `routeguard.Rule.Identified`), `/models/route*` takes the kitchen from the caller and checks `agent_ref`, `/blobs` and `/documents` follow the flag again. | OSS |
| 128 | **Approval observer and expirer seams** | ADR-0034: `contracts.ApprovalObserver` (told when a recipe gate or a paused run starts waiting) and `ApprovalExpirer` (`Handlers.ExpireApproval`: a gate is recorded `timed_out` and takes its `on_timeout` path, a paused run is claimed and ended `cancelled`; never an approval), `/hooks/chat/` skips authentication for signature-checked chat callbacks, notification retries resend their body. Delivery, reminders, escalation and chat replies are Pro; the community edition installs no observer | OSS |
| 129 | **Traffic and cache seams** | ADR-0035: `models.RateLimitedError` (429 with `Retry-After`), `SpendRefusedError` gains unit, period and reset time, `contracts.ResponseCache` (consulted before a call is reserved or sent; a hit costs nothing and shows as `cache_hit` on traces), `contracts.TrafficLimiter` and `Handlers.LimitTraffic` on invoke, stream, session messages and task creation, a gate refusal no longer opens a provider's circuit and stops the fallback when it is about more than one provider, a budgeted run waits out a rate limit. The limits, budgets per agent/key/provider, cache and dashboard are Pro ([ADR-0051](../agentoven-pro/ADR/0051-kitchen-budgets-limits-cache.md)); SDK `budgets`, `limits` and `cache` calls and CLI commands, [docs](docs/budgets-limits-cache.md). Amended 2026-11-03: one chain for every model call (`router.ContentGuard`, `Admit`, `RouteVia`, `ContextReplyJudge`), refusals are 402/429/403/503 on every route, the invocation limiter covers A2A, test, resume and LangChain. | OSS |
| 130 | **Review hardening: drafts, fencing, public trace views** | ADR-0036: an edit to a baked agent waits in `agent.draft` until the next bake or re-cook; the fingerprint covers every field but a named state list; named versions are validated (400/409); `Store.UpdateAgentLifecycle` is the bake tail's only write, one step at a time per agent; `PublicTrace`/`PublicSpan`/`PublicScore`/`PublicSession`/task view for readers without content access (`respondJSON` refuses the full types; route-walking tests `internal/api/content_routes_test.go`); lease epoch + `Fence` checked before every tool dispatch and in a shared journal's append; indexed journal sweep; per-(kitchen, provider, endpoint) circuit that ignores request errors; streamed calls charged when they error. Pro: ADR-0043 amendment. 2026-11-03: agent writes carry `revision` and a stale write is refused (`ErrStale`, 409 to a client that names a stale `revision` or `If-Match`), so a PUT racing a bake loses nothing; `POST /agents/{a}/sessions` answers 201. Not built: public form of recipe runs and approvals | OSS |
| 131 | **Inbound triggers: files on a task, a refused approval is a 403** | ADR-0030: `POST /api/v1/tasks` takes `content_parts` (media by `blob_ref` only: no inline bytes, no URL), so an agent started from an e-mail or a file sees its attachments; `BakeRecipe` answers `permission_denied: ...` with 403 (it was 500: the prefix check compared 16 characters with a 17-character word). Triggers themselves (signed webhook, IMAP mailbox, folder and S3 prefix, delivery log) are Pro ([ADR-0047](../agentoven-pro/ADR/0047-inbound-triggers.md)); SDK `create_trigger`, `fire_trigger`, `list_trigger_deliveries` and `sign_webhook` (Python, TypeScript), CLI `agentoven trigger`, [docs](docs/inbound-triggers.md) | OSS |
| 132 | **Review hardening: tool egress, store errors, SDK route contract** | ADR-0037: `pkg/netguard` refuses tool endpoints on loopback, private, link-local and metadata addresses at registration and at every call (operator and per-kitchen allowlists, metadata never); tools honour `enabled`, keep the secret when the mask is written back and are sealed in the vault in Pro; creates answer 409 for a duplicate and never echo driver text; retired agents leave the list and cannot be PUT; `sdk/fixtures/server-routes.json` generated from the real route tree and a call-site contract test in the Python, TypeScript and Rust SDKs (fixed: Rust `retire_agent`, removed `delete_world_schema`); SDK timeouts, no redirects with the key, JSON-once error bodies, escaped path segments, 0600 config, no secrets in `Debug`. Amended 2026-11-03 (review 4): `pkg/netguard` also refuses zone identifiers, number spellings and embedded-IPv4 ranges and is the one guard for tools, packs and triggers; a kitchen's secret references are `vault://` only (`contracts.KitchenSecretName`, `Resolver.ResolveFor`); native (pack) tools are not editable through `/tools`. Pro: [ADR-0052](../agentoven-pro/ADR/0052-identity-and-surface-hardening-2026-11-03.md) | OSS |
| 133 | **Review hardening: after-authentication hook, kitchen-scoped waits** | ADR-0038: `pkgmw.WithAfterAuth` runs once after authentication with the verified identity (Pro canonicalises the kitchen and writes decision intents there); a wait is released only from its own kitchen (`Engine.ReleaseWait(kitchen, ...)`); a signed run decision never proceeds when the run's state cannot be read; `DecisionRoutes` lives in `pkg/contracts`; `DigestOf` keeps large integers apart. | Done |
| 134 | **Document intelligence (gap G3)** | ADR-0039: `pkg/documents` reads DOCX, XLSX (sheet, range and row numbers), HTML, CSV/TSV, Markdown, RTF and PDF pages in-process with archive, XML, sheet and decompression limits and hidden-text removal; attached files in those formats become text at the router before every provider call; `POST /api/v1/documents/extract` (no model), `/ocr` (page by page through the router, pinned to the named provider, confidence `unverified` unless a second, different model agrees) and `/fields` (JSON Schema fields with page and quote, each quote verified against the text); SDKs (Python, TypeScript, Rust) and `agentoven documents`. Not done: PDF tables, non-JPEG scans, process isolation of the PDF library. Pro: [ADR-0054](../agentoven-pro/ADR/0054-document-intelligence.md) | OSS |
| 135 | **Round 2 residuals: tool auth shapes, channel secrets, signed notes, kitchen-required stores** | ADR-0040: `auth_config` is bearer, api-key or basic and anything else is a 400 (basic works end to end); the vault migration moves a basic password and the secret fields of a refused shape into credentials; a channel's signing secret is write-only (`has_secret`, Pro `secret_ref`) and deliveries are signed from the credential or not sent; the note of a tool, budget or skill decision is in the signed digest; the memory store's lists and the audit filter refuse an empty kitchen; `ModelRouterService.HealthCheck` takes a kitchen. [docs](docs/tool-and-channel-secrets.md), [scheduled runs](docs/scheduled-runs.md). Pro: [ADR-0053](../agentoven-pro/ADR/0053-round-2-residuals-2026-11-03.md) | OSS |
| 136 | **Configuration names are checked against the kitchen** | ADR-0041: `pkg/refcheck` reads an agent or recipe against the kitchen's providers (and their models), prompts, tools, skills, agents, recipes, channels, credentials, environments, and Pro's scenarios and roles; create, update (the draft), bake, re-cook and recipe runs answer 422 with every problem, the nearest valid values and the field path; `GET /agents/{name}/validate` and `/recipes/{name}/validate` list the problems of agents that already run; unknown fields and wrong types are named; new `summary_provider`; dashboards offer only what exists (one `ReferenceSelect`, one hook per resource); Python, TypeScript and Rust have `validate`, `ConfigurationError` and `agentoven agent validate`. Pro: [ADR-0055](../agentoven-pro/ADR/0055-config-reference-validation.md) | Done |
| 137 | **See what the next bake would change** | `GET /api/v1/agents/{name}/draft/diff` (`pkg/models/agent_draft_diff.go`, `DiffAgentDraft`): field-level difference between a baked agent and `Agent.WithDraft()`, lists by element (guardrails by id, ingredients by `kind:name`, name lists by name), secrets masked with `RedactConfig`, `has_draft`, `baked_version`, `changes_behaviour`; `Agent.EnsureModelIngredient` is shared by the bake, the re-cook and the diff, and fixes a re-cook that kept the old model after an edit of `model_name` alone; Python `agent_draft_diff`, TypeScript `agentDraftDiff`, Rust `agent_draft_diff`. Pro: dashboard "Unbaked changes" panel, ADR-0043 amendment. Not built: CLI subcommand, an OSS dashboard view | Done |
| 138 | **Owner decisions of 2026-11-03: community seams** | ADR-0042: approval inputs are content (`PublicApproval`), `null` clears an optional agent field (`AgentEdit.Cleared`, `sdk/fixtures/agent-edit-cases.json`), a retired agent refuses lifecycle writes (409), `BakeDeployments` and `SetOwnEndpointPolicy` hooks, `netguard.Channels()` with `AGENTOVEN_CHANNEL_EGRESS_ALLOW`, gate names `trigger_change` and `dataset_case_erase`. Not built: dashboard control to clear a field | OSS |
| 139 | **Runs that survive a restart** | ADR-0043: steps are checkpointed on the stored run, a process leases the runs it executes and another takes over a run whose lease ran out, a gate re-attaches to its approval record, gate SLA and wait timers are kept by the store (`RunResumeStore`, memory store implements it), a decision is written only while the record is pending (`ApprovalResolver`), a step that may have acted is not run again unless `on_restart: rerun`. `AGENTOVEN_RUN_RESUME=false` switches it off. Not built: per-write fencing, idempotent re-run of agent steps, resumable map steps | OSS |
| 140 | **Tool calls of a hosted process go through the control plane** | ADR-0044: `agent_runner.py` no longer calls a tool's endpoint; it posts the call to `POST /api/v1/internal/tool-calls` with a run token (signed, bound to kitchen, agent and run, short-lived, closed when the relayed call ends) and the control plane runs it through the Go executor's path (allowlist, guardrails, approval, schema, gateway, egress, result guardrails, audit). A tool needing approval is held as a paused run and decided like any other. Pack (`native`) tools work from recipe steps. Not built: restart-safe hold, shared scope registry across replicas, `agentoven_delegate`, framework-native processes | OSS |

---

## �️ Evolution Plan — Data Lifecycle & Platform Maturity

This section describes how AgentOven evolves **holistically** across releases. Each column is a capability
area; rows show the progression from today through long-term maturity.

### Data Lifecycle: Hot → Warm → Cold → Purge

```
            ┌──────────┐   TTL expires    ┌───────────┐   retention window   ┌───────────┐   archive policy   ┌─────────┐
  Ingest ──▶│   HOT    │ ──────────────▶  │   WARM    │ ────────────────▶    │   COLD    │ ──────────────▶    │ PURGE / │
            │ (PG/Mem) │                  │ (PG, r/o) │                     │ (S3/Blob) │                    │  HOLD   │
            └──────────┘                  └───────────┘                     └───────────┘                    └─────────┘
              queries                       read-only                         restore on                       delete or
              real-time                     dashboards                        demand                            legal hold
```

| Phase | Where we are | What's next |
|-------|-------------|-------------|
| **Hot** (R2–R4 ✅) | Traces + audit events live in PG/memory. Real-time queries, dashboards, SSE streaming. | Add time-series indices for faster window queries (R5). |
| **Warm** (R4 ✅) | Retention janitor marks data as "expired" after configurable TTL. Data still queryable until purge cycle. | Read-only partitions in PG, separate tablespace for warm data (R6). |
| **Cold** (R4 ✅) | Archive drivers write JSONL/gzip to local disk (OSS) or S3/Blob/GCS (Pro). `ArchiveRecord` tracks every batch. | Add Glacier/Cool tier support, Parquet format option, cold-storage search index (R6). |
| **Purge** (R4 ✅) | Janitor deletes from hot store after successful archive (fail-safe). Configurable per-kitchen. | Legal hold override (R6), GDPR right-to-erasure API (R6), purge receipts (R7). |

### Platform Capability Matrix

| Capability | R1–R3 (Done) | R4 (Done) | R5 (Done) | R5.5 (Done) | R6 (Planned) | R7 (Planned) | R8+ (Vision) |
|-----------|-------------|----------|----------|------------|-------------|-------------|-------------|
| **Data Store** | PG + memory | + audit store, aggregation | + VectorDocStore, DataConnectorStore | — | + pagination, time-series indices | + warm/cold partitions | + geo-distributed (Cosmos DB) |
| **Retention** | 7-day TTL eviction | + janitor, archive-before-purge | — | — | + retention policy UI | + legal holds, GDPR erasure | + cross-region retention sync |
| **Archive** | — | Local (OSS), S3/Blob/GCS (Pro) | — | — | + Parquet format, restore API | + Glacier/Cool tiers, cold search | + federated archive (cross-org) |
| **RAG** | — | — | Embedding drivers, vector stores, 5 retrieval strategies, chunker, ingest pipeline | + Agent Monitor, data lake connectors | + Prompt Studio, RAG templates | + managed RAG pipelines | + cross-org knowledge federation |
| **Evaluation** | — | — | RAGAS sidecar (MCP tool) | + Agent Monitor (auto-eval) | — | + LLM-judge framework | + eval marketplace |
| **Compliance** | — | Audit store, thinking audit | — | — | + compliance reports export | + SOC2/HIPAA audit trails | + FedRAMP, GxP certification |
| **Observability** | OTEL traces | + SSE streaming, cost analytics | + embedding/vector health checks | + Agent Monitor A2A agent | + agent health dashboard | + 400-day retention, analytics | + cross-org observability mesh |
| **Auth & Access** | API keys | + RBAC (6 roles, 34 perms) | — | — | + OAuth2 flow | + SSO/SAML, fine-grained RBAC | + cross-org federation auth |
| **Integrations** | A2A + MCP | + LangChain, LangFuse, Slack/Teams/Discord/Email/Zapier | + RAGAS, OpenAI/Ollama embeddings | + Snowflake, Databricks, S3/ADLS/GCS | + Prompt Studio, playground | + eval framework, marketplace | + agent-to-agent marketplace |
| **Routing** | 4 strategies | + streaming, thinking blocks | + RAG-aware query routing (HyDE, Agentic) | — | — | + cost budgets, A/B routing | + custom strategy plugins |
| **Deployment** | Docker + SWA | + retention cleanup | + RAGAS Dockerfile (ACR-ready) | — | + Helm chart | + multi-node | + multi-region, BYOC |

### Key Evolution Themes

**R5 — RAG & Intelligence (Done) ✅**
> Make AgentOven a first-class RAG platform. Embedding drivers (OpenAI, Ollama), vector stores
> (embedded + pgvector), 5 retrieval strategies (naive → agentic), RAGAS evaluation as MCP sidecar,
> and full dashboard integration with 4 new pages.

**R5.5 — Pro Intelligence (Done) ✅**
> Enterprise RAG add-ons. Standalone Agent Monitor (A2A agent for automated quality evaluation),
> data lake connectors (Snowflake, Databricks, S3/ADLS/GCS). Docker images on ACR.

**R6 — Developer Experience**
> Make it delightful to build. Prompt Studio, agent playground, health dashboards,
> TypeScript SDK, custom domains. Pagination and performance polish.

**R6 — Developer Experience**
> Make it delightful to build. Prompt Studio, agent playground, health dashboards,
> TypeScript SDK, custom domains. Pagination and performance polish.

**R7 — Enterprise Differentiation**
> Make it sellable to enterprises. Cost budgets, prompt approval workflows, full SSO/SAML,
> evaluation framework, 400-day retention, cold-storage search, legal holds, GDPR compliance.
> This is where the open-core model generates revenue.

**R8 — Scale & Federation**
> Make it planet-scale. Helm chart, distributed workflow engine, cross-org agent federation,
> multi-region deployment with Cosmos DB. Enterprise SLA guarantees.

### Archive Backend Tier Guide

| Backend | Tier | Best for | Encryption | Compression | Restore SLA |
|---------|------|----------|-----------|-------------|-------------|
| `local` | OSS | Dev/testing, single-node | ✗ | gzip optional | Instant (file read) |
| `s3` | Pro | AWS workloads | KMS (SSE-KMS) | gzip | Minutes (S3 Standard) |
| `s3` + Glacier | Pro (R6) | Long-term compliance | KMS | gzip | Hours (Glacier Flexible) |
| `azure-blob` | Pro | Azure workloads | Key Vault / SSE | gzip | Minutes (Hot/Cool) |
| `azure-blob` + Archive | Pro (R6) | Long-term compliance | Key Vault | gzip | Hours (Archive tier) |
| `gcs` | Pro | GCP workloads | CMEK | gzip | Minutes (Standard) |
| `gcs` + Coldline | Pro (R6) | Long-term compliance | CMEK | gzip | Seconds (Coldline) |

### Data Sovereignty Roadmap (R7+)

For regulated industries, data must stay within geographic boundaries:

| Requirement | Solution | Release |
|------------|---------|---------|
| **Regional archive buckets** | Archive policy supports per-kitchen region config (e.g., `eu-west-1` for EU data) | R6 |
| **Geo-fenced hot store** | Multi-region PG with region-aware routing (kitchen → region mapping) | R7 |
| **Cross-region replication block** | Kitchen-level flag prevents data replication outside designated region | R7 |
| **Data residency audit** | Archive records include region metadata; compliance dashboard shows data location | R6 |

---

## �🐾 PicoClaw Integration Strategy

[PicoClaw](https://github.com/sipeed/picoclaw) (15.6k★) is an ultra-lightweight personal AI assistant
built in Go. It's complementary to AgentOven — PicoClaw is a single-user agent **runtime**,
while AgentOven is a multi-tenant agent **control plane**.

| # | Item | Description | Priority |
|---|------|-------------|----------|
| P1 | **A2A adapter for PicoClaw** | Wrap PicoClaw instances as A2A-compliant agents so they appear in the AgentOven registry. Publish as an open-source Go module. | High — capitalize on PicoClaw's viral growth window |
| P2 | **Chat app gateways (Pro)** | PicoClaw supports Telegram/Discord/DingTalk/LINE. Offer managed chat gateways as a Pro feature — connect any AgentOven agent to messaging platforms. | Medium |
| P3 | **"Personal Kitchen" free tier** | Simplified single-user mode inspired by PicoClaw's simplicity. One kitchen, one agent, no auth — perfect for personal use. | Medium |
| P4 | **Heartbeat & health protocol** | Adopt PicoClaw's heartbeat cron pattern for agent liveness checks in AgentOven's agent health dashboard. | Low |
| P5 | **Community outreach** | Publish integration guide, submit PR to PicoClaw repo linking to AgentOven, present at PicoClaw community calls. | Ongoing |

---

## Legend

| Symbol | Meaning |
|--------|---------|
| ✅ | Complete |
| 🔧 | In progress |
| 📋 | Planned |
| 🔮 | Long-term vision |
| 🐾 | PicoClaw strategy |
| S / M / L | Effort: Small (<1 day) / Medium (1-3 days) / Large (3+ days) |
| OSS | Open-source repo (`agentoven/agentoven`) |
| Pro | Enterprise repo (`agentoven/agentoven-pro`) |
| Infra | Infrastructure / deployment |
