# Hosting Deep Agents

[Deep Agents](https://github.com/langchain-ai/deepagents) is an agent harness built on LangGraph:
`create_deep_agent()` returns a compiled graph that plans with a todo list, keeps a virtual
filesystem, spawns subagents and compresses its own context. AgentOven hosts that graph as a
runtime, next to LangChain and LangGraph, and puts the control plane around it. You write the
agent the way its docs say; AgentOven decides what reaches it and what leaves it.

Deep Agents and AgentOven are complementary. Deep Agents is a library you build an agent with.
AgentOven is the control plane that runs agents of any framework. The runtime below is how the
two meet.

## Setup

```python
# agent.py
from agentoven.runtime import AgentOvenRuntime, DeepAgentsAdapter, build_deep_agent, serve

runtime = AgentOvenRuntime()
agent = build_deep_agent(runtime, tools=[my_tool], system_prompt="You are a research agent.")
serve(DeepAgentsAdapter(agent), runtime)
```

Register the agent with `"runtime": "deepagents"` and `"entrypoint": "python agent.py"`, bake it,
and invoke it like any other agent. A complete example is in
[`examples/deep-research-agent`](../examples/deep-research-agent). The dashboard's runtime picker
has a "Deep Agents" entry, and the CLI knows the framework as `deepagents`.

`build_deep_agent` takes the model from `AGENT_MODEL_PROVIDER`, `AGENT_MODEL_NAME`,
`AGENT_API_KEY` and `AGENT_API_ENDPOINT`, the instructions from `AGENT_DESCRIPTION`, and adds the
agent's resolved MCP tools to the ones you pass. It needs `pip install deepagents` (Python 3.11 or
newer).

## The process contract

The SDK's server speaks everything the control plane expects of an agent process:

| Endpoint | What it is |
|---|---|
| `POST /invoke` | One call: `{message, session_id?}` returns `{response, usage, metadata?}` |
| `POST /invoke/stream` | The same as server-sent events: `token`, `tool_call`, `todos`, then `done` |
| `POST /a2a`, `POST /` | A2A JSON-RPC: `tasks/send`, `tasks/get`, `tasks/cancel` |
| `GET /health` | Liveness, polled by the control plane |

It listens on `AGENT_PORT`, prints `AGENT_READY` once it is accepting connections, and refuses any
POST that does not present the agent's own token (`AGENT_A2A_TOKEN`, header
`X-AgentOven-A2A-Token`), which the control plane presents when it relays a call.

The session ID of a call becomes the LangGraph `thread_id`. Build the agent with a checkpointer
(`build_deep_agent(..., checkpointer=...)`) and a conversation continues across calls.

## What is enforced, and where

AgentOven enforces at the edge of the process, because that is the only place it can:

- **Input guardrails** run on the message before it reaches the agent, on every route: invoke,
  streaming, A2A, sessions.
- **Output guardrails** run on the answer before it reaches the caller. A streamed or A2A reply
  from an agent with output rules is held back and released only once checked, so such an agent
  does not stream token by token.
- **The agent's token** keeps anything else that can reach the port from running the agent.

These cannot be applied inside the process:

- **Tools.** The agent's tools, including Deep Agents' built-in `execute` and file tools, run in
  your process with its permissions. AgentOven does not sandbox them. Give the process only the
  access the agent needs.
- **Subagents.** Deep Agents' built-in `task` tool spawns subagents inside the process. AgentOven
  does not see or govern them. (An agent run by AgentOven's own loop can spawn subagents that are
  bound by the parent's budget and guardrails; see the Pro subagent runtime.)
- **Model spend.** The agent calls its model provider directly. The usage it reports is recorded,
  but the control plane cannot cap it, so per-task spend caps and background tasks apply to
  agents on AgentOven's own loop. Capping a hosted framework's spend needs the metered model
  gateway (Pro), which is on the roadmap.
