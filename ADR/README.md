# Architecture Decision Records (ADRs)

This directory contains **Architecture Decision Records** for the AgentOven OSS repository (`agentoven/agentoven`).

## What is an ADR?

An ADR is a short document that captures an important architectural decision made along with its context and consequences. ADRs are numbered sequentially and are **immutable once accepted** — if a decision is reversed, a new ADR is created that supersedes the old one.

## Format

Each ADR follows this template:

```
# ADR-NNNN: Title

- **Status:** Proposed | Accepted | Deprecated | Superseded by ADR-XXXX
- **Date:** YYYY-MM-DD
- **Author(s):** Name(s)
- **Supersedes:** ADR-XXXX (if applicable)

## Context

What is the issue that we're seeing that is motivating this decision or change?

## Decision

What is the change that we're proposing and/or doing?

## Consequences

What becomes easier or more difficult to do because of this change?

## Alternatives Considered

What other options were evaluated and why were they rejected?
```

## Naming Convention

- Files are named `NNNN-short-title.md` (e.g., `0001-use-adr-for-decisions.md`)
- Numbers are zero-padded to 4 digits
- Use lowercase kebab-case for the title portion

## Index

| ADR | Title | Status | Date |
|-----|-------|--------|------|
| [0001](0001-use-adr-for-decisions.md) | Use ADRs for architectural decisions | Accepted | 2026-02-25 |
| [0002](0002-pluggable-auth-provider-chain.md) | Pluggable authentication with provider chain | Accepted | 2026-02-25 |
| [0003](0003-open-core-two-repo-model.md) | Open-core two-repo architecture | Accepted | 2026-02-25 |
| [0004](0004-provider-first-embedding-architecture.md) | Provider-first embedding architecture | Accepted | 2026-02-25 |
| [0005](0005-kitchen-metaphor-domain-language.md) | Kitchen metaphor domain language | Accepted | 2026-02-25 |
| [0006](0006-python-sdk-reqwest-blocking.md) | Python SDK reqwest blocking | Accepted | 2026-02-25 |
| [0007](0007-control-plane-as-a2a-gateway.md) | Control plane as A2A gateway (stable agent URLs) | Accepted | 2026-02-22 |
| [0008](0008-three-layer-product-architecture.md) | Three-layer product architecture (Dashboard · Pro Dashboard · Agent Viewer) | Accepted | 2026-03-01 |
| [0009](0009-pluggable-test-runner-architecture.md) | Pluggable test runner architecture | Accepted | 2026-02-23 |
| [0010](0010-kitchen-level-compliance-policies.md) | Kitchen-level compliance policies | Accepted | 2026-03-03 |
| [0011](0011-oss-local-test-runner.md) | OSS local test runner for agentic agents | Accepted | 2026-03-03 |
| [0012](0012-agent-packaging-distribution.md) | Agent packaging and distribution (.aopack) | Accepted | 2026-03-03 |
| [0013](0013-agent-to-ui-protocol.md) | Agent-to-UI protocol (A2UI) | Accepted | 2026-03-03 |
