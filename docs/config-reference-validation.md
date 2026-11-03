# Configuration names are checked against your kitchen

An agent or a recipe names things: the provider and model it runs on, the model that summarizes its history, tools,
prompts, skills, other agents, notification channels, credentials, approver roles. AgentOven checks every one of those names
against what your kitchen has, when you save and when you bake, and tells you what is wrong and what you probably meant.

## In the dashboard

Every field that names something that must exist is a dropdown of what exists, with its state (a disabled tool, a skill that is
not approved, a retired agent, a provider whose last test failed). If a saved value names something that is gone, it shows as a
red "not found" chip with the closest real names to click; the form will not save until you replace or remove it.

For models you choose the provider first, then one of that provider's models. Only a provider that serves models outside its
list (OpenRouter, LiteLLM, Ollama, or a provider registered without a list) offers "Enter a custom model id".

The agent page and the recipe page show **Configuration: valid** or **N problems**, with the list.

## From the API

A create, an update (the draft), a bake, a re-cook or a recipe run that names something the kitchen does not have is refused
with `422` and every problem at once:

```
POST /api/v1/agents  {"name": "support", "model_provider": "anthropic", "model_name": "claude-haiku-4-5",
                      "summary_provider": "anthropic", "summary_model": "claude-hiku-4-5", "skills": ["pdf-reeder"]}

422 {"code": "invalid_configuration", "error": "...", "problems": [
  {"path": "summary_model", "code": "not_found", "severity": "error", "kind": "model", "value": "claude-hiku-4-5",
   "within": "anthropic", "suggestions": ["claude-haiku-4-5"],
   "message": "summary_model: \"claude-hiku-4-5\" is not a model of provider \"anthropic\" (did you mean \"claude-haiku-4-5\"?)"},
  {"path": "skills[0]", "code": "not_found", "kind": "skill", "value": "pdf-reeder", "suggestions": ["pdf-reader"], ...}],
 "warnings": []}
```

`path` is the field in the JSON you sent. Codes: `not_found`, `not_usable` (exists but disabled, not approved, retired),
`unknown_field` (`summry_model`), `wrong_type`, `out_of_range`, `duplicate`, `required`, `invalid_value`, and `unlisted` (a warning:
a model id an open provider does not list). Only errors refuse; warnings do not.

Scoping: a provider, tool or agent of another kitchen does not exist for you, and is never offered as a suggestion.

### Agents that already hold a bad name

Nothing that is running stops. Validation applies when you write. An edit is refused only for what it makes worse: an agent
saved before the checks, holding a skill that has since been deleted, can still have its description edited. A bake refuses it.
To see what is wrong now:

```
GET /api/v1/agents/support/validate   -> {"agent": "support", "valid": false, "problems": [...], "warnings": [...]}
GET /api/v1/recipes/nightly/validate
```

### Compaction model

`summary_model` is the model that summarizes a long conversation; `summary_provider` pins it to one provider (leave it out and
any provider that lists the model is used). `PUT` takes `""` for both to go back to the agent's own model. `PUT` also takes
`context_budget`, `reasoning_strategy`, `approval_tools` and `subagents` (`[]` clears a list).

## SDKs and CLI

The server is the authority; the clients can check first, before sending, with the same rules and the same suggestions.

```python
from agentoven import AgentOvenClient, ConfigurationError
c = AgentOvenClient(...)
problems = c.validate_agent({"name": "support", "model_provider": "anthropic", "model_name": "claude-hiku-4-5"})
try:
    c.create_agent(cfg, validate=True)     # checks first; default is off
except ConfigurationError as e:
    for p in e.problems: print(p.path, p.value, p.suggestions)
```

TypeScript: `client.validateAgent(cfg)`, `createAgent(cfg, { validate: true })`, `ConfigurationError`. Rust: `client.validate_agent`,
`with_validation(true)`, `ConfigurationError`. CLI:

```
agentoven agent validate support          # what the server finds now; exit 1 on any error
agentoven agent register ... --validate   # check before sending
agentoven recipe validate nightly
```

A 422 from the server is shown as a list (field, bad value, nearest valid values) in the CLI and raised as `ConfigurationError`
in the SDKs, whether or not you asked for the client check.

See [ADR-0041](../ADR/0041-config-reference-validation.md).
