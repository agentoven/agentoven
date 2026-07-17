# ADR-0026: RAG/KAG Runtime as a Separate Process

**Status:** Proposed  
**Date:** 2026-07-10  
**Authors:** AgentOven Engineering  
**Scope:** OSS

---

## Context

The control plane today executes RAG queries inline: the handler calls `Pipeline.Query()`, which runs a `switch` over strategy types, calls the embedding driver, hits the vector store, calls the LLM for answer generation, and returns — all inside the API server process.

This is the wrong place for execution. The control plane's responsibility is auth, routing, policy, and orchestration. Running retrieval logic inside it creates three problems:

1. **It cannot be scaled independently.** A slow GraphRAG query ties up an API server goroutine. RAG workloads and API workloads have completely different resource profiles.

2. **New strategies require a rebuild and redeploy of the entire control plane.** Adding PageIndex, GraphRAG, or a custom enterprise retrieval module means modifying and redeploying the API server binary.

3. **There is no run-level observability for RAG.** Every agent invocation is a `Trace`. Every recipe execution is a `RecipeRun`. But RAG queries are fire-and-forget — there is no `RAGRun` record, no duration tracking, no per-strategy telemetry, no retry surface.

### The seam already exists

`ExternalRAGService` (`internal/rag/external.go`) is an HTTP proxy that forwards `Query` and `Ingest` calls to an external URL and maps the responses to `contracts.RAGService`. It was designed for LlamaIndex and Haystack. It is the exact seam needed to delegate all RAG execution out of the control plane. The `RAGRegistry` already holds it alongside the built-in `Pipeline`.

### Precedent in this codebase

This pattern is established. `scheduler` and `operator` are standalone binaries that the `server` binary delegates to. The control plane does not schedule or orchestrate Kubernetes deployments — it hands off to specialist processes. The RAG Runtime is the same pattern applied to retrieval.

---

## Decision

### The RAG Runtime is a standalone binary

A new binary — `agentoven-rag-runtime` (OSS) — is the exclusive owner of all retrieval execution. The control plane delegates to it via `ExternalRAGService`. No strategy code lives in the control plane after this ADR.

```
control-plane (server)               rag-runtime (new binary)
─────────────────────                ─────────────────────────
Auth, routing, policy     ──POST──▶  Strategy dispatch
Recipe orchestration                 Run tracking (RAGRun)
Trace reference storage              Module registry
                          ◀──JSON──  []SearchResult + run metadata
```

The control plane wires the runtime at startup via a single environment variable:

```
AGENTOVEN_RAG_RUNTIME_URL=http://localhost:9200
```

When set, the server registers the runtime URL as an `ExternalRAGService` named `"rag-runtime"` and sets it as the default in `RAGRegistry`. When not set, the built-in `Pipeline` is used as today (backward compatible; zero config change for existing deployments).

---

### The RAG Runtime API

The runtime exposes a minimal HTTP API:

```
GET  /health                    — liveness probe
GET  /info                      — version, loaded modules, supported strategies
POST /query                     — execute a RAG/KAG query (returns RAGQueryResult)
POST /ingest                    — ingest documents into a namespace
GET  /runs                      — list recent RAGRun records
GET  /runs/{id}                 — get a specific RAGRun
POST /modules/register          — register a new strategy module at runtime
DELETE /modules/{name}          — deregister a module
GET  /modules                   — list all loaded modules
```

`POST /query` and `POST /ingest` accept the same JSON shapes as the control plane's `/api/v1/rag/query` and `/api/v1/rag/ingest` — because the runtime replaces the `ExternalRAGService` target and `ExternalRAGService` already knows those shapes.

---

### Strategy modules inside the runtime

Strategy logic lives inside the runtime, not the control plane. The runtime has its own module registry using the same `init()` self-registration pattern established for embedding and vector store drivers:

```go
// Each built-in strategy is a sub-package of the runtime.
// init() registers it into the runtime's module registry.

// internal/modules/naive/init.go
func init() {
    runtime.RegisterModule(&NaiveModule{})
}
```

`agentoven-rag-runtime/cmd/main.go` blank-imports the built-in modules to activate them:

