"""
agentoven.client — full AgentOven client with REST coverage for Pro endpoints.

This module wraps the native Rust-backed client (which covers the core OSS
operations) and adds pure-Python HTTP methods for every Pro-only API surface:
kitchens, users, guardrails, environments, promotions, schedules, test suites,
sessions, service accounts, and the traceability matrix.

Usage::

    from agentoven.client import AgentOvenClient

    client = AgentOvenClient(
        url="https://agentoven.example.com",
        api_key="svc_...",
        kitchen="payments",
    )

    # Core (native)
    client.register(agent)
    client.bake(agent)

    # Pro (pure Python REST)
    client.create_guardrail({
        "kind": "llamaguard",
        "stage": "both",
        "config": {"endpoint": "http://ollama:11434"},
    })
    schedules = client.list_schedules()
"""

from __future__ import annotations

import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any, Mapping, Optional, Sequence, Union

from agentoven._native import (
    Agent,
    AgentOvenClient as _NativeClient,
    AgentStatus,
    Branch,
    Ingredient,
    IngredientKind,
    Recipe,
    Step,
)

from agentoven.flow import Flow
from agentoven.tasks import TaskMethods
from agentoven.media import Attachment, AudioInput, InvokeResult
from agentoven.modalities import (
    AgentCard,
    Modalities,
    Provider,
    ProviderUpdate,
    validate_modalities,
)

if TYPE_CHECKING:
    from agentoven.documents import Documents
    from agentoven.notifications import NotificationConnection
    from agentoven.realtime import RealtimeConnection

__all__ = [
    "AgentOvenClient",
    "AgentOvenAPIError",
    "AudioModalityError",
    "OidcSigningRequired",
    "DatasetExport",
    "RealtimeToken",
    "KITCHEN_ROLES",
    "PERSON_ROLES",
    "SIGNING_GATES",
    "SIGNING_TOKEN_HEADER",
    # Re-export data types so callers only need to import from agentoven.client
    "Agent",
    "AgentStatus",
    "Branch",
    "Ingredient",
    "IngredientKind",
    "Recipe",
    "Step",
]

#: The header that carries a signing token on a decision call (the control plane's ``signatures.Header``).
SIGNING_TOKEN_HEADER = "X-Signing-Token"

#: The roles a person or credential can hold inside one kitchen. ``restaurateur`` is platform-wide, so it is
#: a role of a person in the user directory, never of a kitchen member, binding or credential. The retired
#: ``admin`` is no role: it was a Head Chef inside a kitchen and a Restaurateur across the installation.
KITCHEN_ROLES = ("head_chef", "chef", "baker", "taster", "auditor", "legal", "finance", "viewer")
#: The roles of a person in the user directory: the kitchen roles and the platform-wide ``restaurateur``.
PERSON_ROLES = ("restaurateur",) + KITCHEN_ROLES

#: The gates a decision can be signed at (Pro, ADR-0041). ``legal_hold_release`` lifts a legal hold (a dataset's
#: or the audit trail's) and is signed by the legal team; ``approval_skip`` runs a recipe on an environment that
#: requires approval, without that approval.
SIGNING_GATES = (
    "tool_approval", "budget_extension", "resume_guidance", "recipe_gate", "wait_release",
    "environment_promotion", "skill_approval", "bot_action", "live_tool_replay",
    "dataset_publish", "dataset_purge", "legal_hold_release", "approval_skip",
    "secret_binding", "secret_migrate", "budget_raise",
)

#: What a budget is measured in, the periods it resets on, and what a rate limit can count (Pro, ADR-0051).
BUDGET_UNITS = ("usd", "tokens")
BUDGET_PERIODS = ("daily", "weekly", "monthly", "lifetime")
LIMIT_SCOPES = ("kitchen", "agent", "api_key", "provider")
CACHE_MODES = ("off", "deterministic", "always")

_RETENTION_MODES = ("expire", "persist", "regulated")
_REPLAY_TOOLS = ("frozen", "blocked", "live")
_UNSET: Any = object()


