# ADR-0017: Framework-Native Managed Agents

- **Date:** 2026-05-14
- **Status:** Accepted
- **Deciders:** siddartha
- **Relates to:** [ADR-0007](0007-control-plane-as-a2a-gateway.md), [ADR-0009](0009-pluggable-test-runner-architecture.md), [ADR-0012](0012-agent-packaging-distribution.md)

## Context

AgentOven's managed-mode executor runs the entire LLM reasoning loop inside the
control plane (Go): model routing, MCP tool execution, context window compression,
session memory. This works well for agents built against AgentOven's own abstractions.

However, the ecosystem of existing agent frameworks is large:

- **LangChain** — stateless chains and agent executors with hundreds of tool integrations
- **LangGraph** — stateful graphs with checkpointing and human-in-the-loop interrupts
- **CrewAI** — crew-based multi-agent orchestration
- **AutoGen, Haystack, Semantic Kernel** — other widely-used frameworks

Today, integrating any of these requires the developer to:

1. Abandon their existing agent code and rewrite against AgentOven's model, or
2. Register the agent as `mode=external` and lose all lifecycle management, tracing,
   guardrails, environment promotion, and provider management

Neither is acceptable. AgentOven as a platform should be the **best way to manage,
observe, and operate agents regardless of the framework they were built in** — not a
reason to rewrite them.

### Existing Process Manager Capability

`control-plane/internal/process` already spawns arbitrary processes (local Python,
Docker, K8s) and injects agent configuration via environment variables. What it was
missing was:

1. A `runtime` field on the Agent model to distinguish execution intent
2. Richer ingredient injection (tools as JSON, prompts, data sources)
3. A proxy path in the invoke handler that routes calls to the spawned process
4. A Python SDK module that reads the injected env vars and serves an A2A-compatible
   HTTP endpoint the process manager can probe

### LangGraph and Recipes

LangGraph graphs model multi-step workflows with conditional routing and human
checkpoints. AgentOven already has a native analogue:

- LangGraph nodes → Recipe `agent` steps
- LangGraph conditional edges → Recipe `condition` / `router` steps
- LangGraph `interrupt_before` checkpoints → Recipe `human_gate` steps

`human_gate` is already a fully-implemented, durable Recipe primitive (ApprovalRecord
in Postgres, SLA timeouts, Slack/Teams/API approval channels). This means the right
mapping is:

| LangGraph concept | AgentOven surface |
|---|---|
| Simple atomic graph (no interrupts) | Agent with `runtime=langgraph` |
| Multi-phase graph with checkpoints | Recipe (DAG) with `human_gate` steps |
| Individual graph node | Agent step in the Recipe DAG |

## Decision

### 1. `AgentRuntime` field on the Agent model

Add a `runtime` field (alongside the existing `mode` and `execution_mode` fields):

```go
// pkg/models/models.go

type AgentRuntime string

const (
    // RuntimeAgentOven is the default. The Go executor runs the LLM loop.
    RuntimeAgentOven AgentRuntime = "agentoven"
    // RuntimeLangChain — user's process wraps a LangChain Runnable or AgentExecutor.
    RuntimeLangChain AgentRuntime = "langchain"
    // RuntimeLangGraph — user's process wraps a compiled LangGraph graph (atomic).
    RuntimeLangGraph AgentRuntime = "langgraph"
    // RuntimeCrewAI — user's process wraps a CrewAI Crew.
    RuntimeCrewAI AgentRuntime = "crewai"
    // RuntimeCustom — user's process implements the /invoke protocol directly.
    RuntimeCustom AgentRuntime = "custom"
)

// IsFrameworkNative returns true when the agent's LLM loop runs in an external
// process rather than the AgentOven Go executor.
func (a *Agent) IsFrameworkNative() bool {
    return a.Runtime != "" && a.Runtime != RuntimeAgentOven
}
```

