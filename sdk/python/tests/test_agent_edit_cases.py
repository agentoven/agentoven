"""apply_edit lays an edit over an agent the way the control plane does (sdk/fixtures/agent-edit-cases.json is run by
the Go models test, this one, the TypeScript SDK and the Rust client)."""

import json
from pathlib import Path

import pytest

from agentoven.refcheck import apply_edit

DATA = json.loads((Path(__file__).resolve().parents[2] / "fixtures" / "agent-edit-cases.json").read_text())


def test_the_fixture_is_the_one_the_other_implementations_run():
    assert len(DATA["cases"]) >= 9


@pytest.mark.parametrize("case", DATA["cases"], ids=[c["name"] for c in DATA["cases"]])
def test_edit_case(case):
    agent = json.loads(json.dumps(case["agent"]))
    edit = json.loads(json.dumps(case["edit"]))
    assert apply_edit(agent, edit) == case["result"]
    assert agent == case["agent"] and edit == case["edit"], "the inputs must not change"
