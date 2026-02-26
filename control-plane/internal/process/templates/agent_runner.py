#!/usr/bin/env python3
"""
AgentOven Agent Runner — A2A-compatible agent process.

This script is auto-generated and launched by the AgentOven control plane
when an agent is baked. It runs as an independent HTTP server that:
  1. Listens for A2A JSON-RPC 2.0 requests
  2. Calls the configured LLM via the provider's API
  3. Returns responses as A2A task results

Environment variables (set by the control plane):
  AGENT_NAME           — agent name
  AGENT_KITCHEN        — kitchen (tenant) scope
  AGENT_PORT           — port to listen on
  AGENT_DESCRIPTION    — agent description / system prompt
  AGENT_MODEL_PROVIDER — provider kind (openai, anthropic, ollama, azure-openai)
  AGENT_MODEL_NAME     — model name (gpt-5, claude-4-sonnet, etc.)
  AGENT_API_KEY        — provider API key
  AGENT_API_ENDPOINT   — provider API endpoint (optional, for Azure OpenAI / Ollama)
  AGENT_SKILLS         — comma-separated skill list
  AGENT_MAX_TURNS      — max agentic loop turns (default: 10)
"""

import json
import os
import sys
import uuid
import traceback
from datetime import datetime, timezone
from http.server import HTTPServer, BaseHTTPRequestHandler

# ── Configuration ─────────────────────────────────────────────

AGENT_NAME = os.environ.get("AGENT_NAME", "unnamed-agent")
AGENT_KITCHEN = os.environ.get("AGENT_KITCHEN", "default")
AGENT_PORT = int(os.environ.get("AGENT_PORT", "9000"))
AGENT_DESCRIPTION = os.environ.get("AGENT_DESCRIPTION", "An AgentOven managed agent")
MODEL_PROVIDER = os.environ.get("AGENT_MODEL_PROVIDER", "openai")
MODEL_NAME = os.environ.get("AGENT_MODEL_NAME", "gpt-5")
API_KEY = os.environ.get("AGENT_API_KEY", "")
API_ENDPOINT = os.environ.get("AGENT_API_ENDPOINT", "")
SKILLS = [s.strip() for s in os.environ.get("AGENT_SKILLS", "").split(",") if s.strip()]
MAX_TURNS = int(os.environ.get("AGENT_MAX_TURNS", "10"))

# ── LLM Client ───────────────────────────────────────────────

def get_api_url():
    """Get the chat completions URL for the configured provider."""
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


def call_llm(messages):
    """Call the configured LLM and return the response text."""
    import urllib.request
    import urllib.error

    url = get_api_url()

    if MODEL_PROVIDER == "anthropic":
        # Anthropic uses a different API format
        system_msg = ""
        user_msgs = []
        for m in messages:
            if m["role"] == "system":
                system_msg = m["content"]
            else:
                user_msgs.append(m)

        body = {
            "model": MODEL_NAME,
            "max_tokens": 4096,
            "messages": user_msgs,
        }
        if system_msg:
            body["system"] = system_msg

        headers = {
            "Content-Type": "application/json",
            "x-api-key": API_KEY,
            "anthropic-version": "2023-06-01",
        }
    elif MODEL_PROVIDER == "ollama":
        body = {
            "model": MODEL_NAME,
            "messages": messages,
            "stream": False,
        }
        headers = {"Content-Type": "application/json"}
    else:
        # OpenAI / Azure OpenAI compatible
        body = {
            "model": MODEL_NAME,
            "messages": messages,
            "max_completion_tokens": 4096,
        }
        headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {API_KEY}",
        }
        if MODEL_PROVIDER == "azure-openai":
            headers = {
                "Content-Type": "application/json",
                "api-key": API_KEY,
            }

    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers=headers, method="POST")

    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            result = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        error_body = e.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"LLM API error {e.code}: {error_body}")

    # Extract response text
    if MODEL_PROVIDER == "anthropic":
        return result.get("content", [{}])[0].get("text", "")
    if MODEL_PROVIDER == "ollama":
        return result.get("message", {}).get("content", "")
    # OpenAI / Azure OpenAI
    return result.get("choices", [{}])[0].get("message", {}).get("content", "")


# ── A2A Task Storage ─────────────────────────────────────────

tasks = {}  # task_id → task dict


def create_task(task_id, user_message):
    """Create a new task and run the agent loop."""
    task = {
        "id": task_id,
        "status": {"state": "working"},
        "artifacts": [],
        "history": [],
    }
    tasks[task_id] = task

    try:
        messages = []
        if AGENT_DESCRIPTION:
            messages.append({"role": "system", "content": AGENT_DESCRIPTION})
        messages.append({"role": "user", "content": user_message})

        response_text = call_llm(messages)

        task["artifacts"].append({
            "parts": [{"type": "text", "text": response_text}],
        })
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
        print(f"[{AGENT_NAME}] Task {task_id} failed: {e}", file=sys.stderr)
        traceback.print_exc(file=sys.stderr)

    return task


