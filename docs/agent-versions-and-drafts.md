# Agent versions, drafts and who can read trace content

## An agent runs what was baked

`PUT /api/v1/agents/{name}` on an agent that has been baked does not change what the agent does. The edit is kept in the
agent's `draft` (shown by `GET /api/v1/agents/{name}`), and the next bake or re-cook applies all of it, resolves the
ingredients and, if the configuration differs from the stored version, takes the next minor version. The version a trace names
is therefore the configuration that served the call. An agent that was never baked has nothing running and takes an edit at
once. Tags are labels and always apply at once.

```
PUT  /api/v1/agents/support {"guardrails": [], "max_turns": 20}   -> 200, "draft": {...}; the agent still runs 0.1.0 as baked
POST /api/v1/agents/support/recook {}                                -> 202; the draft is applied and the version becomes 0.2.0
```

## What counts as a change

Everything about an agent that decides what it says or does: the resolved prompt, tools, model and backup, guardrails, turn
and context limits, skills, runtime and mode, the bot's allowlists, the description (it is the system prompt when there is no
prompt ingredient), framework, entrypoint and repository, approval tools, subagents, output schema, summary model and
reasoning strategy. Status, timestamps, tags, endpoints and the running process do not count (`models.BakedFingerprint`).

## Naming a version

`{"version": "1.2.0"}` on a bake or re-cook: it must be `MAJOR.MINOR.PATCH` (400 otherwise), and it must be higher than every
stored version (409 otherwise). Naming the current version works only when the configuration is the one stored under it.
Leave it out and the bake names the version.

## Content a role without `trace:read:content` does not receive

Timings, status, usage, cost, model and provider names stay. Prompts, answers, thinking, span payloads, event attributes, error
text, a score's comment, a session's messages, a task's result, error and pending tool arguments are not in the response, and
`content_withheld` is `true`. This holds on every route that returns them (traces, spans, timeline, scores, sessions, tasks,
and in Pro the judgment ledger and dataset cases); `GET /tasks/{id}/events` and `POST /langfuse/export` answer 403.
