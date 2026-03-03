# ADR-0001: Use ADRs for Architectural Decisions

- **Status:** Accepted
- **Date:** 2026-02-25
- **Author(s):** Siddartha Kopparapu

## Context

AgentOven has grown to span multiple repositories (OSS + Pro), multiple languages (Rust, Go, Python, TypeScript), and multiple deployment surfaces (control plane, CLI, SDKs, dashboard, web properties). Architectural decisions have historically been captured in planning documents (`release-three-plan.md`, `AUTH-PLAN.md`) or inline in `copilot-instructions.md`, making them hard to discover, reference, and track over time.

As the project grows and more contributors join, we need a lightweight, standardized way to record *why* decisions were made — not just *what* was built.

## Decision

We adopt **Architecture Decision Records (ADRs)** stored in an `/ADR` folder at the root of each repository:

- **`agentoven/agentoven`** (OSS) → `/ADR/` — decisions affecting the open-source control plane, SDKs, CLI, and protocols
- **`agentoven/agentoven-pro`** (Enterprise) → `/ADR/` — decisions specific to enterprise features, licensing, cloud providers

Each ADR:
- Is a Markdown file named `NNNN-short-title.md`
- Follows a consistent template (Context → Decision → Consequences → Alternatives)
- Is immutable once accepted (superseded by new ADRs, never edited retroactively)
- Is indexed in `ADR/README.md`

## Consequences

- **Easier:** New contributors can understand *why* the codebase is structured a certain way. Code reviews can reference ADRs. Historical context is preserved even if original authors leave.
- **Harder:** Slightly more process overhead per decision. Need discipline to write ADRs before or alongside implementation.
- **Migration:** Key past decisions (auth architecture, embedding model, open-core split) are back-filled as initial ADRs to bootstrap the record.

## Alternatives Considered

1. **Wiki/Notion pages** — Rejected because they're disconnected from the code and harder to keep in sync with the repo.
2. **Inline comments in code** — Insufficient for cross-cutting decisions that span multiple files or packages.
3. **GitHub Discussions/Issues** — Good for deliberation but poor for final record-keeping; discussions get buried.
