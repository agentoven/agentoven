"""The reference checks (agentoven.refcheck) behave as the control plane's pkg/refcheck does.

sdk/fixtures/refcheck-cases.json holds the cases (catalogs, agent and recipe inputs, the problems expected); the Go
test, this one and the other SDKs' tests run the same file, so the implementations cannot drift apart. A problem is
identified by (path, code, severity, kind, value, within) compared as a sorted multiset; suggestions are compared
where a case lists them.
"""

import json
from pathlib import Path

import pytest

from agentoven import refcheck
from agentoven.agents import AgentBuilder, agent_to_wire, recipe_to_wire
from agentoven.refcheck import Catalog, Problem

FIXTURE = Path(__file__).resolve().parents[2] / "fixtures" / "refcheck-cases.json"
DATA = json.loads(FIXTURE.read_text())
CATALOGS = {name: Catalog.from_dict(c) for name, c in DATA["catalogs"].items()}


def _ids():
    return [c["name"] for c in DATA["cases"]]


def test_the_fixture_is_the_one_the_other_implementations_run():
    # A case file that shrank or lost a kind would pass trivially: hold it to what the Go reference pins.
    kinds = {c["kind"] for c in DATA["cases"]}
    assert kinds == {"agent", "recipe", "nearest"}
    assert len(DATA["cases"]) >= 71


@pytest.mark.parametrize("case", DATA["cases"], ids=_ids())
def test_fixture_case(case):
    if case["kind"] == "nearest":
        assert refcheck.nearest(case["value"], case["candidates"], 3) == case["expect"]
        return
    opts = case.get("options") or {}
    catalog = CATALOGS[case["catalog"]]
    check = refcheck.check_agent if case["kind"] == "agent" else refcheck.check_recipe
    got = check(case["input"], catalog, bake=bool(opts.get("bake")), environment=opts.get("environment", ""))
    want = [Problem.from_dict(p) for p in case["expect"]]
    assert sorted(p.key() for p in got) == sorted(p.key() for p in want), "\n".join(p.message for p in got)
    by_key = {p.key(): p for p in got}
    for raw, w in zip(case["expect"], want):
        if "suggestions" in raw:
            assert by_key[w.key()].suggestions == w.suggestions, w.path
    for p in got:
        assert p.message, f"{p.path} has no message"


# ── What the fixture cannot say: the SDK's own surface ──────────────────────


def test_a_problem_round_trips_through_the_servers_json():
    wire = {"path": "summary_model", "code": "not_found", "severity": "error", "kind": "model", "value": "x",
            "within": "anthropic", "message": "m", "suggestions": ["y"]}
    p = Problem.from_dict(wire)
    assert p.to_dict() == wire
    assert p.blocking
    assert not Problem("a", "unlisted", severity="warning").blocking


def test_split_and_introduced():
    old = Problem("a", "not_found", kind="tool", value="x")
    moved = Problem("b", "not_found", kind="tool", value="x")  # the same problem, elsewhere in the list
    new = Problem("c", "not_found", kind="tool", value="y")
    warn = Problem("d", "unlisted", severity="warning")
    errs, warns = refcheck.split([old, warn, new])
    assert errs == [old, new] and warns == [warn]
    assert refcheck.introduced([old], [moved, new, warn]) == [new]
    assert refcheck.introduced([old], [moved, moved]) == [moved]  # held once, not twice


def test_a_catalog_part_that_could_not_be_read_is_not_checked_against_nothing():
    cat = Catalog.from_dict(DATA["catalogs"]["base"])
    cat.unknown = frozenset({"tools", "skills", "agents", "providers", "recipes"})
    agent = {"name": "a", "model_provider": "ghost", "model_name": "m", "skills": ["nope"], "subagents": ["nobody"],
             "approval_tools": ["nothing"], "ingredients": [{"kind": "tool", "name": "ghost", "required": True}]}
    assert refcheck.check_agent(agent, cat) == []
    recipe = {"name": "r", "steps": [{"name": "a", "kind": "sub_recipe", "recipe_ref": "missing"}]}
    assert refcheck.check_recipe(recipe, cat) == []
    # the parts that were read are still checked
    cat.unknown = frozenset({"tools"})
    got = refcheck.check_agent(agent, cat)
    assert {p.path for p in got} == {"model_provider", "skills[0]", "subagents[0]"}


def test_unknown_credentials_and_environments_are_skipped_too():
    cat = Catalog.from_dict(DATA["catalogs"]["base"])
    cat.unknown = frozenset({"credentials", "environments", "channels"})
    recipe = {"name": "r", "default_environment": "nowhere",
              "steps": [{"name": "a", "kind": "notify", "auth_key_ref": "nokey", "config": {"channels": ["nochannel"]}}]}
    assert refcheck.check_recipe(recipe, cat, environment="nowhere") == []


