"""
AgentOven Server — a small FastAPI app that hosts a framework agent the way the AgentOven
control plane expects to find one.

The control plane starts an agent process, waits for ``AGENT_READY`` on its stdout, checks
``GET /health``, then relays calls to it. This server speaks that whole contract, so an agent
written with LangChain, LangGraph, Deep Agents or anything else needs only a handler:

  - ``POST /invoke``          synchronous call: ``{message, session_id?, variables?}``
  - ``POST /invoke/stream``   the same as server-sent events
  - ``POST /a2a`` and ``POST /``   A2A JSON-RPC (``tasks/send``, ``tasks/get``, ``tasks/cancel``)
  - ``GET /health``           liveness, polled by the control plane
  - ``GET /.well-known/agent-card.json``

Every POST is authenticated with the agent's own token (``AGENT_A2A_TOKEN``, presented in
``X-AgentOven-A2A-Token``): the control plane presents it when it relays a call, so a caller
that merely reaches the port cannot run the agent. A process started without a token still
serves, with a warning, because it cannot tell the control plane from anyone else.

Usage::

    from agentoven.runtime import serve, DeepAgentsAdapter, build_deep_agent

    serve(DeepAgentsAdapter(build_deep_agent()))      # blocks; prints AGENT_READY, then serves
"""

from __future__ import annotations

import hmac
import inspect
import json
import sys
import time
import uuid
from dataclasses import dataclass, field
from typing import Any, AsyncIterator, Dict, Optional, Union

try:
    from fastapi import FastAPI, Request  # type: ignore[import]
    from fastapi.responses import JSONResponse, StreamingResponse  # type: ignore[import]
    import uvicorn  # type: ignore[import]
except ImportError as exc:
    raise ImportError(
        "fastapi and uvicorn are required by agentoven.runtime. "
        "Install them with: pip install 'agentoven[runtime]'"
    ) from exc

from agentoven.runtime.runtime import RUN_ID_HEADER, AgentOvenRuntime, current_run_id

#: Header the control plane presents on every call it relays to this process.
A2A_TOKEN_HEADER = "X-AgentOven-A2A-Token"


@dataclass
class InvokeContext:
    """What a call carries besides its message."""

    #: The conversation this call belongs to. A framework with a checkpointer (LangGraph, Deep
    #: Agents) should use it as its thread ID, so a conversation continues across calls.
    session_id: Optional[str] = None
    trace_id: Optional[str] = None
    variables: Dict[str, str] = field(default_factory=dict)


@dataclass
class HandlerResult:
    """A handler's answer, with what it cost and anything worth showing alongside it."""

    response: str
    #: ``{"input_tokens": n, "output_tokens": n, "total_tokens": n}``. The control plane
    #: records it against the run; a handler that cannot report it leaves it zero.
    usage: Dict[str, int] = field(default_factory=dict)
    #: Free-form extras (for Deep Agents, the todo list and the files it wrote).
    metadata: Dict[str, Any] = field(default_factory=dict)


HandlerOutput = Union[str, HandlerResult]


class InvokeHandler:
    """
    What the server hosts. Any object with an async ``run`` qualifies::

        async def run(self, message: str) -> str

    A handler may also accept ``ctx: InvokeContext`` (a second positional parameter named
    ``ctx``, or ``**kwargs``) to receive the session, and may return a ``HandlerResult`` to
    report usage. A handler with an async-generator ``stream(message, ctx)`` is used for
    ``/invoke/stream``; otherwise the answer is sent as one token followed by ``done``.
    """

    async def run(self, message: str, ctx: Optional[InvokeContext] = None) -> HandlerOutput:  # noqa: D102
        raise NotImplementedError


def _accepts_ctx(fn: Any) -> bool:
    try:
        params = inspect.signature(fn).parameters
    except (TypeError, ValueError):
        return False
    return "ctx" in params or any(p.kind is inspect.Parameter.VAR_KEYWORD for p in params.values())


def _usage(raw: Optional[Dict[str, Any]]) -> Dict[str, int]:
    raw = raw or {}
    i, o = int(raw.get("input_tokens", 0) or 0), int(raw.get("output_tokens", 0) or 0)
    t = int(raw.get("total_tokens", 0) or 0) or (i + o)
    return {"input_tokens": i, "output_tokens": o, "total_tokens": t}


def _result_of(out: HandlerOutput) -> HandlerResult:
    return out if isinstance(out, HandlerResult) else HandlerResult(response=str(out))


