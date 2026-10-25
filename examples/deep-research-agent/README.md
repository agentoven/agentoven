# Deep research agent

A [Deep Agents](https://github.com/langchain-ai/deepagents) research agent hosted by AgentOven
as a `deepagents` runtime. The agent plans with a todo list, reads pages, keeps notes in its
virtual filesystem and writes a cited report. AgentOven runs it as a process and puts the control
plane around it: guardrails on what goes in and out, the A2A gateway, sessions, tracing and
audit.

## Run it

```bash
pip install -r requirements.txt          # Python 3.11+ (Deep Agents requires it)

# Register the agent, then bake it. The control plane starts the entrypoint as the agent's
# process, so point it at agent.py by absolute path, with the Python that has deepagents:
#   "entrypoint": "/path/to/venv/bin/python /path/to/examples/deep-research-agent/agent.py"
# (or set "repo_url" to a git repository holding agent.py, and the entrypoint runs inside the clone)
curl -X POST http://localhost:8080/api/v1/agents \
  -H 'Content-Type: application/json' -d @agent.json
curl -X POST http://localhost:8080/api/v1/agents/deep-researcher/bake

# Ask it something
curl -X POST http://localhost:8080/api/v1/agents/deep-researcher/invoke \
  -H 'Content-Type: application/json' \
  -d '{"message": "What changed in the latest Go release? Cite your sources."}'
```

`/invoke/stream` streams the same run as server-sent events: the text of each message, each tool
call and every change to the todo list, so a client can show progress.

A `session_id` in the request continues a conversation. It becomes the LangGraph `thread_id`, so
pass a checkpointer to `build_deep_agent(..., checkpointer=...)` if you want the agent to
remember earlier calls.

## What AgentOven does and does not govern

| | Enforced by AgentOven |
|---|---|
| The message that comes in (`/invoke`, `/invoke/stream`, A2A, sessions) | Input guardrails |
| The answer that goes out, including streamed and A2A replies | Output guardrails (a streamed reply is held back and released only once checked) |
| Who may call the process | The agent's own token, presented by the control plane |
| The agent's tools, its file and `execute` tools, and the subagents it spawns | **Not governed.** They run in this process, with its permissions. AgentOven does not sandbox them: give the process only the access the agent needs. |
| The agent's model spend | **Reported, not capped.** The model is called directly from this process. Per-task spend caps need the metered model gateway (Pro). |
