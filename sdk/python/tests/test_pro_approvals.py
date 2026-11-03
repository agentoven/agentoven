"""Approvals where people work (Pro, ADR-0050): what is waiting, who was told, and the replies that decide nothing."""

import pytest

from agentoven import AgentOvenClient

RID = "0f1e2d3c-aaaa-bbbb-cccc-0123456789ab"


@pytest.fixture
def client(api):
    return AgentOvenClient(url=api.url, api_key="ao_key", kitchen="acme")


def test_requests_are_listed_by_status_gate_and_record(api, client):
    api.reply("GET", "/api/v1/approval-requests?status=open", {"requests": [{"id": RID, "gate": "tool_approval", "delivered": 3}]})
    assert client.list_approval_requests()[0]["delivered"] == 3
    api.reply("GET", "/api/v1/approval-requests?status=all&gate=recipe_gate&record_id=run%201%3Astep%2F2", {"requests": []})
    assert client.list_approval_requests("all", "recipe_gate", "run 1:step/2") == []
    with pytest.raises(ValueError):
        client.list_approval_requests("everything")


def test_notify_status_is_the_latest_request_with_its_timeline_or_none(api, client):
    api.reply("GET", "/api/v1/approval-requests?status=all&gate=budget_extension&record_id=t1", {"requests": [{"id": RID}]})
    api.reply("GET", f"/api/v1/approval-requests/{RID}", {"request": {"id": RID}, "events": [{"kind": "notified", "channel": "ops"}], "chain": []})
    got = client.approval_notify_status("budget_extension", "t1")
    assert got["events"][0]["channel"] == "ops"
    api.reply("GET", "/api/v1/approval-requests?status=all&gate=budget_extension&record_id=never", {"requests": []})
    assert client.approval_notify_status("budget_extension", "never") is None


def test_the_chat_replies_are_posts_to_the_request_and_return_it(api, client):
    for action in ("claim", "unclaim", "decline"):
        api.reply("POST", f"/api/v1/approval-requests/{RID}/{action}", {"request": {"id": RID, "claimed_by": "ana"}})
        assert getattr(client, f"{action}_approval_request")(RID)["id"] == RID
        assert api.last["method"] == "POST" and api.last["path"] == f"/api/v1/approval-requests/{RID}/{action}"
    api.reply("POST", f"/api/v1/approval-requests/{RID}/snooze", {"request": {"id": RID}})
    client.snooze_approval_request(RID, 30)
    assert api.last["body"] == {"minutes": 30}


def test_a_snooze_is_bounded_before_it_is_sent(api, client):
    for bad in (0, -5, 1441):
        with pytest.raises(ValueError):
            client.snooze_approval_request(RID, bad)
    assert api.requests == []


def test_the_request_id_is_escaped_in_the_path(api, client):
    api.reply("POST", "/api/v1/approval-requests/..%2Fadmin/claim", {"request": {}})
    client.claim_approval_request("../admin")
    assert api.last["path"] == "/api/v1/approval-requests/..%2Fadmin/claim"


def test_the_escalation_rules_are_read_and_replaced(api, client):
    api.reply("GET", "/api/v1/approval-escalation", {"policy": {}, "default": {"max_reminders": 3}, "gates": ["tool_approval"], "min_interval_seconds": 60})
    assert client.get_approval_escalation()["min_interval_seconds"] == 60
    api.reply("PUT", "/api/v1/approval-escalation", {"policy": {"*": {}}})
    client.set_approval_escalation({"*": {"remind_after_seconds": 3600, "max_reminders": 2}})
    assert api.last["body"] == {"policy": {"*": {"remind_after_seconds": 3600, "max_reminders": 2}}}
