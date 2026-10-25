"""
Deep Agents adapter for the AgentOven runtime server.

`Deep Agents <https://github.com/langchain-ai/deepagents>`_ builds an agent harness around
LangGraph: ``create_deep_agent()`` returns a compiled LangGraph graph that plans with a todo
list, keeps a virtual filesystem, spawns subagents and compresses its own context. AgentOven
hosts that graph as a runtime next to LangChain and LangGraph, so a Deep Agent gets the
control plane around it (the A2A gateway, guardrails on what goes in and what comes out,
sessions, tracing, audit) without changing how it is written.

Usage::

    from agentoven.runtime import serve, DeepAgentsAdapter, build_deep_agent

    agent = build_deep_agent(tools=[search], system_prompt="You are a research agent.")
    serve(DeepAgentsAdapter(agent))

What AgentOven does and does not govern here
--------------------------------------------

The control plane enforces its guardrails and A2A authentication at the edge of this process:
on the message that comes in and the answer that goes out. It cannot see inside the process.
The agent's own tools, including Deep Agents' built-in ``execute`` and file tools, and the
subagents it spawns with its built-in ``task`` tool, run here, in your code, with your
permissions, and AgentOven does not sandbox them. Give the process only the access the agent
needs.

The agent's model calls go to the provider directly (``AGENT_API_ENDPOINT`` / ``AGENT_API_KEY``),
so their cost is reported in the usage this adapter returns but cannot be capped by the control
plane in the community edition.
"""

from __future__ import annotations

import inspect
from typing import Any, AsyncIterator, Dict, List, Optional

from agentoven.runtime.adapters._context import invoke_kwargs
from agentoven.runtime.runtime import AgentOvenRuntime
from agentoven.runtime.server import HandlerResult, InvokeContext


def _field(msg: Any, name: str, default: Any = None) -> Any:
    """Read a field from a LangChain message object or from a dict-shaped message."""
    if isinstance(msg, dict):
        return msg.get(name, default)
    return getattr(msg, name, default)


def _is_ai(msg: Any) -> bool:
    kind = _field(msg, "type") or _field(msg, "role")
    return kind in ("ai", "assistant")


def _text(content: Any) -> str:
    """The text of a message's content, which is a string or a list of content parts."""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(
            part.get("text", "") if isinstance(part, dict) else str(part)
            for part in content
            if not (isinstance(part, dict) and part.get("type") not in (None, "text"))
        )
    return "" if content is None else str(content)


def final_answer(result: Any) -> str:
    """The last AI message's text in a graph result, or the result itself if there is none."""
    messages = result.get("messages", []) if isinstance(result, dict) else []
    for msg in reversed(messages):
        if _is_ai(msg):
            text = _text(_field(msg, "content"))
            if text:
                return text
    return str(result)


