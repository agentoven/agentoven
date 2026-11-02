"""
agentoven.tasks — background tasks (``/api/v1/tasks``).

A task is an agent run that outlives the request that started it, under its own spend cap and with a
durable journal. It stops by itself when it finishes, when it reaches its budget (a person extends it
or ends it), when it needs a tool approved, or when it keeps repeating itself.

The methods live on :class:`TaskMethods`, a mixin for :class:`agentoven.client.AgentOvenClient`::

    from agentoven import AgentOvenClient
    from agentoven.tasks import TaskMethods

    class Client(TaskMethods, AgentOvenClient):
        pass

    client = Client(url="https://agentoven.example.com", api_key="...", kitchen="research")
    task = client.start_task("researcher", "find why checkout latency doubled", max_cost_usd=5)
    for event in client.task_events(task["id"]):
        print(event["seq"], event["type"])
    done = client.wait_for_task(task["id"])
    if done["status"] == "paused" and done["pause_kind"] == "budget":
        client.resume_task(task["id"], extend={"add_cost_usd": 5, "reason": "close to done"})

(Once ``AgentOvenClient`` lists :class:`TaskMethods` among its bases the methods are on it directly.)
"""

from __future__ import annotations

import json
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from typing import Any, Iterator, Mapping, Optional

__all__ = ["TaskMethods", "TASK_RUNNING_STATUSES", "TASK_PAUSE_GATES", "parse_sse"]

#: A task in one of these statuses is still working; every other status is one nothing will leave
#: until a person acts (paused, interrupted) or ever (completed, failed, cancelled, ...).
TASK_RUNNING_STATUSES = ("queued", "running")

#: The signed gate each kind of pause is decided at.
TASK_PAUSE_GATES = {
    "approval": "tool_approval",
    "budget": "budget_extension",
    "stall": "resume_guidance",
}

_EXTEND_KEYS = ("add_cost_usd", "add_tokens", "add_active_seconds", "add_turns")


def _seg(value: str) -> str:
    return urllib.parse.quote(value, safe="")


def parse_sse(lines: Iterator[str]) -> Iterator[dict[str, Any]]:
    """Server-sent events from an iterator of text lines: ``{"id", "event", "data"}`` for each one.
    Comment lines (the control plane's ``: ping`` keep-alives) are skipped."""
    event: dict[str, Any] = {"id": None, "event": "", "data": ""}
    has_data = False
    for raw in lines:
        line = raw.rstrip("\r\n")
        if line == "":
            if has_data or event["event"]:
                yield event
            event, has_data = {"id": None, "event": "", "data": ""}, False
        elif line.startswith(":"):
            continue
        elif line.startswith("id:"):
            try:
                event["id"] = int(line[3:].strip())
            except ValueError:
                event["id"] = None
        elif line.startswith("event:"):
            event["event"] = line[6:].strip()
        elif line.startswith("data:"):
            value = line[5:]
            value = value[1:] if value.startswith(" ") else value
            event["data"] = (event["data"] + "\n" + value) if has_data else value
            has_data = True
    if has_data:
        yield event


