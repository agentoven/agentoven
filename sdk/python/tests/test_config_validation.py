"""Configuration reference validation on the client: the 422 the control plane answers is a ConfigurationError, the
validate routes, and ``validate=True`` (the same check, run before anything is sent; never on by default, and the
server stays the authority)."""

import json

import pytest

from agentoven import AgentBuilder, AgentOvenAPIError, AgentOvenClient, ConfigurationError, refcheck
from agentoven.client import _native_call
from agentoven.flow import Flow, agent as flow_agent

LISTS = {
    "/api/v1/models/providers": [
        {"name": "anthropic", "kind": "anthropic", "models": ["claude-haiku-4-5", "claude-sonnet-4-5"]},
        {"name": "flaky", "kind": "openai", "models": ["gpt-4o"], "last_test_healthy": False},
    ],
    "/api/v1/prompts": [],
    "/api/v1/tools": [{"name": "search", "enabled": True, "capabilities": ["tool"]}],
    "/api/v1/skills": {"skills": [{"name": "pdf-reader", "status": "accepted"}]},
    "/api/v1/agents": [{"name": "writer", "status": "ready"}],
    "/api/v1/recipes": [],
    "/api/v1/channels": [],
    "/api/v1/environments": [{"slug": "dev"}, {"slug": "prod"}],
    "/api/v1/credentials": [],
}

PROBLEM = {
    "path": "summary_model", "code": "not_found", "severity": "error", "kind": "model", "value": "claude-hiku-4-5",
    "within": "anthropic", "message": 'summary_model: "claude-hiku-4-5" is not a model of provider "anthropic" (did you mean "claude-haiku-4-5"?)',
    "suggestions": ["claude-haiku-4-5"],
}
REFUSAL = {
    "error": "the agent's configuration is not valid: " + PROBLEM["message"],
    "code": "invalid_configuration",
    "problems": [PROBLEM],
    "warnings": [{"path": "model_provider", "code": "not_usable", "severity": "warning", "kind": "provider", "value": "flaky",
                  "message": 'model_provider: provider "flaky" failed its last connectivity test'}],
}
BAD = {"name": "a1", "model_provider": "anthropic", "model_name": "claude-haiku-4-5",
       "summary_provider": "anthropic", "summary_model": "claude-hiku-4-5"}


@pytest.fixture
def client(api):
    for path, body in LISTS.items():
        api.reply("GET", path, body)
    return AgentOvenClient(url=api.url, api_key="ao_key", kitchen="acme")


def sent(api, verb):
    return [r for r in api.requests if r["method"] == verb]


# ── the 422 ─────────────────────────────────────────────────────────────────


def test_a_422_with_the_configuration_code_is_a_configuration_error(api, client):
    api.reply("POST", "/api/v1/agents", REFUSAL, status=422)
    with pytest.raises(ConfigurationError) as e:
        client.create_agent(BAD)
    err = e.value
    assert isinstance(err, AgentOvenAPIError) and err.status_code == 422
    assert [(p.path, p.value, p.suggestions) for p in err.problems] == [("summary_model", "claude-hiku-4-5", ["claude-haiku-4-5"])]
    assert err.problems[0].kind == "model" and err.problems[0].within == "anthropic" and err.problems[0].blocking
    assert [(w.path, w.blocking) for w in err.warnings] == [("model_provider", False)]
    text = str(err)
    assert REFUSAL["error"] in text
    assert text.count('did you mean "claude-haiku-4-5"') == 2          # the server's sentence, then the problem's line
    assert "warning: model_provider" in text


def test_a_422_of_another_kind_stays_a_plain_api_error(api, client):
    api.reply("POST", "/api/v1/agents", {"error": "name is required"}, status=422)
    with pytest.raises(AgentOvenAPIError) as e:
        client.create_agent({"description": "no name"})
    assert not isinstance(e.value, ConfigurationError)
    api.reply("POST", "/api/v1/agents", {"error": "x", "code": "something_else", "problems": [PROBLEM]}, status=422)
    with pytest.raises(AgentOvenAPIError) as e:
        client.create_agent({"name": "a"})
    assert not isinstance(e.value, ConfigurationError)


def test_a_refusal_with_a_malformed_list_still_raises_the_typed_error(api, client):
    api.reply("POST", "/api/v1/agents", {"error": "bad", "code": "invalid_configuration", "problems": "oops", "warnings": [3, {"path": "x", "code": "c"}]}, status=422)
    with pytest.raises(ConfigurationError) as e:
        client.create_agent({"name": "a"})
    assert e.value.problems == [] and [w.path for w in e.value.warnings] == ["x"]