# ── Agent Card ────────────────────────────────────────────────

def get_agent_card():
    """Return the A2A agent card for discovery."""
    return {
        "name": AGENT_NAME,
        "description": AGENT_DESCRIPTION,
        "url": f"http://localhost:{AGENT_PORT}",
        "version": "1.0.0",
        "capabilities": {
            "streaming": False,
            "pushNotifications": False,
        },
        "skills": [{"id": s, "name": s} for s in SKILLS] if SKILLS else [],
        "defaultInputModes": ["text"],
        "defaultOutputModes": ["text"],
    }


# ── JSON-RPC 2.0 Handler ─────────────────────────────────────

def handle_jsonrpc(request):
    """Process an A2A JSON-RPC 2.0 request."""
    method = request.get("method", "")
    params = request.get("params", {})
    req_id = request.get("id")

    if method == "tasks/send":
        # Create or continue a task
        task_id = params.get("id", str(uuid.uuid4()))
        message = params.get("message", {})
        user_text = ""
        for part in message.get("parts", []):
            # A2A parts: {"text": "..."} or {"type": "text", "text": "..."}
            if isinstance(part, dict) and "text" in part:
                user_text += part.get("text", "")

        if not user_text:
            return jsonrpc_error(req_id, -32602, "No text content in message")

        task = create_task(task_id, user_text)
        return jsonrpc_result(req_id, task)

    elif method == "tasks/get":
        task_id = params.get("id", "")
        task = tasks.get(task_id)
        if not task:
            return jsonrpc_error(req_id, -32602, f"Task '{task_id}' not found")
        return jsonrpc_result(req_id, task)

    elif method == "tasks/cancel":
        task_id = params.get("id", "")
        task = tasks.get(task_id)
        if task:
            task["status"] = {"state": "canceled"}
        return jsonrpc_result(req_id, {"id": task_id, "status": {"state": "canceled"}})

    else:
        return jsonrpc_error(req_id, -32601, f"Method '{method}' not found")


def jsonrpc_result(req_id, result):
    return {"jsonrpc": "2.0", "id": req_id, "result": result}


def jsonrpc_error(req_id, code, message):
    return {"jsonrpc": "2.0", "id": req_id, "error": {"code": code, "message": message}}


# ── HTTP Server ───────────────────────────────────────────────

class AgentHandler(BaseHTTPRequestHandler):
    """HTTP request handler for the A2A agent."""

    def log_message(self, format, *args):
        """Override to use structured logging."""
        print(f"[{AGENT_NAME}] {format % args}", file=sys.stderr)

    def do_GET(self):
        if self.path == "/health" or self.path == "/status":
            self._respond_json(200, {
                "status": "healthy",
                "agent": AGENT_NAME,
                "kitchen": AGENT_KITCHEN,
                "model": f"{MODEL_PROVIDER}/{MODEL_NAME}",
                "pid": os.getpid(),
            })
        elif self.path == "/.well-known/agent-card.json" or self.path == "/agent-card":
            self._respond_json(200, get_agent_card())
        else:
            self._respond_json(404, {"error": "not found"})

    def do_POST(self):
        content_length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(content_length)

        try:
            request = json.loads(body)
        except json.JSONDecodeError:
            self._respond_json(400, {"error": "invalid JSON"})
            return

        # Handle A2A JSON-RPC
        if self.path == "/" or self.path == "/a2a":
            result = handle_jsonrpc(request)
            self._respond_json(200, result)
        else:
            self._respond_json(404, {"error": "not found"})

    def _respond_json(self, status, data):
        body = json.dumps(data).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def main():
    """Start the agent HTTP server."""
    if not API_KEY and MODEL_PROVIDER not in ("ollama",):
        print(f"[{AGENT_NAME}] WARNING: No API key set (AGENT_API_KEY). LLM calls will fail.", file=sys.stderr)

    server = HTTPServer(("0.0.0.0", AGENT_PORT), AgentHandler)
    print(f"[{AGENT_NAME}] 🔥 Agent process started on port {AGENT_PORT}", file=sys.stderr)
    print(f"[{AGENT_NAME}]    Model: {MODEL_PROVIDER}/{MODEL_NAME}", file=sys.stderr)
    print(f"[{AGENT_NAME}]    Kitchen: {AGENT_KITCHEN}", file=sys.stderr)
    print(f"[{AGENT_NAME}]    PID: {os.getpid()}", file=sys.stderr)
    sys.stderr.flush()

    # Write ready signal so the parent process knows we're listening
    print("AGENT_READY", flush=True)

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print(f"\n[{AGENT_NAME}] Shutting down...", file=sys.stderr)
        server.shutdown()


if __name__ == "__main__":
    main()