```go
import (
    _ "github.com/agentoven/agentoven/rag-runtime/internal/modules/naive"
    _ "github.com/agentoven/agentoven/rag-runtime/internal/modules/sentencewindow"
    _ "github.com/agentoven/agentoven/rag-runtime/internal/modules/parentdocument"
    _ "github.com/agentoven/agentoven/rag-runtime/internal/modules/hyde"
    _ "github.com/agentoven/agentoven/rag-runtime/internal/modules/agentic"
    _ "github.com/agentoven/agentoven/rag-runtime/internal/modules/pageindex"
)
```

#### `POST /modules/register` — runtime registration without restart

To add a new strategy to a **running** runtime instance:

```bash
curl -X POST http://localhost:9200/modules/register \
  -d '{
    "name": "graphrag",
    "endpoint": "http://localhost:9300",
    "display_name": "GraphRAG — community summary retrieval"
  }'
```

The runtime calls `GET {endpoint}/health` to verify the module is live, then adds it to its registry. From that moment, any query with `"strategy": "graphrag"` is dispatched to that module's `POST {endpoint}/execute`. **No restart of the runtime. No restart of the control plane.**

A strategy module is any HTTP process exposing:
```
GET  /health    → 200 {"status":"ok"}
GET  /metadata  → {"name":"graphrag", "display_name":"...", ...}
POST /execute   → accepts RAGQueryRequest, returns []SearchResult
```

Any language. Any framework.

---

### Every RAG/KAG query is a tracked Run

The runtime creates a `RAGRun` record for every query:

```go
type RAGRun struct {
    ID          string          `json:"id"`
    Kitchen     string          `json:"kitchen"`
    Strategy    RAGStrategy     `json:"strategy"`
    Module      string          `json:"module"`      // which module handled it
    Namespace   string          `json:"namespace"`
    Question    string          `json:"question"`
    Status      string          `json:"status"`      // running, completed, failed
    ChunksFound int             `json:"chunks_found"`
    TokensUsed  int64           `json:"tokens_used"`
    LatencyMs   int64           `json:"latency_ms"`
    Error       string          `json:"error,omitempty"`
    StartedAt   time.Time       `json:"started_at"`
    CompletedAt *time.Time      `json:"completed_at,omitempty"`
    TraceRef    string          `json:"trace_ref,omitempty"` // links back to control-plane trace
}
```

The control plane's `ExternalRAGService` receives the `RAGQueryResult` which includes `run_id`. The handler stores `run_id` on the `Trace` record as a reference — same way a `RecipeRun` ID is stored on a trace. The run detail lives in the runtime; the trace lives in the control plane.

In OSS, runs are stored in memory (evicted after 1000 entries, FIFO). The runtime's `GET /runs` endpoint surfaces them. Pro adds Postgres persistence.

---

### `RAGModule` interface (inside the runtime)

```go
// Module is the interface every strategy implementation satisfies.
// Both compiled-in (init-registered) and runtime-registered remote modules
// implement this — callers cannot tell the difference.
type Module interface {
    Name()        string
    Metadata()    ModuleMetadata
    Execute(ctx context.Context, req QueryRequest, deps ModuleDeps) ([]SearchResult, error)
    HealthCheck(ctx context.Context) error
}

// ModuleDeps carries shared infrastructure the runtime provides to each module.
type ModuleDeps struct {
    Embeddings  EmbeddingDriver   // nil for non-vector strategies (PageIndex, GraphRAG)
    VectorDB    VectorStoreDriver // nil for non-vector strategies
    LLMRouter   LLMRouterClient   // for HyDE, agentic, answer-generation
}
```

The runtime's LLM calls go back through the control plane's provider API (`AGENTOVEN_CONTROL_PLANE_URL` — same env var from ADR-0017), so modules never need direct model credentials.

---

## Consequences

**Positive:**
- Control plane has zero strategy code after this ADR. The switch statement is deleted. No rebuild of the server binary to add strategies.
- Runtime scales independently — deploy more replicas of `rag-runtime` behind a load balancer for high-throughput retrieval without scaling the API server.
- Every query is observable as a `RAGRun` — duration, tokens, strategy, chunks found. Same first-class status as `RecipeRun` and `Trace`.
- Custom strategies can be registered into a running instance via `POST /modules/register` with no restart of anything.
- Backward compatible: existing deployments with no `AGENTOVEN_RAG_RUNTIME_URL` set continue using the built-in `Pipeline` unchanged.
- The Python PageIndex sidecar (ADR-0020) works as a module registered via `POST /modules/register` — zero migration needed.