def test_the_native_clients_refusal_becomes_the_same_error():
    def refused():
        raise RuntimeError("Register failed (422): " + json.dumps(REFUSAL))

    with pytest.raises(ConfigurationError) as e:
        _native_call(refused)
    assert e.value.problems[0].path == "summary_model" and isinstance(e.value.__cause__, RuntimeError)

    for text in ("Register failed (500): boom", "Register failed (422): not json", 'Bake failed (422): {"error": "x"}',
                 "Request failed: error sending request"):
        def other(text=text):
            raise RuntimeError(text)

        with pytest.raises(RuntimeError) as e:
            _native_call(other)
        assert not isinstance(e.value, ConfigurationError) and str(e.value) == text
    assert _native_call(lambda a, b=0: a + b, 1, b=2) == 3


# ── validate=True ───────────────────────────────────────────────────────────


def test_create_agent_sends_what_it_is_given_unless_asked_to_check(api, client):
    api.reply("POST", "/api/v1/agents", {"name": "a1"})
    assert client.create_agent(BAD) == {"name": "a1"}                      # the server is the authority: no check here
    assert client.create_agent(BAD, validate=False) == {"name": "a1"}
    assert [r["body"] for r in sent(api, "POST")] == [BAD, BAD]
    assert sent(api, "GET") == []                                           # not even a list was read


def test_validate_refuses_before_anything_is_sent_with_the_same_error(api, client):
    with pytest.raises(ConfigurationError) as e:
        client.create_agent(BAD, validate=True)
    assert sent(api, "POST") == [] and sent(api, "PUT") == []
    assert [(p.path, p.suggestions) for p in e.value.problems] == [("summary_model", ["claude-haiku-4-5"])]
    assert e.value.status_code == 422
    assert "summary_model" in str(e.value) and "claude-haiku-4-5" in str(e.value)


def test_validate_lets_a_valid_agent_through_and_warnings_do_not_refuse(api, client):
    api.reply("POST", "/api/v1/agents", {"name": "a1"})
    spec = AgentBuilder("a1").model("flaky", "gpt-4o").summary_model("anthropic", "claude-haiku-4-5").skills("pdf-reader").subagents("writer")
    client.create_agent(spec, validate=True)
    body = sent(api, "POST")[0]["body"]
    assert body["summary_provider"] == "anthropic" and body["summary_model"] == "claude-haiku-4-5"
    assert body["skills"] == ["pdf-reader"] and body["subagents"] == ["writer"]


def test_validate_does_not_judge_what_it_could_not_read(api):
    # nothing is scripted: every list answers 404 (this edition has no such route / the key may not list it)
    client = AgentOvenClient(url=api.url, kitchen="acme")
    api.reply("POST", "/api/v1/agents", {"name": "a1"})
    client.create_agent(BAD, validate=True)
    assert len(sent(api, "POST")) == 1
    api.reply("GET", "/api/v1/tools", {"error": "forbidden"}, status=403)
    cat = client.load_catalog()
    assert "tools" in cat.unknown and "agents" in cat.unknown


def test_validate_does_not_swallow_a_server_failure(api, client):
    api.reply("GET", "/api/v1/agents", {"error": "down"}, status=500)
    with pytest.raises(AgentOvenAPIError) as e:
        client.create_agent(BAD, validate=True)
    assert e.value.status_code == 500 and not isinstance(e.value, ConfigurationError)
    assert sent(api, "POST") == []


def test_update_agent_sends_only_the_fields_and_clears_with_empty_values(api, client):
    api.reply("PUT", "/api/v1/agents/a1", {"name": "a1"})
    client.update_agent("a1", {"summary_model": "", "skills": []}, context_budget=2000)
    assert sent(api, "PUT")[0]["body"] == {"summary_model": "", "skills": [], "context_budget": 2000}
    client.update_agent("a1", AgentBuilder("a1").summary_model("anthropic", "claude-haiku-4-5"))
    assert sent(api, "PUT")[1]["body"] == {"name": "a1", "summary_provider": "anthropic", "summary_model": "claude-haiku-4-5"}
    client.update_agent("a1")
    assert sent(api, "PUT")[2]["body"] == {}


def test_update_agent_validate_holds_a_change_to_what_it_causes(api, client):
    # the stored agent already names a skill that is gone: a change that does not touch it is not refused for it
    api.reply("GET", "/api/v1/agents/a1", {"name": "a1", "model_provider": "anthropic", "model_name": "claude-haiku-4-5", "skills": ["old-skill"]})
    api.reply("PUT", "/api/v1/agents/a1", {"name": "a1"})
    client.update_agent("a1", {"description": "new words"}, validate=True)
    assert len(sent(api, "PUT")) == 1
    with pytest.raises(ConfigurationError) as e:
        client.update_agent("a1", {"summary_provider": "anthropic", "summary_model": "claude-hiku-4-5"}, validate=True)
    assert [p.path for p in e.value.problems] == ["summary_model"]       # not skills[0], which it already had
    assert len(sent(api, "PUT")) == 1


