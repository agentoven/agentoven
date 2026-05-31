#!/usr/bin/env python3
"""
AgentOven Agent Runner — A2A + REST agent process.

This script is launched by the AgentOven control plane when an agent is baked.
It runs as an independent HTTP server that:
  1. Listens for A2A JSON-RPC 2.0 requests  (POST /a2a, POST /)
  2. Listens for REST invoke requests         (POST /invoke)
  3. Streams SSE responses                    (POST /invoke/stream)
  4. Calls the configured LLM with native tool calling
  5. Executes MCP tools via JSON-RPC 2.0 HTTP calls
  6. Supports agent delegation via the control plane API

Environment variables (set by the control plane):
  AGENT_NAME                  — agent name
  AGENT_KITCHEN               — kitchen (tenant) scope
  AGENT_PORT                  — port to listen on (default: 9000)
  AGENT_DESCRIPTION           — agent description / system prompt
  AGENT_MODEL_PROVIDER        — provider kind: openai, anthropic, ollama, azure-openai
  AGENT_MODEL_NAME            — model name (gpt-4o, claude-opus-4-5, etc.)
  AGENT_API_KEY               — provider API key
  AGENT_API_ENDPOINT          — provider API endpoint (Azure OpenAI / Ollama override)
  AGENT_TOOLS_JSON            — JSON array of ResolvedTool objects from bake
  AGENT_MAX_TURNS             — max agentic loop turns (default: 10)
  AGENT_SKILLS                — comma-separated skill list
  AGENTOVEN_CONTROL_PLANE_URL — control plane base URL for delegation callbacks
  CONTROL_PLANE_TOKEN         — X-Service-Token value for control plane API auth
"""

import json
import os
import sys
import uuid
import traceback
from datetime import datetime, timezone
from http.server import HTTPServer, BaseHTTPRequestHandler
from socketserver import ThreadingMixIn
import urllib.request
import urllib.error

# ── Configuration ──────────────────────────────────────────────────────────────

AGENT_NAME        = os.environ.get("AGENT_NAME", "unnamed-agent")
AGENT_KITCHEN     = os.environ.get("AGENT_KITCHEN", "default")
AGENT_PORT        = int(os.environ.get("AGENT_PORT", "9000"))
AGENT_DESCRIPTION = os.environ.get("AGENT_DESCRIPTION", "An AgentOven managed agent")
MODEL_PROVIDER    = os.environ.get("AGENT_MODEL_PROVIDER", "openai")
MODEL_NAME        = os.environ.get("AGENT_MODEL_NAME", "gpt-4o")
API_KEY           = os.environ.get("AGENT_API_KEY", "")
API_ENDPOINT      = os.environ.get("AGENT_API_ENDPOINT", "")
SKILLS            = [s.strip() for s in os.environ.get("AGENT_SKILLS", "").split(",") if s.strip()]
MAX_TURNS         = int(os.environ.get("AGENT_MAX_TURNS", "10"))

CONTROL_PLANE_URL   = os.environ.get("AGENTOVEN_CONTROL_PLANE_URL", "")
CONTROL_PLANE_TOKEN = os.environ.get("CONTROL_PLANE_TOKEN", "")

# ── Tool Loading ───────────────────────────────────────────────────────────────

def _load_resolved_tools():
    """Load resolved tool definitions from AGENT_TOOLS_JSON env var."""
    raw = os.environ.get("AGENT_TOOLS_JSON", "")
    if not raw:
        return []
    try:
        return json.loads(raw)
    except Exception as e:
        print(f"[{AGENT_NAME}] WARNING: failed to parse AGENT_TOOLS_JSON: {e}", file=sys.stderr)
        return []


RESOLVED_TOOLS = _load_resolved_tools()  # list of ResolvedTool dicts


def _build_tool_defs():
    """Build OpenAI-compatible tool definitions from RESOLVED_TOOLS + agentoven_delegate."""
    defs = []
    for t in RESOLVED_TOOLS:
        # Copy schema so we don't mutate the cached RESOLVED_TOOLS entry
        schema = dict(t.get("schema") or {})
        desc = schema.pop("description", t.get("name", ""))
        defs.append({
            "type": "function",
            "function": {
                "name": t["name"],
                "description": desc,
                "parameters": schema if schema else {"type": "object", "properties": {}},
            },
        })

    # Always include the virtual delegation tool
    defs.append({
        "type": "function",
        "function": {
            "name": "agentoven_delegate",
            "description": (
                "Delegate a task to another specialist agent in this kitchen. "
                "Use when the request requires a capability owned by a different agent."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "agent": {
                        "type": "string",
                        "description": "Name of the target agent to delegate to",
                    },
                    "message": {
                        "type": "string",
                        "description": "The task or message to send to the target agent",
                    },
                },
                "required": ["agent", "message"],
            },
        },
    })
    return defs


