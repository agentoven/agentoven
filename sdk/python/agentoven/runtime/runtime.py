"""
AgentOven Runtime — reads agent configuration from environment variables
injected by the AgentOven process manager at bake time.

Environment variables (all set by the control plane):
  AGENT_NAME                 — agent name
  AGENT_KITCHEN              — kitchen slug
  AGENT_RUNTIME              — runtime identifier (langchain|langgraph|deepagents|crewai|custom)
  AGENT_PORT                 — port this process should listen on (AGENTOVEN_PORT is accepted too)
  AGENT_A2A_TOKEN            — this agent's own token; every POST must present it in
                               X-AgentOven-A2A-Token (the control plane does when it relays)
  AGENT_MODEL_PROVIDER       — provider slug (openai|anthropic|azure-openai|ollama|…)
  AGENT_MODEL_NAME           — model name (e.g. gpt-4o, claude-opus-4-5, llama3.2)
  AGENT_API_KEY              — the model provider's key, or a gateway credential (see below)
  AGENT_API_ENDPOINT         — the model provider's base URL, when it is not the default
  AGENT_MODEL_GATEWAY        — "openai" when model calls go through AgentOven's metered gateway:
                               AGENT_API_ENDPOINT / AGENT_API_KEY then name the gateway and a
                               credential for it (never the provider's own key), and every
                               provider is spoken to as OpenAI chat completions
  AGENT_GATEWAY_MODEL        — the model's name to the gateway, "provider/model"
  AGENT_MODEL_TEMPERATURE    — float, default 0.7
  AGENT_MAX_TOKENS           — int, default 4096
  AGENT_DESCRIPTION          — the agent's system prompt (AGENT_SYSTEM_PROMPT is accepted too)
  AGENT_PROMPT_TEMPLATE      — full prompt template (may include {input} etc.)
  AGENT_TOOLS_JSON           — JSON array of tool descriptors from the ingredient resolver
  AGENT_DATA_SOURCES_JSON    — JSON array of data source descriptors
  AGENTOVEN_CONTROL_PLANE_URL — control plane base URL (for MCP gateway calls)
  CONTROL_PLANE_TOKEN        — token for calling back to the control plane
  AGENTOVEN_API_KEY          — API key for calling back to the control plane
  AGENTOVEN_PRO_TOKEN        — set if running under AgentOven Pro
"""

from __future__ import annotations

import contextvars
import json
import os
from typing import Any

#: The run the current call belongs to. The server sets it from the control plane's
#: ``X-AgentOven-Run-Id`` header; a model client built in gateway mode sends it on every model
#: call, so the gateway can hold the call to the right run's budget.
current_run_id: "contextvars.ContextVar[str]" = contextvars.ContextVar("agentoven_run_id", default="")

#: Header the control plane (and so the SDK) uses to carry the run ID.
RUN_ID_HEADER = "X-AgentOven-Run-Id"


