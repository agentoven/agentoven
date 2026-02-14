# agentoven

Python SDK for [AgentOven](https://agentoven.dev) — the enterprise agent control plane.

Built with native Rust performance via [PyO3](https://pyo3.rs).

## Install

```bash
pip install agentoven
```

## Quick Start

```python
from agentoven import AgentOvenClient, Agent

# Connect to the control plane
client = AgentOvenClient(url="http://localhost:8080")

# Register an agent
agent = Agent(name="my-assistant", description="A helpful AI assistant")
result = client.register_agent(agent)
print(result)

# List all agents
agents = client.list_agents()
for a in agents:
    print(a)

# Deploy an agent
client.bake("my-assistant")

# Pause an agent
client.cool("my-assistant")
```

## Features

- **Native performance** — Rust core compiled to a Python extension
- **A2A protocol** — built on the Agent-to-Agent open standard
- **Multi-framework** — works with LangGraph, CrewAI, AutoGen, and more
- **Kitchen isolation** — workspace-based multi-tenancy

## License

MIT
