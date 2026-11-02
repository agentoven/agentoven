"""Guardrail rules with actions and the test route (Pro, ADR-0049): the routes and bodies."""

import pytest

from agentoven import AgentOvenClient


@pytest.fixture
def client(api):
    return AgentOvenClient(url=api.url, api_key="ao_key", kitchen="acme")


def test_a_rule_is_created_with_its_action_and_stage(api, client):
    api.reply("POST", "/api/v1/guardrails", {"id": "g1", "action": "redact"}, status=201)
    rule = {"name": "pii", "kind": "pii_detection", "stage": "all", "action": "redact", "config": {"patterns": ["pii"]}}
    assert client.create_guardrail(rule)["action"] == "redact"
    assert api.last["body"] == rule
    assert api.last["headers"]["x-kitchen"] == "acme"


def test_update_and_delete_use_the_rules_id(api, client):
    api.reply("PUT", "/api/v1/guardrails/g1", {"id": "g1", "action": "warn"})
    client.update_guardrail("g1", {"action": "warn"})
    assert api.last["body"] == {"action": "warn"}
    api.reply("DELETE", "/api/v1/guardrails/g1", {})
    client.delete_guardrail("g1")
    assert api.last["method"] == "DELETE" and api.last["path"] == "/api/v1/guardrails/g1"


def test_toggle_flips_with_patch_or_sets_when_told(api, client):
    api.reply("PATCH", "/api/v1/guardrails/g1/toggle", {"enabled": False})
    client.toggle_guardrail("g1")
    assert api.last["method"] == "PATCH" and api.last["path"] == "/api/v1/guardrails/g1/toggle"
    api.reply("PUT", "/api/v1/guardrails/g1", {"enabled": True})
    client.toggle_guardrail("g1", True)
    assert api.last["method"] == "PUT" and api.last["body"] == {"enabled": True}


def test_get_guardrail_finds_the_rule_in_the_list(api, client):
    api.reply("GET", "/api/v1/guardrails", [{"id": "g1", "name": "a"}, {"id": "g2", "name": "b"}])
    assert client.get_guardrail("g2")["name"] == "b"
    with pytest.raises(LookupError):
        client.get_guardrail("nope")


def test_kinds_lists_actions_and_detectors(api, client):
    api.reply("GET", "/api/v1/guardrails/kinds", {"actions": ["block", "redact"], "detectors": ["iban"]})
    assert client.guardrail_kinds()["detectors"] == ["iban"]


def test_testing_a_text_sends_only_what_was_given(api, client):
    api.reply("POST", "/api/v1/guardrails/test", {"outcome": "redacted", "text": "pay [IBAN]"})
    out = client.test_guardrails("pay DE89370400440532013000", stage="output")
    assert out["text"] == "pay [IBAN]"
    assert api.last["body"] == {"text": "pay DE89370400440532013000", "stage": "output"}
    rules = [{"kind": "pii_detection", "action": "mask", "config": {"patterns": ["credit_card"]}}]
    client.test_guardrails("x", rules=rules, include_custom=True)
    assert api.last["body"] == {"text": "x", "stage": "input", "rules": rules, "include_custom": True}


def test_exceptions_are_removed_by_their_own_id(api, client):
    api.reply("POST", "/api/v1/guardrails/g1/exceptions", {"id": "e1"}, status=201)
    client.add_guardrail_exception("g1", "support-bot", "migration", "2026-12-01T00:00:00Z")
    assert api.last["body"] == {"agent_name": "support-bot", "reason": "migration", "expires_at": "2026-12-01T00:00:00Z"}
    api.reply("DELETE", "/api/v1/guardrails/g1/exceptions/e1", {})
    client.remove_guardrail_exception("g1", "e1")
    assert api.last["path"] == "/api/v1/guardrails/g1/exceptions/e1"