def test_update_agent_validate_judges_the_draft_of_a_baked_agent(api, client):
    api.reply("GET", "/api/v1/agents/a1", {"name": "a1", "model_provider": "anthropic", "model_name": "claude-haiku-4-5",
                                            "draft": {"summary_provider": "anthropic", "summary_model": "claude-hiku-4-5"}})
    api.reply("PUT", "/api/v1/agents/a1", {"name": "a1"})
    # the draft's bad summary model is already in what the agent is being built into: fixing it is allowed
    client.update_agent("a1", {"summary_model": "claude-haiku-4-5"}, validate=True)
    assert len(sent(api, "PUT")) == 1


def test_register_validate_refuses_before_the_native_client_is_called(api, client):
    class NativeMustNotRun:
        def register(self, agent):
            raise AssertionError("sent")

    client._native = NativeMustNotRun()
    from agentoven import Agent, Ingredient

    agent = Agent("a1", model_provider="anthropic", model_name="claude-hiku-4-5", ingredients=[Ingredient.tool("serch")])
    with pytest.raises(ConfigurationError) as e:
        client.register(agent, validate=True)
    assert {(p.path, p.value) for p in e.value.problems} == {("model_name", "claude-hiku-4-5"), ("ingredients[0].name", "serch")}


def test_register_validate_passes_a_good_agent_to_the_native_client(api, client):
    from agentoven import Agent, Ingredient

    calls = []

    class Native:
        def register(self, agent):
            calls.append(agent.name)
            return "ok"

    client._native = Native()
    assert client.register(Agent("a1", model_provider="anthropic", model_name="claude-haiku-4-5", ingredients=[Ingredient.tool("search")]), validate=True) == "ok"
    assert calls == ["a1"]


def test_register_maps_the_native_422(api, client):
    from agentoven import Agent

    class Native:
        def register(self, agent):
            raise RuntimeError("Register failed (422): " + json.dumps(REFUSAL))

    client._native = Native()
    with pytest.raises(ConfigurationError) as e:
        client.register(Agent("a1"))
    assert e.value.problems[0].suggestions == ["claude-haiku-4-5"]


def test_bake_validate_judges_the_stored_agent_with_its_draft_at_bake_strictness(api, client):
    calls = []

    class Native:
        def bake(self, target, version=None, environment=None, input=None):
            calls.append(target)
            return "baked"

    client._native = Native()
    api.reply("GET", "/api/v1/agents/empty", {"name": "empty", "mode": "managed"})
    with pytest.raises(ConfigurationError) as e:
        client.bake("empty", validate=True)
    assert [(p.path, p.code) for p in e.value.problems] == [("model_provider", "required")] and calls == []
    assert "cannot be baked" in str(e.value)

    api.reply("GET", "/api/v1/agents/drafted", {"name": "drafted", "mode": "managed", "draft": {"model_provider": "anthropic", "model_name": "claude-haiku-4-5"}})
    assert client.bake("drafted", validate=True) == "baked"
    api.reply("GET", "/api/v1/agents/ghost", {"error": "not found"}, status=404)
    assert client.bake("ghost", validate=True) == "baked"       # a missing agent is the server's 404 to give
    api.reply("GET", "/api/v1/agents/hidden", {"error": "forbidden"}, status=403)
    assert client.bake("hidden", validate=True) == "baked"      # one this key may not read is left to the server too
    assert client.bake("empty") == "baked"                       # off by default
    assert calls == ["drafted", "ghost", "hidden", "empty"]


# ── recipes ─────────────────────────────────────────────────────────────────


def test_create_recipe_validate_refuses_an_agent_that_is_not_there(api, client):
    api.reply("POST", "/api/v1/recipes", {"name": "f"})
    flow = Flow("f", "", [flow_agent("Write", "writr")])
    with pytest.raises(ConfigurationError) as e:
        client.create_recipe(flow, validate=True)
    assert sent(api, "POST") == []
    assert [(p.path, p.suggestions) for p in e.value.problems] == [("steps[0].agent_ref", ["writer"])]
    assert "the recipe is not valid" in str(e.value)
    client.create_recipe(flow)                                 # the server decides when asked to
    assert len(sent(api, "POST")) == 1
    client.create_recipe(Flow("g", "", [flow_agent("Write", "writer")]), validate=True)
    assert len(sent(api, "POST")) == 2


def test_update_recipe_validate_ignores_what_the_recipe_already_had(api, client):
    api.reply("PUT", "/api/v1/recipes/f", {"name": "f"})
    api.reply("GET", "/api/v1/recipes/f", {"name": "f", "steps": [{"name": "write", "kind": "agent", "agent_ref": "retired-long-ago"}]})
    flow = Flow("f", "same step", [flow_agent("Write", "retired-long-ago")])
    client.update_recipe(flow, validate=True)
    assert len(sent(api, "PUT")) == 1
    with pytest.raises(ConfigurationError):
        client.update_recipe(Flow("f", "", [flow_agent("Write", "writr")]), validate=True)
    assert len(sent(api, "PUT")) == 1


