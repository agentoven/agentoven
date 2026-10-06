# agentoven-cli

CLI for [AgentOven](https://agentoven.dev) — bake production-ready AI agents from the terminal.

## Install

```bash
cargo install agentoven-cli
```

## Usage

```bash
# Connect to a control plane
agentoven login

# Initialize a new agent project
agentoven init my-agent

# Register your agent
agentoven agent register

# Deploy it
agentoven agent bake my-agent

# Interactive test via A2A
agentoven agent test my-agent

# Check system status
agentoven status
```

## Commands

| Command | Description |
|---------|-------------|
| `agentoven init` | Scaffold a new agent project |
| `agentoven login` | Authenticate with the control plane |
| `agentoven status` | Check control plane connectivity |
| `agentoven agent register` | Register an agent from `agentoven.toml` |
| `agentoven agent list` | List all agents in the kitchen |
| `agentoven agent get <name>` | Show agent details |
| `agentoven agent bake <name>` | Deploy an agent |
| `agentoven agent cool <name>` | Pause a running agent |
| `agentoven agent retire <name>` | Permanently retire an agent |
| `agentoven agent test <name>` | Interactive A2A REPL |
| `agentoven skills list` | List skills registered in the kitchen |
| `agentoven skills get <name>` | Show a skill with its verification verdict and reasoning |
| `agentoven skills install <git-url\|dir>` | Register a skill; a provider reviews it. Exits 2 on `needs_review`, 3 on `rejected` |
| `agentoven skills approve\|reject <name>` | Decide a skill left at `needs_review` |
| `agentoven skills stage <git-url\|dir>` | Stage a skill without reviewing it (Pro) |
| `agentoven skills analyze <name> [--provider <p>]` | Send a staged skill to a model provider for review (Pro) |
| `agentoven recipe create` | Create a multi-agent workflow |
| `agentoven recipe bake <name>` | Execute a recipe |
| `agentoven trace get <id>` | Inspect an execution trace |
| `agentoven trace cost <id>` | View cost breakdown |

## Configuration

The CLI reads from environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTOVEN_URL` | `http://localhost:8080` | Control plane URL |
| `AGENTOVEN_API_KEY` | — | API key for authentication |
| `AGENTOVEN_KITCHEN` | `default` | Active kitchen (workspace) |

## License

MIT
