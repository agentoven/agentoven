# ADR-0035: Traffic and cache seams — OSS hooks for Pro rate limits, budgets per scope and the response cache

**Status:** Accepted (2026-11-03)
**Scope:** OSS control plane (`pkg/models`, `pkg/contracts`, `internal/router`, `internal/executor`, `internal/api`). The implementation is Pro ([agentoven-pro ADR-0051](../../agentoven-pro/ADR/0051-kitchen-budgets-limits-cache.md)); the community edition installs none of it and behaves as before.
**Number:** the OSS number 0034 reserved for this work was taken by the approval-observer ADR the same day, so this is 0035.

## Context

The router has one hook for money (`SetSpendGate`, [ROADMAP #107](../ROADMAP.md)). Pro needs, on the same call path: rate limits that answer `429 Retry-After`, a refusal that says whether it is a budget or a rate, a response cache the router consults before it reserves or sends anything, a way to limit the invocations that start agent work, and a signed decision for raising a shared budget. A review of the router also found that a budget refusal counted as a provider failure in the circuit breaker.

## Decision

1. **`models.RateLimitedError`** (`ErrRateLimited`): scope (`kitchen`, `agent`, `api_key`, `provider`), the limit that ran out, `RetryAfter`. A gate returns it from `Reserve`. `RetryAfterSeconds()` is the whole seconds for a `Retry-After` header, at least one.
2. **`models.SpendRefusedError`** gains `Unit` (`usd` or `tokens`), `Period` and `ResetsAt`, and `Dimension()`; a run paused at a token budget says `tokens`. Existing fields are unchanged.
3. **Router**: a gate refusal (spend or rate) is not a provider failure (`internal/router/refusal.go`: it never counts toward the circuit), and when it is about more than the one provider (scope not `provider`) the fallback to the next provider stops. A limit on a single provider still falls back.
4. **`contracts.ResponseCache`** (`Lookup`, `Store`): consulted in `callProvider` after the model is resolved and before the gate reserves or the driver is called; a hit is marked `Cached`, its usage zeroed (no cost, no budget, no rate limit). Streamed calls are neither looked up nor stored. `RouteResponse.Cached` already existed and was unused; the trace, the router span and an agent run's turn now carry `cache_hit`.
5. **`contracts.TrafficLimiter`** and `Handlers.Traffic`: `Handlers.LimitTraffic` is middleware on `invoke`, `invoke/stream`, session messages and task creation. A `*models.RateLimitedError` answers 429 with `Retry-After` and `{error: rate_limited, scope, limit, retry_after}`; any other error answers 503 (a limiter that cannot answer is not taken to have said yes). A model call that a rate limit refused inside a run is answered 429 too, never 500.
6. **Executor**: a rate limit is retried by a budgeted run after at least the time it names (at most a minute); a budget refusal is still not retried.
7. **`contracts.GateBudgetRaise`** (`budget_raise`): a name for the signed decision ([Pro ADR-0041](../../agentoven-pro/ADR/0041-electronic-signatures-on-human-decisions.md)) of raising or removing a period budget. The community edition has no period budgets and no use for it.

8. **One chain for every model call** (amendment 2026-11-03, review 5). The router is the only door: guardrails on the prompt (`router.ContentGuard`, `SetContentGuard`; the server installs `Handlers.ModelGuard`, the kitchen's workspace rules), then the response cache, the spend gate with the rate limits, the provider, then guardrails on the reply, then the trace and capture, which therefore hold what was handed on. A caller that already judged the exchange with an agent's own rules says so with `router.ContextGuardrailsApplied`; a caller that judges the reply itself (the native session handler) passes `router.ContextReplyJudge` so the refusal or redaction happens before the router records the call. A stream under a rule that applies to replies is held back and judged whole. Calls that are not completions go through the same gate with `ModelRouter.Admit`: speech to text and text to speech (`SpeechFor`), a live session (`RealtimeFor`, settled when it ends, sized from its duration), embeddings, and a provider test (`TestProvider`); an ad-hoc provider (the prompt validator's judge) goes through `RouteVia`. `TestEveryProviderCallPassesTheSpendGate`, `TestEveryModelCallIsJudgedByTheGuard`, `TestEveryExportedRouterMethodIsClassified` (a new exported router method must be classified) and `TestNoOneReachesADriverAroundTheRouter` keep it that way.
9. **Refusals have their own statuses**: `handlers.respondRefusal` answers a used-up budget 402, a rate limit or a refusal caused by calls in flight 429 with `Retry-After`, a guardrail 403 and rules that cannot be read 503, on the raw routes, their streams, session messages, invoke, test and voice. A stream is committed to its 200 only when a frame that says a model answered is written, so a refusal before that is still a status.
10. **Every route that starts agent work asks the invocation limiter**: besides invoke, streams, session messages and tasks, now `/test`, `/runs/{id}/resume`, all A2A endpoints (recipe steps use them) and `/langchain/invoke` (`TestEveryRouteThatRunsAnAgentAsksTheTrafficLimiter`).

## Consequences

- With nothing installed, behaviour is the community edition's as before, except that a budget refusal no longer opens a provider's circuit (a fix).
- A limiter or cache runs on the call path: both must be quick and never fail a call on their own account (a cache miss is always valid; a limiter fails open or closed as its edition decides).

## Not done

- No community-edition rate limit or cache.
- ~~The invocation limit is not applied to A2A message routes~~ done 2026-11-03 (decision 10). The platform workflow engine's own A2A calls are counted too.
- The voice and realtime meters size a call from bytes, characters and duration (providers bill them in minutes and characters), so their cost is an estimate unless the model is priced; the agent a speech call is for is not attributed (budgets of an agent are not charged for speech).
