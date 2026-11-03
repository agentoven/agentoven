"""
agentoven.agents: the agent and recipe wire shapes the reference checks read, and a builder for agent settings
the native ``Agent`` class does not carry (a summarizer model, skills, subagents, approval tools).

The native ``Agent`` / ``Recipe`` / ``Step`` classes (Rust) keep working as they always have. These helpers turn
any of them, a plain dict, a :class:`agentoven.flow.Flow` or an :class:`AgentBuilder` into the JSON the control
plane takes, the same way the native client does, so ``validate=True`` and ``refcheck.check_agent`` judge what
would actually be sent.
"""

from __future__ import annotations

import json
from typing import Any, Dict, List, Mapping

__all__ = ["AgentBuilder", "agent_to_wire", "ingredient_to_wire", "recipe_to_wire"]


def _config_dict(raw: Any) -> Dict[str, Any]:
    """An ingredient's config as the native class stores it: a JSON string (or already an object)."""
    if isinstance(raw, Mapping):
        return dict(raw)
    if isinstance(raw, str) and raw.strip():
        try:
            parsed = json.loads(raw)
        except ValueError:
            return {}
        return parsed if isinstance(parsed, dict) else {}
    return {}


def ingredient_to_wire(ing: Any, model_provider: str = "", model_name: str = "") -> Dict[str, Any]:
    """One ingredient as the control plane takes it: a dict is returned as is, a native ``Ingredient`` is
    converted the way the native client does (``provider`` and ``protocol`` move into ``config``; a model
    ingredient's provider and model are filled in from its name and the agent's)."""
    if isinstance(ing, Mapping):
        return dict(ing)
    kind = str(ing.kind)
    out: Dict[str, Any] = {"kind": kind, "name": ing.name, "required": bool(ing.required)}
    cfg: Dict[str, Any] = {}
    if getattr(ing, "provider", None):
        out["provider"] = ing.provider
    if getattr(ing, "role", None):
        out["role"] = ing.role
    if getattr(ing, "protocol", None):
        cfg["protocol"] = ing.protocol
    cfg.update(_config_dict(getattr(ing, "config", None)))
    if kind == "model":
        provider = getattr(ing, "provider", None) or model_provider
        model = ing.name or model_name
        if provider:
            cfg.setdefault("provider", provider)
        if model:
            cfg.setdefault("model", model)
    if cfg:
        out["config"] = cfg
    return out


def agent_to_wire(agent: Any) -> Dict[str, Any]:
    """An agent as the JSON the control plane takes.

    Accepts a dict (returned as a copy), an :class:`AgentBuilder`, or a native ``Agent``. A native agent's
    ``system_prompt`` becomes a prompt ingredient unless it already has one, as the native client sends it.
    """
    if isinstance(agent, Mapping):
        return dict(agent)
    if hasattr(agent, "to_wire"):
        return agent.to_wire()
    body: Dict[str, Any] = {
        "name": agent.name,
        "description": agent.description,
        "framework": agent.framework,
        "version": agent.version,
        "ingredients": [ingredient_to_wire(i, agent.model_provider, agent.model_name) for i in agent.ingredients],
    }
    for key in ("mode", "runtime", "behavior", "model_provider", "model_name"):
        v = getattr(agent, key, "")
        if v:
            body[key] = v
    prompt = getattr(agent, "system_prompt", None)
    if prompt and not any(i.get("kind") == "prompt" for i in body["ingredients"]):
        body["ingredients"].append({"name": "system-prompt", "kind": "prompt", "config": {"text": prompt}, "required": True})
    return body


