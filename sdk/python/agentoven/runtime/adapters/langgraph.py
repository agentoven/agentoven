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

from typing import Any


class LangGraphAdapter:
    """
    Wraps a compiled LangGraph graph so it satisfies the ``InvokeHandler`` protocol.

    The graph is invoked with ``{"messages": [HumanMessage(content=message)]}``
    and the last ``AIMessage`` content is returned.
    """

    def __init__(self, graph: Any) -> None:
        """
        Args:
            graph: A compiled LangGraph graph (result of ``StateGraph.compile()``).
                   Must accept ``{"messages": list}`` as its input.
        """
        self._graph = graph

    async def run(self, message: str) -> str:
        """Invoke the LangGraph graph and return the final AI response."""
        import asyncio

        try:
            from langchain_core.messages import HumanMessage  # type: ignore[import]
        except ImportError as exc:
            raise ImportError(
                "langchain_core is required for LangGraphAdapter. "
                "Install it with: pip install langchain-core"
            ) from exc

        state = {"messages": [HumanMessage(content=message)]}

        if hasattr(self._graph, "ainvoke"):
            result = await self._graph.ainvoke(state)
        else:
            loop = asyncio.get_event_loop()
            result = await loop.run_in_executor(None, self._graph.invoke, state)

        return _extract_last_ai_message(result)


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