class AgentOvenServer:
    """Hosts a handler under the AgentOven process contract."""

    def __init__(self, handler: Any, runtime: Optional[AgentOvenRuntime] = None) -> None:
        self._handler = handler
        self._runtime = runtime or AgentOvenRuntime()
        self._start_time = time.time()
        self._tasks: Dict[str, Dict[str, Any]] = {}
        self._warned_open = False
        self._server: Optional[uvicorn.Server] = None
        self._app = self._build_app()

    @property
    def app(self) -> FastAPI:
        return self._app

    # ── Calling the handler ───────────────────────────────────────────────────

    async def _run(self, message: str, ctx: InvokeContext) -> HandlerResult:
        run = self._handler.run
        out = await (run(message, ctx=ctx) if _accepts_ctx(run) else run(message))
        return _result_of(out)

    async def _events(self, message: str, ctx: InvokeContext) -> AsyncIterator[Dict[str, Any]]:
        """Frames for /invoke/stream, in the shape the control plane's clients read."""
        stream = getattr(self._handler, "stream", None)
        if stream is None:
            result = await self._run(message, ctx)
            if result.response:
                yield {"type": "token", "content": result.response}
            yield self._done(result)
            return

        final: Optional[HandlerResult] = None
        gen = stream(message, ctx=ctx) if _accepts_ctx(stream) else stream(message)
        async for item in gen:
            if isinstance(item, HandlerResult):
                final = item
            elif isinstance(item, dict):
                yield item
            else:
                yield {"type": "token", "content": str(item)}
        yield self._done(final or HandlerResult(response=""))

    @staticmethod
    def _done(result: HandlerResult) -> Dict[str, Any]:
        frame: Dict[str, Any] = {"type": "done", "content": result.response, "usage": _usage(result.usage)}
        if result.metadata:
            frame["metadata"] = result.metadata
        return frame

    # ── Caller authentication ─────────────────────────────────────────────────

    def _caller_authorized(self, request: Request) -> bool:
        token = self._runtime.a2a_token
        if not token:
            if not self._warned_open:
                self._warned_open = True
                print(
                    f"[{self._runtime.name}] WARNING: no AGENT_A2A_TOKEN; accepting unauthenticated "
                    "calls from anything that can reach this port",
                    file=sys.stderr,
                    flush=True,
                )
            return True
        presented = request.headers.get(A2A_TOKEN_HEADER, "")
        return bool(presented) and hmac.compare_digest(presented.encode(), token.encode())

    # ── App construction ──────────────────────────────────────────────────────

    def _build_app(self) -> FastAPI:
        runtime = self._runtime
        app = FastAPI(title=f"AgentOven Runtime: {runtime.name}", docs_url=None, redoc_url=None)

        @app.middleware("http")
        async def authenticate(request: Request, call_next):  # type: ignore[no-untyped-def]
            if request.method == "POST" and not self._caller_authorized(request):
                return JSONResponse(
                    {"error": "authentication_required", "message": "This agent only accepts calls from the control plane."},
                    status_code=401,
                )
            return await call_next(request)

        def run_id_of(request: Request, body: Dict[str, Any]) -> str:
            return request.headers.get(RUN_ID_HEADER) or str(body.get("run_id") or "")

        def ctx_of(body: Dict[str, Any]) -> InvokeContext:
            # The control plane sends the session as session_id and as thread_id (the key a
            # LangGraph checkpointer uses); either names the conversation.
            return InvokeContext(
                session_id=body.get("session_id") or body.get("thread_id") or None,
                trace_id=body.get("trace_id") or None,
                variables=body.get("variables") or {},
            )

        async def body_of(request: Request) -> Optional[Dict[str, Any]]:
            try:
                body = await request.json()
            except (json.JSONDecodeError, UnicodeDecodeError):
                return None
            return body if isinstance(body, dict) else None

        @app.post("/invoke")
        async def invoke(request: Request) -> JSONResponse:
            body = await body_of(request)
            if body is None:
                return JSONResponse({"error": "invalid JSON"}, status_code=400)
            message = body.get("message", "")
            if not message:
                return JSONResponse({"error": "missing 'message' field"}, status_code=400)
            t0 = time.time()
            token = current_run_id.set(run_id_of(request, body))
            try:
                result = await self._run(message, ctx_of(body))
            except Exception as exc:  # noqa: BLE001 - reported to the caller, not swallowed
                import traceback

                traceback.print_exc(file=sys.stderr)
                return JSONResponse({"error": str(exc)}, status_code=500)
            finally:
                current_run_id.reset(token)
            out: Dict[str, Any] = {
                "response": result.response,
                "latency_ms": int((time.time() - t0) * 1000),
                "usage": _usage(result.usage),
            }
            if result.metadata:
                out["metadata"] = result.metadata
            return JSONResponse(out)

        @app.post("/invoke/stream")
        async def invoke_stream(request: Request):  # type: ignore[no-untyped-def]
            body = await body_of(request)
            if body is None:
                return JSONResponse({"error": "invalid JSON"}, status_code=400)
            message = body.get("message", "")
            if not message:
                return JSONResponse({"error": "missing 'message' field"}, status_code=400)
            ctx = ctx_of(body)
            run_id = run_id_of(request, body)

            async def frames() -> AsyncIterator[bytes]:
                # Set here, not above: the response body runs after this handler returns.
                current_run_id.set(run_id)
                try:
                    async for ev in self._events(message, ctx):
                        yield f"data: {json.dumps(ev)}\n\n".encode("utf-8")
                except Exception as exc:  # noqa: BLE001
                    yield f"data: {json.dumps({'type': 'error', 'message': str(exc)})}\n\n".encode("utf-8")

            return StreamingResponse(
                frames(),
                media_type="text/event-stream",
                headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
            )

        @app.post("/a2a")
        async def a2a(request: Request) -> JSONResponse:
            return await self._jsonrpc(request)

        @app.post("/")
        async def a2a_root(request: Request) -> JSONResponse:
            return await self._jsonrpc(request)

        @app.get("/health")
        @app.get("/status")
        async def health() -> JSONResponse:
            return JSONResponse(
                {
                    "status": "healthy",
                    "agent": runtime.name,
                    "kitchen": runtime.kitchen,
                    "runtime": runtime.runtime,
                    "uptime_seconds": round(time.time() - self._start_time, 1),
                }
            )

        @app.get("/.well-known/agent-card.json")
        @app.get("/agent-card")
        async def agent_card() -> JSONResponse:
            return JSONResponse(
                {
                    "name": runtime.name,
                    "kitchen": runtime.kitchen,
                    "runtime": runtime.runtime,
                    "version": "1.0",
                    "capabilities": {"invoke": True, "streaming": True},
                    "defaultInputModes": ["text"],
                    "defaultOutputModes": ["text"],
                }
            )

        return app

    # ── A2A JSON-RPC ──────────────────────────────────────────────────────────

    async def _jsonrpc(self, request: Request) -> JSONResponse:
        try:
            req = await request.json()
        except (json.JSONDecodeError, UnicodeDecodeError):
            return JSONResponse({"error": "invalid JSON"}, status_code=400)
        if not isinstance(req, dict):
            return JSONResponse({"error": "invalid JSON"}, status_code=400)
        run_token = current_run_id.set(request.headers.get(RUN_ID_HEADER, ""))
        try:
            return await self._jsonrpc_call(req)
        finally:
            current_run_id.reset(run_token)

    async def _jsonrpc_call(self, req: Dict[str, Any]) -> JSONResponse:

        req_id, method, params = req.get("id"), req.get("method", ""), req.get("params") or {}

        def ok(result: Any) -> JSONResponse:
            return JSONResponse({"jsonrpc": "2.0", "id": req_id, "result": result})

        def err(code: int, message: str) -> JSONResponse:
            return JSONResponse({"jsonrpc": "2.0", "id": req_id, "error": {"code": code, "message": message}})

        if method == "tasks/send":
            message = params.get("message") or {}
            text = "".join(
                p.get("text", "") for p in message.get("parts", []) if isinstance(p, dict) and "text" in p
            )
            if not text:
                return err(-32602, "No text content in message")
            task_id = params.get("id") or str(uuid.uuid4())
            ctx = InvokeContext(session_id=params.get("sessionId") or params.get("session_id") or task_id)
            try:
                result = await self._run(text, ctx)
                task = {
                    "id": task_id,
                    "status": {"state": "completed"},
                    "artifacts": [{"parts": [{"type": "text", "text": result.response}]}],
                    "usage": _usage(result.usage),
                }
            except Exception as exc:  # noqa: BLE001
                task = {
                    "id": task_id,
                    "status": {
                        "state": "failed",
                        "message": {"role": "agent", "parts": [{"type": "text", "text": str(exc)}]},
                    },
                }
            self._tasks[task_id] = task
            return ok(task)

        if method == "tasks/get":
            task = self._tasks.get(params.get("id", ""))
            return ok(task) if task else err(-32602, f"Task '{params.get('id', '')}' not found")

        if method == "tasks/cancel":
            task_id = params.get("id", "")
            task = self._tasks.get(task_id)
            if task:
                task["status"] = {"state": "canceled"}
            return ok({"id": task_id, "status": {"state": "canceled"}})

        return err(-32601, f"Method '{method}' not found")

    # ── Startup lifecycle ─────────────────────────────────────────────────────

    def _on_started(self) -> None:
        """Called once the server is listening. The process manager waits for this line."""
        print("AGENT_READY", flush=True)

    def run(self) -> None:
        """Start the server (blocking). Prints AGENT_READY once it is accepting connections."""
        config = uvicorn.Config(
            app=self._app,
            host="0.0.0.0",
            port=self._runtime.port,
            log_level="info",
            access_log=False,
        )
        server = uvicorn.Server(config)
        self._server = server

        original_startup = server.startup

        async def patched_startup(sockets: Any = None) -> None:
            await original_startup(sockets)
            if not server.should_exit:  # startup can fail (port in use) without raising
                self._on_started()

        server.startup = patched_startup  # type: ignore[method-assign]
        server.run()


def serve(handler: Any, runtime: Optional[AgentOvenRuntime] = None) -> None:
    """
    Construct an AgentOvenServer and start serving. Blocks until the process is killed.

    Args:
        handler: An object with ``async run(message)`` (see ``InvokeHandler``).
        runtime: Optional AgentOvenRuntime; defaults to one built from the environment.
    """
    AgentOvenServer(handler, runtime).run()
