# ADR-0034: The approval observer and expirer seams (OSS hooks for Pro approvals where people work)

**Status:** Accepted  
**Date:** 2026-11-03  
**Authors:** AgentOven Engineering  
**Scope:** OSS (two small, generic seams, one public path prefix and one fix). Delivery, reminders, escalation, links and chat callbacks are Pro ([agentoven-pro ADR-0050](https://github.com/agentoven/agentoven-pro)).

> The number 0033 was reserved for this record and used by the route guard seam in the meantime; this is the next free one.

---

## Context

A person is asked to decide something in four places: a recipe's human gate, an agent's tool call that needs approval, a run stopped at its budget, a run repeating itself. All four are reached only by opening the dashboard. Nothing tells the people who can decide, nothing chases them, and a decision nobody makes waits for ever (or, for a gate with `max_wait_minutes`, fails the step with no one told).

The control plane cannot decide who to tell (that is roles, an approver policy, channels with drivers for Slack and Teams) and must not make decisions go away on its own. It only has to say *when a person was asked*, and be able to end what nobody answered as a refusal.

## Decision

1. **`contracts.ApprovalObserver`** (`ApprovalRequested(ctx, ApprovalRequest{Kitchen, Gate, RecordID, Deadline})`), installed with `Handlers.SetApprovalObserver`. It is called when a recipe gate starts waiting (`Engine.observeGate`, after the approval record is stored) and when a run pauses for a tool approval, a budget or a stall (`Executor.pausedFor`, after the pause is journaled, and only for a run with a journal, since only that can be resumed). The request names the gate and the record the gate's `DecisionDescriber` reads; it carries nothing about what is decided, so a message made from it can only say what the signing sheet says. The call is synchronous, panic-safe and cannot fail the approval. Without an observer a recipe gate behaves as before, including `DispatchAll` of `gate_waiting` to every channel; with one, the observer owns the channels and the event goes only to the step's MCP tools.
2. **`contracts.ApprovalExpirer`** (`ExpireApproval(ctx, kitchen, gate, recordID, reason)`), implemented by `Handlers`. It is a refusal and nothing else:
   - a recipe gate is recorded `timed_out` (`Engine.ExpireGate`); the waiting step, which polls its record, takes the recipe's `on_timeout: branch` path or fails; it is never approved;
   - a paused run is claimed through the journal the way a person's decision is (`ClaimResume`) and ended `cancelled` with the reason (`Executor.ExpirePause`, then the live task, if any, is brought into line with `TaskManager.Cancel`). Exactly one of a late approval and the expiry wins, on any replica; a crash between the claim and the ending leaves the run "claimed", which the existing recovery refuses to resume automatically.
   It returns false, changing nothing, when the request is no longer pending, is in another kitchen, or is of another gate.
3. **Engine: a gate whose record was set to `timed_out` from outside follows the timeout path** (it used to read any non-pending status as approved-or-rejected). The decide-or-expire step of a gate is serialised in the process (`decideMu`); across replicas the window is one store round trip (see Not done).
4. **A public path prefix `/hooks/chat/`.** Chat platforms call back without an AgentOven credential. Paths under it skip the authentication middleware; each endpoint there must verify the platform's signature before it reads anything else. Nothing is mounted there in the community edition.
5. **Fix: notification retries resend the body.** `WebhookChannelDriver` and the MCP notify call reused one `http.Request` for their three attempts, so a retry after a failure posted an empty body and reported success if the receiver accepted it. Each retry now rewinds the body. (Pro's Slack, Teams, Discord and Zapier drivers had the same fault and are fixed in ADR-0050.)

## Alternatives considered

- **Poll for pending approvals from Pro.** Gates and promotions are rows, but a paused run is a journal and has no row Pro could query; a hook is the only place all four are known the moment they happen.
- **A `Resolved` hook as well.** Not needed: Pro re-reads the decision's describer before it sends anything and closes a request that is no longer waiting. A resolved hook would add three call sites for latency the describer already covers.
- **Expire by calling `Resume` with a rejection.** Rejecting a tool approval lets the agent carry on with a denial and spend more; ending the run is what "nobody answered" should mean. A rejection also is a human decision, which a timeout is not.

## Consequences

- The community edition changes in two ways only: the retry fix, and a gate that is expired (which nothing in the community edition does).
- A new gate kind that waits for a person must call the observer where it creates its record, and implement a `DecisionDescriber`, to be chased.

## Not done

- Bot actions, skill reviews, dataset gates, legal-hold release and approval skip are not announced (Pro ADR-0050 lists them).
- The compare-and-set of a gate's record is in the engine, not the store: two replicas deciding and expiring the same gate in the same instant could both write. A store method `UpdateApprovalIf(status)` would close it.
- Recipe gates are still held in the process's memory (existing limitation): a restart orphans a waiting gate, and a decision on it changes only its record.
