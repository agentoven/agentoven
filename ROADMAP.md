# AgentOven Roadmap

> Last updated: 21 February 2026
>
> This document tracks the complete feature roadmap for AgentOven — the open-source,
> framework-agnostic enterprise agent control plane built on A2A and MCP protocols.
>
> Architecture rules:
> - **OSS** = lean, stable, in-memory store only, 4 community providers (OpenAI, Azure OpenAI, Anthropic, Ollama)
> - **Pro** = PostgreSQL, cloud provider drivers (Bedrock, Foundry, Vertex), RBAC, SSO, audit, federation

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

---

## 🔮 Long-term Vision

| # | Item | Description | Repo |
|---|------|-------------|------|
| 93 | **Agent marketplace** | Marketplace for agent templates, recipes, and tool integrations. Curated by community, vetted by AgentOven team. | OSS + Pro |
| 94 | **Compliance certifications** | SOC2, HIPAA, FedRAMP, GxP compliance for Pro/Enterprise tier. | Pro |
| 95 | **Custom routing strategies** | Plugin system for user-defined routing strategies (e.g., A/B test, shadow traffic, canary). | Pro |
| 96 | **SageMaker driver** | AWS SageMaker endpoints as a model provider for enterprise. | Pro |

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
