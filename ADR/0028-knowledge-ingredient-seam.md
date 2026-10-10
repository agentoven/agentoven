# ADR-0028: The `knowledge` ingredient seam (OSS hooks for the Pro knowledge engine)

**Status:** Proposed  
**Date:** 2026-10-10  
**Authors:** AgentOven Engineering  
**Scope:** OSS (a small, generic seam). The engine is Pro, in the separate repo `agentoven-knowledge`.

---

## Context

An agent gets retrieval today by stacking three ingredients (`embedding`, `vectorstore`, `retriever`). The resolver reads only a few
keys from a retriever, and the executor retrieves once per invoke and pastes the result into the system prompt (`retrieveContext`):
failures are skipped silently, the context budget is ignored, and the model cannot choose when or what to retrieve. Pro is gaining a
knowledge engine (ADR-0036 in `agentoven-pro`, and K-001..K-014 in `agentoven-knowledge`) that must be usable as an ingredient of an
agent. [ADR-0026](0026-rag-runtime-separate-process.md) already proposed a separate RAG runtime behind `ExternalRAGService`.

## Decision

Add the minimum generic seam to OSS, following the `IngredientScenario` precedent (OSS records the reference, Pro runs it):

1. `pkg/models`: `IngredientKnowledge` kind; `ResolvedKnowledge` (knowledge base, pinned pipeline version, mode, budget, ACL mode, filters,
   memory and eval blocks); `ResolvedIngredients.Knowledge`.
2. `internal/resolver`: a `KnowledgeResolver` interface and a setter. With none registered, a `knowledge` ingredient resolves to a clear
   "requires Pro" error (required) or a warning (optional).
3. `internal/executor`: a `ContextProvider` interface (`Assemble`, `Tools`) and a setter; the caller's **principal** is carried into the
   executor and recipe steps so a provider can filter by who is asking. With none registered, nothing changes.
4. Tool dispatch for the provider's tools reuses `allResolvedTools` and the existing tool path; no new transport.
5. `StepRAG` may name a `knowledge_base` and `pipeline`; without a provider it keeps today's behaviour.
6. Existing ingredients and `/api/v1/rag/*` are unchanged.

Fixes that block the engine are filed as separate small issues (RAG ignoring the configured vector store, non-deterministic default embedding,
Pro-registered embedding drivers never discovered, `filter` ignored on query, `doc_index`/`chunk_index` mix-up, dashboard field mismatch).

## Alternatives considered

- Keep extending `retriever` with engine-specific keys: makes OSS know engine fields.
- Put the engine in OSS: contradicts the Pro-only decision and the open-core boundary.
- Tool-only (no ingredient): loses bake-time validation, version pinning, budget accounting and promotion gates.

## Consequences

- A small OSS change with tests; OSS behaviour is identical without Pro.
- Agents outside the Go executor (process-backed, framework-native) reach knowledge only through MCP tools and the HTTP search route.
- Tracking: roadmap item R18 and related issues in `agentoven-knowledge/ROADMAP.md`; OSS issues carry the `knowledge-engine` label.