class AgentOvenRuntime:
    """Reads AgentOven environment variables and exposes them as typed properties."""

    def __init__(self) -> None:
        self._name: str = os.environ.get("AGENT_NAME", "")
        self._kitchen: str = os.environ.get("AGENT_KITCHEN", "")
        self._runtime: str = os.environ.get("AGENT_RUNTIME", "custom")
        self._provider: str = os.environ.get("AGENT_MODEL_PROVIDER", "")
        self._model: str = os.environ.get("AGENT_MODEL_NAME", "")
        self._temperature: float = float(os.environ.get("AGENT_MODEL_TEMPERATURE", "0.7"))
        self._max_tokens: int = int(os.environ.get("AGENT_MAX_TOKENS", "4096"))
        # The control plane sets AGENT_DESCRIPTION; AGENT_SYSTEM_PROMPT is the older name.
        self._system_prompt: str = os.environ.get("AGENT_SYSTEM_PROMPT") or os.environ.get("AGENT_DESCRIPTION", "")
        self._prompt_template: str = os.environ.get("AGENT_PROMPT_TEMPLATE", "{input}")
        self._tools_json: str = os.environ.get("AGENT_TOOLS_JSON", "[]")
        self._data_sources_json: str = os.environ.get("AGENT_DATA_SOURCES_JSON", "[]")
        self._control_plane_url: str = os.environ.get("AGENTOVEN_CONTROL_PLANE_URL", "")
        self._api_key: str = os.environ.get("AGENTOVEN_API_KEY", "")
        # The control plane sets AGENT_PORT. AGENTOVEN_PORT is the older name; 9000 matches the
        # built-in runner when neither is set.
        self._port: int = int(os.environ.get("AGENT_PORT") or os.environ.get("AGENTOVEN_PORT") or "9000")
        self._a2a_token: str = os.environ.get("AGENT_A2A_TOKEN", "")
        self._model_api_key: str = os.environ.get("AGENT_API_KEY", "")
        self._model_endpoint: str = os.environ.get("AGENT_API_ENDPOINT", "")
        self._cp_token: str = os.environ.get("CONTROL_PLANE_TOKEN", "")
        self._gateway: str = os.environ.get("AGENT_MODEL_GATEWAY", "")
        self._gateway_model: str = os.environ.get("AGENT_GATEWAY_MODEL", "")
        self._pro_token: str = os.environ.get("AGENTOVEN_PRO_TOKEN", "")

    # ── Identity ──────────────────────────────────────────────────────────────

    @property
    def name(self) -> str:
        return self._name

    @property
    def kitchen(self) -> str:
        return self._kitchen

    @property
    def runtime(self) -> str:
        return self._runtime

    @property
    def port(self) -> int:
        return self._port

    @property
    def has_pro_token(self) -> bool:
        return bool(self._pro_token)

    @property
    def a2a_token(self) -> str:
        """The token callers must present (X-AgentOven-A2A-Token); empty when none was set."""
        return self._a2a_token

    @property
    def control_plane_token(self) -> str:
        return self._cp_token

    # ── Model ─────────────────────────────────────────────────────────────────

    @property
    def model_provider(self) -> str:
        return self._provider

    @property
    def model_name(self) -> str:
        return self._model

    @property
    def uses_model_gateway(self) -> bool:
        """True when model calls go through AgentOven's metered gateway."""
        return self._gateway == "openai" and bool(self._model_endpoint)

    @property
    def model_api_key(self) -> str:
        """The model provider's key, as injected by the control plane."""
        return self._model_api_key

    @property
    def model_endpoint(self) -> str:
        """The model provider's base URL, or empty for the provider's default."""
        return self._model_endpoint

    @property
    def temperature(self) -> float:
        return self._temperature

    @property
    def max_tokens(self) -> int:
        return self._max_tokens

    # ── Prompts ───────────────────────────────────────────────────────────────

    @property
    def system_prompt(self) -> str:
        return self._system_prompt

    @property
    def prompt_template(self) -> str:
        return self._prompt_template

    # ── Tools / Data ──────────────────────────────────────────────────────────

    @property
    def tools(self) -> list[dict[str, Any]]:
        try:
            return json.loads(self._tools_json) or []
        except json.JSONDecodeError:
            return []

    @property
    def data_sources(self) -> list[dict[str, Any]]:
        try:
            return json.loads(self._data_sources_json) or []
        except json.JSONDecodeError:
            return []

    # ── LangChain helpers ─────────────────────────────────────────────────────

    def build_langchain_llm(self) -> Any:
        """
        Construct a LangChain chat model from the injected environment.

        Supports: openai, azure-openai, anthropic, ollama, groq.
        Raises ImportError if the required LangChain integration is not installed.
        """
        provider = self._provider.lower()
        model = self._model
        kwargs: dict[str, Any] = {
            "temperature": self._temperature,
            "max_tokens": self._max_tokens,
        }

        if self.uses_model_gateway:
            return self._build_gateway_llm(kwargs)

        if self._model_api_key:
            kwargs["api_key"] = self._model_api_key

        if provider in ("openai",):
            from langchain_openai import ChatOpenAI  # type: ignore[import]
            if self._model_endpoint:
                kwargs["base_url"] = self._model_endpoint
            return ChatOpenAI(model=model, **kwargs)

        if provider in ("azure-openai", "azure_openai"):
            from langchain_openai import AzureChatOpenAI  # type: ignore[import]
            if self._model_endpoint:
                kwargs["azure_endpoint"] = self._model_endpoint
            return AzureChatOpenAI(azure_deployment=model, **kwargs)

        if provider in ("anthropic",):
            from langchain_anthropic import ChatAnthropic  # type: ignore[import]
            if self._model_endpoint:
                kwargs["base_url"] = self._model_endpoint
            return ChatAnthropic(model=model, **kwargs)  # type: ignore[arg-type]

        if provider in ("ollama",):
            from langchain_ollama import ChatOllama  # type: ignore[import]
            ollama_kwargs: dict[str, Any] = {}
            if self._model_endpoint:
                ollama_kwargs["base_url"] = self._model_endpoint
            return ChatOllama(model=model, temperature=self._temperature, **ollama_kwargs)

        if provider in ("groq",):
            from langchain_groq import ChatGroq  # type: ignore[import]
            return ChatGroq(model=model, **kwargs)

        raise ValueError(
            f"Unknown model provider '{self._provider}'. "
            "Supported: openai, azure-openai, anthropic, ollama, groq."
        )

    def _build_gateway_llm(self, kwargs: dict[str, Any]) -> Any:
        """A chat model that talks to AgentOven's gateway, whatever the underlying provider.

        The gateway speaks OpenAI chat completions for every provider, so the OpenAI client is
        used and the real model is named ``provider/model``. Each call carries the run ID of the
        request being handled, which is how the gateway holds it to that run's budget.
        """
        try:
            from langchain_openai import ChatOpenAI  # type: ignore[import]
            import httpx  # type: ignore[import]
        except ImportError as exc:
            raise ImportError(
                "langchain-openai and httpx are required to use the AgentOven model gateway. "
                "Install them with: pip install langchain-openai httpx"
            ) from exc

        def add_run(request: "httpx.Request") -> None:
            run_id = current_run_id.get()
            if run_id:
                request.headers[RUN_ID_HEADER] = run_id

        async def add_run_async(request: "httpx.Request") -> None:
            add_run(request)

        return ChatOpenAI(
            model=self._gateway_model or f"{self._provider}/{self._model}",
            base_url=self._model_endpoint,
            api_key=self._model_api_key,
            http_client=httpx.Client(event_hooks={"request": [add_run]}),
            http_async_client=httpx.AsyncClient(event_hooks={"request": [add_run_async]}),
            **kwargs,
        )

    def build_mcp_tools(self) -> list[Any]:
        """
        Build LangChain-compatible tool wrappers from the agent's MCP tool descriptors.

        Returns a list of BaseTool instances. Requires langchain_core.
        Each tool descriptor from AGENT_TOOLS_JSON must have:
          - name: str
          - description: str
          - endpoint: str  (MCP gateway endpoint)
        """
        tool_descs = self.tools
        if not tool_descs:
            return []

        try:
            from langchain_core.tools import StructuredTool  # type: ignore[import]
            import httpx  # type: ignore[import]
        except ImportError as exc:
            raise ImportError(
                "langchain_core and httpx are required for build_mcp_tools(). "
                "Install them with: pip install langchain-core httpx"
            ) from exc

        built: list[Any] = []
        for desc in tool_descs:
            endpoint = desc.get("endpoint", "")
            if not endpoint:
                continue

            tool_name: str = desc.get("name", "unknown_tool")
            tool_desc: str = desc.get("description", "")
            api_key = self._api_key

            def _make_caller(ep: str, key: str):  # closure to capture per-iteration values
                def call_tool(**kwargs: Any) -> str:
                    headers = {"Content-Type": "application/json"}
                    if key:
                        headers["Authorization"] = f"Bearer {key}"
                    resp = httpx.post(ep, json=kwargs, headers=headers, timeout=30)
                    resp.raise_for_status()
                    return resp.text
                return call_tool

            built.append(
                StructuredTool.from_function(
                    name=tool_name,
                    description=tool_desc,
                    func=_make_caller(endpoint, api_key),
                )
            )

        return built