def test_wrong_types_at_the_top_level_are_problems_not_crashes():
    cat = CATALOGS["base"]
    got = refcheck.check_agent({"name": "a", "model_name": 5, "skills": "pdf-reader", "max_turns": "ten",
                                "ingredients": ["tool"], "subagents": ["writer", 3]}, cat)
    assert {(p.path, p.code) for p in got} >= {
        ("model_name", "wrong_type"), ("skills", "wrong_type"), ("max_turns", "wrong_type"),
        ("ingredients[0]", "wrong_type"), ("subagents[1]", "wrong_type"),
    }
    got = refcheck.check_recipe({"name": "r", "steps": [3, {"name": "a", "kind": "agent", "agent_ref": "writer", "depends_on": "x"}]}, cat)
    assert {(p.path, p.code) for p in got} >= {("steps[0]", "wrong_type"), ("steps[1].depends_on", "wrong_type")}


def test_nan_and_booleans_are_not_numbers():
    cat = CATALOGS["base"]
    got = refcheck.check_agent({"name": "a", "max_turns": True}, cat)
    assert [(p.path, p.code) for p in got] == [("max_turns", "wrong_type")]
    got = refcheck.check_agent({"name": "a", "ingredients": [{"kind": "retriever", "name": "r", "config": {"top_k": float("nan")}}]}, cat)
    assert [(p.path, p.code) for p in got] == [("ingredients[0].config.top_k", "wrong_type")]


def test_messages_name_the_field_the_value_and_the_nearest():
    got = refcheck.check_agent({"name": "a", "model_provider": "anthropic", "model_name": "claude-haiku-4-5",
                                "summary_provider": "anthropic", "summary_model": "claude-hiku-4-5"}, CATALOGS["base"])
    assert got[0].message == 'summary_model: "claude-hiku-4-5" is not a model of provider "anthropic" (did you mean "claude-haiku-4-5"?)'
    assert "summary_model" in refcheck.format_problems(got)


def test_apply_edit_and_with_draft_follow_the_servers_rules():
    stored = {"name": "a", "model_name": "m1", "summary_model": "s", "subagents": ["x"], "max_turns": 5,
              "draft": {"model_name": "m2", "summary_model": "", "subagents": [], "max_turns": 0, "description": ""}}
    got = refcheck.with_draft(stored)
    assert got["model_name"] == "m2"
    assert got["summary_model"] == ""      # a string that is present clears
    assert got["subagents"] == []          # a list that is present clears
    assert got["max_turns"] == 5           # a zero leaves it
    assert "draft" not in got and "draft" in stored
    after = refcheck.apply_edit(stored, {"skills": [], "approval_tools": ["t"]})
    assert after["approval_tools"] == ["t"] and after["skills"] == []   # an empty skills list clears them, as on the server
    assert refcheck.with_draft({"name": "plain"}) == {"name": "plain"}


class _FakeClient:
    """Answers the lists load_catalog reads; a status for a path makes that read fail like the API would."""

    def __init__(self, lists, failures=None):
        self.lists = lists
        self.failures = failures or {}
        self.paths = []

    def _get(self, path):
        from agentoven.client import AgentOvenAPIError

        self.paths.append(path)
        if path in self.failures:
            raise AgentOvenAPIError(self.failures[path], {"error": "no"})
        return self.lists.get(path, [])


LISTS = {
    "/api/v1/models/providers": [{"name": "anthropic", "kind": "anthropic", "models": ["claude-haiku-4-5"], "last_test_healthy": False},
                                 {"name": "router", "kind": "openrouter", "models": None}],
    "/api/v1/prompts": [{"name": "triage", "version": 1}, {"name": "triage", "version": 3}, {"name": "other"}],
    "/api/v1/tools": [{"name": "search", "enabled": True, "capabilities": ["tool"]}, {"name": "old", "enabled": False}],
    "/api/v1/skills": {"skills": [{"name": "pdf-reader", "status": "accepted"}]},
    "/api/v1/agents": [{"name": "writer", "status": "ready"}, {"name": "gone", "status": "retired"}],
    "/api/v1/recipes": [{"name": "nightly"}, {"name": "dead", "archived_at": "2026-01-01T00:00:00Z"}],
    "/api/v1/channels": [{"name": "ops"}],
    "/api/v1/environments": [{"slug": "prod", "name": "Production"}],
    "/api/v1/credentials": [{"name": "partner-key"}],
    "/api/v1/scenarios": [{"id": "refund-flow"}],
}


def test_load_catalog_reads_the_lists_the_api_serves():
    cat = refcheck.load_catalog(_FakeClient(LISTS))
    assert [(p.name, p.kind, p.models, p.healthy) for p in cat.providers] == [
        ("anthropic", "anthropic", ["claude-haiku-4-5"], False), ("router", "openrouter", [], None)]
    assert cat.prompts == {"triage": 3, "other": 1}
    assert [(t.name, t.enabled, t.capabilities) for t in cat.tools] == [("search", True, ["tool"]), ("old", False, ["tool"])]
    assert [(s.name, s.status) for s in cat.skills] == [("pdf-reader", "accepted")]
    assert [(a.name, a.status) for a in cat.agents] == [("writer", "ready"), ("gone", "retired")]
    assert cat.recipes == ["nightly"]          # an archived recipe cannot run
    assert cat.channels == ["ops"] and cat.environments == ["prod"] and cat.credentials == ["partner-key"]
    assert cat.scenarios.known and cat.scenarios.names == ["refund-flow"]
    assert not cat.roles.known                 # there is no route that lists roles
    assert cat.unknown == frozenset()


