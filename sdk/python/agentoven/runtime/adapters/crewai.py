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

    def __init__(self, crew: Any, inputs: Any = None) -> None:
        """
        Args:
            crew: A CrewAI ``Crew`` instance (or any object with a
                  ``kickoff`` / ``akickoff`` method that accepts
                  ``inputs={"message": str}``).
            inputs: How the incoming message becomes the crew's inputs. A crew's tasks name their
                    own placeholders (``{topic}``, ``{filename}``), so the default
                    ``{"message": message}`` only suits a crew that asks for ``{message}``. Give
                    a string to name the one input the message fills, or a function
                    ``message -> dict`` to build them all.
        """
        self._crew = crew
        self._inputs = inputs

    def _inputs_for(self, message: str) -> dict:
        if self._inputs is None:
            return {"message": message}
        if isinstance(self._inputs, str):
            return {self._inputs: message}
        return dict(self._inputs(message))

    async def run(self, message: str) -> Any:
        """Kick off the crew with the incoming message and return the result with its token usage."""
        import asyncio

        inputs = self._inputs_for(message)

        if hasattr(self._crew, "akickoff"):
            result = await self._crew.akickoff(inputs=inputs)
        else:
            loop = asyncio.get_event_loop()
            result = await loop.run_in_executor(
                None, lambda: self._crew.kickoff(inputs=inputs)
            )

        from agentoven.runtime.server import HandlerResult

        return HandlerResult(response=_extract_output(result), usage=_usage_of(result))


def _usage_of(result: Any) -> dict:
    """Token usage CrewAI reports on its output (``token_usage``), so spend is recorded, not zero."""
    meta = getattr(result, "token_usage", None)
    if meta is None and isinstance(result, dict):
        meta = result.get("token_usage")

    def num(name: str) -> int:
        value = meta.get(name) if isinstance(meta, dict) else getattr(meta, name, 0)
        return int(value or 0)

    i, o = num("prompt_tokens"), num("completion_tokens")
    return {"input_tokens": i, "output_tokens": o, "total_tokens": num("total_tokens") or (i + o)}


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
