# ADR-0003: Open-Core Two-Repo Architecture

- **Status:** Accepted
- **Date:** 2026-02-25
- **Author(s):** Siddartha Kopparapu

## Context

AgentOven follows an open-core business model: the core control plane is open-source (MIT), while enterprise features (SSO, RBAC, cloud providers, advanced observability) are commercial. We needed to decide how to structure the codebase to support this model.

## Decision

We use a **two-repository architecture**:

- **`agentoven/agentoven`** (MIT, public) — full control plane, community features, SDKs, CLI
- **`agentoven/agentoven-pro`** (commercial, private) — enterprise overlays only

### Integration Contract

The OSS repo exposes **service interfaces** in `pkg/contracts/` that the Pro repo implements:

- `ProviderDriver` — model provider abstraction (OSS: OpenAI, Anthropic, Ollama; Pro: Bedrock, Foundry, Vertex)
- `EmbeddingCapableDriver` — optional interface on ProviderDriver
- `PlanResolver` — Kitchen → PlanLimits (OSS: static community limits; Pro: license-based)
- `AuthProvider` / `AuthProviderChain` — pluggable authentication
- `ArchiveDriver` — data retention backends (OSS: local file; Pro: S3, Azure Blob, GCS)
- `ChannelDriver` — notification dispatch (OSS: webhook; Pro: Slack, Teams)
- `PromptValidatorService` — prompt security (OSS: structure checks; Pro: injection detection, LLM judge)
- `ChatGatewayDriver` — chat platform messaging (Pro: Telegram, Discord, Slack)

### Wiring Pattern

Pro's `cmd/server/main.go` imports the OSS `Server` struct, creates it via `buildServer()`, then registers additional drivers and overrides:

```go
router.RegisterDriver("bedrock", providers.NewBedrockDriver())
server.PlanResolver = tier.NewProPlanResolver(licenseValidator)
```

### Go Module Setup

Pro uses `go.mod` `replace` directive during development:
```
replace github.com/agentoven/agentoven/control-plane => ../ent-agent-core/control-plane
```

## Consequences

- **Easier:** OSS repo is fully functional standalone. Pro never forks OSS code — it only extends via interfaces. Clear separation of MIT vs. commercial code.
- **Harder:** Interface changes in OSS can break Pro. Need careful API versioning. Two repos to keep in sync during releases.
- **Risk:** If interfaces are too narrow, Pro has to work around them. If too wide, OSS leaks enterprise concepts.

## Alternatives Considered

1. **Monorepo with build tags** — Rejected because Go build tags are fragile and make it hard to verify the OSS build doesn't accidentally include Pro code.
2. **Single repo with license-gated features** — Rejected because it makes the MIT license ambiguous and complicates open-source contributions.
3. **Plugin system (Go plugins / shared libs)** — Rejected due to Go's limited plugin support on non-Linux platforms and version coupling issues.
