# ADR-0004: Provider-First Embedding Architecture

- **Status:** Accepted
- **Date:** 2026-02-25
- **Author(s):** Siddartha Kopparapu

## Context

AgentOven needs text embeddings for RAG pipelines. The traditional approach is to configure embedding models separately from chat/completion providers. This creates redundant configuration: a user who already has an OpenAI provider with an API key must separately configure an OpenAI embedding endpoint with the same key.

## Decision

We adopt a **provider-first embedding architecture**: embeddings are auto-discovered from configured model providers. When a user registers an OpenAI provider with an API key, embedding capabilities become available automatically.

### How It Works

1. Provider drivers implement the optional `EmbeddingCapableDriver` interface:
   ```go
   type EmbeddingCapableDriver interface {
       EmbeddingModels() []EmbeddingModelInfo
       Embed(ctx, provider, model, texts) ([][]float64, error)
   }
   ```

2. At startup, the server iterates registered providers and discovers embedding capabilities

3. Each provider's default embedding model is auto-registered in the embedding registry

4. Fallback: env vars (`OPENAI_API_KEY`, `OLLAMA_URL`) work for quick setup via standalone embedding drivers

### Drivers with Embedding Support

| Driver | Embedding Models | Dimensions |
|--------|-----------------|------------|
| OpenAI | text-embedding-3-small, 3-large, ada-002 | 1536, 3072, 1536 |
| Azure OpenAI | Same as OpenAI (via Foundry) | Same |
| Ollama | nomic-embed-text, mxbai-embed-large, all-minilm | 768, 1024, 384 |
| Bedrock (Pro) | Titan v2/v1, Titan Image, Cohere English/Multilingual | 1024, 1536, 1024 |
| Foundry (Pro) | text-embedding-3-small/large, ada-002 | 1536, 3072, 1536 |
| Vertex (Pro) | text-embedding-004/005, multilingual-002, gecko@003 | 768 |
| Anthropic | ❌ No embedding API | — |

### Adapter Pattern

`ProviderEmbeddingAdapter` bridges `ProviderDriver` credentials → `EmbeddingDriver` interface, so the RAG pipeline doesn't need to know whether embeddings came from a standalone driver or an auto-discovered provider.

## Consequences

- **Easier:** Zero extra configuration for users who already have a chat provider. Adding embedding support to a new provider is a single optional interface.
- **Harder:** Provider lifecycle becomes more complex — removing a provider may break an active RAG pipeline that depends on its embeddings.
- **Trade-off:** Couples embedding availability to provider health. If the OpenAI provider goes down, both chat and embedding fail.

## Alternatives Considered

1. **Separate embedding configuration** — Rejected because it doubles config overhead and confuses users who expect "I added OpenAI" to mean all OpenAI capabilities.
2. **Universal embedding service** — Rejected because different providers have different auth, endpoints, and response formats — a single abstraction would be too leaky.
3. **Embedding as MCP tool** — Considered but rejected because embeddings are a core pipeline primitive, not an external tool call. Latency and batching requirements differ from tool invocations.
