"""
LangGraph adapter for the AgentOven runtime server.

Wraps a compiled LangGraph ``StateGraph`` or ``CompiledGraph`` in the
``InvokeHandler`` protocol expected by AgentOvenServer.

Usage::

    from langgraph.graph import StateGraph
    from agentoven.runtime import LangGraphAdapter, serve

    graph = StateGraph(...)
    # ... add nodes/edges ...
    compiled = graph.compile()

    serve(LangGraphAdapter(compiled))
"""

from __future__ import annotations

from typing import Any, Optional

from agentoven.runtime.adapters._context import invoke_kwargs


class LangGraphAdapter:
    """
    Wraps a compiled LangGraph graph so it satisfies the ``InvokeHandler`` protocol.

    The graph is invoked with ``{"messages": [HumanMessage(content=message)]}``
    and the last ``AIMessage`` content is returned.
    """

    def __init__(self, graph: Any, context: Any = None) -> None:
        """
        Args:
            graph: A compiled LangGraph graph (result of ``StateGraph.compile()``).
                   Must accept ``{"messages": list}`` as its input.
            context: What a graph built with ``context_schema=`` reads from ``runtime.context``:
                     an object, or a callable that builds one for each call. Defaults to an
                     instance of the graph's own ``context_schema`` (its default settings).
        """
        self._graph = graph
        self._context = context

    async def run(self, message: str, ctx: Optional[Any] = None) -> Any:
        """Invoke the LangGraph graph and return the final AI response.

        The conversation's session ID becomes the LangGraph ``thread_id``, so a graph compiled
        with a checkpointer continues a conversation across calls.
        """
        import asyncio

        try:
            from langchain_core.messages import HumanMessage  # type: ignore[import]
        except ImportError as exc:
            raise ImportError(
                "langchain_core is required for LangGraphAdapter. "
                "Install it with: pip install langchain-core"
            ) from exc

        state = {"messages": [HumanMessage(content=message)]}

        session = getattr(ctx, "session_id", None)
        config = {"configurable": {"thread_id": session}} if session else {}

        extra = invoke_kwargs(self._graph, self._context)
        if hasattr(self._graph, "ainvoke"):
            result = await self._graph.ainvoke(state, config, **extra)
        else:
            loop = asyncio.get_event_loop()
            result = await loop.run_in_executor(None, lambda: self._graph.invoke(state, config, **extra))

        # Usage is totalled from the AI messages' own usage_metadata, so the control plane records
        # what the graph's model calls cost instead of zero.
        from agentoven.runtime.adapters.deepagents import usage_of
        from agentoven.runtime.server import HandlerResult

        return HandlerResult(response=_extract_last_ai_message(result), usage=usage_of(result))


def _extract_last_ai_message(result: Any) -> str:
    """Return the content of the last AI message in the graph output."""
    try:
        from langchain_core.messages import AIMessage  # type: ignore[import]
    except ImportError:
        AIMessage = None  # type: ignore[assignment]

    messages = result.get("messages", []) if isinstance(result, dict) else []
    # Walk backwards to find the last AI message
    for msg in reversed(messages):
        if AIMessage is not None and isinstance(msg, AIMessage):
            content = msg.content
            if isinstance(content, list):
                # Multi-part content — join text parts
                return "".join(
                    part.get("text", "") if isinstance(part, dict) else str(part)
                    for part in content
                )
            return str(content)
        # Fallback: check for type attribute (dict-style messages)
        if isinstance(msg, dict) and msg.get("type") == "ai":
            return str(msg.get("content", ""))

    # If no AI message found, return string representation of result
    return str(result)