def recipe_to_wire(recipe: Any) -> Dict[str, Any]:
    """A recipe as the JSON the control plane takes: a dict (copied), a :class:`agentoven.flow.Flow`, or a native
    ``Recipe``. A native step's textual ``timeout`` ("30s") is not read here (the native client converts it when
    it sends), so its range is not checked from the SDK."""
    if isinstance(recipe, Mapping):
        return dict(recipe)
    if hasattr(recipe, "to_steps") and hasattr(recipe, "to_wire"):  # a Flow: judged as written, not as FlowError sees it
        return recipe.to_wire(validate=False)
    steps: List[Dict[str, Any]] = []
    for s in recipe.steps:
        step: Dict[str, Any] = {"name": s.name, "kind": "human_gate" if s.human_gate else s.kind}
        if s.agent:
            step["agent_ref"] = s.agent
            step["agent"] = s.agent
        if s.depends_on:
            step["depends_on"] = list(s.depends_on)
        if s.notify:
            step["notify_tools"] = list(s.notify)
        branches = list(getattr(s, "branches", None) or [])
        if branches:
            step["branches"] = [{"condition": b.condition, "next_step": b.next} for b in branches]
        for src, dst in (("default_next", "default_next"), ("loop_condition", "loop_condition"),
                         ("max_iterations", "max_iterations"), ("source_path", "source_path"),
                         ("max_concurrency", "max_concurrency"), ("recipe_ref", "recipe_ref")):
            v = getattr(s, src, None)
            if v not in (None, ""):
                step[dst] = v
        steps.append(step)
    return {"name": recipe.name, "description": recipe.description, "version": recipe.version, "steps": steps}


class AgentBuilder:
    """Build the settings of an agent, including those the native ``Agent`` does not have.

    ::

        spec = (
            AgentBuilder("support-bot")
            .model("anthropic", "claude-sonnet-4-5")
            .summary_model("anthropic", "claude-haiku-4-5")      # the compaction model
            .skills("pdf-reader")
            .subagents("triage-bot")
        )
        client.create_agent(spec, validate=True)    # checked against the kitchen first; the server checks again

    Every setter returns the builder. :meth:`to_wire` is the JSON the control plane takes; a dict works anywhere a
    builder does.
    """

    def __init__(self, name: str) -> None:
        self._body: Dict[str, Any] = {"name": name}
        self._ingredients: List[Any] = []

    def _set(self, **fields: Any) -> "AgentBuilder":
        self._body.update(fields)
        return self

    def description(self, text: str) -> "AgentBuilder":
        return self._set(description=text)

    def mode(self, mode: str) -> "AgentBuilder":
        """``managed`` (AgentOven runs it) or ``external``."""
        return self._set(mode=mode)

    def runtime(self, runtime: str) -> "AgentBuilder":
        return self._set(runtime=runtime)

    def behavior(self, behavior: str) -> "AgentBuilder":
        """``reactive`` or ``agentic``."""
        return self._set(behavior=behavior)

    def model(self, provider: str, model: str) -> "AgentBuilder":
        """The model the agent runs on: a provider of the kitchen and a model it serves."""
        return self._set(model_provider=provider, model_name=model)

    def backup_model(self, provider: str, model: str) -> "AgentBuilder":
        """The model it falls back to."""
        return self._set(backup_provider=provider, backup_model=model)

    def summary_model(self, provider: str, model: str) -> "AgentBuilder":
        """The (cheap) model that summarizes the agent's history when its context fills: a provider of the kitchen
        and a model it serves. An empty ``provider`` lets any provider that lists the model serve it."""
        return self._set(summary_provider=provider, summary_model=model)

    def context_budget(self, tokens: int) -> "AgentBuilder":
        return self._set(context_budget=tokens)

    def reasoning_strategy(self, strategy: str) -> "AgentBuilder":
        """``react``, ``plan-and-execute`` or ``reflexion``."""
        return self._set(reasoning_strategy=strategy)

    def max_turns(self, turns: int) -> "AgentBuilder":
        return self._set(max_turns=turns)

    def skills(self, *names: str) -> "AgentBuilder":
        return self._set(skills=list(names))

    def approval_tools(self, *names: str) -> "AgentBuilder":
        """Tools whose calls wait for a person."""
        return self._set(approval_tools=list(names))

    def subagents(self, *names: str) -> "AgentBuilder":
        """Agents this one may delegate to."""
        return self._set(subagents=list(names))

    def ingredient(self, ingredient: Any) -> "AgentBuilder":
        """Add an ingredient: a native ``Ingredient`` (``Ingredient.tool("search")``) or a dict."""
        self._ingredients.append(ingredient)
        return self

    def set(self, **fields: Any) -> "AgentBuilder":
        """Any other field of the agent, by its JSON name."""
        return self._set(**fields)

    def to_wire(self) -> Dict[str, Any]:
        body = dict(self._body)
        if self._ingredients:
            provider, model = str(body.get("model_provider", "")), str(body.get("model_name", ""))
            body["ingredients"] = [ingredient_to_wire(i, provider, model) for i in self._ingredients]
        return body

    def __repr__(self) -> str:
        return f"AgentBuilder(name={self._body['name']!r})"

