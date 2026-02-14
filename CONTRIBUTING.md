# Contributing to AgentOven 🏺

Thank you for your interest in contributing to AgentOven! We welcome contributions
from everyone, whether it's a bug fix, new feature, documentation improvement, or
a new model provider integration.

## Getting Started

### Prerequisites

- **Rust** 1.75+ (install via [rustup](https://rustup.rs/))
- **Go** 1.22+ (install via [golang.org](https://go.dev/dl/))
- **Node.js** 20+ (install via [nvm](https://github.com/nvm-sh/nvm))
- **Python** 3.11+ (install via [pyenv](https://github.com/pyenv/pyenv))
- **Docker** & **Docker Compose** (for local infrastructure)
- **maturin** (for Python SDK: `pip install maturin`)

### Clone & Build

```bash
git clone https://github.com/agentoven/agentoven.git
cd agentoven

# Build everything
make build

# Run tests
make test

# Install the CLI locally
make install-cli

# Start the control plane + dependencies
make docker-up
make dev-control-plane
```

## Project Structure

| Directory | Language | Description |
|---|---|---|
| `crates/a2a-ao` | Rust | A2A protocol SDK (standalone crate) |
| `crates/agentoven-core` | Rust | SDK core library |
| `crates/agentoven-cli` | Rust | CLI tool |
| `control-plane/` | Go | Control plane API server |
| `sdk/python/` | Rust + Python | Python SDK (PyO3 bindings) |
| `sdk/typescript/` | Rust + TypeScript | TypeScript SDK (napi-rs bindings) |
| `ui/` | TypeScript/React | Dashboard UI |
| `docs/` | MDX | Documentation site |
| `infra/` | YAML/HCL | Docker, Helm, Terraform |

## Key Contribution Areas

### 🦀 a2a-ao — A2A Protocol Rust SDK
The first Rust implementation of the A2A protocol. We aim to contribute this
upstream to the official A2A project (Linux Foundation).

### 🔌 Model Provider Integrations
Add new providers to the model router:
- `control-plane/internal/router/providers/`
- Implement the `Provider` interface

### 🧪 Evaluation Judges
Build custom evaluation judges for domain-specific quality metrics.

### 📚 Documentation & Examples
Help others bake better agents with tutorials, guides, and example recipes.

## Development Workflow

1. **Fork** the repository
2. **Create a branch** for your feature: `git checkout -b feat/my-feature`
3. **Make your changes** with tests
4. **Run tests**: `make test`
5. **Run lints**: `make lint`
6. **Submit a PR** with a clear description

## Commit Convention

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(a2a-ao): add streaming support for task updates
fix(control-plane): handle concurrent agent registration
docs(sdk): add Python quickstart guide
```

## Code of Conduct

We follow the [Contributor Covenant Code of Conduct](https://www.contributor-covenant.org/version/2/1/code_of_conduct/).
Be kind, be respectful, be collaborative.

## License

By contributing, you agree that your contributions will be licensed under the
[MIT License](LICENSE).

---

🏺 **Happy baking!**