def usage_of(result: Any) -> Dict[str, int]:
    """Total the token usage the graph's AI messages report (LangChain's ``usage_metadata``)."""
    total = {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
    messages = result.get("messages", []) if isinstance(result, dict) else []
    for msg in messages:
        meta = _field(msg, "usage_metadata") if _is_ai(msg) else None
        if not isinstance(meta, dict):
            continue
        i, o = int(meta.get("input_tokens", 0) or 0), int(meta.get("output_tokens", 0) or 0)
        total["input_tokens"] += i
        total["output_tokens"] += o
        total["total_tokens"] += int(meta.get("total_tokens", 0) or 0) or (i + o)
    return total


def _progress(result: Any) -> Dict[str, Any]:
    """What a Deep Agent leaves behind besides its answer: its todo list and the files it wrote."""
    meta: Dict[str, Any] = {}
    if isinstance(result, dict):
        if result.get("todos"):
            meta["todos"] = result["todos"]
        files = result.get("files")
        if isinstance(files, dict) and files:
            meta["files"] = sorted(files.keys())
    return meta


class DeepAgentsAdapter:
    """
    Wraps a Deep Agent (a compiled LangGraph graph) so the AgentOven server can host it.

    A call is run as ``{"messages": [{"role": "user", "content": message}]}``. The conversation's
    session ID becomes the LangGraph ``thread_id``, so an agent built with a checkpointer
    continues a conversation across calls. The answer is the last AI message; the todo list
    and file names the agent ended with are returned as metadata, so progress is visible.
    """

    def __init__(self, graph: Any, context: Any = None) -> None:
        self._graph = graph
        self._context = context

    @staticmethod
    def _config(ctx: Optional[InvokeContext]) -> Dict[str, Any]:
        if ctx is not None and ctx.session_id:
            return {"configurable": {"thread_id": ctx.session_id}}
        return {}

    @staticmethod
    def _state(message: str) -> Dict[str, Any]:
        return {"messages": [{"role": "user", "content": message}]}

    async def run(self, message: str, ctx: Optional[InvokeContext] = None) -> HandlerResult:
        """Run the agent to completion."""
        import asyncio

        state, config = self._state(message), self._config(ctx)
        extra = invoke_kwargs(self._graph, self._context)
        if hasattr(self._graph, "ainvoke"):
            result = await self._graph.ainvoke(state, config, **extra)
        else:
            loop = asyncio.get_event_loop()
            result = await loop.run_in_executor(None, lambda: self._graph.invoke(state, config, **extra))
        return HandlerResult(response=final_answer(result), usage=usage_of(result), metadata=_progress(result))

    async def stream(self, message: str, ctx: Optional[InvokeContext] = None) -> AsyncIterator[Any]:
        """
        Run the agent and report what it does as it goes: the text of each AI message as a
        ``token`` event, each tool call as a ``tool_call`` event, and every change to the todo
        list as a ``todos`` event. Ends with a ``HandlerResult`` holding the final answer.
        """
        if not hasattr(self._graph, "astream"):
            yield await self.run(message, ctx)
            return

        last_answer = ""
        usage = {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
        todos: List[Any] = []
        files: List[str] = []
        async for chunk in self._graph.astream(
            self._state(message), self._config(ctx), stream_mode="updates", **invoke_kwargs(self._graph, self._context)
        ):
            if not isinstance(chunk, dict):
                continue
            for _node, update in chunk.items():
                if not isinstance(update, dict):
                    continue
                msgs = update.get("messages") or []
                if not isinstance(msgs, list):
                    msgs = [msgs]
                for msg in msgs:
                    if not _is_ai(msg):
                        continue
                    text = _text(_field(msg, "content"))
                    if text:
                        last_answer = text
                        yield {"type": "token", "content": text}
                    for call in _field(msg, "tool_calls") or []:
                        yield {"type": "tool_call", "name": _field(call, "name", ""), "arguments": _field(call, "args", {})}
                    meta = _field(msg, "usage_metadata")
                    if isinstance(meta, dict):
                        i, o = int(meta.get("input_tokens", 0) or 0), int(meta.get("output_tokens", 0) or 0)
                        usage["input_tokens"] += i
                        usage["output_tokens"] += o
                        usage["total_tokens"] += int(meta.get("total_tokens", 0) or 0) or (i + o)
                if update.get("todos") and update["todos"] != todos:
                    todos = update["todos"]
                    yield {"type": "todos", "todos": todos}
                if isinstance(update.get("files"), dict):
                    files = sorted(update["files"].keys())

        meta_out: Dict[str, Any] = {}
        if todos:
            meta_out["todos"] = todos
        if files:
            meta_out["files"] = files
        yield HandlerResult(response=last_answer, usage=usage, metadata=meta_out)


def build_deep_agent(
    runtime: Optional[AgentOvenRuntime] = None,
    *,
    tools: Optional[List[Any]] = None,
    subagents: Optional[List[Any]] = None,
    system_prompt: Optional[str] = None,
    checkpointer: Any = None,
    **kwargs: Any,
) -> Any:
    """
    Build a Deep Agent from the configuration AgentOven injects into the process.

    The model comes from ``AGENT_MODEL_PROVIDER`` / ``AGENT_MODEL_NAME`` / ``AGENT_API_KEY`` /
    ``AGENT_API_ENDPOINT``; the instructions from ``AGENT_DESCRIPTION`` unless ``system_prompt``
    is given; the tools from the agent's resolved MCP tools plus any in ``tools``. Extra keyword
    arguments go to ``create_deep_agent`` unchanged.

    ``checkpointer`` makes conversations continue across calls (keyed by the session ID); without
    one, each call starts fresh.
    """
    try:
        from deepagents import create_deep_agent  # type: ignore[import]
    except ImportError as exc:  # pragma: no cover - depends on the user's environment
        raise ImportError(
            "The deepagents package is required for the deepagents runtime. "
            "Install it with: pip install deepagents (it needs Python 3.11 or newer)"
        ) from exc

    runtime = runtime or AgentOvenRuntime()
    all_tools = list(runtime.build_mcp_tools()) + list(tools or [])
    prompt = system_prompt if system_prompt is not None else runtime.system_prompt

    params = inspect.signature(create_deep_agent).parameters
    call: Dict[str, Any] = dict(kwargs)
    call.setdefault("model", runtime.build_langchain_llm())
    if "tools" in params:
        call["tools"] = all_tools
    # The instructions parameter was renamed across releases; pass whichever this one has.
    for name in ("system_prompt", "instructions"):
        if name in params and prompt:
            call[name] = prompt
            break
    if subagents and "subagents" in params:
        call["subagents"] = subagents
    if checkpointer is not None and "checkpointer" in params:
        call["checkpointer"] = checkpointer
    return create_deep_agent(**call)
