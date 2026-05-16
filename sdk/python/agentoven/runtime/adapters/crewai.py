"""
CrewAI adapter for the AgentOven runtime server.

Wraps a CrewAI ``Crew`` (or any object with a ``kickoff`` / ``akickoff``
method) in the InvokeHandler protocol expected by AgentOvenServer.

Usage::

    from crewai import Crew, Agent, Task
    from agentoven.runtime import serve
    from agentoven.runtime.adapters import CrewAIAdapter

    crew: Crew = Crew(agents=[...], tasks=[...])
    serve(CrewAIAdapter(crew))
"""

from __future__ import annotations

from typing import Any


class CrewAIAdapter:
    """
    Wraps a CrewAI ``Crew`` so it satisfies the ``InvokeHandler`` protocol.

    ``akickoff`` is used when available (CrewAI ≥ 0.51); otherwise
    ``kickoff`` is called in a thread executor to avoid blocking the
    event loop.
    """

    def __init__(self, crew: Any) -> None:
        """
        Args:
            crew: A CrewAI ``Crew`` instance (or any object with a
                  ``kickoff`` / ``akickoff`` method that accepts
                  ``inputs={"message": str}``).
        """
        self._crew = crew

    async def run(self, message: str) -> str:
        """Kick off the crew with the incoming message and return the result."""
        import asyncio

        inputs = {"message": message}

        if hasattr(self._crew, "akickoff"):
            result = await self._crew.akickoff(inputs=inputs)
        else:
            loop = asyncio.get_event_loop()
            result = await loop.run_in_executor(
                None, lambda: self._crew.kickoff(inputs=inputs)
            )

        return _extract_output(result)


def _extract_output(result: Any) -> str:
    """Extract a plain string from various CrewAI output shapes."""
    # CrewAI ≥ 0.51 returns a CrewOutput object with a .raw attribute
    if hasattr(result, "raw"):
        return str(result.raw)
    if isinstance(result, str):
        return result
    if isinstance(result, dict):
        for key in ("output", "result", "answer", "text", "response", "raw"):
            if key in result:
                return str(result[key])
    return str(result)
