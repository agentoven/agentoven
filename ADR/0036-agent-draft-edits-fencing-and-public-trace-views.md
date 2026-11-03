# ADR-0036: Draft edits, fingerprint rules, lease fencing and public views of trace content

**Status:** Accepted (built 2026-11-03)
**Date:** 2026-11-03
**Scope:** OSS control plane: `pkg/models` (agent edit, fingerprint, lifecycle, public views), `internal/store`, `internal/api/handlers`, `internal/executor`, `internal/router`, `internal/retention`, `pkg/agentexec`. Pro implements the PostgreSQL parts; the decisions are recorded in agentoven-pro ADR-0043 (amendment 2026-11-03).

## Context

An adversarial review of the 2026-11-02 work found that the rules held on the happy path and not around it: an edit changed what an agent does under an unchanged version label, a named version ran a new configuration under an old label, the bake's background step overwrote later edits, trace bodies leaked through routes the guard did not cover, a frozen replica could run a tool of a task another replica had adopted, and one tenant's failures opened a provider's circuit for all tenants. These are fixes with seams, not new features.

## Decisions

1. **A baked agent runs what was baked.** `Agent.Draft` (`models.AgentEdit`) holds edits made since; `Agent.Edit` applies an edit at once only when nothing has been baked, `ApplyDraft` is what a bake does first. `PUT /agents/{name}` and `POST /agents/{name}/recook` share the type. Pro keeps the draft in the `settings` JSON column.
2. **The fingerprint lists what it leaves out, not what it takes** (`models.agentStateFields`). A new `Agent` field counts until classified; two tests fail until it is.
3. **A named version is validated** (`ValidVersion`, `DecideNamedVersion`, `VersionBumpNamed`, `models.VersionError` answered 400/409). Both stores decide from the stored row, never from the caller's copy.
4. **`Store.UpdateAgentLifecycle(kitchen, name, guard, change)`** is the only write a bake's background step makes (status, error tag, backend endpoint, process), guarded by status and version. `bakeSerializer` runs the step one at a time per agent for the newest request only.
5. **Trace content has public types** (`PublicTrace`, `PublicSpan`, `PublicScore`, `PublicSession`, the task view) listing what a reader without content access gets; `respondJSON` refuses the full types unless they went through `h.traceOut` and its siblings. `Handlers.SetTraceContentGuard` is unchanged. `/tasks/{id}/events` and `/langfuse/export` need content access outright.
6. **Leases have a fencing epoch.** `TaskLeases.Epoch`; `executor.Fence` travels on the task's context (`agentexec.WithFence`, `FenceFrom`, `ErrFenced`); `dispatchToolCall` verifies it before running a tool; a `RecordLog` that gets a context with a fence must refuse a write the fence does not cover. `RecordLog.ListAll` becomes `ListSweepable`. `LogJournal.SetHeld` and `retention.Janitor.SetOnTraceDeleted` let a deployment tie journals to legal hold and trace retention.
7. **Router**: the circuit is keyed by (kitchen, provider, endpoint) and counts provider faults only (`RequestError`, `RequestRejected`, `classifyStatus`); a streamed call is charged for what was generated (`streamCharge`).

## Consequences

- Clients that PUT an agent and invoke it expecting the change must bake or re-cook first; `GET` shows `draft`.
- Embedders implementing `TaskLeases` add `Epoch`; implementers of `RecordLog` replace `ListAll` with `ListSweepable` and honour a `Fence`.
- Gemini 429 and 5xx are now retried like the other drivers (they were never marked retryable).

## Not done

- Recipe runs and approvals have no public form (see agentoven-pro ADR-0043 amendment).
- `PUT` writes the whole agent row.
- The file journal has no fence (a single process owns it); the memory test `RecordLog` does not check fences, the PostgreSQL one does.
