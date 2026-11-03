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

## What the next bake would change

`GET /api/v1/agents/{name}/draft/diff` compares the agent as it was baked with the agent as the draft will leave it
(`Agent.WithDraft`). It changes nothing and needs the same permission as reading the agent (`agent:read` in Pro); an agent
of another kitchen is a 404.

```
GET /api/v1/agents/support/draft/diff
{ "agent": "support", "has_draft": true, "baked": true, "baked_version": "0.1.0", "changes_behaviour": true,
  "changes": [
    { "field": "max_turns", "kind": "changed", "before": 10, "after": 20 },
    { "field": "skills", "kind": "added", "before": null, "after": "web" },
    { "field": "guardrails", "key": "g1", "kind": "changed", "details": [ { "field": "enabled", "kind": "changed", "before": true, "after": false } ], ... },
    { "field": "ingredients", "key": "tool:search", "kind": "removed", "before": { ... }, "after": null } ] }
```

- `kind` is `added`, `changed` or `removed`. A field is named by its JSON path (`bot.max_actions`). Lists are diffed by element:
  guardrails by `id`, ingredients by `kind:name` (the `key`), skills, approval tools, subagents and a bot's domains by name; a
  changed guardrail or ingredient lists the fields inside it in `details`.
- Secrets in an ingredient's or a rule's config come back masked (`****`) on both sides, the same masking `GET /agents/{name}`
  uses. A changed secret is a `changed` whose two sides read the same; whether it changed is decided on the real values.
- `changes_behaviour` says whether the draft moves the behaviour fingerprint, which is what makes the bake write a new version.
  It judges configuration fields only: a bake also re-resolves prompts and models.
- An agent with no draft (or one nothing was baked for) answers `has_draft: false` and `"changes": []`. A draft that sets what
  is already baked has `has_draft: true` and no changes.
- Clients: Python `client.agent_draft_diff(name)`, TypeScript `pro.agentDraftDiff(name)`, Rust `Client::agent_draft_diff(name)`;
  the Pro dashboard shows it as "Unbaked changes" on the agent page.

## Two requests at once

Every agent carries a `revision`, which goes up with each write to it. An edit, a bake and a re-cook that run at the same
moment do not overwrite each other: the one that lost the race reads the agent again and applies its change to what is stored
now, so the edit is either applied or waiting in the draft, and the bake has happened. If you want your edit refused when the
agent has changed since you read it, send the revision you read, as `"revision": 7` in the body or as `If-Match: "7"`:

```
PUT /api/v1/agents/support  If-Match: "7"  {"description": "..."}   -> 409 {"current_revision": 9, "current_version": "0.2.0"}
```

A 409 from a bake or edit without a revision means the agent kept changing under it for five attempts in a row: read it and
try again.

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

## Clearing a field, and retired agents

A `PUT` stays partial: a field you leave out stays as it is. To clear an optional field, send JSON `null` for it:
`{"backup_provider": null, "backup_model": null}`. The optional fields are `description`, `framework`, `backup_provider`,
`backup_model`, `execution_mode`, `max_turns`, `bot`, `ingredients`, `context_budget`, `reasoning_strategy`,
`summary_provider` and `summary_model`. `null` for `model_provider`, `model_name`, `mode`, `behavior` or `runtime` is a
`400`: an agent cannot run without them. For the lists (`skills`, `guardrails`, `approval_tools`, `subagents`) `null` still
means "no opinion", and `[]` clears. On a baked agent the clear waits in the draft like any edit.

A retired agent (deleted with `DELETE /api/v1/agents/{name}`) refuses an edit, a bake, a re-cook, a cool and a rewarm with
`409`; register it again with `POST /api/v1/agents` to bring it back.