class TaskMethods:
    """Methods for background tasks. Needs the host class's ``_request``, ``_get``, ``_headers``, ``_url``,
    ``sign`` and ``signed_request`` (``AgentOvenClient`` has them)."""

    def start_task(
        self,
        agent: str,
        message: str,
        *,
        session_id: Optional[str] = None,
        max_cost_usd: Optional[float] = None,
        max_tokens: Optional[int] = None,
        max_active_seconds: Optional[int] = None,
        max_turns: Optional[int] = None,
        metadata: Optional[Mapping[str, str]] = None,
    ) -> dict[str, Any]:
        """Start ``agent`` as a background task and return at once with ``{"id", "status", "agent", "links",
        "budget"}``. The caps only ever tighten what the agent and the server already set: a task is never
        unbounded. A dollar cap on a model with no known price is refused (422); a full queue answers 429."""
        if not str(message).strip():
            raise ValueError("a task needs a message")
        body: dict[str, Any] = {"agent": agent, "message": message}
        if session_id:
            body["session_id"] = session_id
        budget = {
            k: v
            for k, v in (
                ("max_cost_usd", max_cost_usd),
                ("max_tokens", max_tokens),
                ("max_active_seconds", max_active_seconds),
                ("max_turns", max_turns),
            )
            if v is not None
        }
        for key, value in budget.items():
            if value <= 0:
                raise ValueError(f"{key} must be more than zero")
        if budget:
            body["budget"] = budget
        if metadata:
            body["metadata"] = dict(metadata)
        return self._request("POST", "/api/v1/tasks", body)  # type: ignore[attr-defined]

    def list_tasks(self, status: Optional[str] = None) -> list[dict[str, Any]]:
        """The kitchen's tasks that are running, paused or unfinished (a task that finished before the
        last restart is read by ID with :meth:`get_task`). ``status`` narrows by status."""
        path = "/api/v1/tasks" + (f"?status={urllib.parse.quote(status)}" if status else "")
        return self._request("GET", path).get("tasks", [])  # type: ignore[attr-defined]

    def get_task(self, task_id: str) -> dict[str, Any]:
        """One task: status, turns, spend, what it is paused for (``pause_kind``, ``budget_pause``,
        ``pending``, ``stall``), and its answer (``result``) once it has one. 404 for a task of another kitchen."""
        return self._request("GET", f"/api/v1/tasks/{_seg(task_id)}")  # type: ignore[attr-defined]

    def cancel_task(self, task_id: str) -> dict[str, Any]:
        """Stop a task. It cannot be resumed afterwards. Safe to call on one that is finishing or finished."""
        return self._request("POST", f"/api/v1/tasks/{_seg(task_id)}/cancel", {})  # type: ignore[attr-defined]

    def resume_task(
        self,
        task_id: str,
        *,
        approved: Optional[bool] = None,
        note: Optional[str] = None,
        extend: Optional[Mapping[str, Any]] = None,
        password: Optional[str] = None,
        signing_token: Optional[str] = None,
    ) -> dict[str, Any]:
        """Continue a task that is paused or was interrupted.

        An interrupted task (it stopped without finishing) needs no decision: call with no arguments. A paused
        one needs one: ``extend={"add_cost_usd": 5, "reason": "..."}`` (also ``add_tokens``, ``add_active_seconds``,
        ``add_turns``) at a budget pause, ``approved=True`` or ``False`` (with ``note``) at a tool approval or a
        stall. Only a person with the ``budget:extend`` permission can extend. An extension is given an ID, so a
        retry applies it once.

        On a Pro server a decision is signed: pass ``password=`` (the SDK signs it for the gate the pause waits
        at) or ``signing_token=`` (one you hold, signed for exactly this decision). Never both.
        """
        if password and signing_token:
            raise ValueError("pass password= or signing_token=, not both")
        ext: Optional[dict[str, Any]] = None
        if extend:
            ext = {k: v for k, v in extend.items() if v not in (None, 0)}
            if not any(k in ext for k in _EXTEND_KEYS):
                raise ValueError("an extension adds something: add_cost_usd, add_tokens, add_active_seconds or add_turns")
            for k in _EXTEND_KEYS:
                if k in ext and ext[k] <= 0:
                    raise ValueError(f"{k} must be more than zero")
            ext.setdefault("id", str(uuid.uuid4()))
        body: Optional[dict[str, Any]] = None
        if approved is not None or note is not None or ext is not None:
            if approved is False and ext is not None:
                raise ValueError("a denial does not extend a budget")
            body = {"approved": True if approved is None else bool(approved)}
            if note is not None:
                body["note"] = note
            if ext is not None:
                body["extend"] = ext
        path = f"/api/v1/tasks/{_seg(task_id)}/resume"
        if (password or signing_token) and body is not None:
            token = signing_token
            if password:
                kind = self.get_task(task_id).get("pause_kind", "")
                gate = TASK_PAUSE_GATES.get(kind)
                if gate is None:
                    raise ValueError(f"task {task_id} is not waiting for a decision, so there is nothing to sign")
                payload = {k: body[k] for k in ("note", "extend") if k in body}
                token = self.sign(  # type: ignore[attr-defined]
                    gate, task_id, "approved" if body["approved"] else "rejected", payload, password
                )
            return self.signed_request("POST", path, body, signing_token=token)  # type: ignore[attr-defined]
        return self._request("POST", path, body if body is not None else {})  # type: ignore[attr-defined]

    def wait_for_task(self, task_id: str, *, timeout: Optional[float] = None, poll_interval: float = 2.0) -> dict[str, Any]:
        """Poll until the task is no longer queued or running, and return it: finished, or stopped for a
        person (``paused``, ``interrupted``). Raises :class:`TimeoutError` after ``timeout`` seconds."""
        deadline = None if timeout is None else time.monotonic() + timeout
        while True:
            task = self.get_task(task_id)
            if task.get("status") not in TASK_RUNNING_STATUSES:
                return task
            if deadline is not None and time.monotonic() + poll_interval > deadline:
                raise TimeoutError(f"task {task_id} is still {task.get('status')} after {timeout}s")
            time.sleep(poll_interval)

    def task_events(self, task_id: str, after: int = 0) -> Iterator[dict[str, Any]]:
        """Follow a task's events as they happen. Yields ``{"seq", "time", "type", "event"}`` for each one
        (``event`` is the executor event: tokens, tool calls, ``task_status`` changes, budget warnings) and
        ends when the task does. ``after`` is the last sequence number you saw: a dropped connection carries
        on from there. Events are kept in memory on the replica that runs the task; for one that is not
        running here the stream is its current status and ends."""
        path = f"/api/v1/tasks/{_seg(task_id)}/events" + (f"?after={int(after)}" if after else "")
        headers = {**self._headers(), "Accept": "text/event-stream"}  # type: ignore[attr-defined]
        req = urllib.request.Request(self._url + path, headers=headers, method="GET")  # type: ignore[attr-defined]
        try:
            resp = urllib.request.urlopen(req)
        except urllib.error.HTTPError as exc:
            from agentoven.client import AgentOvenAPIError

            raw = exc.read()
            try:
                detail: Any = json.loads(raw)
            except Exception:
                detail = raw.decode(errors="replace")
            raise AgentOvenAPIError(exc.code, detail) from exc
        with resp:
            lines = (raw.decode("utf-8") for raw in resp)
            for sse in parse_sse(lines):
                try:
                    payload = json.loads(sse["data"])
                except ValueError:
                    continue
                ev = payload.get("event") or {}
                yield {
                    "seq": sse["id"] if sse["id"] is not None else payload.get("seq"),
                    "time": payload.get("time"),
                    "type": ev.get("type") or sse["event"],
                    "event": ev,
                }
