# agentoven

Python SDK for [AgentOven](https://agentoven.dev) — the enterprise agent control plane.

Built with native Rust performance via [PyO3](https://pyo3.rs).

## Install

```bash
pip install agentoven
```

## Quick Start

```python
from agentoven import AgentOvenClient, Agent

# Connect to the control plane
client = AgentOvenClient(url="http://localhost:8080")

# Register an agent
agent = Agent(name="my-assistant", description="A helpful AI assistant")
result = client.register_agent(agent)
print(result)

# List all agents
agents = client.list_agents()
for a in agents:
    print(a)

# Deploy an agent
client.bake("my-assistant")

# Pause an agent
client.cool("my-assistant")
```

## Features

- **Native performance** — Rust core compiled to a Python extension
- **A2A protocol** — built on the Agent-to-Agent open standard
- **Multi-framework** — works with LangGraph, CrewAI, AutoGen, and more
- **Kitchen isolation** — workspace-based multi-tenancy

## License

MIT


## Recipes

Build a recipe the way the dashboard's recipe builder does, check it, create it, run it and answer its approvals.
The steps written are the same as the dashboard writes, so the recipe opens there too. Plain Python: no native module is needed to build one.

```python
from agentoven import AgentOvenClient, Flow
from agentoven.flow import agent, ask, check, decide, mentions, together

flow = Flow("refund-review", "Triage a ticket, ask a person about refunds", [
    agent("Triage", "triage-agent"),
    decide("Needs a person?", mentions("refund", "chargeback"),
        yes=[ask("Approve the refund", within="1 day",
                 approved=[agent("Issue the refund", "refunder")],
                 rejected=[agent("Explain the decision", "writer")])],
        no=[together([agent("Reply", "writer")], [check("Tone check", "reviewer")])]),
    agent("Log the outcome", "logger"),
])

client = AgentOvenClient("http://localhost:8080", api_key="...")
client.create_recipe(flow, check_agents=True)          # a FlowError lists every problem first
run = client.run_recipe("refund-review", {"text": "I want a refund"})
state = client.wait_for_recipe_run("refund-review", run["run_id"])   # returns at a gate, or when finished
if state["status"] == "paused":
    client.reject_gate(run["run_id"], state["pending_gates"][0], "The tone is too casual")
```

Builders: `agent`, `check`, `lookup`, `decide`, `ask`, `together`, `wait`, `notify`, `sub_recipe`, `each`, and `mentions` for a condition.
A `notify` step pushes to a live-feed channel that `client.listen("channel")` reads (`pip install "agentoven[realtime]"`).
Also `Flow.add`, `Flow.validate`, `Flow.to_wire`, `update_recipe`, `delete_recipe`, `list_recipe_runs` and `approve_gate`.

## Realtime voice (Pro)

```python
# pip install "agentoven[realtime]"
async with client.realtime("support-agent", voice="marin") as call:
    await call.send_audio(pcm_chunk)      # PCM16, 24 kHz, mono
    async for event in call:
        if event.type == "audio":
            speaker.write(event.audio)    # raw PCM16 bytes
        elif event.type == "transcript" and event.final:
            print(event.role, event.text)
```

The agent always runs on its own provider, which must offer the `realtime`
modality. If the agent's input guardrails block what the caller said, you get an
`error` event and then iterating raises `RealtimeError` (close code 1008) with the
reason; `call.close_code` / `call.close_reason` are set once the call ends.

Browsers cannot set headers on a WebSocket: see "Realtime in a browser" below.


## Modalities

A *modality* is something an agent can take in or give out beyond text: `image`,
`pdf`, `video`, `audio` (cascaded voice: speech-to-text in, text-to-speech out)
and `realtime` (live speech-to-speech). Modalities belong to the **provider**: an
agent has exactly the modalities of its own provider. Text is always on and has
no entry. You do not have to set anything: defaults come from the provider
driver and the model catalog.

### Enable or disable modalities on a provider

```python
from agentoven import AgentOvenClient, Modalities, AudioConfig, RealtimeConfig, CLEAR

client = AgentOvenClient(url="http://localhost:8080", api_key="...")

client.create_provider(
    "openai", "openai", api_key="sk-...", models=["gpt-4o"],
    modalities=Modalities(
        pdf=False,                                    # switch PDF input off
        video=True,                                   # widen for a gateway the catalog doesn't know
        audio=AudioConfig(stt_model="whisper-1", tts_model="tts-1"),
        realtime=RealtimeConfig(enabled=True, model="gpt-realtime"),
    ),
)

# Updates are merged per modality. CLEAR deletes a key (or a whole modality)
# and puts it back on its default.
client.update_provider("openai", modalities=Modalities(pdf=CLEAR, audio=AudioConfig(tts_model="tts-1-hd")))

provider = client.get_provider("openai")
print(provider.modalities)          # effective: ['text', 'image', 'audio', ...]
print(provider.supports("audio"))
```

The same rules the server enforces are checked before the request is sent
(unknown modality, an entry for `text`, unknown settings, wrong types) and raise
`ModalitiesError`. `audio` and `realtime` cannot be enabled beyond what the
provider kind supports; the server answers 400 (`AgentOvenAPIError`).

### Check what an agent can do

```python
card = client.agent_card("support-agent")
print(card.modalities)              # e.g. ['text', 'image', 'audio', 'realtime']
if card.supports("audio"):
    ...
client.supports("support-agent", "pdf")   # shorthand
```

### Send an image or PDF

```python
from agentoven import Attachment

session = client.create_agent_session("vision-agent")
reply = client.send_session_message(
    "vision-agent", session["id"], "Summarise these.",
    attachments=[
        Attachment.from_path("chart.png"),                       # MIME type inferred
        Attachment.from_path("report.pdf"),
        Attachment.from_url("https://example.com/photo.jpg"),
        Attachment.from_bytes(raw_bytes, mime_type="image/webp"),
    ],
)
print(reply["content"])
```

Audio and video attachments work the same way. Audio is transcribed by the
server first, so any agent whose provider offers `audio` can take it.

### Cascaded voice

```python
from agentoven import AudioModalityError

try:
    result = client.invoke(
        "support-agent",
        audio="question.wav",        # path, Path, bytes, or AudioInput.from_base64(...)
        voice_output=True,
        voice="alloy",
    )
except AudioModalityError:
    print("this agent's provider does not offer the audio modality")
else:
    print(result.transcript)         # what the agent heard
    print(result.response)           # its text reply
    result.save_audio("answer")      # decoded bytes in result.audio.data -> answer.mp3
```

### Realtime (live speech-to-speech)

Server-side, with your API key (needs `pip install "agentoven[realtime]"`):

```python
async with client.realtime("support-agent", voice="marin") as call:
    await call.send_audio(pcm_chunk)
    async for event in call:
        ...
```

### Realtime in a browser

Browsers cannot set headers on a WebSocket. Your backend mints a short-lived
token (bound to the agent and kitchen, checked only when the socket opens) and
gives it to the page:

```python
minted = client.realtime_token("support-agent", ttl_seconds=60)   # 1-300
return {"token": minted.token, "expires_in": minted.expires_in}   # your endpoint
```

The page opens the socket with the WebSocket subprotocols
`["agentoven.v1", "agentoven-token.<token>"]` (the TypeScript SDK does this with
`connectRealtime({ token })`). From Python the same flow, with no auth headers:

```python
async with AgentOvenClient(url="http://localhost:8080").realtime("support-agent", token=minted.token) as call:
    ...
```

## Roles

`restaurateur` (platform-wide, a role of a person in the user directory) and, inside a kitchen, `head_chef`, `chef`,
`baker`, `taster`, `auditor`, `legal`, `finance` and `viewer` (`KITCHEN_ROLES`, `PERSON_ROLES`). The retired `admin` is
refused wherever a role is given (`create_user`, `create_service_account`, `create_kitchen_binding`,
`set_approver_policy`): it was a Head Chef inside a kitchen and a Restaurateur across the installation.

## Agent versions

An agent's version moves only on a bake whose configuration differs from the last bake's. The first bake keeps the
registered version (`0.1.0`), a bake that changes nothing keeps its version, a changed one takes the next minor
version and keeps the old one, and editing a registered agent does not bump it.

```python
client.list_agent_versions("support-agent")          # [{"version": "0.1.0", ...}, {"version": "0.2.0", ...}]
client.get_agent_version("support-agent", "0.1.0")   # the agent as it was when that version was baked
```

## Signed decisions (Pro)

A decision at a gate is signed: you prove who you are again and get a single-use token bound to exactly that
decision, sent in the `X-Signing-Token` header (`SIGNING_TOKEN_HEADER`). `sign` does both steps with the password of
your local account; an account that signs in through an identity provider gets `OidcSigningRequired` (sign in the
dashboard instead). Every method that takes a decision accepts `password=` (it signs for you) or `signing_token=`.

```python
token = client.sign("legal_hold_release", "dataset:ds-1", "approved", {"reason": "case closed"}, password)
client.lift_dataset_legal_hold("ds-1", "case closed", signing_token=token)    # or: password=password

client.run_recipe("refunds", {"order": 7}, environment="prod", password=password)   # gate approval_skip
client.signed_request("POST", "/api/v1/promotions", body, signing_token=token)      # any other gate
```

Two gates came with the roles: `legal_hold_release` (a dataset's or the audit trail's legal hold, record id
`dataset:<id>` or `audit:<kitchen>`, payload `{"reason"}`) and `approval_skip` (running a recipe on an environment that
requires approval, record id `<recipe>@<environment>`, payload `{"input"}`). Also: `list_signatures`,
`get_signature`, `verify_signatures`, `get_approver_policy`, `set_approver_policy`.

## Replay (Pro)

```python
out = client.replay_call(trace_id, span_id, [{"label": "cooler", "edits": {"temperature": 0.1}}], reason="why")
run = client.replay_run(trace_id, from_span, {"model": "claude-haiku"})                 # tools frozen
run = client.replay_run(trace_id, from_span, tools="live", live_tools=["lookup"], password=password)   # signed
capture = client.get_span_capture(trace_id, span_id)
```

A replay is an investigation, never evidence: models are not deterministic, and no simulated tool result is ever
made up. `prepare_live_replay` returns what a live replay signs, computed by the server.

## Governed datasets (Pro)

`list_datasets`, `create_dataset`, `get_dataset`, `update_dataset`, `add_dataset_case`, `add_dataset_case_from_trace`,
`list_dataset_cases`, `retire_dataset_case`, `erase_dataset_case`, `publish_dataset`, `list_dataset_versions`,
`get_dataset_version`, `verify_dataset_version`, `export_dataset`, `lower_dataset_minimum`,
`place_dataset_legal_hold`, `lift_dataset_legal_hold`, `purge_dataset`, `list_expiring_datasets`,
`list_dataset_reviews`, `list_dataset_tombstones`, and `place_audit_legal_hold` / `lift_audit_legal_hold`. On a
regulated dataset publishing, purging and lowering the minimum are signed (`password=` or `signing_token=`).

## Identity-provider bindings and credential owners (Pro)

`list_kitchen_bindings`, `create_kitchen_binding(kitchen_id, claim_value, role)`, `delete_kitchen_binding`,
`set_service_account_owner`, `set_scoped_key_owner`, `my_kitchens`. A service account or scoped key belongs to a person
(`owner_email=` when it is created); when the owner leaves the kitchen it stops until a Head Chef gives it to someone.

## Budgets, rate limits and the response cache (Pro)

```python
client.set_budget(500, period="monthly")                                    # the kitchen, dollars
client.set_budget(2_000_000, unit="tokens", agent="triage", period="daily")  # one agent (or provider=, api_key=)
client.set_budget(800, period="monthly", password=pw)                        # a raise is a signed decision (gate budget_raise)
client.list_budgets(); client.list_budget_ledger(); client.list_budget_alerts(); client.budget_usage("agent", days=7)
client.remove_budget(agent="triage", unit="tokens", password=pw)

client.set_limit("agent", "triage", requests_per_minute=60, tokens_per_minute=200_000)   # "*" for each on its own
client.list_limits(); client.remove_limit("agent", "triage")

client.set_cache_policy("always", agent="faq-bot", ttl_seconds=900)
client.get_cache(); client.purge_cache("faq-bot"); client.remove_cache_policy("faq-bot")
```

`RateLimitedError` (HTTP 429: `retry_after` from `Retry-After`, `scope`, `limit`) and `BudgetExceededError` (HTTP 402:
`scope`, `unit`, `period`, `resets_at`) are `AgentOvenAPIError`s; the SDK does not retry for you. Lowering a budget or
creating one needs no signature; raising or removing one needs `password=` or `signing_token=` (record id from
`budget_record_id`). See [docs/budgets-limits-cache.md](../../docs/budgets-limits-cache.md).


## Checking a configuration against the kitchen

An agent names a provider, a model, a summarizer (compaction) model, skills, tools, prompts and subagents; a recipe
names agents, steps, channels, credentials, environments. The control plane refuses a write or a bake that names
something the kitchen does not have, with HTTP 422 and the list of every problem, each with the field, the bad value
and the nearest valid ones. The SDK raises it as `ConfigurationError` (an `AgentOvenAPIError`) with `.problems` and
`.warnings` (lists of `agentoven.refcheck.Problem`: `path`, `code`, `severity`, `kind`, `value`, `within`,
`suggestions`).

```python
from agentoven import AgentBuilder, AgentOvenClient, ConfigurationError

spec = (AgentBuilder("support-bot")
        .model("anthropic", "claude-sonnet-4-5")
        .summary_model("anthropic", "claude-hiku-4-5")      # the compaction model; a typo
        .skills("pdf-reader"))
try:
    client.create_agent(spec, validate=True)                # checked here first; nothing is sent when it fails
except ConfigurationError as e:
    for p in e.problems:
        print(p.path, p.value, p.suggestions)               # summary_model claude-hiku-4-5 ['claude-haiku-4-5']
```

`validate=True` (on `register`, `create_agent`, `update_agent`, `bake`, `create_recipe`, `update_recipe`, `run_recipe`,
`bake_recipe`) runs the same rules before sending, against the lists the client reads (`client.load_catalog()`). It is
off by default and the server stays the authority: it checks again whatever the client did. A list you may not read
(403, 404, a Pro-only route) is left unchecked, never treated as empty; scenarios are checked only where the server
lists them, and recipe approver roles are not checked from the SDK. A warning (a disabled optional tool, a provider
whose last test failed, a model id an OpenRouter, LiteLLM or Ollama provider does not list) never refuses a write.
No authentication is needed beyond what the server already asks for.

Without sending anything: `client.validate_agent(agent_or_dict_or_builder)` and `client.validate_recipe(recipe)`
return the problems (errors and warnings; `agentoven.refcheck.split` separates them), `client.agent_validate(name)`
and `client.recipe_validate(name)` ask the server about a stored agent or recipe as its next bake would judge it, and
`agentoven.refcheck.check_agent` / `check_recipe` / `nearest` are the pure functions (they run the cases in
`sdk/fixtures/refcheck-cases.json`, as the Go implementation, the TypeScript SDK and the Rust client do).

## Timeouts, redirects and the route contract

A request gives up after 120 seconds (an invocation waits for a model): `AgentOvenClient(timeout=30)`, or
`AGENTOVEN_TIMEOUT=30` in the environment. A redirect is refused with an `AgentOvenAPIError` that names the new
location (Python's urllib would send your API key to it); use that URL as the server URL. Ids and names are
escaped as one path segment, so a name such as `../../x` cannot choose another route.

`register` / `register_agent` brings a retired agent (one that was deleted) back with the same name and id: it
creates when the agent is absent or retired and updates when it is live. There is no `delete_world_schema`: the
server has no such route.

`tests/test_route_contract.py` reads every call the client makes and checks its verb and path against
`sdk/fixtures/server-routes.json`, the server's route list generated from its router (Pro repository:
`UPDATE_SDK_ROUTES=1 go test ./internal/routeguard -run SDKRoute`). A method for a route the server does not have
fails the build.