TOOL_DEFS = _build_tool_defs()


def _find_resolved_tool(name):
    """Return the ResolvedTool entry for the given tool name, or None."""
    for t in RESOLVED_TOOLS:
        if t.get("name") == name:
            return t
    return None


# ── LLM Client ─────────────────────────────────────────────────────────────────

def _get_api_url():
    """Return the chat completions URL for the configured provider."""
    if API_ENDPOINT:
        base = API_ENDPOINT.rstrip("/")
        if MODEL_PROVIDER == "azure-openai":
            return f"{base}/openai/deployments/{MODEL_NAME}/chat/completions?api-version=2024-12-01-preview"
        if MODEL_PROVIDER == "ollama":
            return f"{base}/api/chat"
        return f"{base}/v1/chat/completions"

    if MODEL_PROVIDER == "openai":
        return "https://api.openai.com/v1/chat/completions"
    if MODEL_PROVIDER == "anthropic":
        return "https://api.anthropic.com/v1/messages"
    if MODEL_PROVIDER == "ollama":
        return "http://localhost:11434/api/chat"
    return "https://api.openai.com/v1/chat/completions"


def _build_llm_request(messages, tools=None, stream=False):
    """Return (url, headers, body) for a chat completion request."""
    url = _get_api_url()

    if MODEL_PROVIDER == "anthropic":
        system_msg = ""
        user_msgs = []
        for m in messages:
            if m.get("role") == "system":
                system_msg = m.get("content", "")
            else:
                user_msgs.append(m)

        body = {
            "model": MODEL_NAME,
            "max_tokens": 4096,
            "messages": user_msgs,
            "stream": stream,
        }
        if system_msg:
            body["system"] = system_msg
        if tools:
            body["tools"] = [
                {
                    "name": t["function"]["name"],
                    "description": t["function"].get("description", ""),
                    "input_schema": t["function"].get("parameters", {}),
                }
                for t in tools
            ]
        headers = {
            "Content-Type": "application/json",
            "x-api-key": API_KEY,
            "anthropic-version": "2023-06-01",
        }

    elif MODEL_PROVIDER == "ollama":
        body = {
            "model": MODEL_NAME,
            "messages": messages,
            "stream": stream,
        }
        headers = {"Content-Type": "application/json"}

    else:
        # OpenAI / Azure OpenAI compatible
        body = {
            "model": MODEL_NAME,
            "messages": messages,
            "max_completion_tokens": 4096,
            "stream": stream,
        }
        if tools:
            body["tools"] = tools
            body["tool_choice"] = "auto"
        if stream:
            body["stream_options"] = {"include_usage": True}

        if MODEL_PROVIDER == "azure-openai":
            headers = {"Content-Type": "application/json", "api-key": API_KEY}
        else:
            headers = {"Content-Type": "application/json", "Authorization": f"Bearer {API_KEY}"}

    return url, headers, body


def call_llm(messages):
    """Single-turn LLM call without tools. Returns response text string."""
    url, headers, body = _build_llm_request(messages, tools=None, stream=False)
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            result = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        error_body = e.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"LLM API error {e.code}: {error_body}")

    if MODEL_PROVIDER == "anthropic":
        return result.get("content", [{}])[0].get("text", "")
    if MODEL_PROVIDER == "ollama":
        return result.get("message", {}).get("content", "")
    return result.get("choices", [{}])[0].get("message", {}).get("content", "")