@pytest.mark.parametrize("status", [403, 404])
def test_a_list_the_caller_may_not_read_is_unknown_not_empty(status):
    cat = refcheck.load_catalog(_FakeClient(LISTS, {"/api/v1/tools": status, "/api/v1/scenarios": status, "/api/v1/credentials": status}))
    assert cat.unknown == frozenset({"tools", "credentials"})
    assert cat.tools == [] and cat.credentials == []
    assert not cat.scenarios.known           # scenarios are Pro only: a 404 means "not checked"
    assert [a.name for a in cat.agents] == ["writer", "gone"]


def test_a_failure_that_is_not_an_answer_is_raised_not_read_as_nothing():
    from agentoven.client import AgentOvenAPIError

    with pytest.raises(AgentOvenAPIError) as e:
        refcheck.load_catalog(_FakeClient(LISTS, {"/api/v1/agents": 500}))
    assert e.value.status_code == 500
    with pytest.raises(AgentOvenAPIError):
        refcheck.load_catalog(_FakeClient(LISTS, {"/api/v1/tools": 401}))


def test_the_wire_shape_of_the_native_classes_is_what_the_checks_read():
    from agentoven import Agent, Branch, Ingredient, Recipe, Step

    agent = Agent("a", model_provider="openai", model_name="gpt-4o", system_prompt="Be kind.",
                  ingredients=[Ingredient.model("gpt-4o", provider="openai"), Ingredient.tool("serch", protocol="mcp")])
    wire = agent_to_wire(agent)
    assert wire["ingredients"][0]["config"] == {"provider": "openai", "model": "gpt-4o"}
    assert wire["ingredients"][1]["config"] == {"protocol": "mcp"}
    assert wire["ingredients"][2]["config"] == {"text": "Be kind."}
    got = refcheck.check_agent(agent, CATALOGS["base"])
    assert [(p.path, p.value, p.suggestions) for p in got] == [("ingredients[1].name", "serch", ["search"])]

    recipe = Recipe("r", steps=[Step("a", agent="writr"), Step("g", human_gate=True, depends_on=["a"], notify=["mailr"]),
                                Step("t", kind="router", agent="writer", branches=[Branch("x", "zz")], default_next="a")])
    assert recipe_to_wire(recipe)["steps"][1]["kind"] == "human_gate"
    got = refcheck.check_recipe(recipe, CATALOGS["base"])
    assert {(p.path, p.value) for p in got} == {("steps[0].agent_ref", "writr"), ("steps[1].notify_tools[0]", "mailr"), ("steps[2].branches[0].next_step", "zz")}


def test_the_builder_sets_the_compaction_model_and_the_check_reads_it():
    spec = (AgentBuilder("a1").model("anthropic", "claude-haiku-4-5").summary_model("anthropic", "claude-hiku-4-5")
            .backup_model("openai", "gpt-4o").skills("pdf-reader").approval_tools("search").subagents("writer")
            .context_budget(4000).reasoning_strategy("react"))
    wire = spec.to_wire()
    assert wire["summary_provider"] == "anthropic" and wire["summary_model"] == "claude-hiku-4-5"
    assert wire["skills"] == ["pdf-reader"] and wire["subagents"] == ["writer"] and wire["context_budget"] == 4000
    got = refcheck.check_agent(spec, CATALOGS["base"])
    assert [(p.path, p.suggestions) for p in got] == [("summary_model", ["claude-haiku-4-5"])]
    assert agent_to_wire(wire) == wire and agent_to_wire(wire) is not wire


def test_the_builder_converts_native_ingredients_with_the_agents_model():
    from agentoven import Ingredient

    wire = AgentBuilder("a").model("openai", "gpt-4o").ingredient(Ingredient.model("gpt-4o")).ingredient({"kind": "skill", "name": "pdf-reader"}).to_wire()
    assert wire["ingredients"][0]["config"] == {"provider": "openai", "model": "gpt-4o"}
    assert wire["ingredients"][1] == {"kind": "skill", "name": "pdf-reader"}
    assert "ingredients" not in AgentBuilder("b").to_wire()


def test_a_flow_is_judged_as_written():
    from agentoven.flow import Flow, agent

    flow = Flow("f", "", [agent("Write", "writr")])
    got = refcheck.check_recipe(flow, CATALOGS["base"])
    assert [(p.path, p.code, p.suggestions) for p in got] == [("steps[0].agent_ref", "not_found", ["writer"])]