def test_run_recipe_validate_checks_the_environment_and_the_steps(api, client):
    api.reply("GET", "/api/v1/recipes/r1", {"name": "r1", "steps": [{"name": "a", "kind": "agent", "agent_ref": "writer"}]})
    api.reply("POST", "/api/v1/recipes/r1/bake", {"run_id": "run1", "status": "running"})
    with pytest.raises(ConfigurationError) as e:
        client.run_recipe("r1", {"x": 1}, environment="prd", validate=True)
    assert [(p.path, p.suggestions) for p in e.value.problems] == [("environment", ["prod"])]
    assert sent(api, "POST") == []
    assert client.run_recipe("r1", {"x": 1}, environment="prod", validate=True)["run_id"] == "run1"
    assert client.run_recipe("r1", {"x": 1}, environment="prd")["run_id"] == "run1"    # not asked: the server decides


def test_run_recipe_422_is_a_configuration_error(api, client):
    api.reply("POST", "/api/v1/recipes/r1/bake", {"error": "the recipe cannot run: it is not valid", "code": "invalid_configuration",
                                                  "problems": [{"path": "environment", "code": "not_found", "severity": "error", "message": "environment: \"prd\" is not an environment of this kitchen"}],
                                                  "warnings": []}, status=422)
    with pytest.raises(ConfigurationError) as e:
        client.run_recipe("r1", environment="prd")
    assert e.value.problems[0].path == "environment"


def test_bake_recipe_validate_reads_the_stored_recipe(api, client):
    api.reply("GET", "/api/v1/recipes/r1", {"name": "r1", "steps": [{"name": "n", "kind": "notify", "config": {"channels": ["ops-slak"]}}]})

    class Native:
        def bake_recipe(self, name, input=None):
            raise AssertionError("sent")

    client._native = Native()
    with pytest.raises(ConfigurationError) as e:
        client.bake_recipe("r1", validate=True)
    assert [p.path for p in e.value.problems] == ["steps[0].config.channels[0]"]


# ── the validate routes and the pure check through the client ──────────────


def test_agent_validate_and_recipe_validate_read_the_servers_verdict(api, client):
    verdict = {"agent": "a1", "valid": False, "problems": [PROBLEM], "warnings": []}
    api.reply("GET", "/api/v1/agents/a1/validate", verdict)
    api.reply("GET", "/api/v1/recipes/r%2F1/validate", {"recipe": "r/1", "valid": True, "problems": [], "warnings": []})
    assert client.agent_validate("a1") == verdict
    assert client.recipe_validate("r/1")["valid"] is True
    assert api.requests[-1]["path"] == "/api/v1/recipes/r%2F1/validate"     # a name is one path segment


def test_agent_draft_diff_reads_what_the_next_bake_would_change(api, client):
    diff = {"agent": "a 1", "has_draft": True, "baked": True, "baked_version": "0.1.0", "changes_behaviour": True,
            "changes": [{"field": "max_turns", "kind": "changed", "before": 5, "after": 9}]}
    api.reply("GET", "/api/v1/agents/a%201/draft/diff", diff)
    assert client.agent_draft_diff("a 1") == diff
    assert api.requests[-1]["path"] == "/api/v1/agents/a%201/draft/diff"


def test_validate_agent_returns_errors_and_warnings_together(api, client):
    got = client.validate_agent({"name": "a1", "model_provider": "flaky", "model_name": "gpt-4o", "summary_model": "gpt-4oo", "summary_provider": "flaky"})
    assert [(p.path, p.severity) for p in got] == [("model_provider", "warning"), ("summary_provider", "warning"), ("summary_model", "error")]
    assert [p.path for p in refcheck.blocking(got)] == ["summary_model"]
    assert client.validate_agent({"name": "a"}, bake=True)[0].code == "required"
    cat = client.load_catalog()
    n = len(api.requests)
    client.validate_agent(BAD, catalog=cat)
    client.validate_recipe({"name": "r", "steps": []}, catalog=cat, environment="nope")
    assert len(api.requests) == n                                            # a catalog passed in is reused, not read again


def test_load_catalog_reads_each_list_once(api, client):
    client.load_catalog()
    paths = [r["path"] for r in api.requests]
    assert sorted(paths) == sorted(list(LISTS) + ["/api/v1/scenarios"])
    assert all(r["method"] == "GET" for r in api.requests)
    assert all(r["headers"].get("authorization") == "Bearer ao_key" and r["headers"].get("x-kitchen") == "acme" for r in api.requests)
