"""
AgentOven Server — lightweight FastAPI wrapper that:
  - Serves POST /invoke, GET /status, GET /.well-known/agent-card.json
  - Prints AGENT_READY to stdout when the HTTP server is accepting connections
  - Delegates invoke calls to a user-supplied async handler

Usage::

    from agentoven.runtime import serve, AgentOvenRuntime, LangChainAdapter

    runtime = AgentOvenRuntime()
    adapter = LangChainAdapter(my_agent_executor)
    serve(adapter)           # blocks; prints AGENT_READY then listens
"""

from __future__ import annotations

import asyncio
import sys
import time
from typing import Any, Awaitable, Callable, Protocol, runtime_checkable

try:
    from fastapi import FastAPI, HTTPException, Request  # type: ignore[import]
    from fastapi.responses import JSONResponse  # type: ignore[import]
    import uvicorn  # type: ignore[import]
except ImportError as exc:
    raise ImportError(
        "fastapi and uvicorn are required by agentoven.runtime. "
        "Install them with: pip install 'agentoven[runtime]'"
    ) from exc

from agentoven.runtime.runtime import AgentOvenRuntime


@runtime_checkable
class InvokeHandler(Protocol):
    """
    Any object with an async `run(message: str) -> str` method qualifies as an
    InvokeHandler and can be passed to serve() or AgentOvenServer.
    """

    async def run(self, message: str) -> str:  # noqa: D102
        ...


class AgentOvenServer:
    """
    FastAPI-based server that wraps a user handler and manages the AgentOven
    lifecycle (AGENT_READY, /invoke, /status, agent-card).
    """

    def __init__(
        self,
        handler: InvokeHandler,
        runtime: AgentOvenRuntime | None = None,
    ) -> None:
        self._handler = handler
        self._runtime = runtime or AgentOvenRuntime()
        self._start_time = time.time()
        self._app = self._build_app()

    # ── App construction ──────────────────────────────────────────────────────

    def _build_app(self) -> FastAPI:
        runtime = self._runtime
        handler = self._handler
        start_time = self._start_time
        app = FastAPI(title=f"AgentOven Runtime — {runtime.name}", docs_url=None, redoc_url=None)

        @app.post("/invoke")
        async def invoke(request: Request) -> JSONResponse:
            body: dict[str, Any] = await request.json()
            message: str = body.get("message", "")
            if not message:
                raise HTTPException(status_code=400, detail="'message' field is required")
            t0 = time.time()
            response = await handler.run(message)
            latency_ms = int((time.time() - t0) * 1000)
            return JSONResponse({
                "response": response,
                "latency_ms": latency_ms,
                # usage is optional; frameworks that track tokens should override
                "usage": {
                    "input_tokens": 0,
                    "output_tokens": 0,
                    "total_tokens": 0,
                },
            })

        @app.get("/status")
        async def status() -> JSONResponse:
            return JSONResponse({
                "status": "ok",
                "agent": runtime.name,
                "kitchen": runtime.kitchen,
                "runtime": runtime.runtime,
                "uptime_seconds": round(time.time() - start_time, 1),
            })

        @app.get("/.well-known/agent-card.json")
        async def agent_card() -> JSONResponse:
            return JSONResponse({
                "name": runtime.name,
                "kitchen": runtime.kitchen,
                "runtime": runtime.runtime,
                "version": "1.0",
                "capabilities": {"invoke": True},
            })

        return app

    # ── Startup lifecycle ─────────────────────────────────────────────────────

    def _on_started(self) -> None:
        """Called once the server is listening. Prints AGENT_READY to stdout."""
        print("AGENT_READY", flush=True)

    # ── Serve ─────────────────────────────────────────────────────────────────

    def run(self) -> None:
        """Start the server (blocking). Sends AGENT_READY when ready."""
        port = self._runtime.port
        config = uvicorn.Config(
            app=self._app,
            host="0.0.0.0",
            port=port,
            log_level="info",
            access_log=False,
        )
        server = uvicorn.Server(config)

        # Override the startup callback to emit AGENT_READY
        original_startup = server.startup

        async def patched_startup(sockets: Any = None) -> None:
            await original_startup(sockets)
            self._on_started()

        server.startup = patched_startup  # type: ignore[method-assign]
        server.run()


def serve(handler: InvokeHandler, runtime: AgentOvenRuntime | None = None) -> None:
    """
    Convenience function: construct an AgentOvenServer and start serving.

    Blocks until the process is killed.

    Args:
        handler: An object with ``async run(message: str) -> str``.
        runtime: Optional AgentOvenRuntime instance; defaults to one built
                 from environment variables.
    """
    AgentOvenServer(handler, runtime).run()
