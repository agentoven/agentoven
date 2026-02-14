<p align="center">
  <img src="docs/static/img/logo.svg" alt="AgentOven" width="200" />
</p>

<h1 align="center">AgentOven</h1>

<p align="center">
  <strong>Bake production-ready AI agents.</strong>
</p>

<p align="center">
  The open-source enterprise agent control plane with native A2A + MCP support.
</p>

<p align="center">
  <a href="https://agentoven.dev/docs">Documentation</a> •
  <a href="https://agentoven.dev/docs/quickstart">Quickstart</a> •
  <a href="https://github.com/agentoven/agentoven/discussions">Community</a> •
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT License" /></a>
  <a href="https://crates.io/crates/agentoven"><img src="https://img.shields.io/crates/v/agentoven.svg" alt="crates.io" /></a>
  <a href="https://pypi.org/project/agentoven/"><img src="https://img.shields.io/pypi/v/agentoven.svg" alt="PyPI" /></a>
  <a href="https://www.npmjs.com/package/agentoven"><img src="https://img.shields.io/npm/v/agentoven.svg" alt="npm" /></a>
</p>

---

## What is AgentOven?

AgentOven is a **framework-agnostic agent control plane** that standardizes how AI agents are built, deployed, observed, and orchestrated across an enterprise.

Think of it as a **clay oven** 🏺 — you put in raw ingredients (models, tools, data, prompts) and **production-ready agents come out the chimney**.

### The Problem

- Agents are built ad-hoc with no consistency
- No governance, audit trail, or cost visibility
- Locked into single vendors (Databricks, Azure, LangChain)
- Multi-agent workflows are stitched together manually
- No standard protocol for agent-to-agent collaboration

### The Solution

AgentOven provides a unified control plane with:

| Capability | Description |
|---|---|
| 🏺 **Agent Registry** | Version, discover, and manage agents as first-class resources |
| 🔀 **Model Router** | Intelligent routing across providers with fallback, cost optimization |
| 🤝 **A2A Native** | [Agent-to-Agent protocol](https://github.com/google/A2A) built-in from day 1 |
| 🔧 **MCP Gateway** | [Model Context Protocol](https://modelcontextprotocol.io/) for tool/data integration |
| 📊 **Observability** | OpenTelemetry tracing on every invocation, cost & latency dashboards |
| 🔄 **Workflow Engine** | DAG-based multi-agent orchestration via A2A task lifecycle |
| 📝 **Prompt Studio** | Versioned prompt management with diff view and A/B variants |
| 🧪 **Evaluation** | Automated evals with LLM judges and regression detection |
| 💰 **Cost Tracking** | Per-request token counting, tenant-level chargeback |
| 🔐 **Governance** | RBAC, audit logs, SOC2/GDPR compliant |

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                   AgentOven Control Plane                     │
│         (Registry · Router · RBAC · Cost · Tenancy)          │ ← Go
├──────────────────────┬──────────────────────────────────────┤
│    A2A Gateway       │         MCP Gateway                   │
│  (Agent ↔ Agent)     │      (Agent ↔ Tools/Data)            │ ← Go
├──────────────────────┴──────────────────────────────────────┤
│                   AgentOven Runtime                           │
│     (Execute · Instrument · Route · Enforce Policies)        │ ← Rust
├─────────┬─────────┬──────────┬──────────┬───────────────────┤
│LangGraph│ CrewAI  │OpenAI SDK│ AutoGen  │ Custom Agents     │
└─────────┴─────────┴──────────┴──────────┴───────────────────┘
```

## Quick Start

### Install the CLI

```bash
# macOS
brew install agentoven/tap/agentoven

# Cargo
cargo install agentoven-cli

# Or download the binary
curl -fsSL https://agentoven.dev/install.sh | sh
```

### Install the Python SDK

```bash
pip install agentoven
```

### Install the TypeScript SDK

```bash
npm install agentoven
```

### Bake your first agent

```python
from agentoven import Agent, Ingredient, Recipe

# Define your agent as a recipe
agent = Agent(
    name="summarizer",
    version="1.0.0",
    description="Summarizes documents with citations",
    ingredients=[
        Ingredient.model("gpt-4o", provider="azure-openai"),
        Ingredient.model("claude-sonnet", provider="anthropic", role="fallback"),
        Ingredient.tool("document-reader", protocol="mcp"),
    ],
)

# Register with the oven
agent.register()

# Bake (deploy) the agent
agent.bake(environment="production")

# The agent is now discoverable via A2A
# Other agents can find it at:
#   /.well-known/agent-card.json
```

### Create a multi-agent workflow (recipe)

```python
from agentoven import Recipe, Step

# A Recipe is a multi-agent workflow
recipe = Recipe(
    name="document-review",
    steps=[
        Step("planner", agent="task-planner", timeout="30s"),
        Step("researcher", agent="doc-researcher", parallel=True),
        Step("summarizer", agent="summarizer"),
        Step("reviewer", agent="quality-reviewer"),
        Step("approval", human_gate=True, notify=["team-leads"]),
    ],
)

# Each step communicates via A2A protocol
result = recipe.bake(input={"document_url": "https://..."})
```

## Kitchen Vocabulary 🏺

AgentOven uses a **clay oven** metaphor throughout:

| Term | Meaning |
|---|---|
| **Oven** | The AgentOven control plane |
| **Recipe** | A multi-agent workflow (DAG) |
| **Ingredient** | A model, tool, prompt, or data source |
| **Bake** | Deploy an agent or run a workflow |
| **Kitchen** | A workspace/project |
| **Baker** | A user/team building agents |
| **Menu** | The agent catalog/registry |

## Project Structure

```
agentoven/
├── crates/                    # Rust workspace
│   ├── a2a-ao/               # A2A protocol SDK (standalone crate)
│   ├── agentoven-core/       # SDK core library
│   └── agentoven-cli/        # CLI tool
├── control-plane/            # Go control plane service
├── sdk/
│   ├── python/               # Python SDK (PyO3 bindings)
│   └── typescript/           # TypeScript SDK (napi-rs bindings)
├── ui/                       # React dashboard
├── docs/                     # Documentation (Docusaurus)
└── infra/                    # Docker, Helm, Terraform
```

## Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### Key areas to contribute:

- 🦀 **a2a-ao** — The A2A Rust SDK by AgentOven (help us shape the ecosystem)
- 🔌 **Model providers** — Add new provider integrations
- 🧪 **Evaluators** — Build custom evaluation judges
- 📚 **Docs & examples** — Help others bake better agents

## Open-Core Model

| | OSS (MIT) | Enterprise |
|---|---|---|
| Agent Registry | ✅ Single-tenant | Multi-tenant, org hierarchy |
| A2A + MCP | ✅ Full protocol | + cross-org federation |
| CLI + SDKs | ✅ Full | ✅ Full |
| Model Router | ✅ Routing + fallback | + cost optimizer, budgets |
| Observability | ✅ 7-day retention | 400-day, advanced analytics |
| Auth | API keys, OAuth | SSO/SAML, RBAC, audit logs |
| Compliance | SOC2, GDPR | + FedRAMP, HIPAA, GxP |
| Deployment | Self-hosted | + managed cloud, BYOC, SLA |

## License

[MIT](LICENSE) — free to use, modify, and distribute.

---

<p align="center">
  <strong>🏺 Baked with care by the AgentOven community.</strong>
</p>