def _call_llm_with_tools(messages, tools):
    """
    Non-streaming LLM call with tool support.
    Returns (response_text, tool_calls, usage) where tool_calls is a list of
    {"id": str, "function": {"name": str, "arguments": str}} dicts.
    """
    url, headers, body = _build_llm_request(messages, tools=tools, stream=False)
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            result = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        error_body = e.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"LLM API error {e.code}: {error_body}")

    if MODEL_PROVIDER == "anthropic":
        raw_usage = result.get("usage", {})
        usage = {
            "input_tokens":  raw_usage.get("input_tokens", 0),
            "output_tokens": raw_usage.get("output_tokens", 0),
            "total_tokens":  raw_usage.get("input_tokens", 0) + raw_usage.get("output_tokens", 0),
        }
        content = result.get("content", [])
        tool_calls = []
        text_parts = []
        for block in content:
            if block.get("type") == "tool_use":
                tool_calls.append({
                    "id": block.get("id", str(uuid.uuid4())),
                    "function": {
                        "name": block.get("name", ""),
                        "arguments": json.dumps(block.get("input", {})),
                    },
                })
            elif block.get("type") == "text":
                text_parts.append(block.get("text", ""))
        return "\n".join(text_parts), tool_calls, usage

    elif MODEL_PROVIDER == "ollama":
        msg = result.get("message", {})
        return msg.get("content", ""), [], {}

    else:
        # OpenAI / Azure OpenAI
        raw_usage = result.get("usage", {})
        usage = {
            "input_tokens":  raw_usage.get("prompt_tokens", 0),
            "output_tokens": raw_usage.get("completion_tokens", 0),
            "total_tokens":  raw_usage.get("total_tokens", 0),
        }
        choice = result.get("choices", [{}])[0]
        msg = choice.get("message", {})
        text = msg.get("content") or ""
        raw_tcs = msg.get("tool_calls") or []
        tool_calls = [
            {"id": tc.get("id", str(uuid.uuid4())), "function": tc.get("function", {})}
            for tc in raw_tcs
        ]
        return text, tool_calls, usage


def _call_llm_stream(messages, tools, on_token, on_tool_call_start, on_usage):
    """
    Streaming LLM call.
    Calls on_token(text) for each delta token chunk.
    Calls on_tool_call_start() when the first tool call delta appears.
    Calls on_usage(usage_dict) when the stream ends.
    Returns (tool_calls, usage).
    """
    url, headers, body = _build_llm_request(messages, tools=tools, stream=True)
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers=headers, method="POST")

    tool_calls_acc = {}  # index → {id, name, arguments}
    usage = {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
    tool_call_started = False

    try:
        with urllib.request.urlopen(req, timeout=180) as resp:
            for raw_line in resp:
                line = raw_line.decode("utf-8").strip()
                if not line or not line.startswith("data: "):
                    continue
                chunk_str = line[6:]
                if chunk_str == "[DONE]":
                    break
                try:
                    chunk = json.loads(chunk_str)
                except Exception:
                    continue

                if MODEL_PROVIDER == "anthropic":
                    ctype = chunk.get("type", "")
                    if ctype == "content_block_delta":
                        delta = chunk.get("delta", {})
                        if delta.get("type") == "text_delta":
                            on_token(delta.get("text", ""))
                    elif ctype == "message_start":
                        u = chunk.get("message", {}).get("usage", {})
                        usage["input_tokens"] = u.get("input_tokens", 0)
                    elif ctype == "message_delta":
                        u = chunk.get("usage", {})
                        usage["output_tokens"] = u.get("output_tokens", 0)
                        usage["total_tokens"] = usage["input_tokens"] + usage["output_tokens"]
                else:
                    # OpenAI / Azure OpenAI
                    if chunk.get("usage"):
                        u = chunk["usage"]
                        usage = {
                            "input_tokens":  u.get("prompt_tokens", 0),
                            "output_tokens": u.get("completion_tokens", 0),
                            "total_tokens":  u.get("total_tokens", 0),
                        }
                    choices = chunk.get("choices", [])
                    if not choices:
                        continue
                    delta = choices[0].get("delta", {})
                    if delta.get("content"):
                        on_token(delta["content"])
                    for tc in delta.get("tool_calls", []):
                        idx = tc.get("index", 0)
                        if idx not in tool_calls_acc:
                            tool_calls_acc[idx] = {"id": "", "name": "", "arguments": ""}
                            if not tool_call_started:
                                on_tool_call_start()
                                tool_call_started = True
                        if tc.get("id"):
                            tool_calls_acc[idx]["id"] = tc["id"]
                        fn = tc.get("function", {})
                        if fn.get("name"):
                            tool_calls_acc[idx]["name"] += fn["name"]
                        tool_calls_acc[idx]["arguments"] += fn.get("arguments", "")

    except urllib.error.HTTPError as e:
        error_body = e.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"LLM streaming error {e.code}: {error_body}")

    usage["total_tokens"] = usage.get("total_tokens") or (
        usage["input_tokens"] + usage["output_tokens"]
    )
    on_usage(usage)

    tool_calls = [
        {
            "id": v["id"] or str(uuid.uuid4()),
            "function": {"name": v["name"], "arguments": v["arguments"]},
        }
        for _, v in sorted(tool_calls_acc.items())
    ]
    return tool_calls, usage


