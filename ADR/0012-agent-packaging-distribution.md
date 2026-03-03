# ADR-0012: Agent Packaging and Distribution

- **Date:** 2026-03-03
- **Status:** Accepted
- **Deciders:** siddartha

## Context

AgentOven agents are currently defined by their registration payload (JSON body sent
to `POST /api/v1/agents`) and stored in the control plane's database. There is no
portable format for sharing, versioning, or distributing agents outside of a specific
AgentOven instance.

### Problems

1. **No portability** — moving an agent from dev to staging requires re-registering
   with the same JSON payload. There's no "export → import" flow.
2. **No versioned artifacts** — agent configs evolve but there's no immutable
   artifact that represents a specific version of an agent.
3. **No sharing** — teams can't share agent recipes, templates, or proven
   configurations through a registry or marketplace.
4. **No offline inspection** — there's no way to review an agent's full config
   (ingredients, prompts, tools, metadata) without querying the API.
5. **No CI/CD integration** — pipelines can't build, test, and publish agent
   artifacts like they do with Docker images or Helm charts.

### Design Goals

- A self-contained file format that includes everything needed to register an agent
- CLI commands for build → test → publish → install workflow
- A registry for sharing agents (future — marketplace)
- Works with both OSS and Pro (Pro adds signed packages, private registries)

## Decision

### 1. Agent Package Format (.aopack)

An `.aopack` file is a gzipped tar archive with a defined structure:

```
my-agent-1.0.0.aopack
├── agent.yaml          # Agent definition (required)
├── README.md           # Human-readable docs (optional)
├── prompts/            # Prompt templates (optional)
│   ├── system.txt
│   └── user.txt
├── tests/              # Test cases (optional)
│   └── regression.json
├── tools/              # MCP tool schemas (optional)
│   └── weather.json
└── metadata/           # Additional metadata (optional)
    └── CHANGELOG.md
```

### 2. agent.yaml Schema

```yaml
apiVersion: agentoven.dev/v1
kind: AgentPackage
metadata:
  name: weather-bot
  version: 1.0.0
  description: "A weather information agent using OpenWeatherMap"
  author: "team-alpha"
  license: "MIT"
  tags: ["weather", "utility", "external-api"]
  kitchen: ""  # empty = any kitchen

spec:
  # Agent registration fields (maps to POST /api/v1/agents body)
  mode: a2a
  framework: custom
  description: "Answers weather queries using OpenWeatherMap API"

  # Ingredients
  ingredients:
    - kind: model
      config:
        provider: openai
        model: gpt-4o-mini
    - kind: tool
      config:
        name: weather-lookup
        type: mcp
    - kind: prompt
      config:
        name: weather-system-prompt
        template_file: prompts/system.txt

  # A2A capabilities
  capabilities:
    streaming: true
    push_notifications: false
    state_transition_history: true

  # Test reference
  tests:
    - file: tests/regression.json
      run_on_install: false  # Pro: run_on_install: true triggers test after deploy

  # Dependencies (other agents this agent calls)
  dependencies: []

  # Environment variables required (names only, not values)
  required_env:
    - OPENWEATHER_API_KEY
```

### 3. CLI Commands

```bash
# Initialize a new agent package in current directory
agentoven pack init

# Build the package (validates + creates .aopack file)
agentoven pack build
# → my-agent-1.0.0.aopack

# Inspect a package without installing
agentoven pack inspect my-agent-1.0.0.aopack

# Install (register agent from package)
agentoven pack install my-agent-1.0.0.aopack
# → Registers agent with control plane, creates prompts, registers tools

# Publish to registry (future)
agentoven pack publish my-agent-1.0.0.aopack

# Search registry (future)
agentoven pack search weather

# Export an existing agent as a package
agentoven pack export weather-bot
# → weather-bot-1.0.0.aopack
```

### 4. Build Process

`agentoven pack build`:

1. Read `agent.yaml` from current directory
2. Validate schema (required fields, version format, ingredient kinds)
3. Resolve local file references (prompts/*.txt → inline or bundle)
4. Run test cases if `--test` flag is set
5. Create gzipped tar archive with deterministic ordering
6. Compute SHA-256 hash of archive
7. Write `{name}-{version}.aopack` and `{name}-{version}.aopack.sha256`

### 5. Install Process

`agentoven pack install`:

1. Verify SHA-256 hash (if `.sha256` file present)
2. Extract archive to temp directory
3. Read `agent.yaml`
4. Create prompts (if `prompts/` directory exists)
5. Register MCP tools (if `tools/` directory exists)
6. Register agent with the control plane
7. Optionally run tests (if `tests/` exist and `--test` flag)
8. Report success with agent name and status

### 6. Pro Extensions

| Feature | OSS | Pro |
|---------|-----|-----|
| Build packages | ✅ | ✅ |
| Install from file | ✅ | ✅ |
| Export agents | ✅ | ✅ |
| Sign packages (cosign) | ❌ | ✅ |
| Private registry | ❌ | ✅ |
| Run-on-install tests | ❌ | ✅ |
| Dependency resolution | ❌ | ✅ |
| Version constraints | ❌ | ✅ |

## Consequences

### Positive

- Agents become portable, shareable, versioned artifacts
- CI/CD pipelines can build → test → publish agents like containers
- `agent.yaml` is human-readable and version-controllable
- Export/import enables easy migration between environments
- Foundation for future agent marketplace

### Negative

- Another file format to maintain and document
- Package validation adds complexity to the CLI
- Registry infrastructure needed for publish/search (future work)
- Version conflicts possible when installing multiple packages

## Alternatives Considered

### A. Docker images for agents

Rejected — agents are configurations, not runtime binaries. Docker adds
unnecessary overhead for what is essentially a YAML + prompts bundle.
Agents run inside the AgentOven control plane, not as standalone containers.

### B. Helm-chart-like format

Rejected — too heavyweight. Helm charts solve Kubernetes deployment complexity.
Agent packages are simpler (config + prompts + tests). However, the `agent.yaml`
schema is inspired by Helm's `Chart.yaml`.

### C. Git-based distribution only

Rejected — works for open-source but not for private/commercial agents.
A file-based format supports both git repos and registries.

### D. JSON instead of YAML

Rejected — YAML is more readable for configuration with comments. The API
uses JSON for wire format; the package format uses YAML for human authoring.
