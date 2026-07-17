# ADR-0022: Dashboard Sidebar Navigation Grouping & Human-Gate Approval UX

- **Status:** Proposed
- **Date:** 2026-07-04
- **Author(s):** AgentOven Engineering

## Context

Both dashboards (OSS `agentoven/control-plane/dashboard` and `agentoven-pro/dashboard`) render their sidebar as a single flat, ungrouped list of `NavLink`s at a fixed width:

- OSS `Layout.tsx` — 13 items, zero grouping, zero collapse, fixed `w-60` (240px).
- Pro `ProLayout.tsx` — 30 items + 1 cosmetic "ENTERPRISE" divider, fixed 240px (inline styles), no collapse, no search. Items are individually gated by `hasRole(...roles)` but otherwise unorganized.

This has become a usability problem as Pro has grown from a handful of core pages to 30 top-level destinations spanning agent building, data/RAG, provider config, governance (audit/compliance/cost), and admin/licensing concerns — all rendered in one undifferentiated scrolling list.

Separately, while auditing the dashboards for this work we found a related, higher-severity UX bug in the Pro **DishShelf** pipeline-run view (`agentoven-pro/dashboard/src/pages/PipelineView.tsx`): the human-gate approval banner **flickers — appearing and disappearing** while a run is paused awaiting approval, and the approve/reject action is only exposed at the **top of the page**, disconnected from the DAG node it applies to, rather than being reached by clicking into the step itself.

### Root cause of the gate flicker

`PipelineView.tsx` has **three independent code paths** that each call `recipeRuns.get(...)` and then call `setSelectedRun()` / `setPendingGates()` separately, with no coordination between them:

1. A `useEffect` keyed on `[selectedRecipe?.name, selectedRun?.id]` that loads run detail once when a run is selected (uses a `canceled` flag to guard against stale responses).
2. A hand-rolled `setInterval(..., 2000)` polling loop keyed on `[isTerminalRunStatus, selectedRun?.id, selectedRun?.status, selectedRecipe?.name]` — this one has **no stale-response guard**, unlike (1).
3. `handleGateDecision()` itself, which calls `approveGate`/`rejectGate` and then does its **own** extra `recipeRuns.get()` refetch immediately after.

Because (2) and (3) are not sequenced, a poll tick can land in between a user's approve/reject action and the backend's next-step transition being visible, returning a stale snapshot where `status` is still `'paused'` and `pending_gates` still contains the just-resolved step name. This stale snapshot overwrites the fresh, already-cleared state that (3) just set — producing the visible "reappear, then disappear again" flicker.

A second, independent contributing factor: the top summary-bar banner's visibility condition (`selectedRun.status === 'paused' && pendingGates.length > 0`) depends on **two separately-set pieces of state** that are supposed to arrive together in one API response but are written via two sequential `setState` calls. Any one of the three fetch paths partially failing (e.g. the "load run details" effect's `catch { setPendingGates([]) }`, which clears gates without touching `status`) can desync them for one render.

A third, purely-a-UX issue (not a bug, a design gap): the gate action exists in **two places** — the top summary bar (`pendingGates[0]` only — additional pending gates are invisible) and the `StepDetailPanel` modal (opened by clicking the DAG node, gated on a *different* condition: `!step.status || step.gate_status === 'waiting'`). Two different trigger conditions for what should be one piece of state is itself a source of inconsistency, independent of the race above.

We also note `Modal` is invoked with a hardcoded `insetLeft={240}` (`PipelineView.tsx`) matching the current fixed sidebar width. This will silently break centering once sidebar collapse (icon-rail mode, see Decision 1) ships, since the sidebar will no longer always be 240px wide.

There is already a shared, guarded polling hook in the Pro dashboard (`usePolling` in `hooks.ts`, built on `useAPI`), but `PipelineView.tsx` does not use it — it duplicates the fetch/interval logic by hand without the hook's cancellation semantics. This is worth calling out as a repeated anti-pattern to avoid re-introducing elsewhere.

## Decision

### 1. Group and make the sidebar collapsible (both dashboards)

Replace the flat `nav` / `navItems` arrays with a grouped config: `{ id, label, items }[]`. Same taxonomy in both dashboards for consistency:

