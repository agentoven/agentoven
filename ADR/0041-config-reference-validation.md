# ADR-0041: Configuration names are checked against the kitchen (dropdowns, a server validator, SDK pre-checks)

**Status:** Accepted (built 2026-11-03)
**Date:** 2026-11-03
**Scope:** OSS control plane: `pkg/refcheck` (the checks), `pkg/contracts/references.go` (`KitchenReferences`), `internal/resolver/catalog.go` (`LoadCatalog`), `internal/api/handlers/config_refs.go` and the create, update, bake and re-cook paths of agents and recipes; `pkg/models` (`Agent.SummaryProvider`, a wider `AgentEdit`); the dashboard, the Python and TypeScript SDKs and the Rust client and CLI.
**Pro side:** [Pro ADR-0055](../../agentoven-pro/ADR/0055-config-reference-validation.md) (scenario and role lists, the permissions, the Postgres tests).

## Context

An agent's configuration is full of names that must exist in the kitchen: its provider and model, the model that
summarizes its history, tools, prompts, skills, subagents, the channels, credentials and approver roles of a recipe.
Every one of them was a free-text field in the dashboard and a bare string in the SDKs. A typo (`claude-hiku-4-5`,
`pdf-reeder`) was accepted on save and failed at the first bake or run, as a sentence the resolver had assembled.
The owner asked for dropdowns that offer only what exists and for a validator in the SDKs "so they will not give random
names". A dropdown and an SDK check are conveniences: a script with `curl` bypasses both, so the enforcement has to be on
the server.

## Decisions

1. **One pure checker, `pkg/refcheck`.** `CheckAgent`, `CheckRecipe` and `Decode` read a configuration against a
   `Catalog` (a snapshot of what one kitchen has: providers with their models and last test, prompts with their latest
   version, tools with `enabled` and capabilities, skills with status, agents with status, recipes, channels,
   environments, kitchen credentials; scenarios and roles when the edition can list them) and return every problem, not
   the first. A `Problem` is `{path, code, severity, kind, value, within, message, suggestions}`; the path is the JSON
   the API takes (`summary_model`, `ingredients[2].config.provider`, `steps[1].agent_ref`). Codes: `not_found`,
   `not_usable` (disabled tool, unapproved skill, retired agent), `unlisted`, `unknown_field`, `wrong_type`,
   `out_of_range`, `duplicate`, `required`, `invalid_value`. Only `error` refuses; `warning` informs.
2. **What is covered.** Agent: `model_provider`/`model_name`, `backup_*`, `summary_provider`/`summary_model` (new),
   `skills[]`, `approval_tools[]`, `subagents[]` (not itself, not retired), `bot.tools[]`, every ingredient kind
   (model provider and model, tool, observability, prompt and pinned version, skill, embedding provider, vector store
   backend and index, retriever references to the agent's own embedding and store, scenario with rollouts and pass rate,
   data uri), guardrail kind, stage and action (enabled rules), the fixed-value settings, numbers, duplicates (the same
   ingredient twice, two model or prompt ingredients), the name as a URL part. Recipe: `agent_ref`/`agent` (and that
   they agree), `depends_on`, `branches[].next_step`, `default_next`, `question_from`, circles, `recipe_ref`,
   `notify_tools` (the `notify` capability), `config.channels`, `auth_key_ref`, `approver_roles`, approver e-mails and
   domain, `default_environment` and the environment of a run, loops, retries, timeouts, duplicate step names.
3. **Did you mean.** `refcheck.Nearest`: optimal-string-alignment distance (a swap is one edit) on the text and on the
   text without punctuation; close within a quarter of the length (1 to 3), or when one contains the other; at most three,
   nearest first. The Python, TypeScript and Rust ports run the same cases (`sdk/fixtures/refcheck-cases.json`, 71 cases),
   as the Go test does, so the four cannot drift.
4. **Models and open providers.** A model is valid when it is in the provider's list. Kinds `openrouter`, `litellm` and
   `ollama` serve models their list does not name: an unlisted id is accepted with the warning `unlisted`; a provider with
   an empty list accepts any id (as bakes always did). The dashboard offers a labelled "enter a custom model id" only
   after the provider has been chosen and only for such a provider.
