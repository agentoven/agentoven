#!/usr/bin/env bash
# Smoke test of a running control plane (in-memory store, auth off): the provider, tool and
# agent routes and the bake gate. CI starts the server, then runs this; you can run it by hand
# against any local server:
#
#   AGENTOVEN_PORT=18080 AGENTOVEN_TOOL_EGRESS_ALLOW=localhost ./agentoven-server &
#   scripts/ci/control-plane-smoke.sh http://localhost:18080
#
# The tool registered below points at localhost, which the egress guard refuses unless the server
# was started with AGENTOVEN_TOOL_EGRESS_ALLOW=localhost (ADR-0037).
set -euo pipefail

ROOT="${1:-http://localhost:8080}"
BASE="$ROOT/api/v1"
BODY="$(mktemp)"
trap 'rm -f "$BODY"' EXIT

# call METHOD PATH [JSON] -> prints the body, sets STATUS
call() {
  local method="$1" path="$2" data="${3:-}"
  if [ -n "$data" ]; then
    STATUS=$(curl -s -o "$BODY" -w '%{http_code}' -X "$method" "$BASE$path" \
      -H 'Content-Type: application/json' -d "$data")
  else
    STATUS=$(curl -s -o "$BODY" -w '%{http_code}' -X "$method" "$BASE$path")
  fi
  cat "$BODY"
  echo
}

# expect WANT_STATUS DESCRIPTION: fail, with the response, when the last call did not return it
expect() {
  if [ "$STATUS" != "$1" ]; then
    echo "FAIL: $2: expected HTTP $1, got $STATUS" >&2
    cat "$BODY" >&2
    echo >&2
    exit 1
  fi
}

echo "=== Wait for the server ==="
for _ in $(seq 1 30); do
  if curl -sf "$ROOT/health" > /dev/null; then break; fi
  sleep 1
done
curl -sf "$ROOT/health"
echo

echo "=== Create provider ==="
call POST /models/providers '{"name":"test-ollama","kind":"ollama","endpoint":"http://localhost:11434","models":["llama3"],"config":{}}'
expect 201 "create provider"

echo "=== List providers ==="
call GET /models/providers
expect 200 "list providers"

echo "=== Update provider ==="
call PUT /models/providers/test-ollama '{"is_default":true,"config":{"cost_per_1k_input":0.001}}'
expect 200 "update provider"

echo "=== Create tool ==="
call POST /tools '{"name":"search","endpoint":"http://localhost:3001/mcp","transport":"http","description":"Search tool","enabled":true}'
expect 201 "create tool"

echo "=== Update tool (toggle enabled) ==="
call PUT /tools/search '{"enabled":false}'
expect 200 "update tool"

echo "=== Create agent with ingredients ==="
# Every name an agent refers to (provider, skills, tools) must exist in the kitchen (ADR-0041),
# so this agent names only the provider created above.
call POST /agents '{
  "name":"test-agent",
  "description":"Integration test agent",
  "framework":"custom",
  "model_provider":"test-ollama",
  "model_name":"llama3",
  "ingredients":[
    {"name":"system-prompt","kind":"prompt","config":{"text":"You are a test assistant."},"required":true}
  ]
}'
expect 201 "create agent"

echo "=== Get agent ==="
call GET /agents/test-agent
expect 200 "get agent"

echo "=== Bake agent ==="
# Bake succeeds (202): the ollama provider needs no API key.
call POST /agents/test-agent/bake
expect 202 "bake agent"

sleep 1
echo "=== Agent status after bake ==="
call GET /agents/test-agent
expect 200 "get baked agent"

echo "=== Create agent without a model (bake must be refused) ==="
call POST /agents '{"name":"bad-agent","description":"No model"}'
expect 201 "create agent without a model"
# 422: the configuration check names every problem (ADR-0041); it was a bare 400 before.
call POST /agents/bad-agent/bake
expect 422 "bake an agent without a model"

echo "=== List traces ==="
call GET /traces
expect 200 "list traces"

echo "=== Cleanup ==="
call DELETE /agents/test-agent
call DELETE /agents/bad-agent
call DELETE /tools/search
call DELETE /models/providers/test-ollama

echo "All integration checks passed"