# ── Tool Execution ─────────────────────────────────────────────────────────────

def execute_tool(name, args, trace_id=""):
    """Dispatch a tool call to an MCP server or the delegation handler."""
    if name == "agentoven_delegate":
        return _delegate_to_agent(
            args.get("agent", ""),
            args.get("message", ""),
            trace_id,
        )

    resolved = _find_resolved_tool(name)
    if resolved:
        endpoint = resolved.get("endpoint", "")
        if endpoint:
            return _call_mcp_tool(endpoint, name, args)

    return f"Error: tool '{name}' has no configured endpoint"


def _call_mcp_tool(endpoint, name, args):
    """
    Call an MCP server using JSON-RPC 2.0 (tools/call).
    Falls back to a REST POST /call on any error.
    """
    # Primary: standard MCP JSON-RPC 2.0
    url = endpoint.rstrip("/")
    body = json.dumps({
        "jsonrpc": "2.0",
        "id": str(uuid.uuid4()),
        "method": "tools/call",
        "params": {"name": name, "arguments": args},
    }).encode("utf-8")
    req = urllib.request.Request(
        url, data=body, headers={"Content-Type": "application/json"}, method="POST"
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            result = json.loads(resp.read().decode("utf-8"))
        if "error" in result:
            return f"Tool error: {result['error'].get('message', str(result['error']))}"
        rpc_result = result.get("result", result)
        content = rpc_result.get("content", [])
        if content:
            parts = [p.get("text", "") for p in content if p.get("type") == "text"]
            return "\n".join(parts) if parts else json.dumps(rpc_result)
        return json.dumps(rpc_result)
    except Exception:
        pass  # fall through to REST fallback

    # Fallback: REST POST to /call
    try:
        call_url = endpoint.rstrip("/") + "/call"
        rest_body = json.dumps({"name": name, "arguments": args}).encode("utf-8")
        rest_req = urllib.request.Request(
            call_url, data=rest_body,
            headers={"Content-Type": "application/json"}, method="POST",
        )
        with urllib.request.urlopen(rest_req, timeout=30) as resp2:
            r2 = json.loads(resp2.read().decode("utf-8"))
        content = r2.get("content", [])
        if content:
            return "\n".join(p.get("text", "") for p in content if p.get("type") == "text")
        return json.dumps(r2)
    except Exception as e:
        return f"Tool error: {e}"


def _delegate_to_agent(agent_name, message, trace_id=""):
    """Call another agent in the same kitchen via the control plane invoke API."""
    if not CONTROL_PLANE_URL:
        return "Delegation failed: AGENTOVEN_CONTROL_PLANE_URL is not configured"
    if not agent_name:
        return "Delegation failed: target agent name is required"

    url = f"{CONTROL_PLANE_URL.rstrip('/')}/api/v1/agents/{agent_name}/invoke"
    body = json.dumps({"message": message}).encode("utf-8")
    headers = {
        "Content-Type": "application/json",
        "X-Kitchen-Id": AGENT_KITCHEN,
    }
    if CONTROL_PLANE_TOKEN:
        headers["X-Service-Token"] = CONTROL_PLANE_TOKEN

    req = urllib.request.Request(url, data=body, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            result = json.loads(resp.read().decode("utf-8"))
        return result.get("response", json.dumps(result))
    except urllib.error.HTTPError as e:
        err = e.read().decode("utf-8", errors="replace")
        return f"Delegation error ({e.code}): {err}"
    except Exception as e:
        return f"Delegation error: {e}"


# ── Agentic Loop ───────────────────────────────────────────────────────────────

def _render_system(variables):
    """Substitute {{var}} placeholders in the system prompt."""
    text = AGENT_DESCRIPTION
    for k, v in (variables or {}).items():
        text = text.replace(f"{{{{{k}}}}}", str(v))
    return text


def run_invoke(message, variables=None, trace_id="", max_turns=None):
    """
    Full multi-turn agentic loop (synchronous).
    Returns (response_text, usage_dict).
    """
    max_turns = max_turns or MAX_TURNS
    system = _render_system(variables)

    messages = []
    if system:
        messages.append({"role": "system", "content": system})
    messages.append({"role": "user", "content": message})

    tools = TOOL_DEFS if TOOL_DEFS else None
    total_usage = {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}

    for _turn in range(max_turns):
        text, tool_calls, usage = _call_llm_with_tools(messages, tools)
        for k in total_usage:
            total_usage[k] += usage.get(k, 0)

        if not tool_calls:
            return text or "", total_usage

        # Append assistant turn with tool call declarations
        if MODEL_PROVIDER == "anthropic":
            blocks = []
            if text:
                blocks.append({"type": "text", "text": text})
            for tc in tool_calls:
                blocks.append({
                    "type": "tool_use",
                    "id": tc["id"],
                    "name": tc["function"]["name"],
                    "input": json.loads(tc["function"].get("arguments", "{}")),
                })
            messages.append({"role": "assistant", "content": blocks})
        else:
            assistant_msg = {
                "role": "assistant",
                "tool_calls": [
                    {"id": tc["id"], "type": "function", "function": tc["function"]}
                    for tc in tool_calls
                ],
            }
            if text:
                assistant_msg["content"] = text
            messages.append(assistant_msg)

        # Execute each tool call and append results
        for tc in tool_calls:
            fn_name = tc["function"]["name"]
            try:
                fn_args = json.loads(tc["function"].get("arguments", "{}"))
            except Exception:
                fn_args = {}
            result = execute_tool(fn_name, fn_args, trace_id)
            print(f"[{AGENT_NAME}] tool '{fn_name}' → {str(result)[:120]}", file=sys.stderr)

            if MODEL_PROVIDER == "anthropic":
                messages.append({
                    "role": "user",
                    "content": [{"type": "tool_result", "tool_use_id": tc["id"], "content": str(result)}],
                })
            else:
                messages.append({"role": "tool", "tool_call_id": tc["id"], "content": str(result)})

    return "Maximum turns reached without a final response.", total_usage


def run_invoke_stream(message, variables=None, trace_id="", max_turns=None, write_event=None):
    """
    Streaming agentic loop.
    Calls write_event(dict) with events:
      {"type": "token",       "content": "..."}
      {"type": "tool_call",   "name": "...", "args": {...}}
      {"type": "tool_result", "name": "...", "result": "..."}
      {"type": "done",        "usage": {...}}
      {"type": "error",       "message": "..."}
    """
    max_turns = max_turns or MAX_TURNS
    system = _render_system(variables)

    messages = []
    if system:
        messages.append({"role": "system", "content": system})
    messages.append({"role": "user", "content": message})

    tools = TOOL_DEFS if TOOL_DEFS else None
    total_usage = {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
    accumulated_text = []

    def on_token(text):
        write_event({"type": "token", "content": text})
        accumulated_text.append(text)

    def on_tool_call_start():
        accumulated_text.clear()

    def on_usage(u):
        for k in total_usage:
            total_usage[k] += u.get(k, 0)

    try:
        for _turn in range(max_turns):
            accumulated_text.clear()
            tool_calls, _usage = _call_llm_stream(
                messages, tools, on_token, on_tool_call_start, on_usage
            )
            if not tool_calls:
                break  # final response already streamed via on_token

            # Append assistant message with tool declarations
            assistant_msg = {
                "role": "assistant",
                "tool_calls": [
                    {"id": tc["id"], "type": "function", "function": tc["function"]}
                    for tc in tool_calls
                ],
            }
            if accumulated_text:
                assistant_msg["content"] = "".join(accumulated_text)
            messages.append(assistant_msg)

            # Execute tool calls and stream events
            for tc in tool_calls:
                fn_name = tc["function"]["name"]
                try:
                    fn_args = json.loads(tc["function"].get("arguments", "{}"))
                except Exception:
                    fn_args = {}
                write_event({"type": "tool_call", "name": fn_name, "args": fn_args})
                result = execute_tool(fn_name, fn_args, trace_id)
                result_str = str(result)
                write_event({"type": "tool_result", "name": fn_name, "result": result_str})
                messages.append({"role": "tool", "tool_call_id": tc["id"], "content": result_str})

    except Exception as e:
        write_event({"type": "error", "message": str(e)})
        traceback.print_exc(file=sys.stderr)

    write_event({"type": "done", "usage": total_usage})


# ── A2A Task Storage ───────────────────────────────────────────────────────────

tasks = {}  # task_id → task dict


def _a2a_create_task(task_id, user_message):
    """Create a new A2A task and run the full agentic loop."""
    task = {
        "id": task_id,
        "status": {"state": "working"},
        "artifacts": [],
        "history": [],
    }
    tasks[task_id] = task
    try:
        response_text, _ = run_invoke(user_message, trace_id=task_id)
        task["artifacts"].append({"parts": [{"type": "text", "text": response_text}]})
        task["status"] = {"state": "completed"}
        task["history"].append({
            "role": "agent",
            "parts": [{"type": "text", "text": response_text}],
        })
    except Exception as e:
        task["status"] = {
            "state": "failed",
            "message": {"role": "agent", "parts": [{"type": "text", "text": str(e)}]},
        }
        print(f"[{AGENT_NAME}] task {task_id} failed: {e}", file=sys.stderr)
        traceback.print_exc(file=sys.stderr)
    return task


# ── Agent Card ─────────────────────────────────────────────────────────────────

def _get_agent_card():
    return {
        "name": AGENT_NAME,
        "description": AGENT_DESCRIPTION,
        "url": f"http://localhost:{AGENT_PORT}",
        "version": "1.0.0",
        "capabilities": {
            "streaming": True,
            "pushNotifications": False,
            "toolCalling": bool(RESOLVED_TOOLS),
        },
        "skills": [{"id": s, "name": s} for s in SKILLS] if SKILLS else [],
        "tools": [t["function"]["name"] for t in TOOL_DEFS],
        "defaultInputModes": ["text"],
        "defaultOutputModes": ["text"],
    }


# ── JSON-RPC 2.0 Handler ───────────────────────────────────────────────────────

def _handle_jsonrpc(request):
    method = request.get("method", "")
    params = request.get("params", {})
    req_id = request.get("id")

    if method == "tasks/send":
        task_id  = params.get("id", str(uuid.uuid4()))
        message  = params.get("message", {})
        user_text = "".join(
            part.get("text", "")
            for part in message.get("parts", [])
            if isinstance(part, dict) and "text" in part
        )
        if not user_text:
            return _jsonrpc_error(req_id, -32602, "No text content in message")
        return _jsonrpc_result(req_id, _a2a_create_task(task_id, user_text))

    if method == "tasks/get":
        task_id = params.get("id", "")
        task = tasks.get(task_id)
        if not task:
            return _jsonrpc_error(req_id, -32602, f"Task '{task_id}' not found")
        return _jsonrpc_result(req_id, task)

    if method == "tasks/cancel":
        task_id = params.get("id", "")
        task = tasks.get(task_id)
        if task:
            task["status"] = {"state": "canceled"}
        return _jsonrpc_result(req_id, {"id": task_id, "status": {"state": "canceled"}})

    return _jsonrpc_error(req_id, -32601, f"Method '{method}' not found")


def _jsonrpc_result(req_id, result):
    return {"jsonrpc": "2.0", "id": req_id, "result": result}


def _jsonrpc_error(req_id, code, message):
    return {"jsonrpc": "2.0", "id": req_id, "error": {"code": code, "message": message}}


# ── HTTP Handler ───────────────────────────────────────────────────────────────

class AgentHandler(BaseHTTPRequestHandler):

    def log_message(self, format, *args):  # noqa: A002
        print(f"[{AGENT_NAME}] {format % args}", file=sys.stderr)

    # ── GET ────────────────────────────────────────────────────────────────────

    def do_GET(self):
        if self.path in ("/health", "/status"):
            self._respond_json(200, {
                "status": "healthy",
                "agent": AGENT_NAME,
                "kitchen": AGENT_KITCHEN,
                "model": f"{MODEL_PROVIDER}/{MODEL_NAME}",
                "tools": [t["function"]["name"] for t in TOOL_DEFS],
                "pid": os.getpid(),
            })
        elif self.path in ("/.well-known/agent-card.json", "/agent-card"):
            self._respond_json(200, _get_agent_card())
        else:
            self._respond_json(404, {"error": "not found"})

    # ── POST ───────────────────────────────────────────────────────────────────

    def do_POST(self):
        content_length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(content_length)
        try:
            request = json.loads(body)
        except json.JSONDecodeError:
            self._respond_json(400, {"error": "invalid JSON"})
            return

        path = self.path.split("?")[0]

        if path in ("/", "/a2a"):
            self._respond_json(200, _handle_jsonrpc(request))
        elif path == "/invoke":
            self._handle_invoke(request)
        elif path == "/invoke/stream":
            self._handle_stream(request)
        else:
            self._respond_json(404, {"error": "not found"})

    # ── Invoke (synchronous) ───────────────────────────────────────────────────

    def _handle_invoke(self, req):
        message = req.get("message", "")
        if not message:
            self._respond_json(400, {"error": "missing 'message' field"})
            return

        variables = req.get("variables") or {}
        trace_id  = req.get("trace_id", str(uuid.uuid4()))
        max_turns = req.get("max_turns") or MAX_TURNS

        try:
            response, usage = run_invoke(
                message, variables=variables, trace_id=trace_id, max_turns=max_turns
            )
            self._respond_json(200, {
                "response": response,
                "usage": {
                    "input_tokens":  usage.get("input_tokens", 0),
                    "output_tokens": usage.get("output_tokens", 0),
                    "total_tokens":  usage.get("total_tokens", 0),
                },
            })
        except Exception as e:
            traceback.print_exc(file=sys.stderr)
            self._respond_json(500, {"error": str(e)})

    # ── Stream (SSE) ───────────────────────────────────────────────────────────

    def _handle_stream(self, req):
        message = req.get("message", "")
        if not message:
            self._respond_json(400, {"error": "missing 'message' field"})
            return

        variables = req.get("variables") or {}
        trace_id  = req.get("trace_id", str(uuid.uuid4()))
        max_turns = req.get("max_turns") or MAX_TURNS

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "keep-alive")
        self.send_header("X-Accel-Buffering", "no")
        self.end_headers()

        def write_event(event_dict):
            try:
                line = "data: " + json.dumps(event_dict) + "\n\n"
                self.wfile.write(line.encode("utf-8"))
                self.wfile.flush()
            except Exception:
                pass  # client disconnected

        try:
            run_invoke_stream(
                message, variables=variables, trace_id=trace_id,
                max_turns=max_turns, write_event=write_event,
            )
        except Exception as e:
            write_event({"type": "error", "message": str(e)})

    # ── Helpers ────────────────────────────────────────────────────────────────

    def _respond_json(self, status, data):
        body = json.dumps(data).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


# ── Threaded Server ────────────────────────────────────────────────────────────

class _ThreadedHTTPServer(ThreadingMixIn, HTTPServer):
    """Serve each request in its own thread (enables concurrent SSE + invoke)."""
    daemon_threads = True


# ── Entry Point ────────────────────────────────────────────────────────────────

def main():
    if not API_KEY and MODEL_PROVIDER not in ("ollama",):
        print(
            f"[{AGENT_NAME}] WARNING: AGENT_API_KEY is not set — LLM calls will fail.",
            file=sys.stderr,
        )

    server = _ThreadedHTTPServer(("0.0.0.0", AGENT_PORT), AgentHandler)

    mcp_count  = len(RESOLVED_TOOLS)
    tool_names = ", ".join(t["function"]["name"] for t in TOOL_DEFS) or "(none)"
    print(f"[{AGENT_NAME}] 🔥 Agent process started on port {AGENT_PORT}", file=sys.stderr)
    print(f"[{AGENT_NAME}]    model:     {MODEL_PROVIDER}/{MODEL_NAME}", file=sys.stderr)
    print(f"[{AGENT_NAME}]    kitchen:   {AGENT_KITCHEN}", file=sys.stderr)
    print(f"[{AGENT_NAME}]    tools:     {mcp_count} MCP + agentoven_delegate → {tool_names}", file=sys.stderr)
    print(f"[{AGENT_NAME}]    endpoints: /invoke  /invoke/stream  /a2a  /health", file=sys.stderr)
    print(f"[{AGENT_NAME}]    pid:       {os.getpid()}", file=sys.stderr)
    sys.stderr.flush()

    # Signal the parent process (or health-check) that we are listening
    print("AGENT_READY", flush=True)

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print(f"\n[{AGENT_NAME}] shutting down…", file=sys.stderr)
        server.shutdown()


if __name__ == "__main__":
    main()