5. **Where it runs.** On write (`POST /agents`, `PUT /agents/{name}`, `POST /recipes`, `PUT /recipes/{name}`) and at the
   moment of running (`/bake`, `/recook`, `POST /recipes/{name}/bake`), always against the caller's own kitchen: the
   catalog is read through the kitchen-scoped store, so another kitchen's resource is "does not exist" and never
   appears among the suggestions. The answer is **422** `{"error", "code": "invalid_configuration", "problems": [...],
   "warnings": [...]}`; a bake used to answer 400 with a text. `resolver` stays as the second line for what only
   resolving can tell; the invocation paths that resolve lazily do not check, so nothing that already runs stops.
6. **The draft is checked when it is saved.** The edit is applied to the agent as its next bake would see it
   (`WithDraft`) and that result is checked. An edit is held only to what it makes worse: a problem the agent already
   had (same code, kind and value) is not a reason to refuse an edit that leaves it (`refcheck.Introduced`), so an agent
   saved before the checks existed can still be edited and fixed. A bake takes no such leave.
7. **Existing invalid agents keep running; they are listed.** `GET /api/v1/agents/{name}/validate` and
   `GET /api/v1/recipes/{name}/validate` return `{valid, problems, warnings}` for the configuration as the next bake would
   judge it. The dashboard shows it as a badge on the agent and recipe pages.
8. **Unknown fields and wrong types are named.** Bodies are decoded with `refcheck.Decode`: a field with no such name
   (`summry_model`) is `unknown_field` with the field it was probably meant for, a string where a number belongs is
   `wrong_type`. An update accepts the fields of the agent that it ignores (a client that sends the agent back whole is
   not refused); a name that is on neither list is the typo. A body that is not JSON stays a 400.
9. **`summary_provider`, and the settings PUT dropped.** `Agent.SummaryProvider` pins the summarizer to the provider the
   owner chose (the executor sets `PinnedProvider`); without it the router picks a provider that lists the model, as
   before. Since an edit became a draft, `PUT` ignored `context_budget`, `reasoning_strategy`, `summary_model`,
   `approval_tools` and `subagents`: they are now part of `AgentEdit`; `skills`, `approval_tools` and `subagents` follow the
   guardrails' rule (absent is unchanged, `[]` clears) and the summary settings take `""` to clear.
10. **Edition seam.** `contracts.KitchenReferences` (`Scenarios`, `Roles`) is the only thing an edition supplies; the
    community edition supplies none, so those references are not checked (never read as "empty"). No authentication is
    added or required anywhere: the checks run with authentication off exactly as with it on.
11. **SDKs and CLI.** Python, TypeScript and Rust have `load_catalog`, `validate_agent` / `validate_recipe` (client side,
    the same checks), `agent_validate` / `recipe_validate` (the new routes), `validate=True` (`--validate`) on create,
    update, bake and run, a `ConfigurationError` carrying `.problems` for any 422 `invalid_configuration`, and the CLI
    `agentoven agent validate` and `agentoven recipe validate`. The client check is off unless asked, and the server
    decides regardless.
12. **A sub-recipe step must name a recipe that exists** (it was allowed to name one not yet made, with the failure left
    for the run). A recipe nesting itself or closing a circle is still refused by `checkSubRecipeRefs`.

## Consequences

- A wrong name is refused at the earliest moment with the nearest right ones; the dropdowns make the common path
  impossible to get wrong, the server makes the rest impossible to save.
- Clients that created agents with names that do not exist (tests, scripts) now get 422 and must create the provider,
  tool or skill first. The in-repo scripts and tests were adjusted; `kitchentest` carried a step field (`notify`) that
  never existed.
- One more kitchen read (about nine list calls) per write and per bake. The bake path reads it before it reads the
  agent, so it does not widen the window in which a concurrent write makes the bake stale.

## Not done

- `PUT` cannot clear `backup_provider`/`backup_model` (the edit still treats `""` as unchanged), and still ignores
  `budget`, `output_schema`, `entrypoint`, `repo_url` and `repo_branch` as before.
- Free-form keys inside an ingredient's `config` are not checked (only the keys the resolver reads); an embedding
  model id is not checked (the server has no list of a provider's embedding models); bot `credentials` are secret
  references of any kind and are not looked up; a RAG ingredient's external provider name is free.
- Agents created by Pro's file import and by the evaluation installer are written to the store directly; their bake
  checks them.
- The kitchen's guardrail rules (workspace policy) are validated by their own code, not by `refcheck`.
- The roles list of a community kitchen is unknown, so `approver_roles` is not checked there; the SDKs cannot read the role list
  of Pro either (no route), so they leave `approver_roles` to the server.
