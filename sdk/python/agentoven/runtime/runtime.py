"""
AgentOven Runtime — reads agent configuration from environment variables
injected by the AgentOven process manager at bake time.

Environment variables (all set by the control plane):
  AGENT_NAME                 — agent name
  AGENT_KITCHEN              — kitchen slug
  AGENT_RUNTIME              — runtime identifier (langchain|langgraph|crewai|custom)
  AGENT_MODEL_PROVIDER       — provider slug (openai|anthropic|azure-openai|ollama|…)
  AGENT_MODEL_NAME           — model name (e.g. gpt-4o, claude-opus-4-5, llama3.2)
  AGENT_MODEL_TEMPERATURE    — float, default 0.7
  AGENT_MAX_TOKENS           — int, default 4096
  AGENT_SYSTEM_PROMPT        — agent system prompt text
  AGENT_PROMPT_TEMPLATE      — full prompt template (may include {input} etc.)
  AGENT_TOOLS_JSON           — JSON array of tool descriptors from the ingredient resolver
  AGENT_DATA_SOURCES_JSON    — JSON array of data source descriptors
  AGENTOVEN_CONTROL_PLANE_URL — control plane base URL (for MCP gateway calls)
  AGENTOVEN_API_KEY          — API key for calling back to the control plane
  AGENTOVEN_PORT             — port this process should listen on
  AGENTOVEN_PRO_TOKEN        — set if running under AgentOven Pro
"""

from __future__ import annotations

import json
import os
from typing import Any


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
        self._system_prompt: str = os.environ.get("AGENT_SYSTEM_PROMPT", "")
        self._prompt_template: str = os.environ.get("AGENT_PROMPT_TEMPLATE", "{input}")
        self._tools_json: str = os.environ.get("AGENT_TOOLS_JSON", "[]")
        self._data_sources_json: str = os.environ.get("AGENT_DATA_SOURCES_JSON", "[]")
        self._control_plane_url: str = os.environ.get("AGENTOVEN_CONTROL_PLANE_URL", "")
        self._api_key: str = os.environ.get("AGENTOVEN_API_KEY", "")
        self._port: int = int(os.environ.get("AGENTOVEN_PORT", "8000"))
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

    # ── Model ─────────────────────────────────────────────────────────────────

    @property
    def model_provider(self) -> str:
        return self._provider

    @property
    def model_name(self) -> str:
        return self._model

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

        if provider in ("openai",):
            from langchain_openai import ChatOpenAI  # type: ignore[import]
            return ChatOpenAI(model=model, **kwargs)

        if provider in ("azure-openai", "azure_openai"):
            from langchain_openai import AzureChatOpenAI  # type: ignore[import]
            return AzureChatOpenAI(azure_deployment=model, **kwargs)

        if provider in ("anthropic",):
            from langchain_anthropic import ChatAnthropic  # type: ignore[import]
            return ChatAnthropic(model=model, **kwargs)  # type: ignore[arg-type]

        if provider in ("ollama",):
            from langchain_ollama import ChatOllama  # type: ignore[import]
            return ChatOllama(model=model, temperature=self._temperature)

        if provider in ("groq",):
            from langchain_groq import ChatGroq  # type: ignore[import]
            return ChatGroq(model=model, **kwargs)

        raise ValueError(
            f"Unknown model provider '{self._provider}'. "
            "Supported: openai, azure-openai, anthropic, ollama, groq."
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