Add `Entrypoint string` to Agent — the shell command to execute when baking locally
(e.g. `python myagent.py`). Docker and K8s modes use a container image instead.

**Crucially, `mode` stays `managed`.** AgentOven still owns:
- Lifecycle (bake/cool/rewarm), process spawning, port allocation
- Input/output guardrail evaluation
- Trace recording and OTel span emission
- Ingredient resolution (model endpoint, API key, tools, prompt)
- Scoped-key access control
- Environment promotion

Only the LLM loop moves to the user's process.

### 2. Richer ingredient injection via environment variables

Extend `process.Manager.buildEnvironment()` to inject the full resolved ingredient set:

| Variable | Content |
|---|---|
| `AGENT_RUNTIME` | Value of `agent.Runtime` |
| `AGENT_MODEL_PROVIDER` | Resolved model provider kind (openai, anthropic, ollama, ...) |
| `AGENT_MODEL_NAME` | Resolved model name |
| `AGENT_API_KEY` | Provider API key (from resolved config) |
| `AGENT_API_ENDPOINT` | Provider API endpoint (Azure OpenAI, Ollama, etc.) |
| `AGENT_TOOLS_JSON` | JSON array: `[{name, endpoint, transport, description}]` |
| `AGENT_PROMPT_TEMPLATE` | Resolved prompt template text |
| `AGENT_DATA_SOURCES_JSON` | JSON array of resolved data sources |
| `AGENTOVEN_CONTROL_PLANE_URL` | Control plane base URL for SDK callbacks |

This lets the SDK reconstruct full provider and tool configuration without any
network calls at startup.

### 3. Entrypoint dispatch in process executors

**Local mode** (`process/local.go`):

```
if agent.Entrypoint != "" {
    // Run the developer's process
    parts := strings.Fields(agent.Entrypoint)
    cmd = exec.CommandContext(procCtx, parts[0], parts[1:]...)
} else {
    // Default: run embedded agent_runner.py (existing behaviour)
    cmd = exec.CommandContext(procCtx, pythonBin, scriptPath)
}
```

The readiness protocol is unchanged: the user's process must print `AGENT_READY` to
stdout within 15 seconds. The SDK does this automatically (see §4).

**Docker mode** (`process/docker.go`): if `Entrypoint != ""`, pass it as the Docker
CMD override (`docker run ... <image> <entrypoint>`).

**K8s mode** (`process/k8s.go`): set `spec.containers[0].command` from entrypoint.

### 4. Proxy path in invoke handlers

When `agent.IsFrameworkNative()` is true, the `InvokeAgent`, `TestAgent`, and A2A
`tasks/send` handlers route the call to the agent's running process instead of the
Go executor:

```
proxyToProcess(ctx, agent, message):
    1. Verify agent.Process.Status == running
    2. Open OTel span "agent.run" (same attributes as Executor.Execute)
    3. POST agent.Process.Endpoint/invoke  {message, trace_id, variables}
    4. 30-second timeout
    5. Read response {response, usage}
    6. Store Trace record (same shape as managed-mode traces)
    7. Return response
```

Input/output guardrails still run (before/after the proxy call). Scoped-key checks
still run. The only difference from the caller's perspective is that the LLM loop
executes in the user's process.

### 5. Python SDK `agentoven.runtime` module

A new server-side module (`sdk/python/agentoven/runtime/`) provides:

**`AgentOvenRuntime`** — reads `AGENT_*` env vars and exposes helpers:

```python
rt = AgentOvenRuntime()

# Auto-constructs ChatOpenAI / ChatAnthropic / ChatOllama from injected config
llm = rt.build_langchain_llm()

# Returns list of LangChain BaseTool wrappers over MCP endpoints
tools = rt.build_mcp_tools()

# System prompt text if a prompt ingredient was resolved
prompt = rt.prompt          # str | None
```

**`AgentOvenServer`** — FastAPI server with the process protocol:

```
POST /invoke          {message, trace_id?, variables?} → {response, usage}
GET  /status          → {status: "ok", agent: name, runtime: kind}
GET  /.well-known/agent-card.json → A2A agent card JSON
```

On startup it prints `AGENT_READY` to stdout, triggering `LocalExecutor`'s existing
readiness detection.

**Framework adapters:**

```python
# adapters/langchain.py
class LangChainAdapter:
    def __init__(self, runnable):  # Any LangChain Runnable or AgentExecutor
        ...
    async def run(self, message: str) -> str: ...

# adapters/langgraph.py
class LangGraphAdapter:
    def __init__(self, compiled_graph):  # CompiledGraph from langgraph.compile()
        ...
    async def run(self, message: str) -> str: ...  # runs graph to completion
```

**Top-level convenience:**

```python
# Zero-change integration for an existing agent:
import agentoven.runtime as ao

rt = ao.AgentOvenRuntime()
my_agent = build_my_existing_langchain_agent(
    llm=rt.build_langchain_llm(),
    tools=rt.build_mcp_tools(),
)
ao.serve(ao.LangChainAdapter(my_agent))
```

### 6. Dashboard fields

`AgentForm` gains (mode=managed only):

- **Runtime** dropdown: AgentOven (default) | LangChain | LangGraph | CrewAI | Custom
- **Entrypoint** text input (shown when runtime ≠ agentoven)
- **SDK snippet** (shown when runtime ≠ agentoven):
  ```
  pip install "agentoven[runtime]"
  ```

`Agent` TypeScript interface gains `runtime?: string` and `entrypoint?: string`.

### 7. Recommended mapping for LangGraph

| Use case | Recommended approach |
|---|---|
| Simple graph, no interrupts | `runtime=langgraph` Agent |
| Multi-phase graph | Recipe DAG: each phase = `agent` step, each interrupt = `human_gate` |
| Graph with tool calls | Recipe `agent` steps (the agent runs the graph node, AgentOven routes tools via MCP) |

The `human_gate` step is already fully durable (Postgres-backed ApprovalRecord, SLA
timeouts, multi-channel approval). No engine changes are needed.

## Consequences

### Positive
- Existing LangChain/LangGraph/CrewAI agents can be onboarded with 3 lines of SDK code
- No rewrite required — the developer keeps their framework exactly as-is
- AgentOven manages lifecycle, tracing, guardrails, providers, environments uniformly
  regardless of underlying framework
- Recipe `human_gate` steps map perfectly to LangGraph interrupt_before semantics
- Framework-native agents are fully usable as Recipe `agent` step targets (they
  expose A2A via the SDK server)
- Provider credentials are managed once in AgentOven, injected via env vars — no
  hardcoded API keys in agent code

### Negative / Risks
- The `AGENT_READY` stdout signal contract must be documented clearly for custom runtimes
- `proxyToProcess()` is synchronous with a 30s timeout. Long-running graph executions
  (multi-minute) will time out — callers should use the A2A `tasks/send` async path
- LangGraph graphs with internal state use their own session store (MemorySaver or
  user-provided). Session memory is not unified in OSS. See ADR-0021 (Pro) for the
  unified session store solution.
- MCP tool calls from within the user's process make direct HTTP requests to tool
  endpoints. In-process tool observability (individual tool spans) is not captured
  by the control plane — only the top-level invoke span is recorded.

### Neutral
- `mode=external` agents are unaffected — this ADR adds a new path, not a replacement
- The Go executor path (no runtime field, or runtime=agentoven) is completely unchanged

## Out of Scope (Future ADRs)

- TypeScript SDK `@agentoven/runtime` server module
- Streaming responses from framework processes (requires chunked /invoke protocol)
- Per-tool OTel spans emitted from inside the user's process
- Agent-scoped service-account token injection for MCP auth (see ADR-0017 Pro)
- LangGraph session store integration for OSS (kept as LangGraph-native MemorySaver)
