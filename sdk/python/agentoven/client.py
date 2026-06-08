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
import urllib.error
import urllib.request
from typing import Any, Optional

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

__all__ = [
    "AgentOvenClient",
    # Re-export data types so callers only need to import from agentoven.client
    "Agent",
    "AgentStatus",
    "Branch",
    "Ingredient",
    "IngredientKind",
    "Recipe",
    "Step",
]


class AgentOvenClient:
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
    ) -> None:
        self._url = url.rstrip("/")
        self._api_key = api_key
        self._kitchen = kitchen
        self._native = _NativeClient(url=url, api_key=api_key, kitchen=kitchen)

    # ── Delegation to native client (OSS operations) ──────────────────────

    def register(self, agent: Agent) -> str:
        """Register an agent definition with the control plane."""
        return self._native.register(agent)

    def register_agent(self, agent: Agent) -> str:
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
        """Start (bake) an agent."""
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

    def create_recipe(self, recipe: Recipe) -> str:
        return self._native.create_recipe(recipe)

    def bake_recipe(self, name: str, input: Optional[Any] = None) -> str:
        return self._native.bake_recipe(name, input=input)

    def list_recipes(self) -> str:
        return self._native.list_recipes()

    def get_recipe_runs(self, name: str) -> str:
        return self._native.get_recipe_runs(name)

    def get_recipe_run(self, recipe_name: str, run_id: str) -> str:
        return self._native.get_recipe_run(recipe_name, run_id)

    # ── Kitchen management (Pro) ──────────────────────────────────────────

    def list_kitchens(self) -> list[dict[str, Any]]:
        """List all kitchens accessible to the current user."""
        return self._get("/api/v1/kitchens")

    def get_kitchen(self, kitchen_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/kitchens/{kitchen_id}")

    def create_kitchen(self, name: str, display_name: str = "") -> dict[str, Any]:
        return self._post("/api/v1/kitchens", {"name": name, "display_name": display_name or name})

    def delete_kitchen(self, kitchen_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/kitchens/{kitchen_id}")

    # ── User directory (Pro) ──────────────────────────────────────────────

    def list_users(self) -> list[dict[str, Any]]:
        """List users in the current kitchen."""
        return self._get("/api/v1/users")

    def get_user(self, user_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/users/{user_id}")

    def create_user(self, email: str, role: str, name: str = "") -> dict[str, Any]:
        return self._post("/api/v1/users", {"email": email, "role": role, "name": name})

    def update_user(self, user_id: str, **fields: Any) -> dict[str, Any]:
        return self._put(f"/api/v1/users/{user_id}", fields)

    def delete_user(self, user_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/users/{user_id}")

    # ── Guardrails (Pro) ─────────────────────────────────────────────────

    def list_guardrails(self) -> list[dict[str, Any]]:
        """List kitchen-level guardrail policies."""
        return self._get("/api/v1/guardrails")

    def get_guardrail(self, guardrail_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/guardrails/{guardrail_id}")

    def create_guardrail(self, guardrail: dict[str, Any]) -> dict[str, Any]:
        """Create a guardrail policy.

        Minimal example::

            client.create_guardrail({
                "name": "content-safety",
                "kind": "llamaguard",
                "stage": "both",
                "enabled": True,
                "overridable": False,
                "config": {
                    "endpoint": "http://ollama:11434",
                    "model": "llama-guard3:1b",
                    "fail_open": False,
                },
            })
        """
        return self._post("/api/v1/guardrails", guardrail)

    def update_guardrail(self, guardrail_id: str, guardrail: dict[str, Any]) -> dict[str, Any]:
        return self._put(f"/api/v1/guardrails/{guardrail_id}", guardrail)

    def toggle_guardrail(self, guardrail_id: str, enabled: bool) -> dict[str, Any]:
        return self._put(f"/api/v1/guardrails/{guardrail_id}/toggle", {"enabled": enabled})

    def delete_guardrail(self, guardrail_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/guardrails/{guardrail_id}")

    def list_guardrail_exceptions(self, guardrail_id: str) -> list[dict[str, Any]]:
        """List per-agent exceptions for a guardrail."""
        return self._get(f"/api/v1/guardrails/{guardrail_id}/exceptions")

    def add_guardrail_exception(self, guardrail_id: str, agent_name: str) -> dict[str, Any]:
        return self._post(
            f"/api/v1/guardrails/{guardrail_id}/exceptions", {"agent_name": agent_name}
        )

    def remove_guardrail_exception(self, guardrail_id: str, agent_name: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/guardrails/{guardrail_id}/exceptions/{agent_name}")

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
        return self._get(f"/api/v1/sessions/{session_id}")

    def create_session(self, agent_name: str, **kwargs: Any) -> dict[str, Any]:
        return self._post("/api/v1/sessions", {"agent_name": agent_name, **kwargs})

    def delete_session(self, session_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/sessions/{session_id}")

    # ── Service accounts (Pro) ───────────────────────────────────────────

    def list_service_accounts(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/service-accounts")

    def create_service_account(self, name: str, role: str) -> dict[str, Any]:
        """Create a machine identity. Returns token (shown once — store securely)."""
        return self._post("/api/v1/service-accounts", {"name": name, "role": role})

    def delete_service_account(self, sa_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/service-accounts/{sa_id}")

    # ── Schedules (Pro) ──────────────────────────────────────────────────

    def list_schedules(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/schedules")

    def get_schedule(self, schedule_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/schedules/{schedule_id}")

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
        return self._put(f"/api/v1/schedules/{schedule_id}", fields)

    def delete_schedule(self, schedule_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/schedules/{schedule_id}")

    # ── Test suites (Pro) ────────────────────────────────────────────────

    def list_test_suites(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/test-suites")

    def get_test_suite(self, name: str) -> dict[str, Any]:
        return self._get(f"/api/v1/test-suites/{name}")

    def create_test_suite(self, suite: dict[str, Any]) -> dict[str, Any]:
        """Create a test suite.

        Example::

            client.create_test_suite({
                "name": "smoke",
                "agent_name": "classifier",
                "cases": [
                    {"input": "Refund $50", "expected_contains": "refund", "timeout_secs": 30}
                ],
            })
        """
        return self._post("/api/v1/test-suites", suite)

    def run_test_suite(self, name: str) -> dict[str, Any]:
        """Trigger an immediate (ad-hoc) run of a test suite."""
        return self._post(f"/api/v1/test-suites/{name}/run", {})

    def list_test_runs(self, suite_name: Optional[str] = None) -> list[dict[str, Any]]:
        path = "/api/v1/test-runs"
        if suite_name:
            path += f"?suite={suite_name}"
        return self._get(path)

    # ── Workloads / K8s (Pro) ────────────────────────────────────────────

    def list_workloads(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/workloads")

    def get_workload(self, workload_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/workloads/{workload_id}")

    # ── Agent Environments (Pro) ─────────────────────────────────────────
    # Per-agent environment deployments: bake with provider/model overrides,
    # guardrail policy, and optional external backend URL.

    def list_agent_environments(self, agent_name: str) -> list[dict[str, Any]]:
        """List all environment deployments for an agent."""
        return self._get(f"/api/v1/agents/{agent_name}/environments")

    def get_agent_environment(self, agent_name: str, env_slug: str) -> dict[str, Any]:
        """Get a specific environment deployment for an agent."""
        return self._get(f"/api/v1/agents/{agent_name}/environments/{env_slug}")

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
        return self._put(f"/api/v1/agents/{agent_name}/environments/{env_slug}", body)

    def delete_agent_environment(self, agent_name: str, env_slug: str) -> dict[str, Any]:
        """Initiate cool-down / removal of an agent environment deployment."""
        return self._delete(f"/api/v1/agents/{agent_name}/environments/{env_slug}")

    # ── Scoped API keys (Pro) ───────────────────────────────────────────

    def list_scoped_keys(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/keys")

    def create_scoped_key(
        self,
        label: str,
        agent_names: list[str],
        max_calls: int = 0,
        expires_in: str = "",
    ) -> dict[str, Any]:
        return self._post(
            "/api/v1/keys",
            {
                "label": label,
                "agent_names": agent_names,
                "max_calls": max_calls,
                "expires_in": expires_in,
            },
        )

    def get_scoped_key(self, key_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/keys/{key_id}")

    def revoke_scoped_key(self, key_id: str) -> dict[str, Any]:
        return self._post(f"/api/v1/keys/{key_id}/revoke", {})

    def get_scoped_key_usage(self, key_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/keys/{key_id}/usage")

    def delete_scoped_key(self, key_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/keys/{key_id}")

    # ── Credentials (Pro) ───────────────────────────────────────────────

    def list_credentials(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/credentials")

    def create_credential(self, name: str, value: str, label: str = "") -> dict[str, Any]:
        return self._post("/api/v1/credentials", {"name": name, "value": value, "label": label})

    def delete_credential(self, credential_name: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/credentials/{credential_name}")

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

    def list_traces(self, agent: Optional[str] = None, limit: int = 50) -> list[dict[str, Any]]:
        params = [f"limit={limit}"]
        if agent:
            params.append(f"agent={agent}")
        return self._get("/api/v1/traces?" + "&".join(params))

    def get_trace(self, trace_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/traces/{trace_id}")

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

    def _request(self, method: str, path: str, body: Optional[dict[str, Any]] = None) -> Any:
        url = self._url + path
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(url, data=data, headers=self._headers(), method=method)
        try:
            with urllib.request.urlopen(req) as resp:
                raw = resp.read()
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as exc:
            raw = exc.read()
            try:
                detail = json.loads(raw)
            except Exception:
                detail = raw.decode(errors="replace")
            raise AgentOvenAPIError(exc.code, detail) from exc

    def _get(self, path: str) -> Any:
        return self._request("GET", path)

    def _post(self, path: str, body: dict[str, Any]) -> Any:
        return self._request("POST", path, body)

    def _put(self, path: str, body: dict[str, Any]) -> Any:
        return self._request("PUT", path, body)

    def _delete(self, path: str) -> Any:
        return self._request("DELETE", path)


class AgentOvenAPIError(Exception):
    """Raised when the control plane returns a non-2xx response."""

    def __init__(self, status_code: int, detail: Any) -> None:
        self.status_code = status_code
        self.detail = detail
        super().__init__(f"AgentOven API error {status_code}: {detail}")
