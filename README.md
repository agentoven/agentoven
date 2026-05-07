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
  <a href="https://docs.agentoven.dev">Documentation</a> •
  <a href="https://docs.agentoven.dev/quickstart">Quickstart</a> •
  <a href="https://discord.gg/WxTn6rtpzT">Discord</a> •
  <a href="https://github.com/agentoven/agentoven/discussions">Discussions</a> •
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="Apache 2.0 License" /></a>
  <a href="https://crates.io/crates/a2a-ao"><img src="https://img.shields.io/crates/v/a2a-ao.svg" alt="a2a-ao on crates.io" /></a>
  <a href="https://crates.io/crates/agentoven-core"><img src="https://img.shields.io/crates/v/agentoven-core.svg" alt="agentoven-core on crates.io" /></a>
  <a href="https://crates.io/crates/agentoven-cli"><img src="https://img.shields.io/crates/v/agentoven-cli.svg" alt="agentoven-cli on crates.io" /></a>
  <a href="https://pypi.org/project/agentoven/"><img src="https://img.shields.io/pypi/v/agentoven.svg" alt="PyPI" /></a>
  <a href="https://www.npmjs.com/package/@agentoven/sdk"><img src="https://img.shields.io/npm/v/@agentoven/sdk.svg" alt="npm" /></a>
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
| 💬 **Sessions** | Multi-turn chat sessions with history, thinking mode, and streaming |
| 🧠 **Pantry (Memory)** | Three-layer agent memory: Facts (long-term), Episodes (conversation history), Shelves (knowledge bases) |
| 🛡️ **Guardrails** | Pre/post processing content filters and safety checks; workspace-level defaults with per-agent exceptions |
| 🧪 **Evaluation** | Automated evals with LLM judges and regression detection |
| 💰 **Cost Tracking** | Per-request token counting, tenant-level chargeback |
| 🔐 **Governance** | Pluggable auth (API keys, service accounts, SSO), RBAC, audit logs |
| 🔎 **RAG Pipelines** | 5 retrieval strategies with vector stores and embedding management |

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

# Or download the binary (Linux, macOS)
curl -fsSL https://raw.githubusercontent.com/agentoven/agentoven/main/install.sh | sh
```

### Install the Python SDK

```bash
pip install agentoven
```

### Install the TypeScript SDK

```bash
npm install @agentoven/sdk
```

### Bake your first agent

```bash
# Initialize a project
agentoven init --name my-agent --framework openai-sdk

# Set up a model provider (OpenAI, Anthropic, Gemini, OpenRouter, Ollama, or LiteLLM)
agentoven provider add my-openai --kind openai --api-key $OPENAI_API_KEY

# Or use OpenRouter for access to 200+ models with one API key
agentoven provider add my-router --kind openrouter --api-key $OPENROUTER_API_KEY

# Register an agent
agentoven agent register summarizer \
  --description "Summarizes documents with citations" \
  --model-provider my-openai \
  --model-name gpt-4o \
  --system-prompt "You are a document summarizer."

# Bake (deploy) the agent
agentoven agent bake summarizer

# Test it interactively
agentoven agent test summarizer --interactive
```

### Or use the Python SDK

**Simple — single model, no extras:**

```python
from agentoven import Agent, AgentOvenClient

agent = Agent("summarizer",
    description="Summarizes documents with citations",
    model_provider="my-openai",
    model_name="gpt-4o",
    system_prompt="You are a document summarizer.",
)

client = AgentOvenClient()
client.register(agent)
client.bake(agent, environment="production")
```

**Advanced — multi-model fallback, tools, and MCP:**

```python
from agentoven import Agent, Ingredient, AgentOvenClient

agent = Agent("summarizer",
    description="Summarizes documents with citations",
    ingredients=[
        Ingredient.model("gpt-4o", provider="my-openai"),
        Ingredient.model("claude-sonnet", provider="anthropic", role="fallback"),
        Ingredient.tool("document-reader", protocol="mcp"),
        Ingredient.prompt("system", text="You are a document summarizer."),
    ],
)

client = AgentOvenClient()
client.register(agent)
client.bake(agent, environment="production")

# The agent is now discoverable via A2A
# Other agents can find it at:
#   /.well-known/agent-card.json
```

### Multi-Agent Recipes

```python
from agentoven import Recipe, Step, AgentOvenClient

# A Recipe is a multi-agent workflow
recipe = Recipe("document-review",
    steps=[
        Step("planner", agent="task-planner", timeout="30s"),
        Step("researcher", agent="doc-researcher", parallel=True),
        Step("summarizer", agent="summarizer"),
        Step("reviewer", agent="quality-reviewer"),
        Step("approval", human_gate=True, notify=["team-leads"]),
    ],
)

