"""Run-time context for LangGraph 1.x graphs.

A graph built with ``StateGraph(State, context_schema=Context)`` reads its settings from
``runtime.context`` (the model name, the system prompt, ...), and expects the caller to supply
them when it invokes the graph. LangGraph's own server fills them in from the assistant's
configuration; a process hosted by AgentOven has no assistant, so the adapter supplies them.
"""

from __future__ import annotations

from typing import Any, Dict


def resolve_context(graph: Any, context: Any) -> Any:
    """The context object to invoke ``graph`` with, or None when it takes none.

    ``context`` may be the object itself, a class, or a zero-argument callable that builds one per
    call (so a value read from the environment is read when the call is made). When it is not
    given, a graph that declares a ``context_schema`` gets an instance of it built from the
    schema's defaults, which is what ``langgraph dev`` does for an assistant with no overrides.
    """
    if context is not None:
        if isinstance(context, type) or (callable(context) and not _is_value(context)):
            return context()
        return context
    schema = getattr(getattr(graph, "builder", None), "context_schema", None)
    if schema is None:
        return None
    try:
        return schema()
    except TypeError:
        return None


def _is_value(obj: Any) -> bool:
    """A dataclass or pydantic instance is the context itself, even if it defines __call__."""
    return hasattr(obj, "__dataclass_fields__") or hasattr(obj, "model_fields")


def invoke_kwargs(graph: Any, context: Any) -> Dict[str, Any]:
    """Keyword arguments to add to ``invoke``/``ainvoke``/``astream`` for the graph's context."""
    ctx = resolve_context(graph, context)
    return {} if ctx is None else {"context": ctx}
