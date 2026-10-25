# Long-running agents and spend caps

An agent that works for hours makes hundreds of model calls, and a run that goes wrong (a retry
loop, a tool that never makes progress, a context that only grows) goes wrong at that scale. This
page covers what AgentOven does about it for agents that run on its own loop. Agents hosted from a
framework (LangChain, LangGraph, Deep Agents) call their model themselves; see
[Hosting Deep Agents](deep-agents.md) for what can and cannot be capped there.

## Budgets

A budget limits one run. Every field is optional; zero means no limit.

```json
{ "max_cost_usd": 5, "max_tokens": 1000000, "max_active_seconds": 7200, "max_turns": 200 }
```

Set it on the agent (`budget`). A task request can make it smaller with its own `budget`, and so can code
embedding the executor (`agentexec.WithBudget`); nothing but a person resuming a paused run can make it
larger. Time counts only while the run is working: time spent paused or down is not counted.

Before each model call the executor checks the budget, counting what the next call is expected to
cost, and clamps the call's `max_tokens` to what is left, so a run overshoots by at most the prompt
it already sent. At 50% and 80% of a limit it raises a `budget_warning` event. At the limit it
**checkpoints and pauses**; it does not throw the work away.

A cost limit is only as good as the price behind it. A model with no known price (set
`cost_per_1k_input` and `cost_per_1k_output` on the provider) is refused for a dollar cap with a 422;
a token cap works for any model and is exact.

### At a limit

A synchronous `invoke` answers **402**:

```json
{ "error": "run_budget_exhausted", "status": "paused", "trace_id": "…",
  "dimension": "cost", "spent": 5.02, "cap": 5, "resume": "POST /api/v1/agents/{agent}/runs/{id}/resume" }
```

A person decides:

```bash
# raise the cap and carry on
curl -X POST …/api/v1/agents/worker/runs/$ID/resume -d '{"approved":true,"extend":{"add_cost_usd":5,"reason":"worth it"}}'
# or end the run
curl -X POST …/api/v1/agents/worker/runs/$ID/resume -d '{"approved":false,"note":"enough"}'
```

An extension carries an ID, so repeating it (a double click, a retry after a crash) is applied once, and
only one of several concurrent resumes runs. Raising a cap is recorded in the audit log
(`run.budget_extended`, with who and why), as is the pause (`run.budget_paused`). In Pro, raising a cap
needs the `budget:extend` permission.

### Subagents

A subagent a run delegates to is charged to its parent as it spends, out of a share reserved for it
(half of what is left by default, `child_share` to change it); parallel children are reserved from one
reading of what is left, so they cannot oversubscribe it. A subagent cannot pause, because its parent is
in the middle of a tool call: it stops and its parent is told.

## Background tasks

`POST /api/v1/tasks` starts an agent that keeps running after the request returns.

```bash
curl -X POST …/api/v1/tasks -d '{"agent":"worker","message":"audit last quarter\'s invoices","budget":{"max_cost_usd":2}}'
# 202 {"id":"…","status":"queued","links":{…},"budget":{…}}
```

| | |
|---|---|
| `GET /api/v1/tasks/{id}` | status, turns, usage, budget, the result once it finishes |
| `GET /api/v1/tasks/{id}/events` | server-sent events, each with a sequence ID; reconnect with `Last-Event-ID` to carry on |
| `POST /api/v1/tasks/{id}/cancel` | stop it |
| `POST /api/v1/tasks/{id}/resume` | continue a paused (budget, tool approval, repeated call) or interrupted task |

Statuses: `queued`, `running`, `paused`, `completed`, `max_turns`, `failed`, `cancelled`,
`budget_exhausted`, `stalled`, `interrupted`, `abandoned`.

- A task always has limits. Whatever the agent and request leave unset takes the defaults ($5 if the
  model is priced, 1,000,000 tokens, 2 hours, 200 turns), and nothing exceeds the ceilings ($50, 20M
  tokens, 24 hours, 2,000 turns). Set `AGENTOVEN_RUN_BUDGET_*` (defaults) and `AGENTOVEN_RUN_BUDGET_MAX_*`
  (ceilings) to change them.
- Guardrails apply as they do to a request: the input is checked before the task starts, and the event
  stream and the result pass the output rules (a blocked result is withheld, `result_blocked: true`).
- At most 4 tasks run at once per kitchen and 16 overall; up to 64 more wait. Past that the answer is
  429 with `Retry-After`.
- A task is a journaled run, so it survives a restart. It is then `interrupted` and a person resumes it,
  or set `AGENTOVEN_TASK_RECOVERY=auto` and it resumes itself with backoff, up to 3 times. A call that was
  running when the process died is repeated only if its tool is marked idempotent; otherwise the model is
  told its outcome is unknown.
- Tasks are single-node: the registry, the queue and the event history are in the process (the journal,
  and so each task's state, is on its disk). Agents that run their own loop (framework-native or
  process-backed) are refused with a 422, because their spend cannot be capped.

Over A2A, `tasks/send` with `"configuration": {"blocking": false}` returns a task at once; `tasks/get` and
`tasks/cancel` work on it. A paused or interrupted task is `input-required`.

## Keeping a long run on course

For background tasks the loop also:

- cuts a tool result to 32KB, keeping its start and end, and says what it left out;
- at 80% of the context budget replaces the older history with a summary (never separating a tool call
  from its results; the summarizing call is charged to the run and journaled);
- keeps a pinned `agentoven_notes` memory (goal, plan, progress, decisions) in front of the model on every
  turn, so a summarized or resumed run still knows what it was doing;
- warns the model when it repeats a call, and pauses the run when it repeats it five times
  (`AGENTOVEN_STALL_LIMIT`), for a person to send on with guidance or end;
- repeats a transient model error (rate limit, overload) a couple of times with growing delays before it
  leaves the run for a person.
