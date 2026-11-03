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
| [0014](0014-pluggable-scheduler-dispatcher.md) | Pluggable scheduler dispatcher abstraction | Accepted | 2026-05-05 |
| [0015](0015-agent-orchestrator-k8s-crd.md) | Agent orchestrator — CRD-based lifecycle on Kubernetes | Accepted | 2026-05-05 |
| [0016](0016-external-agent-traceability.md) | External agent registration and traceability | Accepted | 2026-05-05 |
| [0017](0017-framework-native-managed-agents.md) | Framework-native managed agents | Accepted | 2026-05-14 |
| [0018](0018-remote-provider-plugin-protocol.md) | Remote provider plugin protocol (gRPC) | Accepted | 2026-05-18 |
| [0019](0019-otel-metrics-pipeline-multi-sink.md) | OTel metrics pipeline and multi-sink collector | Accepted | 2026-05-18 |
| [0020](0020-pageindex-vectorless-rag-strategy.md) | PageIndex vectorless RAG strategy | Proposed | 2026-06-07 |
| [0021](0021-release-090-oss-architecture.md) | Release 0.9.0 OSS architecture — SSE, skills plugin system, multimodal, RAG multi-pipeline | Accepted | 2026-06-23 |
| [0022](0022-dashboard-nav-grouping-and-gate-ux.md) | Dashboard sidebar navigation grouping & human-gate approval UX | Proposed | 2026-07-04 |
| [0023](0023-pluggable-cloud-agent-deployment-backend.md) | Pluggable cloud agent deployment backend (AgenticCore selection) | Proposed | 2026-07-07 |
| [0024](0024-advanced-recipe-flow-and-hpa.md) | Advanced recipe flow control — gate branching, loop controls, multi-scope rate limiting, K8s HPA | Proposed | 2026-07-07 |
| [0025](0025-agent-as-identity-principal.md) | Agent as a first-class identity principal | Proposed | 2026-07-07 |
| [0026](0026-rag-runtime-separate-process.md) | RAG/KAG runtime as a separate process | Proposed | 2026-07-10 |
| [0027](0027-pooled-agent-runtime.md) | Pooled agent runtime — request-time config injection into a per-kitchen warm process pool | Proposed | 2026-08-18 |
| [0028](0028-knowledge-ingredient-seam.md) | The `knowledge` ingredient seam — OSS hooks for the Pro knowledge engine | Proposed | 2026-10-10 |
| [0029](0029-agentic-bot-runtime-seam.md) | The `agenticbot` runtime seam — OSS hooks for Pro-managed computer-use agents | Accepted | 2026-10-12 |
| [0030](0030-task-content-parts.md) | Background tasks take files by blob reference (and a refused approval is a 403, not a 500) | Accepted | 2026-11-03 |
| [0031](0031-native-tool-transport-and-tool-approval-seams.md) | The native tool transport and tool-level approval seams — OSS hooks for Pro tool packs | Accepted | 2026-11-03 |
| [0032](0032-guardrail-actions-and-hops.md) | Guardrail actions (block, redact, mask, warn, require_approval), validated detectors, retrieval hop and custom evaluator seam | Accepted | 2026-11-03 |
| [0033](0033-route-guard-seam-and-kitchen-scoped-ids.md) | The route guard seam, provider credential pinning and ids that belong to a kitchen | Accepted | 2026-11-03 |
| [0034](0034-approval-observer-and-expirer-seams.md) | The approval observer and expirer seams — OSS hooks for Pro approvals where people work | Accepted | 2026-11-03 |
| [0035](0035-traffic-and-cache-seams.md) | Traffic and cache seams — OSS hooks for Pro rate limits, budgets per scope and the response cache | Accepted | 2026-11-03 |
| [0036](0036-agent-draft-edits-fencing-and-public-trace-views.md) | Draft edits, fingerprint rules, lease fencing and public views of trace content — fixes from the 2026-11-03 adversarial review | Accepted | 2026-11-03 |
| [0037](0037-tool-egress-guard-store-errors-and-sdk-route-contract.md) | Tool egress guard, store errors, retired agents and the SDK route contract — fixes from the 2026-11-03 adversarial review | Accepted | 2026-11-03 |
| [0038](0038-after-authentication-hook-and-kitchen-scoped-waits.md) | After-authentication hook, kitchen-scoped wait release, signed run decisions that do not proceed blind, decision routes in `pkg/contracts` | Accepted (built 2026-11-03) | 2026-11-03 |
| [0039](0039-document-intelligence.md) | Document intelligence: in-process reading of Office and text files, OCR through the router, grounded field extraction | Accepted (built 2026-11-03) | 2026-11-03 |
| [0040](0040-round-2-residuals-2026-11-03.md) | Round 2 residuals: tool authentication shapes, channel signing secrets, signed notes, kitchen-required stores | Accepted (built 2026-11-03) | 2026-11-03 |
| [0041](0041-config-reference-validation.md) | Configuration names are checked against the kitchen: `pkg/refcheck`, 422 with nearest suggestions on write and bake, `GET .../validate`, dropdowns, SDK and CLI pre-checks | Accepted (built 2026-11-03) | 2026-11-03 |
| [0042](0042-owner-decisions-2026-11-03-seams.md) | The owner's decisions of 2026-11-03, community seams: approval inputs as content, `null` clears a field, retired agents refuse lifecycle writes, bake-deployment and own-endpoint hooks, separate channel egress allowlist | Accepted (built 2026-11-03) | 2026-11-03 |