# Bake the recipe via the client
client = AgentOvenClient()
client.bake(recipe, input='{"document_url": "https://..."}')
```

---

## Pantry — Agent Memory 🧠

AgentOven ships a three-layer memory system called the **Pantry**. Every agent gets persistent
memory without any extra setup:

| Layer | Kitchen Name | Technical Name | What it stores |
|---|---|---|---|
| 1 | **Staples** | Facts | Long-term key/value facts per user or team. Persists across sessions. |
| 2 | **Leftovers** | Episodes | Auto-summarised conversation history. Written when a session closes. |
| 3 | **Shelves** | Knowledge Bases | Indexed document collections with vector search. |

At session start, the Pantry assembles a **Mise en place** (memory context) — a pre-fetched
bundle of relevant facts, episodes, and knowledge-base chunks — injected into the agent's
context window before the first turn.

```bash
# Store a fact about a user
agentoven pantry staples set --agent my-agent --key "preferred_language" --value "Python"

# Query a knowledge base
agentoven pantry shelves search "how does authentication work?" --kb internal-docs

# Review episodic memory
agentoven pantry leftovers list --agent my-agent --user alice --limit 5
```

> Both kitchen vocab and plain technical terms work in the CLI and API:
> `pantry/staples` ↔ `memory/facts`, `pantry/leftovers` ↔ `memory/episodes`, `pantry/shelves` ↔ `memory/knowledge-bases`

---

## Model Providers

| Provider | Kind | Notes |
|---|---|---|
| OpenAI | `openai` | GPT-4.1, o3/o4-mini, GPT-4o |
| Azure OpenAI | `azure-openai` | Bring your own deployments |
| Anthropic | `anthropic` | Claude Opus 4, Sonnet 4, Haiku |
| Google Gemini | `gemini` | Gemini 2.5 Pro/Flash via AI Studio |
| **OpenRouter** | `openrouter` | **200+ models via one API key** — GPT, Claude, Llama, Mistral, Gemini, DeepSeek |
| Ollama | `ollama` | Local open-source models |
| LiteLLM Proxy | `litellm` | Bring your existing LiteLLM proxy |

```bash
# OpenRouter — single key, any model
agentoven provider add my-router --kind openrouter --api-key sk-or-...
agentoven agent register researcher \
  --model-provider my-router \
  --model-name "anthropic/claude-opus-4"
```

---

## CLI Reference

The `agentoven` CLI provides **55+ commands** across **13 command groups** for complete control of your agent infrastructure.

### Global Flags

```
--url <url>       Control plane URL (env: AGENTOVEN_URL)
--api-key <key>   API key (env: AGENTOVEN_API_KEY)
-k, --kitchen     Kitchen/workspace scope (env: AGENTOVEN_KITCHEN)
--output <fmt>    Output format: text, json, table
--help            Show help for any command
```

### Commands Overview

| Command Group | Subcommands | Description |
|---|---|---|
| `agentoven init` | — | Initialize a new project with `agentoven.toml` |
| `agentoven agent` | `register`, `list`, `get`, `update`, `delete`, `bake`, `recook`, `cool`, `rewarm`, `retire`, `test`, `invoke`, `config`, `card`, `versions` | Full agent lifecycle management |
| `agentoven provider` | `list`, `add`, `get`, `update`, `remove`, `test`, `discover` | Model provider management (OpenAI, Anthropic, Gemini, OpenRouter, Ollama, LiteLLM) |
| `agentoven tool` | `list`, `add`, `get`, `update`, `remove` | MCP tool management |
| `agentoven prompt` | `list`, `add`, `get`, `update`, `remove`, `validate`, `versions` | Versioned prompt template management |
| `agentoven recipe` | `create`, `list`, `get`, `delete`, `bake`, `runs`, `approve` | Multi-agent workflow orchestration |
| `agentoven session` | `list`, `create`, `get`, `delete`, `send`, `chat` | Multi-turn chat session management |
| `agentoven kitchen` | `list`, `get`, `settings`, `update-settings` | Workspace/tenant management |
| `agentoven trace` | `ls`, `get`, `cost`, `audit` | Observability, cost tracking, audit logs |
| `agentoven rag` | `query`, `ingest` | RAG pipeline operations |
| `agentoven dashboard` | — | Start the control plane + open the dashboard UI |
| `agentoven login` | — | Authenticate with the control plane |
| `agentoven status` | — | Show control plane health and agent count |

### Agent Lifecycle

```
  register → bake → ready
                ↓       ↑
              cool → rewarm
                ↓
             retire
```

| Command | Description |
|---|---|
| `agentoven agent register <name>` | Register a new agent (accepts `--config`, `--framework`, `--model-provider`, `--guardrail`, etc.) |
| `agentoven agent bake <name>` | Deploy an agent — resolves ingredients, validates config, sets status to ready |
| `agentoven agent recook <name>` | Hot-swap agent configuration without full redeployment |
| `agentoven agent cool <name>` | Pause a running agent (preserves state) |
| `agentoven agent rewarm <name>` | Bring a cooled agent back to ready |
| `agentoven agent retire <name>` | Permanently decommission an agent |
| `agentoven agent invoke <name>` | Run a managed agent with full agentic loop and execution trace |
| `agentoven agent test <name>` | One-shot or interactive playground for testing agents |
| `agentoven agent card <name>` | Show the A2A Agent Card (discovery metadata) |
| `agentoven agent versions <name>` | Show version history |

### Multi-turn Sessions

```bash
# Create a session
agentoven session create my-agent