class AgentOvenClient(TaskMethods):
    """Full AgentOven client — OSS + Pro operations.

    Parameters
    ----------
    url:
        Control plane base URL. Default: ``http://localhost:8080``.
    api_key:
        Bearer token for authentication (API key or service account token).
    kitchen:
        Active kitchen (workspace) slug. Sent as ``X-Kitchen`` header.
    """

    def __init__(
        self,
        url: str = "http://localhost:8080",
        api_key: Optional[str] = None,
        kitchen: str = "default",
        timeout: Optional[float] = None,
    ) -> None:
        self._url = url.rstrip("/")
        self._api_key = api_key
        self._kitchen = kitchen
        self._timeout = _resolve_timeout(timeout)
        self._native = _NativeClient(url=url, api_key=api_key, kitchen=kitchen)

    # ── Delegation to native client (OSS operations) ──────────────────────

    def register(self, agent: Agent) -> str:
        """Register an agent with the control plane: create it, or update it if it exists.

        The control plane creates with ``POST /api/v1/agents`` and updates with
        ``PUT /api/v1/agents/{name}``, which answers 404 for a name it does not
        know. This asks first and sends the one that fits, so calling it twice
        with an edited agent is safe: the second call updates. An update changes
        the fields you set and leaves the others as they are; it does not change
        the agent's version or bake it: the version moves only on a bake whose
        configuration differs from the last one (the first bake is ``0.1.0``).
        """
        return self._native.register(agent)

    def register_agent(self, agent: Agent) -> str:
        """Same as :meth:`register`: create the agent, or update it if it exists."""
        return self._native.register_agent(agent)

    def get_agent(self, name: str) -> Agent:
        return self._native.get_agent(name)

    def list_agents(self) -> list[Agent]:
        try:
            return self._native.list_agents()
        except Exception:
            items = self._get("/api/v1/agents")
            agents: list[Agent] = []
            for item in items:
                agents.append(
                    Agent(
                        name=item.get("name", ""),
                        description=item.get("description", ""),
                        framework=item.get("framework", "custom"),
                        version=item.get("version", "0.1.0"),
                        model_provider=item.get("model_provider", ""),
                        model_name=item.get("model_name", ""),
                        mode=item.get("mode", "managed"),
                        system_prompt=item.get("system_prompt"),
                        ingredients=[],
                    )
                )
            return agents

    def delete(self, target: Any) -> str:
        return self._native.delete(target)

    def bake(
        self,
        target: Any,
        version: Optional[str] = None,
        environment: Optional[str] = None,
        input: Optional[str] = None,
    ) -> str:
        """Start (bake) an agent.

        An agent's version moves only on a bake whose configuration differs from the last bake's. The first
        bake keeps the version the agent was registered with (``0.1.0`` by default); a bake that changes
        nothing keeps its version; a bake with a changed configuration takes the next minor version
        (``0.2.0``) and the old version stays readable (:meth:`list_agent_versions`, :meth:`get_agent_version`).
        Editing the agent with :meth:`register` does not bump it. Pass ``version`` to set one yourself.
        """
        return self._native.bake(target, version=version, environment=environment, input=input)

    def cool(self, target: Any) -> str:
        """Stop a running agent."""
        return self._native.cool(target)

    def rewarm(self, target: Any) -> str:
        """Restart a cooled agent."""
        return self._native.rewarm(target)

    def register_provider(
        self,
        name: str,
        kind: str,
        api_key: Optional[str] = None,
        endpoint: Optional[str] = None,
        models: list[str] = [],
    ) -> str:
        return self._native.register_provider(
            name, kind, api_key=api_key, endpoint=endpoint, models=models
        )

    def with_kitchen(self, kitchen: str) -> "AgentOvenClient":
        """Return a cloned client bound to a different kitchen."""
        return AgentOvenClient(url=self._url, api_key=self._api_key, kitchen=kitchen, timeout=self._timeout)

    def resolve_recipe_kitchen(
        self, recipe_name: str, candidate_kitchens: list[str]
    ) -> Optional[str]:
        """Return the first kitchen that contains the recipe, else None."""
        for kitchen in candidate_kitchens:
            try:
                self.get_recipe(recipe_name, kitchen=kitchen)
                return kitchen
            except AgentOvenAPIError as exc:
                if exc.status_code == 404:
                    continue
                raise
        return None

    def create_recipe(
        self,
        recipe: Union[Flow, Recipe],
        kitchen: Optional[str] = None,
        check_agents: bool = False,
    ) -> Any:
        """Create a recipe.

        Pass a :class:`agentoven.flow.Flow` (see that module) to build one the way the dashboard does. The flow is
        checked first and a ``FlowError`` lists what is wrong; with ``check_agents=True`` every agent it names must
        also exist in the kitchen. Returns the created recipe as a dict. The older ``Recipe``/``Step`` classes
        are still accepted, and return a confirmation string as they always have.
        """
        client = self if kitchen is None else self.with_kitchen(kitchen)
        if isinstance(recipe, Flow):
            agents = [a["name"] for a in client._get("/api/v1/agents")] if check_agents else None
            return client._post("/api/v1/recipes", recipe.to_wire(agents=agents))
        return client._native.create_recipe(recipe)

    def update_recipe(self, recipe: Flow, kitchen: Optional[str] = None, check_agents: bool = False) -> dict[str, Any]:
        """Replace a recipe's description and steps with those of ``recipe`` (a Flow with the same name)."""
        client = self if kitchen is None else self.with_kitchen(kitchen)
        agents = [a["name"] for a in client._get("/api/v1/agents")] if check_agents else None
        body = recipe.to_wire(agents=agents)
        return client._put(f"/api/v1/recipes/{_seg(recipe.name)}", {"description": body["description"], "steps": body["steps"]})

    def delete_recipe(self, name: str, kitchen: Optional[str] = None) -> dict[str, Any]:
        """Archive a recipe. It is hidden and can no longer be run or edited, but it and its runs are kept (a run
        is a record of what models did); :meth:`restore_recipe` brings it back."""
        client = self if kitchen is None else self.with_kitchen(kitchen)
        return client._delete(f"/api/v1/recipes/{_seg(name)}")

    def restore_recipe(self, name: str, kitchen: Optional[str] = None) -> dict[str, Any]:
        """Bring an archived recipe back."""
        client = self if kitchen is None else self.with_kitchen(kitchen)
        return client._post(f"/api/v1/recipes/{_seg(name)}/restore", {})

    def rerun_recipe_run(
        self,
        name: str,
        run_id: str,
        input: Optional[Mapping[str, Any]] = None,
        kitchen: Optional[str] = None,
    ) -> dict[str, Any]:
        """Run a recipe again with the input (and environment) of an earlier run, or with ``input`` instead.
        Returns at once with the new ``run_id`` and the ``rerun_of`` it repeats; see :meth:`wait_for_recipe_run`."""
        client = self if kitchen is None else self.with_kitchen(kitchen)
        return client._post(f"/api/v1/recipes/{_seg(name)}/runs/{_seg(run_id)}/rerun", {"input": dict(input)} if input is not None else {})

    def run_recipe(
        self,
        name: str,
        input: Optional[Mapping[str, Any]] = None,
        environment: Optional[str] = None,
        kitchen: Optional[str] = None,
        *,
        password: Optional[str] = None,
        signing_token: Optional[str] = None,
    ) -> dict[str, Any]:
        """Start a run. Returns at once with ``run_id`` and ``status``; see :meth:`wait_for_recipe_run`.

        An environment can require approval before a recipe runs on it. A Head Chef or Restaurateur may skip that
        approval (never in a GxP kitchen), and on Pro the skip is a signed decision on the ``approval_skip`` gate:
        record id ``<recipe>@<environment>``, payload ``{"input": <the run's input>}``, so a token for one run
        cannot start another. Pass ``password=`` (the SDK signs it, :meth:`sign`) or a ``signing_token=`` you
        signed for exactly this recipe, environment and input; it needs ``environment``. Leave both out when the
        environment needs no approval. A scheduled or automated run cannot skip an approval.
        """
        client = self if kitchen is None else self.with_kitchen(kitchen)
        run_input = dict(input or {})
        body: dict[str, Any] = {"input": run_input}
        if environment:
            body["environment"] = environment
        headers = None
        if password is not None or signing_token:
            if not environment:
                raise ValueError("skipping an approval is signed for one environment: pass environment=")
            headers = client._signing_headers(password, signing_token, "approval_skip", f"{name}@{environment}", {"input": run_input})
        return client._request("POST", f"/api/v1/recipes/{_seg(name)}/bake", body, headers)

    def list_recipe_runs(self, name: str, kitchen: Optional[str] = None) -> list[dict[str, Any]]:
        """The runs of a recipe, as dicts."""
        client = self if kitchen is None else self.with_kitchen(kitchen)
        return client._get(f"/api/v1/recipes/{_seg(name)}/runs")

    def wait_for_recipe_run(
        self,
        name: str,
        run_id: str,
        timeout: float = 300.0,
        poll: float = 1.0,
        stop_at_gate: bool = True,
        kitchen: Optional[str] = None,
    ) -> dict[str, Any]:
        """Wait for a run to finish and return it (``status``, ``step_results``, ``output``...).

        A run waiting for a person to approve a step is ``paused``. With ``stop_at_gate=True`` (the default) this
        returns then, with ``pending_gates`` naming the steps, so you can :meth:`approve_gate` or
        :meth:`reject_gate`; with ``False`` it keeps waiting. Raises ``TimeoutError`` after ``timeout`` seconds.
        """
        client = self if kitchen is None else self.with_kitchen(kitchen)
        deadline = time.monotonic() + timeout
        while True:
            body = client._get(f"/api/v1/recipes/{_seg(name)}/runs/{_seg(run_id)}")
            run = dict(body.get("run", body))
            if "pending_gates" in body:
                run["pending_gates"] = body["pending_gates"] or []
            status = run.get("status")
            if status in ("completed", "failed", "canceled") or (stop_at_gate and status == "paused"):
                return run
            if time.monotonic() >= deadline:
                raise TimeoutError(f"run {run_id} of {name} is still {status!r} after {timeout:g}s")
            time.sleep(poll)

    # ── Notification channels ─────────────────────────────────────────────

    def list_notification_channels(self) -> list[dict[str, Any]]:
        """The notification channels of the kitchen."""
        return self._get("/api/v1/channels")

    def create_notification_channel(
        self,
        name: str,
        kind: str = "websocket",
        events: Optional[Sequence[str]] = None,
        url: Optional[str] = None,
        secret: Optional[str] = None,
        config: Optional[Mapping[str, Any]] = None,
    ) -> dict[str, Any]:
        """Create a channel. ``kind="websocket"`` (the default) is a live feed: listeners connect to it with
        :meth:`listen`, and a recipe's ``notify`` step names it. A ``webhook`` channel needs ``url`` (and may sign
        with ``secret``). ``events`` limits which platform events (``gate_waiting``, ``step_failed``...) it also
        receives on their own; a ``notify`` step reaches a channel it names whatever it subscribes to."""
        body: dict[str, Any] = {"name": name, "kind": kind, "events": list(events or [])}
        if url:
            body["url"] = url
        if secret:
            body["secret"] = secret
        if config:
            body["config"] = dict(config)
        return self._post("/api/v1/channels", body)

    def delete_notification_channel(self, name: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/channels/{_seg(name)}")

    def notification_ticket(self, channel: str) -> dict[str, Any]:
        """A ticket (``token``, ``expires_in``, ``subprotocol``) for listening to one websocket channel without
        headers, so a browser can: hand ``token`` to the page and connect with :meth:`listen` ``(token=...)`` or
        offer ``subprotocol`` next to ``agentoven.v1``."""
        return self._post(f"/api/v1/channels/{_seg(channel)}/ws-ticket", {})

    def listen(self, channel: str, token: Optional[str] = None) -> "NotificationConnection":
        """Listen to a websocket channel: an async context manager that yields a listener to iterate. See
        :mod:`agentoven.notifications`. Requires ``pip install "agentoven[realtime]"``.

        Authenticates with this client's API key and kitchen. Pass ``token`` (from :meth:`notification_ticket`)
        to connect the way a browser does: no headers, the ticket offered as a WebSocket subprotocol."""
        from agentoven.notifications import (
            TICKET_SUBPROTOCOL_PREFIX,
            WS_SUBPROTOCOL,
            NotificationConnection,
            channel_url,
        )

        url = channel_url(self._url, channel)
        if token:
            return NotificationConnection(url, {}, [WS_SUBPROTOCOL, TICKET_SUBPROTOCOL_PREFIX + token])
        return NotificationConnection(url, self._headers())

    # ── Signed decisions (Pro, ADR-0041) ───────────────────────────────────
    # A decision at a gate is signed: you prove who you are again and the control plane gives you a single-use
    # token bound to exactly that decision, which you send in the ``X-Signing-Token`` header (the constant
    # ``SIGNING_TOKEN_HEADER``). :meth:`sign` does both steps. Every method below that takes a decision accepts
    # ``password=`` (the SDK signs it for you) or ``signing_token=`` (a token you already hold), never both.

    def sign(self, gate: str, record_id: str, meaning: str, payload: Optional[Mapping[str, Any]], password: str) -> str:
        """Sign one decision and return its single-use signing token: asks for the challenge, proves who you are
        with ``password``, and returns the token (good for a minute and for exactly this decision).

        ``gate`` is the kind of decision (see ``SIGNING_GATES``), ``record_id`` what is decided, ``meaning`` is
        ``"approved"`` or ``"rejected"``, and ``payload`` is the content of the decision that is signed with it
        (a reason, an input). Send the token in the ``X-Signing-Token`` header, or pass it as ``signing_token=``
        to the method that takes the decision. Signing the same decision twice gives two tokens; each is used once.

        Two gates are new with the roles: ``legal_hold_release`` (``record_id`` ``dataset:<id>`` or
        ``audit:<kitchen>``, payload ``{"reason": ...}``) lifts a legal hold, and :meth:`lift_dataset_legal_hold` and
        :meth:`lift_audit_legal_hold` sign it for you; ``approval_skip`` (``record_id`` ``<recipe>@<environment>``,
        payload ``{"input": {...}}``) runs a recipe on an environment that requires approval, and
        :meth:`run_recipe` signs it for you when you pass ``password=``.

        An API key or service account cannot sign: it gets ``signature_required``. An account that signs in
        through an identity provider (OIDC) signs in the browser, not here: this raises
        :class:`OidcSigningRequired` before sending the password anywhere.
        """
        challenge = self.signature_challenge(gate, record_id, meaning, payload)
        if challenge.get("reauth") == "oidc":
            raise OidcSigningRequired(
                400,
                {"error": "use_oidc", "message": "this account signs through the identity provider: sign in the dashboard, "
                 "or sign in with a local account (password) to sign from the SDK"},
            )
        try:
            reply = self.request_signing_token(gate, record_id, meaning, challenge["subject"]["digest"], password, payload)
        except AgentOvenAPIError as exc:
            if isinstance(exc.detail, dict) and exc.detail.get("error") == "use_oidc":
                raise OidcSigningRequired(exc.status_code, exc.detail) from exc
            raise
        return reply["signing_token"]

    def sign_decision(self, gate: str, record_id: str, meaning: str, password: str, payload: Optional[dict[str, Any]] = None) -> str:
        """Same as :meth:`sign` with the password before the payload (the older argument order)."""
        return self.sign(gate, record_id, meaning, payload, password)

    def signature_challenge(self, gate: str, record_id: str, meaning: str, payload: Optional[Mapping[str, Any]] = None) -> dict[str, Any]:
        """The first half of signing: what would be signed (``subject``, with its ``digest``, ``title`` and
        ``display``), how you would prove who you are (``reauth``: ``"password"`` or ``"oidc"``) and how long the
        token lives. Refuses (403) if you may not sign this decision."""
        return self._post("/api/v1/signatures/challenge", _signing_request(gate, record_id, meaning, payload))

    def request_signing_token(
        self, gate: str, record_id: str, meaning: str, digest: str, password: str, payload: Optional[Mapping[str, Any]] = None
    ) -> dict[str, Any]:
        """The second half of signing: ``password`` re-authenticates a local account and, if the record is still what
        ``digest`` (from :meth:`signature_challenge`) was computed over, returns ``{"signing_token", "expires_at",
        "digest", "meaning"}``. Prefer :meth:`sign`."""
        body = {**_signing_request(gate, record_id, meaning, payload), "digest": digest, "password": password}
        return self._post("/api/v1/signatures/token", body)

    def signed_request(self, method: str, path: str, body: Optional[Mapping[str, Any]] = None, *, signing_token: str) -> Any:
        """Send any request with a signing token in the ``X-Signing-Token`` header, for a decision this SDK has no
        method for. ``signing_token`` comes from :meth:`sign`."""
        return self._request(method, path, dict(body) if body is not None else None, {SIGNING_TOKEN_HEADER: signing_token})

    def list_signatures(self, gate: str = "", record_id: str = "") -> list[dict[str, Any]]:
        """Signed decisions of the kitchen: who signed (name and role), when, the meaning, and what became of the decision."""
        from urllib.parse import quote

        return self._get(f"/api/v1/signatures?gate={quote(gate)}&record_id={quote(record_id)}")

    def get_signature(self, signature_id: str) -> dict[str, Any]:
        """One signed decision."""
        return self._get(f"/api/v1/signatures/{_seg(signature_id)}")

    def verify_signatures(self) -> dict[str, Any]:
        """Check the kitchen's signature chain: ``ok`` is false, with where it breaks, if one was changed or removed."""
        return self._get("/api/v1/signatures/verify")

    def get_approver_policy(self) -> dict[str, Any]:
        """Who may approve what in this kitchen: ``{"policy": {"<gate>" or "<gate>:<scope>": {"roles": [...],
        "users": [...]}}, "gates": [...]}``. ``gates`` lists the gates a rule can name."""
        return self._get("/api/v1/approver-policy")

    def set_approver_policy(self, policy: Mapping[str, Mapping[str, Any]]) -> dict[str, Any]:
        """Replace the kitchen's approver rules (a Head Chef's; the whole policy is replaced, so send every rule).

        ``policy`` maps ``"<gate>"`` or ``"<gate>:<scope>"`` (an environment slug or an agent name, e.g.
        ``"environment_promotion:prod"``) to ``{"roles": [...], "users": [...]}``. Roles are checked here against
        ``PERSON_ROLES``; the retired ``admin`` is refused (name ``head_chef`` or ``restaurateur``).
        """
        rules: dict[str, Any] = {}
        for key, rule in policy.items():
            roles = list(rule.get("roles") or [])
            for role in roles:
                _check_role(role, PERSON_ROLES, f"approver policy {key!r}")
            rules[key] = {"roles": roles, "users": list(rule.get("users") or [])}
        return self._put("/api/v1/approver-policy", {"policy": rules})

    # ── Approvals where people work (Pro, ADR-0050) ───────────────────────
    # None of these decides anything: a decision is signed (``sign`` / ``approve_gate(password=...)``).

    def list_approval_requests(self, status: str = "open", gate: str = "", record_id: str = "") -> list[dict[str, Any]]:
        """Decisions that were announced to people through the kitchen's channels, newest first. ``status`` is
        ``"open"``, ``"closed"`` or ``"all"``; ``gate`` (``"tool_approval"``, ``"budget_extension"``, ``"recipe_gate"``...)
        and ``record_id`` narrow them. Each says how many messages went out (``delivered``, ``failed``), the reminders
        sent, the escalation ``stage`` and who claimed it."""
        if status not in ("open", "closed", "all"):
            raise ValueError("status is 'open', 'closed' or 'all'")
        query = f"status={status}" + (f"&gate={_seg(gate)}" if gate else "") + (f"&record_id={_seg(record_id)}" if record_id else "")
        return self._get(f"/api/v1/approval-requests?{query}")["requests"]

    def get_approval_request(self, request_id: str) -> dict[str, Any]:
        """One request: ``request``, its ``events`` (who was messaged where, reminders, escalations, claims) and, while
        it is open, the ``chain`` of people who will be asked, in order."""
        return self._get(f"/api/v1/approval-requests/{_seg(request_id)}")

    def approval_notify_status(self, gate: str, record_id: str) -> Optional[dict[str, Any]]:
        """The latest request for a decision (its gate and record id, e.g. ``"recipe_gate"`` and ``"<run>:<step>"``)
        with its timeline: who was told, through which channel, when it was reminded and escalated. ``None`` if it was
        never announced."""
        found = self.list_approval_requests("all", gate, record_id)
        return self.get_approval_request(found[0]["id"]) if found else None

    def claim_approval_request(self, request_id: str) -> dict[str, Any]:
        """Say you are looking at it: reminders are quiet for a while. It does not decide anything."""
        return self._post(f"/api/v1/approval-requests/{_seg(request_id)}/claim", {})["request"]

    def unclaim_approval_request(self, request_id: str) -> dict[str, Any]:
        """Let go of a claim."""
        return self._post(f"/api/v1/approval-requests/{_seg(request_id)}/unclaim", {})["request"]

    def decline_approval_request(self, request_id: str) -> dict[str, Any]:
        """Say it is not yours: it goes to the next person up at once."""
        return self._post(f"/api/v1/approval-requests/{_seg(request_id)}/decline", {})["request"]

    def snooze_approval_request(self, request_id: str, minutes: int = 60) -> dict[str, Any]:
        """Quiet reminders and escalation for ``minutes`` (1 to 1440). The deadline does not move."""
        if not 1 <= int(minutes) <= 1440:
            raise ValueError("a snooze is between 1 and 1440 minutes")
        return self._post(f"/api/v1/approval-requests/{_seg(request_id)}/snooze", {"minutes": int(minutes)})["request"]

    def get_approval_escalation(self) -> dict[str, Any]:
        """How the kitchen chases what waits for a person: ``policy`` (rules by gate, ``"<gate>:<scope>"`` or ``"*"``),
        the built-in ``default``, the ``gates`` a rule can name and the shortest interval allowed."""
        return self._get("/api/v1/approval-escalation")

    def set_approval_escalation(self, policy: Mapping[str, Mapping[str, Any]]) -> dict[str, Any]:
        """Replace the kitchen's rules (a Head Chef's; send every rule). A rule has ``channels``, ``remind_after_seconds``,
        ``remind_every_seconds``, ``max_reminders``, ``escalate_after_seconds``, ``escalation`` (stages of
        ``roles``/``users``/``channels``) and ``expire_after_seconds`` (0 never). Expiring refuses a request; it never approves it."""
        return self._put("/api/v1/approval-escalation", {"policy": {k: dict(v) for k, v in policy.items()}})

    def _signing_headers(
        self,
        password: Optional[str],
        signing_token: Optional[str],
        gate: str,
        record_id: str,
        payload: Optional[Mapping[str, Any]] = None,
        meaning: str = "approved",
    ) -> Optional[dict[str, str]]:
        """The header for one decision: the token you passed, or one signed now with ``password``, or none."""
        if password is not None and signing_token:
            raise ValueError("pass password= or signing_token=, not both")
        if password is not None:
            signing_token = self.sign(gate, record_id, meaning, payload, password)
        return {SIGNING_TOKEN_HEADER: signing_token} if signing_token else None

    def release_wait(
        self, name: str, run_id: str, step: str, kitchen: Optional[str] = None, *, password: Optional[str] = None,
        signing_token: Optional[str] = None,
    ) -> dict[str, Any]:
        """End a ``wait`` step early, so the run carries on now. Raises ``AgentOvenAPIError`` (404) if it is not waiting.

        On Pro the decision is signed: pass the ``password`` of your local account (see :meth:`sign`) or a
        ``signing_token`` you already hold.
        """
        client = self if kitchen is None else self.with_kitchen(kitchen)
        headers = client._signing_headers(password, signing_token, "wait_release", f"{run_id}:{step}")
        return client._request("POST", f"/api/v1/recipes/{_seg(name)}/runs/{_seg(run_id)}/steps/{_seg(step)}/release", {}, headers)

    def approve_gate(
        self, run_id: str, step: str, comments: Optional[str] = None, *, password: Optional[str] = None,
        signing_token: Optional[str] = None,
    ) -> dict[str, Any]:
        """Approve a step that is waiting for a person. The decision is recorded with who made it.

        On Pro every decision is signed: pass the ``password`` of your local account and the SDK signs it
        (:meth:`sign`), or a ``signing_token`` you already hold. Without either the call is unsigned, which Pro
        refuses with ``signature_required``.
        """
        return self._decide_gate(run_id, step, True, comments, password, signing_token)

    def reject_gate(
        self, run_id: str, step: str, reason: Optional[str] = None, *, password: Optional[str] = None,
        signing_token: Optional[str] = None,
    ) -> dict[str, Any]:
        """Reject a waiting step. ``reason`` is recorded and passed to the step that follows the Rejected path."""
        return self._decide_gate(run_id, step, False, reason, password, signing_token)

    def _decide_gate(
        self, run_id: str, step: str, approved: bool, comments: Optional[str], password: Optional[str] = None,
        signing_token: Optional[str] = None,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {"approved": approved}
        if comments:
            body["comments"] = comments
        headers = self._signing_headers(password, signing_token, "recipe_gate", f"{run_id}:{step}", None, "approved" if approved else "rejected")
        return self._request("POST", f"/api/v1/approvals/{_seg(run_id)}/{_seg(step)}", body, headers)

    def bake_recipe(self, name: str, input: Optional[Any] = None, kitchen: Optional[str] = None) -> str:
        native = self._native if kitchen is None else self.with_kitchen(kitchen)._native
        return native.bake_recipe(name, input=input)

    def list_recipes(self, kitchen: Optional[str] = None) -> str:
        native = self._native if kitchen is None else self.with_kitchen(kitchen)._native
        return native.list_recipes()

    def get_recipe(self, name: str, kitchen: Optional[str] = None) -> dict[str, Any]:
        if kitchen is None:
            return self._get(f"/api/v1/recipes/{_seg(name)}")
        return self.with_kitchen(kitchen)._get(f"/api/v1/recipes/{_seg(name)}")

    def get_recipe_runs(self, name: str, kitchen: Optional[str] = None) -> str:
        native = self._native if kitchen is None else self.with_kitchen(kitchen)._native
        return native.get_recipe_runs(name)

    def get_recipe_run(self, recipe_name: str, run_id: str, kitchen: Optional[str] = None) -> str:
        native = self._native if kitchen is None else self.with_kitchen(kitchen)._native
        return native.get_recipe_run(recipe_name, run_id)

    # ── Kitchen management (Pro) ──────────────────────────────────────────

    def list_kitchens(self) -> list[dict[str, Any]]:
        """List all kitchens accessible to the current user."""
        return self._get("/api/v1/kitchens")

    def get_kitchen(self, kitchen_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/kitchens/{_seg(kitchen_id)}")

    def create_kitchen(self, name: str, display_name: str = "") -> dict[str, Any]:
        return self._post("/api/v1/kitchens", {"name": name, "display_name": display_name or name})

    def delete_kitchen(self, kitchen_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/kitchens/{_seg(kitchen_id)}")

    # ── User directory (Pro) ──────────────────────────────────────────────

    def list_users(self) -> list[dict[str, Any]]:
        """List users in the current kitchen."""
        return self._get("/api/v1/users")

    def get_user(self, user_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/users/{_seg(user_id)}")

    def create_user(self, email: str, role: str, name: str = "") -> dict[str, Any]:
        """Add a person to the user directory. ``role`` is one of ``PERSON_ROLES``: ``restaurateur`` (the
        installation's owner) or a kitchen role. The retired ``admin`` is refused."""
        _check_role(role, PERSON_ROLES, "a user")
        return self._post("/api/v1/users", {"email": email, "role": role, "name": name})

    def update_user(self, user_id: str, **fields: Any) -> dict[str, Any]:
        return self._put(f"/api/v1/users/{_seg(user_id)}", fields)

    def delete_user(self, user_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/users/{_seg(user_id)}")

    # ── Guardrails (Pro) ─────────────────────────────────────────────────

    def list_guardrails(self) -> list[dict[str, Any]]:
        """List the kitchen's workspace guardrail rules, each with its ``action``."""
        return self._get("/api/v1/guardrails")

    def guardrail_kinds(self) -> dict[str, Any]:
        """The kinds, stages, actions and PII/secret detectors a rule can use:
        ``{"kinds": [...], "stages": [...], "actions": [...], "detectors": [...]}``."""
        return self._get("/api/v1/guardrails/kinds")

    def get_guardrail(self, guardrail_id: str) -> dict[str, Any]:
        """One rule. Raises ``LookupError`` when the kitchen has no such rule."""
        for rule in self.list_guardrails():
            if rule.get("id") == guardrail_id:
                return rule
        raise LookupError(f"no guardrail {guardrail_id!r} in this kitchen")

    def create_guardrail(self, guardrail: dict[str, Any]) -> dict[str, Any]:
        """Create a guardrail rule. ``action`` is what happens when it fires: ``block`` (the default), ``redact``
        (matches become ``[IBAN]``-style placeholders and the rest of the text continues), ``mask`` (asterisks, last
        four kept), ``warn`` (let it through and record it) or ``require_approval`` (on a tool call: pause for a
        person). ``stage`` is ``input``, ``output``, ``both``, ``retrieval`` (each retrieved chunk before it enters the
        prompt) or ``all``. The rule is checked when it is saved; a rule that could not work is refused (400).

        Redacting personal data and secrets from every reply and every retrieved document::

            client.create_guardrail({
                "name": "no-pii-out",
                "kind": "pii_detection",
                "stage": "all",
                "action": "redact",
                "config": {"patterns": ["pii", "secrets"], "allowlist": ["support@example.com"]},
            })

        A signed webhook of your own (``secret_ref`` names a vault credential that signs the request)::

            client.create_guardrail({
                "name": "dlp",
                "kind": "custom",
                "stage": "output",
                "config": {"endpoint": "https://policy.example.com/check", "secret_ref": "vault://guardrail-hook",
                           "timeout_ms": 3000, "fail_open": False},
            })
        """
        return self._post("/api/v1/guardrails", guardrail)

    def update_guardrail(self, guardrail_id: str, guardrail: dict[str, Any]) -> dict[str, Any]:
        """Change a rule: send only the fields to change (``name``, ``stage``, ``action``, ``config``, ``enabled``...)."""
        return self._put(f"/api/v1/guardrails/{_seg(guardrail_id)}", guardrail)

    def toggle_guardrail(self, guardrail_id: str, enabled: Optional[bool] = None) -> dict[str, Any]:
        """Switch a rule on or off. With ``enabled`` it is set to that; without, it is flipped."""
        if enabled is not None:
            return self.update_guardrail(guardrail_id, {"enabled": enabled})
        return self._request("PATCH", f"/api/v1/guardrails/{_seg(guardrail_id)}/toggle", {})

    def delete_guardrail(self, guardrail_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/guardrails/{_seg(guardrail_id)}")

    def test_guardrails(
        self, text: str, *, stage: str = "input", rules: Optional[list[Mapping[str, Any]]] = None, include_custom: bool = False
    ) -> dict[str, Any]:
        """What rules would do to ``text`` (``stage``: ``input``, ``output`` or ``retrieval``), without running an agent
        or storing anything. With no ``rules`` the kitchen's workspace rules are used (disabled ones too, so a rule can
        be tried before it is switched on); custom endpoint rules are skipped unless ``include_custom`` (which needs
        ``settings:update``). Returns ``{"outcome", "passed", "text", "redacted", "warned", "results"}``: ``outcome``
        is ``pass``, ``redacted``, ``warned``, ``approval_required`` or ``blocked``, ``text`` the text that would
        continue, and each result's ``findings`` the detector types and counts (never the matched text)."""
        body: dict[str, Any] = {"text": text, "stage": stage}
        _put_if(body, rules=[dict(r) for r in rules] if rules else None)
        if include_custom:
            body["include_custom"] = True
        return self._post("/api/v1/guardrails/test", body)

    def list_guardrail_exceptions(self, guardrail_id: str) -> list[dict[str, Any]]:
        """List per-agent exceptions for a guardrail."""
        return self._get(f"/api/v1/guardrails/{_seg(guardrail_id)}/exceptions")

    def add_guardrail_exception(
        self, guardrail_id: str, agent_name: str, reason: str = "", expires_at: Optional[str] = None
    ) -> dict[str, Any]:
        """Exempt one agent from a rule. ``expires_at`` (RFC 3339) makes the exception expire."""
        body: dict[str, Any] = {"agent_name": agent_name, "reason": reason}
        _put_if(body, expires_at=expires_at)
        return self._post(f"/api/v1/guardrails/{_seg(guardrail_id)}/exceptions", body)

    def remove_guardrail_exception(self, guardrail_id: str, exception_id: str) -> dict[str, Any]:
        """Remove an exception by its id (as listed by ``list_guardrail_exceptions``)."""
        return self._delete(f"/api/v1/guardrails/{_seg(guardrail_id)}/exceptions/{_seg(exception_id)}")

    # ── Environments & promotions (Pro) ──────────────────────────────────

    def list_environments(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/environments")

    def create_environment(self, name: str, kind: str = "dev", **kwargs: Any) -> dict[str, Any]:
        return self._post("/api/v1/environments", {"name": name, "kind": kind, **kwargs})

    def promote_agent(
        self,
        agent_name: str,
        from_env: str,
        to_env: str,
        version: Optional[str] = None,
    ) -> dict[str, Any]:
        """Promote an agent from one environment to another."""
        return self._post(
            "/api/v1/promotions",
            {"agent_name": agent_name, "from_env": from_env, "to_env": to_env, "version": version},
        )

    def list_deployments(self, agent_name: Optional[str] = None) -> list[dict[str, Any]]:
        path = "/api/v1/deployments"
        if agent_name:
            path += f"?agent={agent_name}"
        return self._get(path)

    def get_traceability_matrix(self) -> dict[str, Any]:
        """Return the agent × environment deployment traceability grid."""
        return self._get(f"/api/v1/traces/matrix?kitchen={self._kitchen}")

    # ── Sessions (Pro) ───────────────────────────────────────────────────

    def list_sessions(self, agent_name: Optional[str] = None) -> list[dict[str, Any]]:
        path = "/api/v1/sessions"
        if agent_name:
            path += f"?agent={agent_name}"
        return self._get(path)

    def get_session(self, session_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/sessions/{_seg(session_id)}")

    def create_session(self, agent_name: str, **kwargs: Any) -> dict[str, Any]:
        return self._post("/api/v1/sessions", {"agent_name": agent_name, **kwargs})

    def delete_session(self, session_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/sessions/{_seg(session_id)}")

    # ── Service accounts (Pro) ───────────────────────────────────────────

    def list_service_accounts(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/service-accounts")

    def create_service_account(self, name: str, role: str, owner_email: Optional[str] = None) -> dict[str, Any]:
        """Create a machine identity bound to this kitchen. Returns token (shown once — store securely).

        ``role`` is a kitchen role (one of ``KITCHEN_ROLES``; the retired ``admin`` is refused). Every credential
        belongs to a person (``owner_email``, you if left out): a member of this kitchen at a rank that covers the
        role. Creating one on someone else's behalf is a Head Chef's act. When the owner leaves the kitchen the
        credential stops; give it to someone with :meth:`set_service_account_owner`.
        """
        _check_role(role, KITCHEN_ROLES, "a service account")
        body: dict[str, Any] = {"name": name, "role": role}
        if owner_email:
            body["owner_email"] = owner_email
        return self._post("/api/v1/service-accounts", body)

    def delete_service_account(self, sa_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/service-accounts/{_seg(sa_id)}")

    # ── Schedules (Pro) ──────────────────────────────────────────────────

    def list_schedules(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/schedules")

    def get_schedule(self, schedule_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/schedules/{_seg(schedule_id)}")

    def create_schedule(
        self,
        recipe_name: str,
        cron: str,
        timezone: str = "UTC",
        enabled: bool = True,
        **kwargs: Any,
    ) -> dict[str, Any]:
        """Schedule a recipe on a cron expression.

        Example::

            client.create_schedule(
                recipe_name="daily-report",
                cron="0 8 * * MON-FRI",
                timezone="Europe/London",
            )
        """
        return self._post(
            "/api/v1/schedules",
            {"recipe_name": recipe_name, "cron_expr": cron, "timezone": timezone, "enabled": enabled, **kwargs},
        )

    def update_schedule(self, schedule_id: str, **fields: Any) -> dict[str, Any]:
        return self._put(f"/api/v1/schedules/{_seg(schedule_id)}", fields)

    def delete_schedule(self, schedule_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/schedules/{_seg(schedule_id)}")

    # ── Inbound triggers (Pro, ADR-0047) ─────────────────────────────────
    # A trigger starts a recipe or an agent from an event that arrives from outside, as a service account
    # that belongs to a person. It cannot start what the account could not, and it cannot skip an approval.

    def list_triggers(self) -> list[dict[str, Any]]:
        """The kitchen's triggers."""
        return self._get("/api/v1/triggers").get("triggers", [])

    def get_trigger(self, trigger_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/triggers/{_seg(trigger_id)}")

    def create_trigger(
        self,
        name: str,
        service_account_id: str,
        target: str,
        *,
        kind: str = "webhook",
        target_kind: str = "recipe",
        environment: Optional[str] = None,
        input_mapping: Optional[Mapping[str, Any]] = None,
        required_paths: Optional[Sequence[str]] = None,
        config: Optional[Mapping[str, Any]] = None,
        secret_ref: Optional[str] = None,
        rate_limit_per_min: Optional[int] = None,
        description: str = "",
        enabled: bool = True,
    ) -> dict[str, Any]:
        """Create a trigger. ``kind`` is ``webhook``, ``email`` or ``file``; ``target_kind`` is ``recipe`` or ``agent``.

        ``input_mapping`` turns the payload into the run's input: a string ``"$.a.b"`` takes a value (keeping its
        type), ``"text {{ $.a.b }}"`` puts one into text, nothing else is evaluated. An agent target maps
        ``message`` (and optionally ``metadata``). ``config`` holds the settings of the kind, with secrets as
        references (``vault://...``), never values.

        A webhook trigger given no ``secret_ref`` gets a signing secret generated; it is in the response under
        ``secret``, once. Sign a delivery with :func:`sign_webhook`.
        """
        if kind not in TRIGGER_KINDS:
            raise ValueError(f"kind is one of {', '.join(TRIGGER_KINDS)}")
        if target_kind not in ("recipe", "agent"):
            raise ValueError("target_kind is recipe or agent")
        body: dict[str, Any] = {
            "name": name, "kind": kind, "target_kind": target_kind, "target": target,
            "service_account_id": service_account_id, "description": description, "enabled": enabled,
        }
        _put_if(body, environment=environment, input_mapping=input_mapping, required_paths=list(required_paths) if required_paths is not None else None,
                config=config, secret_ref=secret_ref, rate_limit_per_min=rate_limit_per_min)
        return self._post("/api/v1/triggers", body)

    def update_trigger(self, trigger_id: str, **fields: Any) -> dict[str, Any]:
        """Change a trigger: any of description, target_kind, target, environment, service_account_id,
        input_mapping, required_paths, config, rate_limit_per_min, enabled. Switching one off needs only the right
        to manage triggers; anything else needs the authority of the account it runs as."""
        return self._put(f"/api/v1/triggers/{_seg(trigger_id)}", fields)

    def set_trigger_enabled(self, trigger_id: str, enabled: bool) -> dict[str, Any]:
        return self.update_trigger(trigger_id, enabled=enabled)

    def delete_trigger(self, trigger_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/triggers/{_seg(trigger_id)}")

    def rotate_trigger_secret(self, trigger_id: str) -> dict[str, Any]:
        """Replace the signing secret AgentOven generated for a webhook trigger. The new one is in ``secret``, once;
        the old one stops working at once."""
        return self._post(f"/api/v1/triggers/{_seg(trigger_id)}/rotate-secret", {})

    def fire_trigger(self, trigger_id: str, payload: Any = None, *, dry_run: bool = True) -> dict[str, Any]:
        """Try a trigger with a sample payload. A dry run (the default) shows the input the run would get and
        starts nothing; ``dry_run=False`` starts a real run as the trigger's account, recorded as a test."""
        return self._post(f"/api/v1/triggers/{_seg(trigger_id)}/fire", {"payload": {} if payload is None else payload, "dry_run": dry_run})

    def poll_trigger(self, trigger_id: str) -> dict[str, Any]:
        """Look at an e-mail or file trigger's source now, instead of at its next interval."""
        return self._post(f"/api/v1/triggers/{_seg(trigger_id)}/poll", {})

    def list_trigger_deliveries(
        self,
        trigger_id: Optional[str] = None,
        status: Optional[str] = None,
        run_id: Optional[str] = None,
        limit: Optional[int] = None,
        before: Optional[str] = None,
    ) -> list[dict[str, Any]]:
        """What happened to deliveries, newest first. ``status`` is accepted, rejected, duplicate or failed."""
        if status is not None and status not in TRIGGER_DELIVERY_STATUSES:
            raise ValueError(f"status is one of {', '.join(TRIGGER_DELIVERY_STATUSES)}")
        return self._get("/api/v1/triggers/deliveries" + _query(trigger_id=trigger_id, status=status, run_id=run_id, limit=limit, before=before)).get("deliveries", [])

    # ── Test suites (Pro) ────────────────────────────────────────────────
    # The server routes every one of these by id, never by name.

    def list_test_suites(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/test-suites")

    def get_test_suite(self, suite_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/test-suites/{_seg(suite_id)}")

    def create_test_suite(self, suite: dict[str, Any]) -> dict[str, Any]:
        """Create a test suite.

        A case is exactly one of two modes (the server rejects a case that
        mixes or omits both — see internal/testsuite/handlers.go
        validateCases): a plain response check (``input`` +
        ``expected_output``), or — with ``expected_scenario_id`` set — a
        scenario auto-pick check, which asks the agent to run with no
        scenario named and verifies it resolves to this one and passes.

        Example::

            client.create_test_suite({
                "name": "smoke",
                "agent_name": "classifier",
                "cases": [
                    {"name": "refund case", "input": "Refund $50", "expected_output": "refund"},
                    {"name": "picks its own scenario", "expected_scenario_id": "refund-flow", "min_pass_rate": 1},
                ],
            })
        """
        return self._post("/api/v1/test-suites", suite)

    def update_test_suite(self, suite_id: str, suite: dict[str, Any]) -> dict[str, Any]:
        return self._put(f"/api/v1/test-suites/{_seg(suite_id)}", suite)

    def delete_test_suite(self, suite_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/test-suites/{_seg(suite_id)}")

    def run_test_suite(self, suite_id: str, trigger: str = "manual") -> dict[str, Any]:
        """Trigger an immediate (ad-hoc) run of a test suite."""
        return self._post(f"/api/v1/test-suites/{_seg(suite_id)}/run", {"trigger": trigger})

    def list_test_runs(self, suite_id: str) -> list[dict[str, Any]]:
        """Lists runs for one suite. The server has no top-level
        ``GET /api/v1/test-runs`` — only this route, nested under the suite —
        so unlike an earlier version of this method, ``suite_id`` is
        required, not optional."""
        return self._get(f"/api/v1/test-suites/{_seg(suite_id)}/runs")

    def get_test_run(self, suite_id: str, run_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/test-suites/{_seg(suite_id)}/runs/{_seg(run_id)}")

    def cancel_test_run(self, run_id: str) -> dict[str, Any]:
        return self._post(f"/api/v1/test-runs/{_seg(run_id)}/cancel", {})

    # ── World schemas (Pro, ADR-0032) ─────────────────────────────────────

    def list_world_schemas(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/world-schemas")

    def get_world_schema(self, schema_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/world-schemas/{_seg(schema_id)}")

    def create_world_schema(self, schema: dict[str, Any]) -> dict[str, Any]:
        return self._post("/api/v1/world-schemas", schema)

    def save_world_schema(self, schema_id: str, schema: dict[str, Any]) -> dict[str, Any]:
        return self._put(f"/api/v1/world-schemas/{_seg(schema_id)}", schema)

    # ── Scenarios (Pro, ADR-0032) ──────────────────────────────────────────

    def list_scenarios(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/scenarios")

    def get_scenario(self, scenario_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/scenarios/{_seg(scenario_id)}")

    def create_scenario(self, scenario: dict[str, Any]) -> dict[str, Any]:
        return self._post("/api/v1/scenarios", scenario)

    def save_scenario(self, scenario_id: str, scenario: dict[str, Any]) -> dict[str, Any]:
        return self._put(f"/api/v1/scenarios/{_seg(scenario_id)}", scenario)

    def delete_scenario(self, scenario_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/scenarios/{_seg(scenario_id)}")

    def run_scenarios(self, req: dict[str, Any]) -> dict[str, Any]:
        """Submits a scenario run. Leave ``scenario_ids`` out of ``req`` to
        auto-pick: the agent's own scenario ingredients decide what runs and
        how many episodes (each ingredient's own ``rollouts``) — the same
        path a scenario auto-pick test case uses.

        Example::

            client.run_scenarios({"agent_name": "refund-agent"})
        """
        return self._post("/api/v1/scenario-runs", req)

    def get_scenario_run(self, run_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/scenario-runs/{_seg(run_id)}")

    def list_scenario_runs(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/scenario-runs")

    def get_scenario_run_episodes(self, run_id: str) -> list[dict[str, Any]]:
        """The per-episode detail behind a scenario run's aggregate
        pass_rate — one entry per rollout, each with its own graded verdict."""
        return self._get(f"/api/v1/scenario-runs/{_seg(run_id)}/episodes")

    def cancel_scenario_run(self, run_id: str) -> dict[str, Any]:
        return self._post(f"/api/v1/scenario-runs/{_seg(run_id)}/cancel", {})

    # ── Workloads / K8s (Pro) ────────────────────────────────────────────

    def list_workloads(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/workloads")

    def get_workload(self, workload_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/workloads/{_seg(workload_id)}")

    # ── Agent Environments (Pro) ─────────────────────────────────────────
    # Per-agent environment deployments: bake with provider/model overrides,
    # guardrail policy, and optional external backend URL.

    def list_agent_environments(self, agent_name: str) -> list[dict[str, Any]]:
        """List all environment deployments for an agent."""
        return self._get(f"/api/v1/agents/{_seg(agent_name)}/environments")

    def get_agent_environment(self, agent_name: str, env_slug: str) -> dict[str, Any]:
        """Get a specific environment deployment for an agent."""
        return self._get(f"/api/v1/agents/{_seg(agent_name)}/environments/{_seg(env_slug)}")

    def upsert_agent_environment(
        self,
        agent_name: str,
        env_slug: str,
        version: Optional[str] = None,
        provider_name: Optional[str] = None,
        model_name: Optional[str] = None,
        provider_overrides: Optional[dict[str, Any]] = None,
        tool_overrides: Optional[dict[str, Any]] = None,
        guardrail_policy: str = "inherit",
        required_guardrails: Optional[list[str]] = None,
        disabled_guardrails: Optional[list[str]] = None,
        backend_endpoint: Optional[str] = None,
    ) -> dict[str, Any]:
        """Create or update an agent environment deployment.

        :param guardrail_policy: One of "inherit", "strict", "relaxed", "disabled".
        :param backend_endpoint: For external agents only — the A2A backend URL.
        """
        body: dict[str, Any] = {"guardrail_policy": guardrail_policy}
        if version is not None:
            body["version"] = version
        if provider_name is not None:
            body["provider_name"] = provider_name
        if model_name is not None:
            body["model_name"] = model_name
        if provider_overrides is not None:
            body["provider_overrides"] = provider_overrides
        if tool_overrides is not None:
            body["tool_overrides"] = tool_overrides
        if required_guardrails is not None:
            body["required_guardrails"] = required_guardrails
        if disabled_guardrails is not None:
            body["disabled_guardrails"] = disabled_guardrails
        if backend_endpoint is not None:
            body["backend_endpoint"] = backend_endpoint
        return self._put(f"/api/v1/agents/{_seg(agent_name)}/environments/{_seg(env_slug)}", body)

    def delete_agent_environment(self, agent_name: str, env_slug: str) -> dict[str, Any]:
        """Initiate cool-down / removal of an agent environment deployment."""
        return self._delete(f"/api/v1/agents/{_seg(agent_name)}/environments/{_seg(env_slug)}")

    # ── Scoped API keys (Pro) ───────────────────────────────────────────

    def list_scoped_keys(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/keys")

    def create_scoped_key(
        self,
        label: str,
        agent_names: list[str],
        max_calls: int = 0,
        expires_in: str = "",
        owner_email: Optional[str] = None,
    ) -> dict[str, Any]:
        """Create a scoped API key. Like a service account it belongs to a person (``owner_email``, you if left
        out), who must be a member of this kitchen; see :meth:`set_scoped_key_owner`."""
        body: dict[str, Any] = {
            "label": label,
            "agent_names": agent_names,
            "max_calls": max_calls,
            "expires_in": expires_in,
        }
        if owner_email:
            body["owner_email"] = owner_email
        return self._post("/api/v1/keys", body)

    def get_scoped_key(self, key_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/keys/{_seg(key_id)}")

    def revoke_scoped_key(self, key_id: str) -> dict[str, Any]:
        return self._post(f"/api/v1/keys/{_seg(key_id)}/revoke", {})

    def get_scoped_key_usage(self, key_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/keys/{_seg(key_id)}/usage")

    def delete_scoped_key(self, key_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/keys/{_seg(key_id)}")

    # ── Credentials (Pro) ───────────────────────────────────────────────

    def list_credentials(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/credentials")

    def create_credential(self, name: str, value: str, label: str = "") -> dict[str, Any]:
        return self._post("/api/v1/credentials", {"name": name, "value": value, "label": label})

    def delete_credential(self, credential_name: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/credentials/{_seg(credential_name)}")

    # ── Audit (OSS route, exposed here for convenience) ───────────────────

    def list_audit_events(
        self,
        limit: int = 50,
        action: Optional[str] = None,
        agent: Optional[str] = None,
        environment: Optional[str] = None,
    ) -> list[dict[str, Any]]:
        params: list[str] = [f"limit={limit}"]
        if action:
            params.append(f"action={action}")
        if agent:
            params.append(f"agent={agent}")
        if environment:
            params.append(f"environment={environment}")
        return self._get("/api/v1/audit?" + "&".join(params))

    # ── Traces (OSS route) ────────────────────────────────────────────────

    def list_traces(self, agent: Optional[str] = None, limit: int = 50, **filters: Any) -> list[dict[str, Any]]:
        """List traces, newest first.

        `filters` are the list endpoint's own: status, model, provider, tag, session, recipe, environment,
        agent_version, q, min_cost, max_cost, range ("1h", "24h", "7d", "30d"), sort, order, offset, and
        view (the id of a saved view, whose filters apply unless named here as well).
        """
        params: dict[str, Any] = {"limit": limit, **filters}
        if agent:
            params["agent"] = agent
        query = urllib.parse.urlencode({k: v for k, v in params.items() if v is not None and v != ""})
        return self._get("/api/v1/traces?" + query)

    def get_trace(self, trace_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/traces/{_seg(trace_id)}")

    def trace_timeline(self, trace_id: str, limit: Optional[int] = None, cursor: Optional[str] = None) -> dict[str, Any]:
        """The trace laid out on a clock, computed by the server: each span's depth, offset and duration, the
        critical path, retries and fallbacks, tokens and cost by model, and the governance events. A large
        trace comes in pages: pass the previous page's `cursor` for the next."""
        query = urllib.parse.urlencode({k: v for k, v in {"limit": limit, "cursor": cursor}.items() if v})
        return self._get(f"/api/v1/traces/{_seg(trace_id)}/timeline" + (f"?{query}" if query else ""))

    def trace_audit(self, trace_id: str) -> dict[str, Any]:
        """The audit events recorded for a trace (and, for a recipe run, for every trace under it), in order,
        with each event's sequence number and hash where the audit log is chained, and a `coverage` note saying
        from when events can be found by trace."""
        return self._get(f"/api/v1/traces/{_seg(trace_id)}/audit")

    def list_trace_views(self) -> list[dict[str, Any]]:
        """Your saved views of the trace list and the ones shared in this kitchen."""
        return self._get("/api/v1/trace-views")

    def save_trace_view(self, name: str, filter: Optional[Mapping[str, Any]] = None, shared: bool = False) -> dict[str, Any]:
        """Save the filters of a trace list under a name. `filter` takes the keys `list_traces` takes."""
        return self._post("/api/v1/trace-views", {"name": name, "filter": dict(filter or {}), "shared": shared})

    def update_trace_view(self, view_id: str, name: str, filter: Optional[Mapping[str, Any]] = None, shared: bool = False) -> dict[str, Any]:
        return self._put(f"/api/v1/trace-views/{_seg(view_id)}", {"name": name, "filter": dict(filter or {}), "shared": shared})

    def delete_trace_view(self, view_id: str) -> None:
        self._delete(f"/api/v1/trace-views/{_seg(view_id)}")

    # ── Replay (Pro, ADR-0039) ────────────────────────────────────────────
    # Re-run a captured call or run with edits. A replay is an investigation, not a reproduction: models are
    # not deterministic and the world has moved on, so a replay is a new trace named ``replay:<agent>`` and never
    # evidence of what the original run did. The original is never changed. Needs ``trace:replay`` and
    # ``trace:read:content`` (a Chef and above, and the Auditor for the content).

    def replay_call(
        self,
        trace_id: str,
        span_id: str,
        variants: Optional[Sequence[Mapping[str, Any]]] = None,
        reason: str = "",
    ) -> dict[str, Any]:
        """Re-run one captured model call as up to five variants (the server's limit). Runs no tool: the tool calls
        the model asked for come back, unexecuted.

        Each variant is ``{"label": "cooler", "edits": {...}}``; ``edits`` may hold ``model``, ``provider`` (one of
        the kitchen's), ``temperature``, ``max_tokens``, ``top_p``, ``stop``, ``thinking_enabled``,
        ``thinking_budget``, ``tool_choice`` (``"auto"``, ``"none"``, ``"required"`` or an object),
        ``response_format`` and ``messages`` (``[{"index": 1, "content": "..."}]``). The server refuses an edit it
        does not understand. With no variants the call is re-run as captured, which shows how much of the original
        answer was chance. ``reason`` is recorded, and is required for an archived agent's trace.

        The edited prompt goes through the agent's input guardrails and the reply through the output ones; the
        kitchen's spend gate counts the call. Each variant ends ``completed``, ``blocked``, ``refused`` or ``failed``.
        """
        body: dict[str, Any] = {"variants": [dict(v) for v in (variants or [])], "reason": reason}
        return self._post(f"/api/v1/traces/{_seg(trace_id)}/spans/{_seg(span_id)}/replay", body)

    def replay_run(
        self,
        trace_id: str,
        from_span: str,
        edits: Optional[Mapping[str, Any]] = None,
        tools: str = "frozen",
        live_tools: Optional[Sequence[str]] = None,
        reason: str = "",
        *,
        password: Optional[str] = None,
        signing_token: Optional[str] = None,
    ) -> dict[str, Any]:
        """Re-run an agent run from one of its model calls (``from_span``) with ``edits`` (the same keys as
        :meth:`replay_call`). ``tools`` says what the run's tool calls do:

        - ``"frozen"`` (the default): the captured result is returned for as long as the model asks for the same
          tool call with the same arguments; when it asks for one with no captured result the replay stops,
          ``diverged``, and shows both. Nothing is invented and no tool is run.
        - ``"blocked"``: the call is returned, not run, and the replay ends there.
        - ``"live"``: only the tools named in ``live_tools`` run, and only if the agent marked them idempotent
          (tools with side effects, the ``agentoven_*`` tools and calls to other agents never run live). It needs
          the ``trace:replay:live-tools`` permission and is a signed decision on the ``live_tool_replay`` gate,
          bound to the trace, the step, the tool list and the edits: pass ``password=`` (the SDK asks
          :meth:`prepare_live_replay` what to sign and signs it) or a ``signing_token=`` signed from that
          preparation. Without either the server refuses with ``signature_required``.
        """
        if tools not in _REPLAY_TOOLS:
            raise ValueError(f"tools must be one of {', '.join(_REPLAY_TOOLS)}, not {tools!r}")
        names = list(live_tools or [])
        if tools == "live" and not names:
            raise ValueError('tools="live" needs live_tools naming the tools that may run live')
        if tools != "live" and (names or password is not None or signing_token):
            raise ValueError('live_tools, password and signing_token only go with tools="live"')
        body = {"from_span": from_span, "edits": dict(edits or {}), "tools": tools, "live_tools": names, "reason": reason}
        headers = None
        if tools == "live" and (password is not None or signing_token):
            if password is not None and signing_token:
                raise ValueError("pass password= or signing_token=, not both")
            if password is not None:
                prep = self.prepare_live_replay(trace_id, from_span, names, edits, reason)
                signing_token = self.sign(prep["gate"], prep["record_id"], "approved", prep.get("payload"), password)
            headers = {SIGNING_TOKEN_HEADER: signing_token}
        return self._request("POST", f"/api/v1/traces/{_seg(trace_id)}/replay", body, headers)

    def prepare_live_replay(
        self,
        trace_id: str,
        from_span: str,
        live_tools: Sequence[str],
        edits: Optional[Mapping[str, Any]] = None,
        reason: str = "",
    ) -> dict[str, Any]:
        """Check a live replay as far as it can be checked, and return what to sign: ``{"gate", "record_id",
        "payload"}``, computed by the server so you never reproduce the digest of the edits. Pass them to
        :meth:`sign`, then the token to :meth:`replay_run`. Needs ``trace:replay:live-tools``."""
        body = {"from_span": from_span, "edits": dict(edits or {}), "live_tools": list(live_tools), "reason": reason}
        return self._post(f"/api/v1/traces/{_seg(trace_id)}/replay/prepare", body)

    def get_span_capture(self, trace_id: str, span_id: str) -> dict[str, Any]:
        """A model call as it was captured, whole: the request (with the tools it offered) and the response. It is
        the most sensitive thing a trace holds, so reading it needs ``trace:read:content`` and is audited. A call
        recorded before complete capture existed answers 404 ``not_captured``."""
        return self._get(f"/api/v1/traces/{_seg(trace_id)}/spans/{_seg(span_id)}/capture")

    # ── Governed datasets (Pro, ADR-0039) ─────────────────────────────────
    # A dataset is a named set of cases; a case is a snapshot with provenance and a keyed content hash; a
    # version is an append-only manifest of case hashes. Retention is ``expire`` (the default, with a TTL),
    # ``persist`` (kept until purged, reviewed on a date) or ``regulated`` (kept for at least
    # ``min_retention_days``; a purge and any lowering of the minimum are signed).

    def list_datasets(self) -> list[dict[str, Any]]:
        """The kitchen's datasets (``dataset:read``)."""
        return self._get("/api/v1/datasets")

    def create_dataset(
        self,
        name: str,
        description: str = "",
        retention_mode: Optional[str] = None,
        ttl_days: Optional[int] = None,
        review_by: Optional[str] = None,
        min_retention_days: Optional[int] = None,
        purpose: str = "",
    ) -> dict[str, Any]:
        """Create a dataset (``dataset:write``). ``name`` is 1 to 120 characters. ``retention_mode`` is ``"expire"``,
        ``"persist"`` or ``"regulated"`` (the server's default if left out); ``ttl_days`` is for ``expire``,
        ``review_by`` (an RFC 3339 time) for ``persist``, ``min_retention_days`` for ``regulated`` (default 90, never
        below 7). ``purpose`` is required for what keeps data beyond a TTL."""
        _check_retention_mode(retention_mode)
        body: dict[str, Any] = {"name": name, "description": description, "purpose": purpose}
        _put_if(body, retention_mode=retention_mode, ttl_days=ttl_days, review_by=review_by, min_retention_days=min_retention_days)
        return self._post("/api/v1/datasets", body)

    def get_dataset(self, dataset_id: str) -> dict[str, Any]:
        """One dataset with its retention state."""
        return self._get(f"/api/v1/datasets/{_seg(dataset_id)}")

    def update_dataset(
        self,
        dataset_id: str,
        description: str = "",
        retention_mode: Optional[str] = None,
        ttl_days: Optional[int] = None,
        review_by: Optional[str] = None,
        min_retention_days: Optional[int] = None,
        purpose: str = "",
    ) -> dict[str, Any]:
        """Change a dataset's settings (``dataset:write``). The server takes the settings whole: what you leave out
        is cleared, so read :meth:`get_dataset` and send back what should stay. Raising a regulated minimum is
        allowed; lowering it is :meth:`lower_dataset_minimum` (signed); a regulated dataset cannot leave regulated
        mode; a change that makes a dataset expire sooner needs ``dataset:purge``."""
        _check_retention_mode(retention_mode)
        body: dict[str, Any] = {"description": description, "purpose": purpose}
        _put_if(body, retention_mode=retention_mode, ttl_days=ttl_days, review_by=review_by, min_retention_days=min_retention_days)
        return self._put(f"/api/v1/datasets/{_seg(dataset_id)}", body)

    def list_dataset_cases(self, dataset_id: str, retired: bool = False, limit: Optional[int] = None, offset: Optional[int] = None) -> Any:
        """The cases of a dataset's draft. ``retired=True`` shows the retired ones."""
        return self._get(f"/api/v1/datasets/{_seg(dataset_id)}/cases" + _query(retired="true" if retired else None, limit=limit, offset=offset))

    def add_dataset_case(
        self,
        dataset_id: str,
        input: Any,
        expected_output: Any = None,
        metadata: Optional[Mapping[str, Any]] = None,
        provenance: Optional[Mapping[str, Any]] = None,
    ) -> dict[str, Any]:
        """Add a case to the draft (``dataset:write``). ``input`` is required (any JSON); ``metadata`` must be an
        object. ``provenance`` may carry ``source_trace_id``, ``span_id``, ``agent``, ``agent_version``,
        ``environment``, ``provider`` and ``model``."""
        body: dict[str, Any] = {"input": input, "expected_output": expected_output, "metadata": dict(metadata) if metadata is not None else None}
        if provenance:
            body["provenance"] = dict(provenance)
        return self._post(f"/api/v1/datasets/{_seg(dataset_id)}/cases", body)

    def add_dataset_case_from_trace(
        self,
        dataset_id: str,
        trace_id: str,
        span_id: str,
        expected_output: Any = _UNSET,
        metadata: Optional[Mapping[str, Any]] = None,
    ) -> dict[str, Any]:
        """Copy a span of a trace of this kitchen into a case, with the provenance the trace tells (agent, version,
        environment, model). The case is a copy: the trace is purged on its own schedule. The span's own output is
        the expected output unless you pass ``expected_output`` (``None`` included, which records none). Only the
        messages of the call are copied, not the whole capture. To add a replay's result, pass the replay's trace
        and span here."""
        body: dict[str, Any] = {"trace_id": trace_id, "span_id": span_id}
        if expected_output is not _UNSET:
            body["expected_output"] = expected_output
        if metadata is not None:
            body["metadata"] = dict(metadata)
        return self._post(f"/api/v1/datasets/{_seg(dataset_id)}/cases/from-trace", body)

    def retire_dataset_case(self, dataset_id: str, case_id: str) -> None:
        """Take a case out of the draft. Published versions still list it."""
        self._delete(f"/api/v1/datasets/{_seg(dataset_id)}/cases/{_seg(case_id)}")

    def erase_dataset_case(self, dataset_id: str, case_id: str, reason: str) -> dict[str, Any]:
        """Erase a case's payload (``dataset:purge``): input, expected output and metadata go, the content hash and
        the version manifests stay. Returns ``{"case", "already_erased"}``. A legal hold refuses it."""
        return self._post(f"/api/v1/datasets/{_seg(dataset_id)}/cases/{_seg(case_id)}/erase", {"reason": reason})

    def list_dataset_versions(self, dataset_id: str) -> list[dict[str, Any]]:
        """The published versions of a dataset."""
        return self._get(f"/api/v1/datasets/{_seg(dataset_id)}/versions")

    def get_dataset_version(self, dataset_id: str, version: int) -> dict[str, Any]:
        """One version with its manifest."""
        return self._get(f"/api/v1/datasets/{_seg(dataset_id)}/versions/{int(version)}")

    def verify_dataset_version(self, dataset_id: str, version: int) -> dict[str, Any]:
        """Recompute a version: its manifest digest, the chain to the version before and the content hashes."""
        return self._get(f"/api/v1/datasets/{_seg(dataset_id)}/versions/{int(version)}/verify")

    # ── Secrets (Pro, ADR-0035) ──────────────────────────────────────────────────
    # A secret is a name, a reference and metadata; its value lives in a provider (the built-in encrypted vault, or
    # Vault and the like) and is write-only: no method here returns one. Approving or revoking a binding and a real
    # migration are signed decisions: pass ``password=`` (the SDK signs it) or ``signing_token=``.

    def list_secret_providers(self) -> list[dict[str, Any]]:
        """The built-in provider and the configured ones, with what each can do and whether it is healthy."""
        return self._get("/api/v1/secret-providers")

    def create_secret_provider(
        self, name: str, kind: str, config: Optional[Mapping[str, Any]] = None, *, is_default: bool = False,
        platform: bool = False,
    ) -> dict[str, Any]:
        """Add a provider of a driver kind the platform enabled (``secret-provider:manage``). ``config`` can only
        narrow the platform's settings for the kind."""
        return self._post("/api/v1/secret-providers", {"name": name, "kind": kind, "config": dict(config or {}),
                                                       "is_default": is_default, "platform": platform})

    def set_default_secret_provider(self, name: str, *, platform: bool = False) -> None:
        """Make a provider the default for new secrets (``dbvault`` goes back to the built-in one)."""
        self._put(f"/api/v1/secret-providers/{_seg(name)}/default{'?platform=true' if platform else ''}", {})

    def delete_secret_provider(self, name: str, *, platform: bool = False) -> None:
        """Remove a provider no secret lives in (409 ``in_use`` otherwise)."""
        self._delete(f"/api/v1/secret-providers/{_seg(name)}{'?platform=true' if platform else ''}")

    def list_secrets(self) -> list[dict[str, Any]]:
        """The kitchen's secrets: metadata, never values (``secret:read``)."""
        return self._get("/api/v1/secrets")

    def get_secret(self, name: str) -> dict[str, Any]:
        return self._get(f"/api/v1/secrets/{_seg(name)}")

    def create_secret(
        self, name: str, *, value: Optional[str] = None, provider: Optional[str] = None, ref: Optional[str] = None,
        label: str = "", owner: str = "", allowed_hosts: Optional[list[str]] = None, expires_at: Optional[str] = None,
        require_binding: bool = False, grace_seconds: Optional[int] = None,
    ) -> dict[str, Any]:
        """Create a secret (``secret:write``). With ``value`` it is written to the provider (which must be writable);
        with ``ref`` and no value it links a secret somebody else owns; with neither it adopts a credential made
        before secrets existed. The value is never returned."""
        body: dict[str, Any] = {"name": name, "label": label, "owner": owner, "allowed_hosts": list(allowed_hosts or []),
                                "require_binding": require_binding}
        _put_if(body, value=value, provider=provider, ref=ref, expires_at=expires_at, grace_seconds=grace_seconds)
        return self._post("/api/v1/secrets", body)

    def update_secret(self, name: str, **patch: Any) -> dict[str, Any]:
        """Change a secret's metadata: ``label``, ``owner``, ``allowed_hosts``, ``expires_at``, ``clear_expiry``,
        ``require_binding``, ``grace_seconds``. What you leave out stays as it is."""
        return self._put(f"/api/v1/secrets/{_seg(name)}", patch)

    def delete_secret(self, name: str) -> None:
        self._delete(f"/api/v1/secrets/{_seg(name)}")

    def rotate_secret(
        self, name: str, value: str, *, probe: Optional[Mapping[str, Any]] = None, grace_seconds: Optional[int] = None
    ) -> dict[str, Any]:
        """Rotate to a new value. With ``probe`` (``{"url", "method", "header_scheme", "expect_status"}``) the control
        plane checks the new value against an upstream first; a value it rejects is not rotated in (422
        ``probe_rejected``). The previous version stays valid for the grace window."""
        body: dict[str, Any] = {"value": value}
        _put_if(body, probe=dict(probe) if probe else None, grace_seconds=grace_seconds)
        return self._put(f"/api/v1/secrets/{_seg(name)}/value", body)

    def refresh_secret(self, name: str) -> dict[str, Any]:
        """Pick up a rotation the secret's owner made (a linked secret). Returns ``{"changed", "secret"}``."""
        return self._post(f"/api/v1/secrets/{_seg(name)}/refresh", {})

    def secret_versions(self, name: str) -> list[dict[str, Any]]:
        """A secret's versions, without values."""
        return self._get(f"/api/v1/secrets/{_seg(name)}/versions")

    def list_secret_bindings(self, secret: Optional[str] = None) -> list[dict[str, Any]]:
        """Bindings in the kitchen, or of one secret."""
        return self._get("/api/v1/secret-bindings" + (f"?secret={_seg(secret)}" if secret else ""))

    def propose_secret_binding(self, secret: str, skill: str, server: str = "", header_scheme: str = "bearer") -> dict[str, Any]:
        """Propose that ``secret`` be sent for ``skill`` to ``server`` (``secret:bind``). ``skill="-"`` is a tool
        registered by hand. Nothing happens until a person approves."""
        return self._post(f"/api/v1/secrets/{_seg(secret)}/bindings",
                          {"skill": skill, "server": server, "header_scheme": header_scheme})

    def decide_secret_binding(
        self, binding_id: str, meaning: str, *, password: Optional[str] = None, signing_token: Optional[str] = None
    ) -> dict[str, Any]:
        """Approve (``approved``) or reject/revoke (``rejected``) a binding: a signed decision on the
        ``secret_binding`` gate, by someone other than the person who proposed it."""
        if meaning not in ("approved", "rejected"):
            raise ValueError("meaning is 'approved' or 'rejected'")
        headers = self._signing_headers(password, signing_token, "secret_binding", binding_id, None, meaning)
        return self._request("POST", f"/api/v1/secret-bindings/{_seg(binding_id)}/decision", {"meaning": meaning}, headers)

    def withdraw_secret_binding(self, binding_id: str) -> None:
        """Withdraw a binding that is still pending."""
        self._delete(f"/api/v1/secret-bindings/{_seg(binding_id)}")

    def plan_secret_migration(self, from_provider: str, to_provider: str) -> dict[str, Any]:
        """What moving the kitchen's secrets from one provider to another would do. Nothing changes."""
        return self._post("/api/v1/secrets/migrate", {"from": from_provider, "to": to_provider, "dry_run": True})

    def migrate_secrets(
        self, from_provider: str, to_provider: str, *, password: Optional[str] = None, signing_token: Optional[str] = None
    ) -> dict[str, Any]:
        """Move the kitchen's secrets from one provider to another (``secret:migrate``): copied, read back and
        compared, then repointed in one step; the source is untouched. A signed decision on the ``secret_migrate``
        gate, record ``<from>:<to>``, bound to the digest of the plan."""
        headers = self._signing_headers(password, signing_token, "secret_migrate", f"{from_provider}:{to_provider}")
        return self._request("POST", "/api/v1/secrets/migrate", {"from": from_provider, "to": to_provider}, headers)

    def publish_dataset(
        self, dataset_id: str, *, password: Optional[str] = None, signing_token: Optional[str] = None
    ) -> dict[str, Any]:
        """Publish the draft as the next version (``dataset:publish``). Returns ``{"version", "signed"}``. A
        regulated dataset's version is a signed decision (gate ``dataset_publish``, record id the dataset's id):
        pass ``password=`` or a ``signing_token=``; other datasets ignore it. Refused when the draft is empty
        (422 ``empty_version``) or unchanged (409 ``no_changes``)."""
        headers = self._signing_headers(password, signing_token, "dataset_publish", dataset_id)
        return self._request("POST", f"/api/v1/datasets/{_seg(dataset_id)}/versions", {}, headers)

    def export_dataset(self, dataset_id: str, version: Optional[int] = None) -> "DatasetExport":
        """Export a version (the latest by default) as JSONL (``dataset:export``): a manifest line with its digests
        and the result of verifying it, then one line per case in manifest order. An erased case is exported as
        erased, with no payload. The export is audited."""
        text, headers = self._request_text("GET", f"/api/v1/datasets/{_seg(dataset_id)}/export" + _query(version=version))
        lines = [json.loads(line) for line in text.splitlines() if line.strip()]
        manifest = lines[0] if lines else {}
        return DatasetExport(
            version=int(manifest.get("version") or 0),
            manifest_digest=headers.get("x-manifest-digest", manifest.get("manifest_digest", "")),
            manifest=manifest,
            cases=lines[1:],
            text=text,
        )

    def lower_dataset_minimum(
        self,
        dataset_id: str,
        min_retention_days: int,
        reason: str,
        *,
        password: Optional[str] = None,
        signing_token: Optional[str] = None,
    ) -> dict[str, Any]:
        """Lower a regulated dataset's minimum retention (``dataset:purge``). A signed decision on the
        ``dataset_purge`` gate (record id the dataset's id, payload ``{"action": "lower_minimum",
        "new_minimum_days", "reason"}``): pass ``password=`` or ``signing_token=``. The server refuses a minimum
        below its floor (7 days) or a missing reason before anyone is asked to sign."""
        payload = {"action": "lower_minimum", "new_minimum_days": min_retention_days, "reason": reason}
        headers = self._signing_headers(password, signing_token, "dataset_purge", dataset_id, payload)
        return self._request(
            "POST", f"/api/v1/datasets/{_seg(dataset_id)}/retention/lower-minimum",
            {"min_retention_days": min_retention_days, "reason": reason}, headers,
        )

    def place_dataset_legal_hold(self, dataset_id: str, reason: str) -> dict[str, Any]:
        """Place a legal hold on a dataset (``dataset:purge`` or ``audit:legalhold``). Placing only protects, so it
        takes no signature. While it is held, purge and erasure are refused."""
        return self._put(f"/api/v1/datasets/{_seg(dataset_id)}/legal-hold", {"held": True, "reason": reason})

    def lift_dataset_legal_hold(
        self, dataset_id: str, reason: str, *, password: Optional[str] = None, signing_token: Optional[str] = None
    ) -> dict[str, Any]:
        """Lift a legal hold on a dataset. Only the Legal role may, with a stated reason, and the lift is a signed
        decision on the ``legal_hold_release`` gate: record id ``dataset:<id>``, payload ``{"reason": reason}``.
        Pass ``password=`` (the SDK signs it, :meth:`sign`) or a ``signing_token=`` signed for exactly this."""
        headers = self._signing_headers(password, signing_token, "legal_hold_release", f"dataset:{dataset_id}", {"reason": reason})
        return self._request("PUT", f"/api/v1/datasets/{_seg(dataset_id)}/legal-hold", {"held": False, "reason": reason}, headers)

    def purge_dataset(
        self, dataset_id: str, reason: str, *, password: Optional[str] = None, signing_token: Optional[str] = None
    ) -> dict[str, Any]:
        """Archive a dataset, then delete it, leaving a tombstone (``dataset:purge``). Returns ``{"tombstone",
        "signed"}``. A regulated dataset's purge is a signed decision on the ``dataset_purge`` gate (record id the
        dataset's id, payload ``{"action": "purge", "reason"}``), refused inside its minimum before anyone is asked
        to sign; pass ``password=`` or ``signing_token=`` for it. Other datasets ignore them."""
        headers = self._signing_headers(password, signing_token, "dataset_purge", dataset_id, {"action": "purge", "reason": reason})
        return self._request("POST", f"/api/v1/datasets/{_seg(dataset_id)}/purge", {"reason": reason}, headers)

    def list_expiring_datasets(self, days: Optional[int] = None) -> list[dict[str, Any]]:
        """The datasets the retention janitor removes within ``days`` (30 by default)."""
        return self._get("/api/v1/datasets/expiring" + _query(days=days))

    def list_dataset_reviews(self, days: Optional[int] = None) -> list[dict[str, Any]]:
        """The ``persist`` datasets due for review within ``days`` (30 by default)."""
        return self._get("/api/v1/datasets/reviews" + _query(days=days))

    def list_dataset_tombstones(self) -> list[dict[str, Any]]:
        """What stays of purged datasets: who purged, when, why, and the final manifest digest."""
        return self._get("/api/v1/datasets/tombstones")

    # ── Legal hold on the audit trail (Pro, ADR-0043) ─────────────────────

    def place_audit_legal_hold(self, reason: str = "") -> dict[str, Any]:
        """Place a legal hold on the kitchen's audit trail (``audit:legalhold``). It only protects, so it takes no
        signature. While it is in force retention purge and GDPR erasure are refused."""
        return self._post("/api/v1/audit/legal-hold", {"hold": True, "reason": reason})

    def lift_audit_legal_hold(
        self, reason: str, *, password: Optional[str] = None, signing_token: Optional[str] = None
    ) -> dict[str, Any]:
        """Lift the legal hold on the kitchen's audit trail. Only the Legal role may, with a stated reason, and the
        lift is a signed decision on the ``legal_hold_release`` gate: record id ``audit:<kitchen>`` (this client's
        kitchen), payload ``{"reason": reason}``. Pass ``password=`` or a ``signing_token=``."""
        headers = self._signing_headers(password, signing_token, "legal_hold_release", f"audit:{self._kitchen}", {"reason": reason})
        return self._request("POST", "/api/v1/audit/legal-hold", {"hold": False, "reason": reason}, headers)

    # ── Budgets, rate limits and the response cache (Pro, ADR-0051) ───────

    def list_budgets(self) -> list[dict[str, Any]]:
        """The kitchen's period budgets with what is left: ``scope`` (``kitchen``, ``provider``, ``agent`` or
        ``api_key``), ``unit`` (``usd`` or ``tokens``), ``amount``, ``period``, ``consumed``, ``reserved``,
        ``remaining``, ``used_pct``, ``exhausted``, ``reset_at`` and the ``thresholds``. Needs ``cost:read``."""
        return self._get("/api/v1/spend/budgets")["budgets"]

    def set_budget(
        self,
        amount: float,
        *,
        unit: str = "usd",
        period: Optional[str] = None,
        provider: Optional[str] = None,
        agent: Optional[str] = None,
        api_key: Optional[str] = None,
        thresholds: Optional[Sequence[Mapping[str, Any]]] = None,
        password: Optional[str] = None,
        signing_token: Optional[str] = None,
    ) -> dict[str, Any]:
        """Create or change a period budget (``budget:extend``, the finance decision). With none of ``provider``,
        ``agent`` and ``api_key`` it caps the whole kitchen; with one, only that provider's, agent's or credential's
        calls (``api_key`` is the credential's subject, for example ``scopedkey:<id>``). ``unit`` is ``"usd"`` or
        ``"tokens"`` (a whole number), ``period`` one of ``BUDGET_PERIODS`` (monthly when left out).
        ``thresholds`` is a list of ``{"pct": 80, "action": "alert" | "block"}``; the default warns at 50 and 80
        percent and stops at 100.

        Creating a budget or lowering one is not a decision. **Raising** one (a higher amount, a different period,
        a stop that moves up or goes away) is a signed decision on the ``budget_raise`` gate: record id
        ``<scope>:<name>:<unit>`` (see :func:`budget_record_id`), payload this request's body. Pass ``password=``
        (the SDK signs it, :meth:`sign`) or a ``signing_token=`` signed for exactly this; without one the server
        answers ``signature_required`` when signing is on. Returns the budget as :meth:`list_budgets` shows it."""
        ref = _budget_ref(provider, agent, api_key, unit)
        if period is not None and period not in BUDGET_PERIODS:
            raise ValueError(f"period must be one of {', '.join(BUDGET_PERIODS)}")
        body: dict[str, Any] = {**ref, "amount": amount}
        if period is not None:
            body["period"] = period
        if thresholds is not None:
            for t in thresholds:
                if t.get("action") not in ("alert", "block"):
                    raise ValueError('a threshold\'s action is "alert" or "block"')
            body["thresholds"] = [{"pct": t["pct"], "action": t["action"]} for t in thresholds]
        headers = self._signing_headers(password, signing_token, "budget_raise", budget_record_id(provider, agent, api_key, unit), body)
        return self._request("PUT", "/api/v1/spend/budgets", body, headers)

    def remove_budget(
        self,
        *,
        unit: str = "usd",
        provider: Optional[str] = None,
        agent: Optional[str] = None,
        api_key: Optional[str] = None,
        password: Optional[str] = None,
        signing_token: Optional[str] = None,
    ) -> None:
        """Remove a budget (``budget:extend``). Removing lifts its limit, so it is signed like a raise: the
        ``budget_raise`` gate, payload ``{"delete": true}``; pass ``password=`` or ``signing_token=``. Its ledger
        entries stay. Raises :class:`AgentOvenAPIError` (404) if there is no such budget."""
        ref = _budget_ref(provider, agent, api_key, unit)
        headers = self._signing_headers(password, signing_token, "budget_raise", budget_record_id(provider, agent, api_key, unit), {"delete": True})
        self._request("DELETE", "/api/v1/spend/budgets" + _query(**ref), None, headers)

    def list_budget_ledger(
        self, budget_id: Optional[str] = None, since: Optional[str] = None, before: Optional[int] = None, limit: Optional[int] = None
    ) -> list[dict[str, Any]]:
        """The ledger, newest first: one entry per model call per budget it was charged to (agent, API key,
        provider, model, tokens, cost). ``since`` is an RFC 3339 time, ``before`` an entry id to page from."""
        return self._get("/api/v1/spend/ledger" + _query(budget_id=budget_id, since=since, before=before, limit=limit))["entries"]

    def list_budget_alerts(self, limit: Optional[int] = None) -> list[dict[str, Any]]:
        """The thresholds the kitchen's budgets have crossed, newest first. ``notified`` is false for an alert the
        notification channels were not told about (the kitchen was throttled); it is on record all the same."""
        return self._get("/api/v1/spend/alerts" + _query(limit=limit))["alerts"]

    def budget_usage(self, group: str = "agent", days: Optional[int] = None, limit: Optional[int] = None) -> list[dict[str, Any]]:
        """What the kitchen's model calls amounted to over the last ``days`` (30 by default), by ``group``
        (``"agent"``, ``"model"``, ``"provider"`` or ``"day"``): calls, tokens, ``cost_usd`` and the response
        cache's ``cache_hits``, ``cache_misses`` and ``cache_saved_usd``. Biggest spender first."""
        if group not in ("agent", "model", "provider", "day"):
            raise ValueError('group is "agent", "model", "provider" or "day"')
        return self._get("/api/v1/spend/usage" + _query(group=group, days=days, limit=limit))["usage"]

    def list_limits(self) -> list[dict[str, Any]]:
        """The kitchen's rate limits: ``scope``, ``ref``, ``requests_per_minute``, ``tokens_per_minute``,
        ``invocations_per_minute`` (0 is not limited) and ``rejected`` (requests it refused since the server
        process started). Needs ``cost:read``."""
        return self._get("/api/v1/limits")["limits"]

    def set_limit(
        self,
        scope: str,
        ref: str = "",
        *,
        requests_per_minute: int = 0,
        tokens_per_minute: int = 0,
        invocations_per_minute: int = 0,
    ) -> dict[str, Any]:
        """Create or replace a rate limit (``limit:manage``). ``scope`` is one of ``LIMIT_SCOPES``; ``ref`` names the
        agent, credential subject or provider (``"*"`` gives each its own bucket) and is empty for the kitchen.
        ``requests_per_minute`` counts model calls, ``tokens_per_minute`` the tokens they use and
        ``invocations_per_minute`` agent invocations at the API (not for a provider). A caller over a limit gets
        429 with ``Retry-After`` (:class:`RateLimitedError`)."""
        if scope not in LIMIT_SCOPES:
            raise ValueError(f"scope must be one of {', '.join(LIMIT_SCOPES)}")
        if not (requests_per_minute or tokens_per_minute or invocations_per_minute):
            raise ValueError("a limit sets at least one of requests_per_minute, tokens_per_minute and invocations_per_minute (remove_limit removes it)")
        return self._put("/api/v1/limits", {
            "scope": scope, "ref": ref, "requests_per_minute": requests_per_minute,
            "tokens_per_minute": tokens_per_minute, "invocations_per_minute": invocations_per_minute,
        })

    def remove_limit(self, scope: str, ref: str = "") -> None:
        """Remove a rate limit (``limit:manage``). Raises :class:`AgentOvenAPIError` (404) if there is none."""
        self._delete("/api/v1/limits" + _query(scope=scope, ref=ref))

    def get_cache(self) -> dict[str, Any]:
        """The response cache of this kitchen: ``policies`` (the kitchen's own has an empty ``agent``) and
        ``stats`` (entries, bytes, hits, misses, stores and why requests were not cached, for the server process
        you reached). The hit rate over time is in :meth:`budget_usage`."""
        return self._get("/api/v1/cache")

    def set_cache_policy(
        self, mode: str, *, agent: str = "", ttl_seconds: Optional[int] = None, share_across_principals: bool = False
    ) -> dict[str, Any]:
        """Turn the response cache on, off or deterministic for the kitchen (``agent=""``) or one agent
        (``cache:manage``). ``mode`` is one of ``CACHE_MODES``: ``"deterministic"`` caches requests that ask for
        ``temperature`` 0, ``"always"`` any eligible request. An agent's policy replaces the kitchen's for that
        agent. Entries are kept per caller unless ``share_across_principals`` is true. Setting a policy forgets
        what was stored under the old one."""
        if mode not in CACHE_MODES:
            raise ValueError(f"mode must be one of {', '.join(CACHE_MODES)}")
        body: dict[str, Any] = {"agent": agent, "mode": mode, "share_across_principals": share_across_principals}
        if ttl_seconds is not None:
            body["ttl_seconds"] = ttl_seconds
        return self._put("/api/v1/cache/policy", body)

    def remove_cache_policy(self, agent: str = "") -> None:
        """Remove a cache policy (back to the kitchen's, or to no caching) (``cache:manage``)."""
        self._delete("/api/v1/cache/policy" + _query(agent=agent))

    def purge_cache(self, agent: str = "") -> dict[str, Any]:
        """Forget everything stored for the kitchen, or for one agent, on every replica (``cache:manage``).
        Returns ``{"purged_at": ...}``."""
        return self._post("/api/v1/cache/purge", {"agent": agent} if agent else {})

    # ── Identity-provider bindings, credential owners, my kitchens (Pro, ADR-0042) ──

    def list_kitchen_bindings(self, kitchen_id: str) -> list[dict[str, Any]]:
        """Which group or app role of the identity provider is which role in this kitchen. A binding takes effect
        at a person's next login. Only the kitchen's Head Chef or a Restaurateur."""
        return self._get(f"/api/v1/kitchens/{_seg(kitchen_id)}/bindings")

    def create_kitchen_binding(self, kitchen_id: str, claim_value: str, role: str, label: str = "") -> dict[str, Any]:
        """Bind an identity-provider group (its object id) or app role value to a kitchen ``role`` (one of
        ``KITCHEN_ROLES``; ``restaurateur`` is platform-wide and ``admin`` is retired). Binding a group as
        ``head_chef`` is a Restaurateur's decision."""
        _check_role(role, KITCHEN_ROLES, "a kitchen binding")
        if not claim_value.strip():
            raise ValueError("claim_value is the group object id or app role value the identity provider puts in the token")
        return self._post(f"/api/v1/kitchens/{_seg(kitchen_id)}/bindings", {"claim_value": claim_value, "role": role, "label": label})

    def delete_kitchen_binding(self, kitchen_id: str, binding_id: str) -> None:
        """Remove a binding."""
        self._delete(f"/api/v1/kitchens/{_seg(kitchen_id)}/bindings/{_seg(binding_id)}")

    def set_service_account_owner(self, sa_id: str, owner_email: str) -> dict[str, Any]:
        """Give a service account to another person: a member of the kitchen at a rank that covers the account's
        role. Handing it to someone else is a Head Chef's act. It is how a credential whose owner left works again."""
        return self._post(f"/api/v1/service-accounts/{_seg(sa_id)}/owner", {"owner_email": owner_email})

    def set_scoped_key_owner(self, key_id: str, owner_email: str) -> dict[str, Any]:
        """Give a scoped API key to another person (same rules as :meth:`set_service_account_owner`)."""
        return self._post(f"/api/v1/keys/{_seg(key_id)}/owner", {"owner_email": owner_email})

    @property
    def documents(self) -> "Documents":
        """Read a file's text and tables, transcribe scans, extract fields with their quotes (``client.documents.extract(path)``)."""
        from agentoven.documents import Documents

        return Documents(self)

    @property
    def blobs(self) -> "Blobs":
        """Upload a file once and name it in messages by ``blob_ref`` (``client.blobs.upload(path)``)."""
        from agentoven.blobs import Blobs

        return Blobs(self)

    def my_kitchens(self) -> dict[str, Any]:
        """The kitchens you can enter and your role in each: ``{"enforced", "kitchens": [{"id", "name", "role"}]}``."""
        return self._get("/api/v1/me/kitchens")

    # ── Agent versions ─────────────────────────────────────────────────────
    # An agent's version moves only on a bake whose configuration differs from the last bake's: the first bake
    # keeps the registered version (0.1.0), a bake that changes nothing keeps its version, and a changed one takes
    # the next minor version and keeps the old one. Editing a registered agent (``register``, ``PUT``) does not
    # bump it. Naming a ``version`` on ``bake`` sets that version as given.

    def list_agent_versions(self, name: str) -> list[dict[str, Any]]:
        """The versions an agent has been baked at, each as the agent was configured then."""
        return self._get(f"/api/v1/agents/{_seg(name)}/versions")

    def get_agent_version(self, name: str, version: str) -> dict[str, Any]:
        """One baked version of an agent (``0.1.0`` is the first bake)."""
        return self._get(f"/api/v1/agents/{_seg(name)}/versions/{_seg(version)}")

    # ── Server info ───────────────────────────────────────────────────────

    def server_info(self) -> dict[str, Any]:
        """Return edition, plan, features, and license metadata."""
        return self._get("/api/v1/info")

    def license_status(self) -> dict[str, Any]:
        """Return detailed license status from the Pro endpoint."""
        return self._get("/api/v1/license/status")

    def license_phone_home(self) -> dict[str, Any]:
        """Trigger and return license phone-home status (Pro)."""
        return self._get("/api/v1/license/phone-home")

    # ── Providers & modalities ────────────────────────────────────────────
    # Modalities are a property of the provider: an agent has exactly the
    # modalities of its own provider. See :mod:`agentoven.modalities`.

    def list_providers(self) -> list[Provider]:
        """List the kitchen's model providers, each with its effective ``modalities``."""
        return [Provider.from_dict(p) for p in self._get("/api/v1/models/providers")]

    def get_provider(self, name: str) -> Provider:
        return Provider.from_dict(self._get(f"/api/v1/models/providers/{_seg(name)}"))

    def create_provider(
        self,
        name: str,
        kind: str,
        api_key: Optional[str] = None,
        endpoint: Optional[str] = None,
        models: Optional[Sequence[str]] = None,
        config: Optional[Mapping[str, Any]] = None,
        modalities: Union[Modalities, Mapping[str, Any], None] = None,
        is_default: bool = False,
    ) -> Provider:
        """Register a model provider.

        ``modalities`` takes a :class:`~agentoven.Modalities` (or the raw
        ``config.modalities`` dict) and is validated before the request is sent::

            client.create_provider(
                "openai", "openai", api_key="sk-...", models=["gpt-4o"],
                modalities=Modalities(pdf=False, audio=AudioConfig(stt_model="whisper-1")),
            )
        """
        cfg = _provider_config(config, api_key, modalities, allow_clear=False)
        body: dict[str, Any] = {
            "name": name,
            "kind": kind,
            "models": list(models or []),
            "config": cfg,
            "is_default": is_default,
        }
        if endpoint:
            body["endpoint"] = endpoint
        return Provider.from_dict(self._post("/api/v1/models/providers", body))

    def update_provider(
        self,
        name: str,
        kind: Optional[str] = None,
        endpoint: Optional[str] = None,
        models: Optional[Sequence[str]] = None,
        config: Optional[Mapping[str, Any]] = None,
        api_key: Optional[str] = None,
        modalities: Union[Modalities, Mapping[str, Any], None] = None,
        is_default: Optional[bool] = None,
    ) -> ProviderUpdate:
        """Update a provider. Only what you pass changes.

        ``config`` keys are merged into the stored config, and ``modalities`` is
        merged per modality: set what you pass, leave the rest. Use
        :data:`~agentoven.CLEAR` as a value to delete a setting (or a whole
        modality) and fall back to the default::

            client.update_provider("openai", modalities=Modalities(pdf=CLEAR))

        The server overwrites ``is_default`` on every update, so when you do not
        pass it the current value is read first and sent back unchanged (one
        extra GET) — otherwise any update would silently un-default the provider.
        """
        body: dict[str, Any] = {}
        if kind is not None:
            body["kind"] = kind
        if endpoint is not None:
            body["endpoint"] = endpoint
        if models is not None:
            body["models"] = list(models)
        cfg = _provider_config(config, api_key, modalities, allow_clear=True)
        if cfg:
            body["config"] = cfg
        if is_default is None:
            is_default = self.get_provider(name).is_default
        body["is_default"] = is_default
        data = self._put(f"/api/v1/models/providers/{_seg(name)}", body)
        return ProviderUpdate(
            provider=Provider.from_dict(data.get("provider") or {}),
            agents_burnt=int(data.get("agents_burnt", 0) or 0),
        )

    # ── Tools (MCP gateway) ───────────────────────────────────────────────

    def list_tools(self) -> list[dict[str, Any]]:
        """The tools registered in this kitchen. A tool's authentication
        (``auth_config``) is never returned."""
        return self._get("/api/v1/tools") or []

    def get_tool(self, name: str) -> dict[str, Any]:
        """One tool by name. Raises :class:`AgentOvenAPIError` (404) if there is none."""
        return self._get(f"/api/v1/tools/{_seg(name)}")

    def register_tool(
        self,
        name: str,
        endpoint: str,
        *,
        description: str = "",
        transport: str = "http",
        schema: Optional[Mapping[str, Any]] = None,
        auth_config: Optional[Mapping[str, Any]] = None,
        capabilities: Optional[Sequence[str]] = None,
        enabled: bool = True,
    ) -> dict[str, Any]:
        """Register a tool agents can call, or update it if the name is taken.

        An HTTP tool::

            client.register_tool(
                "refunds",
                "https://payments.internal/refund",
                description="Pay a refund",
                schema={"type": "object", "properties": {"amount": {"type": "number"}}},
            )

        ``transport`` is ``"http"`` (the default), ``"sse"`` or ``"stdio"``; the
        server rejects anything else with a 400. ``schema`` is the JSON Schema of
        the tool's arguments. ``auth_config`` holds how the gateway
        authenticates to the tool (the server stores it and never returns it).
        ``capabilities`` defaults to ``["tool"]``.

        Registering a name that exists updates that tool (the server's update
        replaces ``enabled`` every time, so it is always sent: ``enabled=True``
        by default, ``enabled=False`` registers the tool switched off). The
        lookup is one extra GET. Returns the stored tool.
        """
        body: dict[str, Any] = {
            "name": name,
            "endpoint": endpoint,
            "description": description,
            "transport": transport,
            "enabled": enabled,
        }
        if schema is not None:
            body["schema"] = dict(schema)
        if auth_config is not None:
            body["auth_config"] = dict(auth_config)
        if capabilities is not None:
            body["capabilities"] = list(capabilities)
        try:
            self.get_tool(name)
        except AgentOvenAPIError as exc:
            if exc.status_code != 404:
                raise
            return self._post("/api/v1/tools", body)
        return self._put(f"/api/v1/tools/{_seg(name)}", body)

    def delete_tool(self, name: str) -> None:
        """Remove a tool from the kitchen."""
        self._delete(f"/api/v1/tools/{_seg(name)}")

    # ── Tool packs (Pro, ADR-0048) ────────────────────────────────────────
    # A pack is a named, versioned set of governed tools for one kind of system (read-only PostgreSQL, email and
    # Slack, or any REST API imported from an OpenAPI document). An installer supplies only secret references
    # (``vault://name``); a pack's write tools wait for a person's approval by default.

    def list_tool_pack_catalog(self) -> list[dict[str, Any]]:
        """The packs this server offers, with the secrets, settings and tools of each (``toolpack:read``)."""
        return (self._get("/api/v1/tool-packs/catalog") or {}).get("packs", [])

    def list_tool_packs(self) -> list[dict[str, Any]]:
        """The packs installed in this kitchen, each with its tools, their switches and its approval status."""
        return (self._get("/api/v1/tool-packs") or {}).get("installs", [])

    def get_tool_pack(self, name: str) -> dict[str, Any]:
        return self._get(f"/api/v1/tool-packs/{_seg(name)}")

    def install_tool_pack(
        self, pack: str, secrets: Mapping[str, str], *, name: Optional[str] = None,
        settings: Optional[Mapping[str, Any]] = None, tools: Optional[Mapping[str, Mapping[str, bool]]] = None,
    ) -> dict[str, Any]:
        """Install a catalog pack (``toolpack:install``). ``secrets`` binds each secret role to a ``vault://``
        reference. ``tools`` maps a tool's local name to ``{"enabled": bool, "require_approval": bool}``; switching
        a tool's approval off also needs ``toolpack:govern``. With skill approval required the install waits for
        people (``status`` is ``awaiting_approval``)."""
        body: dict[str, Any] = {"pack": pack, "secrets": dict(secrets)}
        _put_if(body, name=name, settings=dict(settings) if settings is not None else None,
                tools={k: dict(v) for k, v in tools.items()} if tools is not None else None)
        return self._post("/api/v1/tool-packs", body)

    def upgrade_tool_pack(
        self, name: str, *, secrets: Optional[Mapping[str, str]] = None, settings: Optional[Mapping[str, Any]] = None,
    ) -> dict[str, Any]:
        """Move an install to the catalog's current version. Per-tool switches carry over."""
        body: dict[str, Any] = {}
        _put_if(body, secrets=dict(secrets) if secrets is not None else None,
                settings=dict(settings) if settings is not None else None)
        return self._post(f"/api/v1/tool-packs/{_seg(name)}/upgrade", body)

    def rebind_tool_pack_secrets(self, name: str, secrets: Mapping[str, str]) -> dict[str, Any]:
        """Point an install at other ``vault://`` references."""
        return self._put(f"/api/v1/tool-packs/{_seg(name)}/secrets", {"secrets": dict(secrets)})

    def check_tool_pack(self, name: str) -> dict[str, Any]:
        """A connectivity check where one is possible (``{"ok": true}`` or ``{"ok": false, "message": ...}``)."""
        return self._post(f"/api/v1/tool-packs/{_seg(name)}/check", {})

    def set_tool_pack_tool(
        self, pack: str, tool: str, *, enabled: Optional[bool] = None, require_approval: Optional[bool] = None,
    ) -> dict[str, Any]:
        """Switch one tool of an install on or off, or its approval on or off (``toolpack:govern``)."""
        body: dict[str, Any] = {}
        _put_if(body, enabled=enabled, require_approval=require_approval)
        return self._request("PATCH", f"/api/v1/tool-packs/{_seg(pack)}/tools/{_seg(tool)}", body)

    def uninstall_tool_pack(self, name: str) -> None:
        """Remove an install, its tools and its skill (``toolpack:remove``)."""
        self._delete(f"/api/v1/tool-packs/{_seg(name)}")

    def preview_openapi(self, *, url: Optional[str] = None, spec: Optional[str] = None) -> dict[str, Any]:
        """List the operations of an OpenAPI 3 document, each classified read or write, without importing. Give the
        document's ``url`` (the server fetches it, subject to the kitchen's egress allowlist) or its text as ``spec``."""
        body: dict[str, Any] = {}
        _put_if(body, url=url, spec=spec)
        return self._post("/api/v1/tool-packs/openapi/preview", body)

    def import_openapi(
        self, name: str, *, url: Optional[str] = None, spec: Optional[str] = None, base_url: Optional[str] = None,
        auth: Optional[Mapping[str, str]] = None, operations: Optional[Sequence[str]] = None,
        secrets: Optional[Mapping[str, str]] = None, tools: Optional[Mapping[str, Mapping[str, bool]]] = None,
        replace: bool = False,
    ) -> dict[str, Any]:
        """Import an OpenAPI 3 document as a pack of governed tools, one per operation in ``operations`` (default:
        every read operation; a write is imported only when named and waits for approval). ``base_url`` pins the
        API's address (default: the document's first server). ``auth`` is ``{"type": "bearer" | "header" | "query" |
        "basic" | "oauth2_client_credentials" | "none", "name": ..., "token_url": ..., "scopes": ...}`` (default: what
        the document says), and ``secrets`` binds the roles that type needs to ``vault://`` references. ``replace``
        imports a new document over an existing install of the same name."""
        body: dict[str, Any] = {"name": name, "replace": replace}
        _put_if(body, url=url, spec=spec, base_url=base_url, auth=dict(auth) if auth is not None else None,
                operations=list(operations) if operations is not None else None,
                secrets=dict(secrets) if secrets is not None else None,
                tools={k: dict(v) for k, v in tools.items()} if tools is not None else None)
        return self._post("/api/v1/tool-packs/openapi/import", body)

    def list_egress_hosts(self) -> dict[str, Any]:
        """Hosts the kitchen allowlisted for tool packs although they resolve to private addresses."""
        return self._get("/api/v1/tool-packs/egress")

    def allow_egress_host(self, host: str) -> None:
        """Allowlist a host for the kitchen's packs (``toolpack:govern``). Cloud metadata addresses stay refused."""
        self._post("/api/v1/tool-packs/egress", {"host": host})

    def remove_egress_host(self, host: str) -> None:
        self._delete(f"/api/v1/tool-packs/egress/{_seg(host)}")

    # ── Agent card ────────────────────────────────────────────────────────

    def agent_card(self, name: str) -> AgentCard:
        """The agent's A2A card. ``card.modalities`` lists what its provider
        offers and ``card.supports("audio")`` answers for one modality."""
        return AgentCard.from_dict(self._get(f"/api/v1/agents/{_seg(name)}/card"))

    def supports(self, agent: str, modality: str) -> bool:
        """Shorthand for ``client.agent_card(agent).supports(modality)``."""
        return self.agent_card(agent).supports(modality)

    # ── Invoke (text and cascaded voice) ──────────────────────────────────

    def invoke(
        self,
        agent: str,
        message: Optional[str] = None,
        *,
        audio: Union[AudioInput, bytes, str, "os.PathLike[str]", None] = None,
        audio_mime_type: Optional[str] = None,
        voice_output: bool = False,
        voice: Optional[str] = None,
        attachments: Optional[Sequence[Attachment]] = None,
        variables: Optional[Mapping[str, str]] = None,
        session_id: Optional[str] = None,
        thinking_enabled: bool = False,
    ) -> InvokeResult:
        """Run a managed agent once.

        Cascaded voice — needs the ``audio`` modality on the agent's provider::

            result = client.invoke(
                "support-agent",
                audio="question.wav",      # path, bytes, Path or AudioInput
                voice_output=True,
                voice="alloy",
            )
            print(result.transcript)       # what the agent heard
            print(result.response)         # its text reply
            result.save_audio("answer")    # -> answer.mp3

        ``audio`` and ``message`` can be combined (the transcript is appended to
        the message). For raw ``bytes`` of a format the SDK cannot recognise,
        pass ``audio_mime_type``. Raises :class:`AudioModalityError` when the
        agent's provider does not offer audio.

        ``attachments`` send images, PDFs or video with the message (build them
        with :meth:`Attachment.from_path`, ``from_bytes`` or ``from_url``); the
        server answers 422 when the agent's model cannot take that kind of input.
        """
        if not message and audio is None and not attachments:
            raise ValueError("invoke needs a message, audio, or both (or attachments)")
        if voice and not voice_output:
            raise ValueError("voice only applies with voice_output=True")
        body: dict[str, Any] = {}
        if message:
            body["message"] = message
        if audio is not None:
            body["audio"] = AudioInput.coerce(audio, audio_mime_type).to_wire()
        if attachments:
            body["content_parts"] = [a.to_content_part() for a in attachments]
        if voice_output:
            body["voice_output"] = True
        if voice:
            body["voice"] = voice
        if variables:
            body["variables"] = dict(variables)
        if session_id:
            body["session_id"] = session_id
        if thinking_enabled:
            body["thinking_enabled"] = True
        return InvokeResult.from_dict(self._post(f"/api/v1/agents/{_seg(agent)}/invoke", body))

    # ── Agent sessions & attachments ──────────────────────────────────────
    # Multi-turn conversations on an agent (POST /agents/{name}/sessions). Not
    # to be confused with the Pro session directory behind create_session().

    def create_agent_session(
        self,
        agent: str,
        max_turns: Optional[int] = None,
        metadata: Optional[Mapping[str, Any]] = None,
    ) -> dict[str, Any]:
        """Start a conversation with a ready agent. Returns the session (``id``...)."""
        body: dict[str, Any] = {}
        if max_turns:
            body["max_turns"] = max_turns
        if metadata:
            body["metadata"] = dict(metadata)
        return self._post(f"/api/v1/agents/{_seg(agent)}/sessions", body)

    def send_session_message(
        self,
        agent: str,
        session_id: str,
        content: str = "",
        attachments: Optional[Sequence[Attachment]] = None,
        prompt_vars: Optional[Mapping[str, str]] = None,
        metadata: Optional[Mapping[str, Any]] = None,
    ) -> dict[str, Any]:
        """Send a turn, optionally with images, PDFs, audio or video.

        ``attachments`` become typed ``content_parts``; build them with
        :meth:`Attachment.from_path`, ``from_bytes`` or ``from_url``. Audio
        attachments are transcribed first and need the ``audio`` modality;
        images, PDFs and video need ``image`` / ``pdf`` / ``video``. Returns the
        session response (``content``, ``turn_number``, ``usage``...).
        """
        if not content and not attachments:
            raise ValueError("a session message needs content, attachments, or both")
        body: dict[str, Any] = {"content": content}
        if attachments:
            body["content_parts"] = [a.to_content_part() for a in attachments]
        if prompt_vars:
            body["prompt_vars"] = dict(prompt_vars)
        if metadata:
            body["metadata"] = dict(metadata)
        return self._post(
            f"/api/v1/agents/{_seg(agent)}/sessions/{_seg(session_id)}/messages", body
        )

    # ── Realtime voice (Pro) ──────────────────────────────────────────────

    def realtime_token(self, agent: str, ttl_seconds: int = 60) -> "RealtimeToken":
        """Mint a short-lived token a *browser* can use to open the realtime socket.

        Browsers cannot set headers on a WebSocket, so a backend that holds the
        API key calls this and hands the token to the page, which connects with
        the ``agentoven.v1`` / ``agentoven-token.<token>`` subprotocols (see the
        TypeScript ``connectRealtime({ token })``). The token is bound to the
        agent and kitchen, is only checked when the socket opens, and expires in
        ``ttl_seconds`` (1-300).
        """
        if isinstance(ttl_seconds, bool) or not isinstance(ttl_seconds, int) or not 1 <= ttl_seconds <= 300:
            raise ValueError("ttl_seconds must be a whole number of seconds between 1 and 300")
        data = self._post(f"/api/v1/realtime/{_seg(agent)}/token", {"ttl_seconds": ttl_seconds})
        return RealtimeToken(token=data.get("token", ""), expires_in=int(data.get("expires_in", 0) or 0))

    def realtime(
        self,
        agent: str,
        voice: Optional[str] = None,
        model: Optional[str] = None,
        token: Optional[str] = None,
    ) -> "RealtimeConnection":
        """Open a realtime voice session with ``agent``.

        Returns an async context manager yielding a ``RealtimeSession``. See
        :mod:`agentoven.realtime`. Requires ``pip install "agentoven[realtime]"``.

        The agent always runs on its own provider, which must offer the
        ``realtime`` modality. By default the call authenticates with this
        client's API key and kitchen headers. Pass ``token`` (from
        :meth:`realtime_token`) to connect the way a browser does: no auth
        headers, the token offered as the ``agentoven-token.<token>`` WebSocket
        subprotocol.
        """
        from agentoven.realtime import (
            TOKEN_SUBPROTOCOL_PREFIX,
            WS_SUBPROTOCOL,
            RealtimeConnection,
            check_subprotocol_token,
            realtime_url,
        )

        url = realtime_url(self._url, agent, voice, model)
        if token:
            check_subprotocol_token(token)
            return RealtimeConnection(
                url, {}, subprotocols=[WS_SUBPROTOCOL, TOKEN_SUBPROTOCOL_PREFIX + token]
            )
        headers = {"X-Kitchen": self._kitchen, "X-Kitchen-Id": self._kitchen}
        if self._api_key:
            headers["Authorization"] = f"Bearer {self._api_key}"
        return RealtimeConnection(url, headers)

    # ── Internal HTTP helpers ─────────────────────────────────────────────

    def _headers(self) -> dict[str, str]:
        h = {
            "Content-Type": "application/json",
            "X-Kitchen": self._kitchen,
            "X-Kitchen-Id": self._kitchen,
        }
        if self._api_key:
            h["Authorization"] = f"Bearer {self._api_key}"
        return h

    def _request(self, method: str, path: str, body: Optional[dict[str, Any]] = None, headers: Optional[dict[str, str]] = None) -> Any:
        raw, _ = self._send(method, path, body, headers)
        return json.loads(raw) if raw else {}

    def _request_text(self, method: str, path: str, headers: Optional[dict[str, str]] = None) -> tuple[str, dict[str, str]]:
        """A response that is not one JSON document (an export): its text and its headers, lower-cased."""
        raw, response_headers = self._send(method, path, None, headers)
        return raw.decode("utf-8"), response_headers

    def _request_bytes(self, method: str, path: str, data: bytes, content_type: str) -> Any:
        """Send a file as the raw request body (an upload); the reply is JSON."""
        raw, _ = self._send(method, path, None, {"Content-Type": content_type}, raw=data)
        return json.loads(raw) if raw else {}

    def _send(
        self,
        method: str,
        path: str,
        body: Optional[dict[str, Any]],
        headers: Optional[dict[str, str]],
        raw: Optional[bytes] = None,
    ) -> tuple[bytes, dict[str, str]]:
        url = self._url + path
        data = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
        req = urllib.request.Request(url, data=data, headers={**self._headers(), **(headers or {})}, method=method)
        try:
            with _OPENER.open(req, timeout=self._timeout) as resp:
                return resp.read(), {k.lower(): v for k, v in resp.headers.items()}
        except urllib.error.HTTPError as exc:
            raw = exc.read()
            try:
                detail = json.loads(raw)
            except Exception:
                detail = raw.decode(errors="replace")
            if exc.code == 400 and _NO_AUDIO in _error_text(detail):
                raise AudioModalityError(exc.code, detail) from exc
            raise _api_error(exc.code, detail, {k.lower(): v for k, v in exc.headers.items()}) from exc

    def _get(self, path: str) -> Any:
        return self._request("GET", path)

    def _post(self, path: str, body: dict[str, Any]) -> Any:
        return self._request("POST", path, body)

    def _put(self, path: str, body: dict[str, Any]) -> Any:
        return self._request("PUT", path, body)

    def _delete(self, path: str) -> Any:
        return self._request("DELETE", path)


@dataclass
class RealtimeToken:
    """A browser-safe realtime token and its lifetime in seconds."""

    token: str
    expires_in: int

    def __repr__(self) -> str:  # never print the secret
        return f"RealtimeToken(expires_in={self.expires_in})"


_NO_AUDIO = "does not offer the audio modality"


def _budget_ref(provider: Optional[str], agent: Optional[str], api_key: Optional[str], unit: str) -> dict[str, str]:
    """The fields that say which budget: at most one of provider, agent and api_key, and the unit."""
    named = {k: v for k, v in (("provider", provider), ("agent", agent), ("api_key", api_key)) if v}
    if len(named) > 1:
        raise ValueError("a budget counts the kitchen or one provider, one agent or one API key, not a mix")
    if unit not in BUDGET_UNITS:
        raise ValueError(f"unit must be one of {', '.join(BUDGET_UNITS)}")
    return {**named, "unit": unit}


def budget_record_id(provider: Optional[str] = None, agent: Optional[str] = None, api_key: Optional[str] = None, unit: str = "usd") -> str:
    """The record id a signature on a budget change covers: ``<scope>:<name>:<unit>``, for example
    ``provider:openai:usd``, ``agent:triage:tokens`` or ``kitchen::usd`` (the name is empty for the kitchen)."""
    _budget_ref(provider, agent, api_key, unit)
    for scope, name in (("provider", provider), ("agent", agent), ("api_key", api_key)):
        if name:
            return f"{scope}:{name}:{unit}"
    return f"kitchen::{unit}"


TRIGGER_KINDS = ("webhook", "email", "file")
TRIGGER_DELIVERY_STATUSES = ("accepted", "rejected", "duplicate", "failed")


def sign_webhook(secret: str, body: bytes, timestamp: Optional[int] = None) -> dict[str, str]:
    """The headers that sign a delivery to a webhook trigger: ``X-AgentOven-Signature`` (``sha256=`` and the HMAC
    of ``"<timestamp>.<body>"``), ``X-AgentOven-Timestamp``, and an ``X-AgentOven-Delivery`` id that is the same
    on every retry of one delivery (this helper makes a fresh one; pass your own header to keep it on a retry)."""
    import hashlib
    import hmac
    import uuid

    ts = int(time.time()) if timestamp is None else int(timestamp)
    mac = hmac.new(secret.encode(), str(ts).encode() + b"." + body, hashlib.sha256).hexdigest()
    return {"X-AgentOven-Signature": "sha256=" + mac, "X-AgentOven-Timestamp": str(ts), "X-AgentOven-Delivery": str(uuid.uuid4())}


DEFAULT_TIMEOUT = 120.0
"""Seconds one request may take when nothing else is set. Generous, because an invocation waits for a model.
``AgentOven(timeout=...)`` or the ``AGENTOVEN_TIMEOUT`` environment variable changes it; a request with no limit
hangs a script for ever on a server that accepted the connection and went quiet."""


def _resolve_timeout(timeout: Optional[float]) -> float:
    if timeout is None:
        env = os.environ.get("AGENTOVEN_TIMEOUT", "").strip()
        try:
            timeout = float(env) if env else DEFAULT_TIMEOUT
        except ValueError:
            raise ValueError(f"AGENTOVEN_TIMEOUT must be a number of seconds, not {env!r}") from None
    if timeout <= 0:
        raise ValueError("timeout must be a positive number of seconds")
    return timeout


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """Refuse a redirect: urllib would send the Authorization header to wherever it points. A control plane
    that redirects is misconfigured, and the caller is better told (the error names the new location)."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):  # type: ignore[override]
        raise AgentOvenAPIError(code, {"error": "redirect_refused", "message": f"the server redirected to {newurl}: use that URL as the server URL"})


_OPENER = urllib.request.build_opener(_NoRedirect)


def _seg(value: str) -> str:
    """Escape one URL path segment."""
    return urllib.parse.quote(value, safe="")


def _query(**params: Any) -> str:
    """A query string (with its ``?``) of the parameters that are set, or an empty one."""
    q = urllib.parse.urlencode({k: v for k, v in params.items() if v is not None and v != ""})
    return f"?{q}" if q else ""


def _put_if(body: dict[str, Any], **fields: Any) -> None:
    body.update({k: v for k, v in fields.items() if v is not None})


def _signing_request(gate: str, record_id: str, meaning: str, payload: Optional[Mapping[str, Any]]) -> dict[str, Any]:
    if meaning not in ("approved", "rejected"):
        raise ValueError(f"meaning is 'approved' or 'rejected', not {meaning!r}")
    return {"gate": gate, "record_id": record_id, "meaning": meaning, "payload": dict(payload or {})}


def _check_role(role: str, allowed: Sequence[str], what: str) -> None:
    """Refuse a role the control plane does not have. ``admin`` is retired: it was a Head Chef inside a kitchen
    and a Restaurateur across the installation."""
    if role == "admin":
        raise ValueError(
            f"role 'admin' is retired ({what}): use 'head_chef' for a kitchen role"
            + (" or 'restaurateur' for a person across the installation" if "restaurateur" in allowed else "")
        )
    if role not in allowed:
        hint = " ('restaurateur' is platform-wide: assign it in the user directory)" if role == "restaurateur" else ""
        raise ValueError(f"unknown role {role!r} for {what}: one of {', '.join(allowed)}{hint}")


def _check_retention_mode(mode: Optional[str]) -> None:
    if mode is not None and mode not in _RETENTION_MODES:
        raise ValueError(f"retention_mode is one of {', '.join(_RETENTION_MODES)}, not {mode!r}")


def _error_text(detail: Any) -> str:
    if isinstance(detail, dict):
        return str(detail.get("error") or detail.get("message") or detail)
    return str(detail)


def _provider_config(
    config: Optional[Mapping[str, Any]],
    api_key: Optional[str],
    modalities: Union[Modalities, Mapping[str, Any], None],
    allow_clear: bool,
) -> dict[str, Any]:
    cfg: dict[str, Any] = dict(config or {})
    if api_key is not None:
        cfg["api_key"] = api_key
    if modalities is not None:
        if "modalities" in cfg:
            raise ValueError("pass modalities= or config['modalities'], not both")
        cfg["modalities"] = modalities
    if "modalities" in cfg:
        cfg["modalities"] = validate_modalities(cfg["modalities"], allow_clear=allow_clear)
    return cfg


class AgentOvenAPIError(Exception):
    """Raised when the control plane returns a non-2xx response."""

    def __init__(self, status_code: int, detail: Any) -> None:
        self.status_code = status_code
        self.detail = detail
        super().__init__(f"AgentOven API error {status_code}: {detail}")


class RateLimitedError(AgentOvenAPIError):
    """A rate limit refused the request (HTTP 429, Pro ADR-0051): nothing was done, and the same request is
    expected to be admitted after ``retry_after`` seconds (from the ``Retry-After`` header). ``scope`` is what is
    limited (``kitchen``, ``agent``, ``api_key`` or ``provider``) and ``limit`` the dimension that ran out. The
    SDK does not retry for you."""

    def __init__(self, status_code: int, detail: Any, retry_after: Optional[int] = None) -> None:
        super().__init__(status_code, detail)
        self._retry_after = retry_after

    @property
    def _fields(self) -> dict[str, Any]:
        return _refusal_fields(self.detail)

    @property
    def retry_after(self) -> Optional[int]:
        """Seconds to wait: the ``Retry-After`` header, else the body's ``retry_after``."""
        if self._retry_after is not None:
            return self._retry_after
        v = self._fields.get("retry_after")
        return int(v) if isinstance(v, (int, float)) else None

    @property
    def scope(self) -> Optional[str]:
        return self._fields.get("scope")

    @property
    def limit(self) -> Optional[str]:
        return self._fields.get("limit")

    @property
    def message(self) -> str:
        return str(self._fields.get("message") or _error_text(self.detail))


class BudgetExceededError(AgentOvenAPIError):
    """A budget is used up (HTTP 402): the call was not sent. Raised for the gateway's ``budget_exceeded`` and for a
    run that stopped at a budget (``run_budget_exhausted``). ``scope`` names the budget (``run``, ``kitchen``,
    ``provider``, ``agent`` or ``api_key``), ``unit`` is ``usd`` or ``tokens``, ``period`` and ``resets_at`` say when a
    period budget starts again (a person can raise it sooner: :meth:`AgentOvenClient.set_budget`)."""

    @property
    def _fields(self) -> dict[str, Any]:
        return _refusal_fields(self.detail)

    @property
    def scope(self) -> Optional[str]:
        return self._fields.get("scope")

    @property
    def unit(self) -> Optional[str]:
        f = self._fields
        if f.get("unit"):
            return f["unit"]
        return {"cost": "usd", "tokens": "tokens"}.get(f.get("dimension", ""))

    @property
    def period(self) -> Optional[str]:
        return self._fields.get("period")

    @property
    def resets_at(self) -> Optional[str]:
        return self._fields.get("resets_at")

    @property
    def message(self) -> str:
        return str(self._fields.get("message") or _error_text(self.detail))


def _refusal_fields(detail: Any) -> dict[str, Any]:
    """The fields of a refusal, from either shape the server uses: the API's flat object, or the OpenAI-compatible
    gateway's ``{"error": {...}}``."""
    if isinstance(detail, dict):
        inner = detail.get("error")
        return inner if isinstance(inner, dict) else detail
    return {}


def _api_error(status: int, detail: Any, headers: Mapping[str, str]) -> AgentOvenAPIError:
    """The error for a non-2xx reply: a typed one for a rate limit and for a budget that is used up."""
    f = _refusal_fields(detail)
    if status == 429 and (f.get("error") == "rate_limited" or f.get("code") == "rate_limit_exceeded" or "retry-after" in headers):
        try:
            return RateLimitedError(status, detail, int(headers["retry-after"]))
        except (KeyError, ValueError):
            return RateLimitedError(status, detail)
    if status == 402 and ("budget_exceeded" in (f.get("type"), f.get("code"), f.get("error")) or f.get("error") == "run_budget_exhausted"):
        return BudgetExceededError(status, detail)
    return AgentOvenAPIError(status, detail)


class OidcSigningRequired(AgentOvenAPIError):
    """This account signs through the identity provider (OIDC), which re-asks for the password in a browser.
    The SDK can sign with a local account's password only; sign in the dashboard, or use a local account."""

    @property
    def message(self) -> str:
        if isinstance(self.detail, dict) and self.detail.get("message"):
            return str(self.detail["message"])
        return _error_text(self.detail)


@dataclass
class DatasetExport:
    """A dataset version exported as JSONL: the ``manifest`` line (digests, verification), the ``cases`` that
    follow it in manifest order, the ``manifest_digest`` the server sent in ``X-Manifest-Digest``, and the
    exported ``text`` as received (write that to a file to keep the export byte for byte)."""

    version: int
    manifest_digest: str
    manifest: dict[str, Any]
    cases: list[dict[str, Any]]
    text: str


class AudioModalityError(AgentOvenAPIError):
    """The agent's provider does not offer the ``audio`` modality, so it cannot
    take or give speech (HTTP 400). Enable audio on the provider, or move the
    agent to one that supports it — see ``client.agent_card(name).modalities``."""

    @property
    def message(self) -> str:
        return _error_text(self.detail)
