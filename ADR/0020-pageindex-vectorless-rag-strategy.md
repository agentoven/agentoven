# ADR-0020: PageIndex Vectorless RAG Strategy

**Status**: Proposed  
**Date**: 2026-06-07  
**Authors**: AgentOven Engineering

---

## Context

The existing RAG pipeline (`/api/v1/rag/query`) supports embedding-based retrieval strategies:
`naive`, `sentence_window`, `parent_document`, `hyde`, and `agentic`.
All strategies require:
- A vector database (Qdrant, Weaviate, PGVector, etc.)
- An embedding model to chunk and encode documents
- Top-K similarity search at query time

For professional long documents (financial reports, legal contracts, compliance manuals,
regulatory filings), vector similarity is insufficient. Relevant passages are not always
*similar* to the query — they require multi-step reasoning over document structure.

[PageIndex](https://github.com/VectifyAI/PageIndex) (MIT, ~33k GitHub stars) is a
vectorless, reasoning-based RAG system. It:
1. Builds a hierarchical "table-of-contents" tree index from a document (offline)
2. At query time, LLM reasoning traverses the tree to find relevant sections
3. Returns traceable results with full section paths and page references
4. Requires **no vector DB** and **no chunking**

Benchmark: 98.7% accuracy on FinanceBench, outperforming all vector-based RAG systems.

---

## Decision

Add `pageindex` as a first-class RAG strategy in AgentOven Pro. The integration is
implemented as a Python sidecar service that the control plane proxies to.

**Architecture: Python sidecar**

```
                ┌──────────────────────────┐
  /rag/query    │   agentoven-pro (Go)      │
  strategy=     │   RAGHandler              │
  pageindex ──► │   if strategy==pageindex  │
                │     → proxy to sidecar    │
                └────────────┬─────────────┘
                             │  HTTP POST (internal)
                ┌────────────▼─────────────┐
                │   pageindex-sidecar       │
                │   (Python FastAPI)        │
                │   POST /index             │
                │   POST /query             │
                │   GET  /tree/{doc_id}     │
                │   GET  /documents         │
                └──────────────────────────┘
                             │
                ┌────────────▼─────────────┐
                │   Document store          │
                │   (local JSON files or    │
                │    PostgreSQL JSONB)       │
                └──────────────────────────┘
```

**Why sidecar instead of embedding PageIndex into Go?**

PageIndex is a pure Python library (LiteLLM + PDF parsing). Rewriting it in Go would
duplicate ~4000 lines of research-grade code and lose ecosystem compatibility. A thin
FastAPI sidecar is the minimum viable integration path. This matches ADR-0006
(Python SDK / reqwest blocking) philosophy of using native runtimes for language-specific
libraries.

---

## New API Endpoints (proxied to sidecar)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/rag/pageindex/index` | Build PageIndex tree for a document (text or PDF URL) |
| `GET`  | `/api/v1/rag/pageindex/tree/{doc_id}` | Return the generated tree JSON |
| `GET`  | `/api/v1/rag/pageindex/documents` | List all indexed documents in the kitchen |
| `POST` | `/api/v1/rag/query` (strategy=`pageindex`) | Existing endpoint, new strategy case |

---

## Implementation Plan

### Phase 1 — Sidecar service (`pageindex/` in workspace root)

```
pageindex-sidecar/
  Dockerfile
  requirements.txt        ← pageindex, fastapi, uvicorn, litellm
  main.py                 ← FastAPI app
  store.py                ← JSON file or PG JSONB tree store
  config.py               ← env: LLM provider, PG URL
```

Key env vars:
```
PAGEINDEX_LLM_PROVIDER=openai|azure|bedrock  (passed to LiteLLM)
PAGEINDEX_LLM_MODEL=gpt-4o
PAGEINDEX_OPENAI_API_KEY=...
PAGEINDEX_STORE_PATH=/data/pageindex          (for file-based store)
PAGEINDEX_PG_URL=...                          (for PG JSONB store)
```

### Phase 2 — Control plane proxy (Go)

In `agentoven/control-plane/internal/api/handlers/rag_handler.go`:

```go
// In RAGHandler.Query():
case "pageindex":
    sidecarURL := os.Getenv("AGENTOVEN_PAGEINDEX_URL") // default: http://pageindex:8081
    resp, err := http.Post(sidecarURL+"/query", "application/json", body)
    // proxy response back to caller
```

In `router.go` — add three new routes:
```go
r.Post("/rag/pageindex/index",           h.PageIndexIndex)
r.Get("/rag/pageindex/tree/{docID}",     h.PageIndexGetTree)
r.Get("/rag/pageindex/documents",        h.PageIndexListDocs)
```

### Phase 3 — Helm / k8s

Add `pageindex-sidecar` as a new Deployment in `agentoven-pro/charts/agentoven-pro/templates/`:
- Shares the same namespace as `agentoven-pro`
- Communicates over cluster-internal DNS: `http://pageindex-sidecar:8081`
- PVC or PG for tree storage
- `AGENTOVEN_PAGEINDEX_URL` injected into the control plane deployment via values

New `aks-values.yaml` key:
```yaml
pageindex:
  enabled: true
  image: ghcr.io/agentoven/pageindex-sidecar:0.1.0
  llmProvider: azure
  llmModel: gpt-4o
  storePath: /data/pageindex
```

### Phase 4 — Dashboard (already implemented)

- `RAGStrategy` union type includes `'pageindex'`
- `RAGQueryResult` includes `tree_path` and `reasoning_trace`
- `PageIndexNode` tree viewer component renders the visited nodes
- New "Indexed Docs" tab lists documents
- New "Index Document" tab lets users submit text to build the tree

---

## Response Format Extension

The existing `RAGQueryResult` is extended (backwards-compatible):

```json
{
  "answer": "...",
  "sources": [
    {
      "doc": {
        "id": "annual-report-2024",
        "content": "Revenue grew by 14%...",
        "page": 42,
        "section_path": ["3. Financial Results", "3.2 Revenue"],
        "node_id": "0031"
      },
      "score": 0.94
    }
  ],
  "strategy": "pageindex",
  "chunks_retrieved": 3,
  "latency_ms": 1240,
  "tree_path": [
    { "node_id": "0000", "title": "Annual Report 2024", "start_page": 1, "end_page": 180, ... },
    { "node_id": "0010", "title": "3. Financial Results", "start_page": 38, "end_page": 65, ... },
    { "node_id": "0031", "title": "3.2 Revenue", "start_page": 40, "end_page": 45, ... }
  ],
  "reasoning_trace": "Step 1: Examining top-level nodes...\nStep 2: Entering '3. Financial Results'..."
}
```

---

## Consequences

**Positive**
- No vector DB required for document-heavy use cases
- Results are fully traceable (page + section path)
- Works with any LiteLLM-compatible provider (OpenAI, Azure, Bedrock, Ollama)
- Sidecar is independently deployable and upgradeable
- 98.7% accuracy on professional document benchmarks

**Negative / Trade-offs**
- Indexing is LLM-call-intensive (O(n) pages × tree depth)
- Query latency higher than vector retrieval for very simple lookups
- Requires a separate Python process (adds to resource footprint)
- Sidecar needs its own LLM API key / provider configuration

**Neutral**
- Does not replace vector-based strategies — they coexist
- Feature-gated behind `features.rag` in the license

---

## Alternatives Considered

| Option | Rejected reason |
|--------|----------------|
| Port PageIndex to Go | ~4k lines of research Python; no ecosystem benefit |
| Use PageIndex Cloud API | Vendor dependency, data leaves the cluster |
| WASM Python via wasmer | Immature; heavy bundle; LiteLLM incompatible |
| Embed Python via CGo | Deployment complexity; no standard pattern in Go ecosystem |
