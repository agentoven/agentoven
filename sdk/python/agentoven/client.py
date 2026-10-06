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

from agentoven.media import Attachment, AudioInput, InvokeResult
from agentoven.modalities import (
    AgentCard,
    Modalities,
    Provider,
    ProviderUpdate,
    validate_modalities,
)

if TYPE_CHECKING:
    from agentoven.realtime import RealtimeConnection

__all__ = [
    "AgentOvenClient",
    "AgentOvenAPIError",
    "AudioModalityError",
    "RealtimeToken",
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

    def with_kitchen(self, kitchen: str) -> "AgentOvenClient":
        """Return a cloned client bound to a different kitchen."""
        return AgentOvenClient(url=self._url, api_key=self._api_key, kitchen=kitchen)

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

    def create_recipe(self, recipe: Recipe, kitchen: Optional[str] = None) -> str:
        native = self._native if kitchen is None else self.with_kitchen(kitchen)._native
        return native.create_recipe(recipe)

    def bake_recipe(self, name: str, input: Optional[Any] = None, kitchen: Optional[str] = None) -> str:
        native = self._native if kitchen is None else self.with_kitchen(kitchen)._native
        return native.bake_recipe(name, input=input)

    def list_recipes(self, kitchen: Optional[str] = None) -> str:
        native = self._native if kitchen is None else self.with_kitchen(kitchen)._native
        return native.list_recipes()

    def get_recipe(self, name: str, kitchen: Optional[str] = None) -> dict[str, Any]:
        if kitchen is None:
            return self._get(f"/api/v1/recipes/{name}")
        return self.with_kitchen(kitchen)._get(f"/api/v1/recipes/{name}")

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
    # The server routes every one of these by id, never by name.

    def list_test_suites(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/test-suites")

    def get_test_suite(self, suite_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/test-suites/{suite_id}")

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
        return self._put(f"/api/v1/test-suites/{suite_id}", suite)

    def delete_test_suite(self, suite_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/test-suites/{suite_id}")

    def run_test_suite(self, suite_id: str, trigger: str = "manual") -> dict[str, Any]:
        """Trigger an immediate (ad-hoc) run of a test suite."""
        return self._post(f"/api/v1/test-suites/{suite_id}/run", {"trigger": trigger})

    def list_test_runs(self, suite_id: str) -> list[dict[str, Any]]:
        """Lists runs for one suite. The server has no top-level
        ``GET /api/v1/test-runs`` — only this route, nested under the suite —
        so unlike an earlier version of this method, ``suite_id`` is
        required, not optional."""
        return self._get(f"/api/v1/test-suites/{suite_id}/runs")

    def get_test_run(self, suite_id: str, run_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/test-suites/{suite_id}/runs/{run_id}")

    def cancel_test_run(self, run_id: str) -> dict[str, Any]:
        return self._post(f"/api/v1/test-runs/{run_id}/cancel", {})

    # ── World schemas (Pro, ADR-0032) ─────────────────────────────────────

    def list_world_schemas(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/world-schemas")

    def get_world_schema(self, schema_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/world-schemas/{schema_id}")

    def create_world_schema(self, schema: dict[str, Any]) -> dict[str, Any]:
        return self._post("/api/v1/world-schemas", schema)

    def save_world_schema(self, schema_id: str, schema: dict[str, Any]) -> dict[str, Any]:
        return self._put(f"/api/v1/world-schemas/{schema_id}", schema)

    def delete_world_schema(self, schema_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/world-schemas/{schema_id}")

    # ── Scenarios (Pro, ADR-0032) ──────────────────────────────────────────

    def list_scenarios(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/scenarios")

    def get_scenario(self, scenario_id: str) -> dict[str, Any]:
        return self._get(f"/api/v1/scenarios/{scenario_id}")

    def create_scenario(self, scenario: dict[str, Any]) -> dict[str, Any]:
        return self._post("/api/v1/scenarios", scenario)

    def save_scenario(self, scenario_id: str, scenario: dict[str, Any]) -> dict[str, Any]:
        return self._put(f"/api/v1/scenarios/{scenario_id}", scenario)

    def delete_scenario(self, scenario_id: str) -> dict[str, Any]:
        return self._delete(f"/api/v1/scenarios/{scenario_id}")

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
        return self._get(f"/api/v1/scenario-runs/{run_id}")

    def list_scenario_runs(self) -> list[dict[str, Any]]:
        return self._get("/api/v1/scenario-runs")

    def get_scenario_run_episodes(self, run_id: str) -> list[dict[str, Any]]:
        """The per-episode detail behind a scenario run's aggregate
        pass_rate — one entry per rollout, each with its own graded verdict."""
        return self._get(f"/api/v1/scenario-runs/{run_id}/episodes")

    def cancel_scenario_run(self, run_id: str) -> dict[str, Any]:
        return self._post(f"/api/v1/scenario-runs/{run_id}/cancel", {})

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
        """
        if not message and audio is None:
            raise ValueError("invoke needs a message, audio, or both")
        if voice and not voice_output:
            raise ValueError("voice only applies with voice_output=True")
        body: dict[str, Any] = {}
        if message:
            body["message"] = message
        if audio is not None:
            body["audio"] = AudioInput.coerce(audio, audio_mime_type).to_wire()
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
            if exc.code == 400 and _NO_AUDIO in _error_text(detail):
                raise AudioModalityError(exc.code, detail) from exc
            raise AgentOvenAPIError(exc.code, detail) from exc

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


def _seg(value: str) -> str:
    """Escape one URL path segment."""
    return urllib.parse.quote(value, safe="")


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


class AudioModalityError(AgentOvenAPIError):
    """The agent's provider does not offer the ``audio`` modality, so it cannot
    take or give speech (HTTP 400). Enable audio on the provider, or move the
    agent to one that supports it — see ``client.agent_card(name).modalities``."""

    @property
    def message(self) -> str:
        return _error_text(self.detail)