| Group | OSS items | Pro items (additional) |
|---|---|---|
| *(pinned, ungrouped)* | Overview | Overview |
| Agents | Agents, Recipes, DishShelf, Prompts | + Test Suites, Environments, Promotion History |
| Context | Model Catalog, Embeddings, Vector Stores, RAG Pipelines, Connectors | *(same)* |
| Providers | Providers, Tools | + MCP Tools, Scheduler, Sessions |
| Governance | Traces | + Traceability Matrix, Audit Trail, Compliance, Cost Analytics |
| Admin & Cost | *(omitted — no items)* | Kitchens, Kitchen Settings, User Management, Service Accounts, Scoped Keys, Cross-Kitchen Grants, License, Agent Workloads |

Groups are collapsible (not just static headers), expand/collapse state persisted in `localStorage`, auto-expand the group containing the active route. Existing per-item RBAC (`item.roles` in Pro) is preserved unchanged; a group with zero visible items for the current role renders nothing.

Add a nav search/filter box (substring match over labels) and an icon-rail collapse toggle (240px ↔ ~64px) to both sidebars, each independently implemented (Tailwind in OSS, inline styles in Pro) — no shared component package across the two repos.

### 2. Fix the human-gate flicker

- Consolidate all run-detail fetching in `PipelineView.tsx` through a single guarded fetch path (either adopt the existing `usePolling`/`useAPI` hook, or add the same `canceled`/request-sequencing guard the "load run details" effect already uses to the polling `setInterval` and to `handleGateDecision`'s post-action refetch).
- On gate decision, optimistically remove the resolved step from `pendingGates` client-side immediately (before the network round-trip resolves), so a concurrent stale poll response cannot briefly resurrect it.
- Treat `status` and `pendingGates` as one atomic piece of state set together from a single response object, not two independent `setState` calls that other code paths can update individually.

### 3. Fix the human-gate placement/duplication

- The DAG node is the single place a gate decision can be acted on: clicking a `human_gate` node (already wired to `setDetailStep`) opens the existing `StepDetailPanel` modal, which keeps the Approve/Reject buttons.
- The top summary-bar loses its Approve/Reject buttons. It becomes a passive indicator only — e.g. "Awaiting gate: `<n>` step(s) — click a node below to review" — that does not itself resolve the gate, eliminating the second trigger condition and the duplicate action surface.
- Use one shared visibility condition (`step.step_kind === 'human_gate' && step.gate_status === 'waiting'`) for both the node's amber-pending visual state and the modal's action panel, instead of the modal's current, subtly different `!step.status || step.gate_status === 'waiting'` check.
- Update `Modal`'s `insetLeft` usage in `PipelineView.tsx` to read the current sidebar width (240 expanded / ~64 collapsed) rather than a hardcoded `240`, so it stays correct once icon-rail collapse (Decision 1) ships.

## Consequences

**Easier:**
- Both dashboards scale to more nav items without becoming an unreadable scroll list.
- Gate approval becomes deterministic — one state update path, one action surface, no more flicker.
- Icon-rail collapse reclaims horizontal space for the DAG canvas and other wide content.

**Harder / cost:**
- Two independent implementations to keep in sync (no shared component library between OSS and Pro repos) — future nav changes must be applied twice.
- Consolidating the gate polling touches actively-used pipeline-run code; regressions here affect a customer-facing approval workflow, so this needs careful manual verification (see plan) before shipping.
- Removing the top-bar action changes existing user muscle memory for anyone currently approving gates from that bar; needs a changelog/release note callout.

## Alternatives Considered

- **Shared nav/UI package across OSS and Pro repos:** rejected for now — the two repos are intentionally separate (ADR-0003 open-core two-repo model) and a shared package adds release-coordination overhead disproportionate to a navigation-only change.
- **Keep both gate-action surfaces (top bar + modal) but just fix the race:** rejected — even with the race fixed, two different trigger conditions for the same action is a latent source of future bugs; consolidating to one is strictly simpler.
- **Full responsive/mobile drawer sidebar:** out of scope for this decision; icon-rail collapse addresses the immediate horizontal-space problem without taking on breakpoint/mobile-drawer design work.