**Negative / Risks:**
- Adds a network hop (localhost or cluster-internal) to every RAG query. Latency cost is typically 1–3 ms on same-host, 3–10 ms in-cluster. For latency-critical queries, keep using the built-in `Pipeline` (no `AGENTOVEN_RAG_RUNTIME_URL` set).
- Runtime restart loses in-memory run records and runtime-registered modules in OSS. Operators must re-register modules after restart. Pro mitigates with Postgres persistence.
- Two things to deploy instead of one. Mitigated by shipping a default `docker-compose.yml` that starts both with a single command.

---

## Implementation plan

| Phase | Scope | Target |
|---|---|---|
| P0 | New `crates/rag-runtime/` top-level directory (Go module) | 0.9.x OSS |
| P0 | `Module` interface + `ModuleRegistry` + `RAGRun` model | 0.9.x OSS |
| P0 | HTTP server: `/health`, `/info`, `/query`, `/ingest`, `/runs`, `/modules` | 0.9.x OSS |
| P0 | Extract 5 built-in strategies from `pipeline.go` into `internal/modules/` sub-packages | 0.9.x OSS |
| P0 | Control plane: wire `ExternalRAGService` from `AGENTOVEN_RAG_RUNTIME_URL`; keep `Pipeline` fallback | 0.9.x OSS |
| P0 | PageIndex native Go module (replaces Python sidecar) | 0.9.x OSS |
| P0 | `docker-compose.yml` updated to include `rag-runtime` service | 0.9.x OSS |
| P1 | `POST /modules/register` + `RemoteModule` HTTP proxy | 0.9.x OSS |
| P1 | `GET /runs` + `GET /runs/{id}` + `run_id` stored on control-plane `Trace` | 0.9.x OSS |
| P2 | Pro: Postgres persistence for `RAGRun` records + module registrations | Pro 0.9.x |
| P2 | Pro: runtime auto-discovery (control plane polls runtime `/info` on startup) | Pro 0.9.x |

---

## Key files

| File | Change |
|---|---|
| `agentoven/rag-runtime/` | New top-level Go module |
| `agentoven/rag-runtime/cmd/main.go` | Binary entrypoint; blank-imports built-in modules |
| `agentoven/rag-runtime/internal/module/module.go` | `Module` interface + `ModuleRegistry` |
| `agentoven/rag-runtime/internal/module/remote.go` | `RemoteModule` — HTTP proxy to external subprocess |
| `agentoven/rag-runtime/internal/run/run.go` | `RAGRun` model + in-memory store |
| `agentoven/rag-runtime/internal/server/server.go` | HTTP handler for all endpoints |
| `agentoven/rag-runtime/internal/modules/naive/` | Extracted from `control-plane/internal/rag/pipeline.go` |
| `agentoven/rag-runtime/internal/modules/sentencewindow/` | Extracted |
| `agentoven/rag-runtime/internal/modules/parentdocument/` | Extracted |
| `agentoven/rag-runtime/internal/modules/hyde/` | Extracted |
| `agentoven/rag-runtime/internal/modules/agentic/` | Extracted |
| `agentoven/rag-runtime/internal/modules/pageindex/` | New — Go-native impl |
| `agentoven/control-plane/internal/rag/pipeline.go` | Remove strategy switch; becomes fallback only |
| `agentoven/control-plane/internal/api/handlers/rag_handlers.go` | Wire `ExternalRAGService` from env var; store `run_id` on trace |
| `agentoven/docker-compose.yml` | Add `rag-runtime` service |

## Related ADRs

- ADR-0014: Pluggable Scheduler Dispatcher — same "separate binary, delegate via env-var URL" pattern
- ADR-0017: Framework-Native Managed Agents — `AGENTOVEN_CONTROL_PLANE_URL` reused by runtime modules for LLM calls
- ADR-0020: PageIndex Vectorless RAG Strategy — PageIndex becomes a native module in the runtime
- Pro ADR-0027: Provider-Agnostic RAG Pipelines — named pipeline config routes to this runtime
