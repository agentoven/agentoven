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
| `agentoven agent versions <name> [version]` | The versions an agent was baked at, or one of them. A version moves only on a bake with a changed configuration; the first bake is `0.1.0`; edits do not bump it |
| `agentoven trace replay call <trace> <span> --variant '{...}'` | Re-run one captured call as up to five variants (Pro). Runs no tool |
| `agentoven trace replay run <trace> --from-span <s> [--tools frozen\|blocked\|live]` | Re-run a run from a step. `live` (with `--live-tool`) is a signed decision |
| `agentoven trace replay prepare <trace> --from-span <s> --live-tool <t>` | What a live replay would sign |
| `agentoven trace capture <trace> <span>` | A captured call, whole (audited) |
| `agentoven dataset ls\|create\|get\|update` | Governed datasets (Pro) |
| `agentoven dataset add-case\|add-from-trace\|cases\|retire-case\|erase-case` | Cases, from JSON or from a trace or replay span |
| `agentoven dataset publish\|versions\|version\|export` | Versions and JSONL export. Publishing a regulated dataset is signed |
| `agentoven dataset lower-minimum\|purge` | Retention changes; both signed on a regulated dataset |
| `agentoven dataset hold place\|lift` / `agentoven legal-hold place\|lift [--dataset <id>]` | Legal holds. Lifting is the Legal role's, with a reason, signed |
| `agentoven dataset expiring\|reviews\|tombstones` | Retention reports |
| `agentoven signature ls\|get\|verify` | Signed decisions and the signature chain |
| `agentoven signature sign --gate <g> --record <r>` | Sign a decision and print its single-use token |
| `agentoven signature policy get\|set` | Who may approve what |
| `agentoven kitchen bindings ls\|add\|rm <kitchen>` | Identity-provider group bindings (Pro) |
| `agentoven kitchen mine` | The kitchens you can enter and your role in each |
| `agentoven service-account create --owner <email>` / `service-account owner <id> <email>` | Credentials belong to a person |
| `agentoven key ls\|owner <id> <email>` | Scoped API keys and their owners |
| `agentoven budgets list\|set\|rm\|ledger\|alerts\|usage` | Period budgets for the kitchen, an agent, a provider or an API key, in dollars or tokens (Pro). Raising or removing one is signed (`--sign`) |
| `agentoven limits list\|set\|rm` | Rate limits: requests, tokens and invocations per minute (Pro) |
| `agentoven cache show\|set\|rm\|purge` | The response cache: policy per kitchen or agent, and purge (Pro) |

### Roles

`restaurateur` (platform-wide) and, inside a kitchen, `head_chef`, `chef`, `baker`, `taster`, `auditor`, `legal`,
`finance`, `viewer`. The retired `admin` is refused wherever a role is given: it was a Head Chef inside a kitchen
and a Restaurateur across the installation.

### Signing a decision

Pro signs every human decision. Commands that take one accept `--sign` (or `--signing-token <token>` /
`AGENTOVEN_SIGNING_TOKEN`). `--sign` proves who you are again with the password of your local account, read from
`AGENTOVEN_SIGNING_PASSWORD` or asked for at a prompt. **A password is never a flag**, so it never lands in a shell
history or a process list. An account that signs in through an identity provider (OIDC) re-authenticates in a
browser, which a command line cannot do: the command says so and the decision is signed in the dashboard.

```
agentoven dataset hold lift <id> --reason "case closed" --sign          # gate legal_hold_release
agentoven recipe bake refunds --environment prod --sign                  # gate approval_skip: record refunds@prod
TOKEN=$(agentoven signature sign --gate dataset_purge --record <id> \
          --payload '{"action":"purge","reason":"retention over"}')      # then: --signing-token "$TOKEN"
```

## Configuration

The CLI reads from environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTOVEN_URL` | `http://localhost:8080` | Control plane URL |
| `AGENTOVEN_API_KEY` | — | API key for authentication |
| `AGENTOVEN_KITCHEN` | `default` | Active kitchen (workspace) |
| `AGENTOVEN_TIMEOUT` | `120` | Seconds one request may take (also `timeout_secs` in the config file) |

`~/.agentoven/config.toml` holds your API key and login token, so it is written readable by you only (mode 0600,
its directory 0700), including when an older version left it wider. The key and token never appear in debug output.
`agentoven agent retire` is `DELETE /api/v1/agents/{name}`; registering the same name again brings the agent back.
A name is escaped as one path segment, and a redirect from the control plane is an error, not followed (it would carry
your key to the new address). `crates/agentoven-core/tests/route_contract.rs` checks every call against the server's
route list (`sdk/fixtures/server-routes.json`).

## License

MIT