# Interactive chat with thinking mode
agentoven session chat my-agent <session-id> --thinking

# Send a single message
agentoven session send my-agent <session-id> --message "Summarize this doc"
```

### RAG Operations

```bash
# Ingest documents into a collection
agentoven rag ingest ./docs/ --collection knowledge-base --chunk-size 1000

# Query with different strategies
agentoven rag query "What is AgentOven?" --strategy naive --sources
agentoven rag query "How does routing work?" --strategy hyde --top-k 10
```

---

## Kitchen Vocabulary 🏺

AgentOven uses a **clay oven** metaphor throughout:

| Term | Meaning |
|---|---|
| **Oven** | The AgentOven control plane |
| **Recipe** | A multi-agent workflow (DAG) |
| **Ingredient** | A model, tool, prompt, or data source |
| **Bake** | Deploy an agent or run a workflow |
| **Cool** | Pause a running agent |
| **Rewarm** | Bring a cooled agent back to ready |
| **Retire** | Permanently decommission an agent |
| **Re-cook** | Hot-swap agent configuration |
| **Kitchen** | A workspace/project (tenant boundary) |
| **Baker** | A user/team building agents |
| **Menu** | The agent catalog/registry |
| **Pantry** | The agent memory system (also: memory) |
| **Staples** | Long-term persistent facts per agent (also: facts) |
| **Leftovers** | Episodic session summaries — what was discussed before (also: episodes) |
| **Shelves** | Knowledge bases with document ingestion and vector search (also: knowledge-bases) |
| **Mise en place** | Pre-fetched memory context assembled before an agent run (also: memory context) |

## Project Structure

```
agentoven/
├── crates/                    # Rust workspace
│   ├── a2a-ao/               # A2A protocol SDK (standalone crate)
│   ├── agentoven-core/       # SDK core library
│   └── agentoven-cli/        # CLI tool (55+ commands)
├── control-plane/            # Go control plane service
│   ├── cmd/server/           # Entry point
│   ├── pkg/                  # Public interfaces (contracts, models)
│   └── internal/             # Router, MCP gateway, workflow engine, RAG, auth
├── sdk/
│   ├── python/               # Python SDK (PyO3 bindings)
│   └── typescript/           # TypeScript SDK (napi-rs bindings)
├── infra/                    # Docker, Helm, Terraform
└── site/                     # Static landing page
```

## Examples

Ready-to-run examples are in the [`examples/`](https://github.com/agentoven/agentoven/tree/main/examples) directory at the root of this repository:

| Example | Description |
|---|---|
| [`weather-agent`](https://github.com/agentoven/agentoven/tree/main/examples/weather-agent) | Tool calling, multi-turn reasoning, and OTel span waterfall via a local MCP weather server |

Each example contains a `README.md` with full setup instructions.

---

## Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### Key areas to contribute:

- 🦀 **a2a-ao** — The A2A Rust SDK by AgentOven (help us shape the ecosystem)
- 🔌 **Model providers** — Add new provider integrations
- 🧪 **Evaluators** — Build custom evaluation judges
- 📚 **Docs & examples** — Help others bake better agents

## Open-Core Model

| | OSS (Apache 2.0) | Enterprise |
|---|---|---|
| Agent Registry | ✅ Single-tenant | Multi-tenant, org hierarchy |
| A2A + MCP | ✅ Full protocol | + cross-org federation |
| CLI + SDKs | ✅ Full (55+ commands) | ✅ Full |
| Model Router | ✅ Routing + fallback | + cost optimizer, budgets |
| Sessions | ✅ Multi-turn chat | ✅ Multi-turn chat |
| **Pantry (Memory)** | ✅ Facts, Episodes, Knowledge Bases | + team-scoped memory, memory policies |
| RAG Pipelines | ✅ 5 strategies | + quality monitor |
| Workflow Patterns | ✅ All 6 patterns | + custom step plugins |
| Observability | ✅ 7-day retention | 400-day, advanced analytics |
| Guardrails | ✅ Built-in + workspace defaults | + LLM-judge, PII detection, compliance validators |
| Auth | API keys, service tokens | SSO/SAML, OIDC, RBAC, audit logs |
| Compliance | SOC2, GDPR | + FedRAMP, HIPAA, GxP |
| Deployment | Self-hosted | + managed cloud, BYOC, SLA |

## License

[Apache License 2.0](LICENSE) — free to use, modify, and distribute.

---

<p align="center">
  <strong>🏺 Baked with care by the AgentOven community.</strong>
</p>
