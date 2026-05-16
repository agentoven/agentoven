"""
LangChain adapter for the AgentOven runtime server.

Wraps any LangChain Runnable (AgentExecutor, LCEL chain, etc.) in the
InvokeHandler protocol expected by AgentOvenServer.

Usage::

    from langchain.agents import AgentExecutor
    from agentoven.runtime import LangChainAdapter, serve

    executor: AgentExecutor = ...
    serve(LangChainAdapter(executor))
"""

from __future__ import annotations

from typing import Any


class LangChainAdapter:
    """
    Wraps a LangChain ``Runnable`` so it satisfies the ``InvokeHandler`` protocol.

    The ``ainvoke`` method is used when available; otherwise ``invoke`` is
    called in a thread executor to avoid blocking the event loop.
    """

    def __init__(self, runnable: Any) -> None:
        """
        Args:
            runnable: Any LangChain Runnable — AgentExecutor, LCEL chain, etc.
                      Must accept ``{"input": str}`` as its input dictionary.
        """
        self._runnable = runnable

    async def run(self, message: str) -> str:
        """Invoke the LangChain runnable and return the text response."""
        import asyncio

        input_dict = {"input": message}

        if hasattr(self._runnable, "ainvoke"):
            result = await self._runnable.ainvoke(input_dict)
        else:
            loop = asyncio.get_event_loop()
            result = await loop.run_in_executor(None, self._runnable.invoke, input_dict)

        return _extract_output(result)


def _extract_output(result: Any) -> str:
    """Extract a string response from various LangChain output shapes."""
    if isinstance(result, str):
        return result
    if isinstance(result, dict):
        for key in ("output", "result", "answer", "text", "response"):
            if key in result:
                return str(result[key])
        # Fall back to first string value
        for v in result.values():
            if isinstance(v, str):
                return v
    return str(result)
