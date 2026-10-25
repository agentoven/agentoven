# Agentic bots (computer-use agents)

An agent with `runtime: agenticbot` operates a browser or desktop inside a sandbox. In the community edition the runtime exists as a
seam only: such an agent cannot be saved, baked or invoked, because the community `BotControl` refuses. The Pro control plane is the
sole manager and tracker of bots. Design: [ADR-0029](../ADR/0029-agentic-bot-runtime-seam.md).

## What the open-source side guarantees

| Guarantee | Where |
|---|---|
| A bot starts only through the bot manager (`contracts.BotControl.Prepare`) | `internal/process/manager.go` |
| The sandbox profile the manager chose is applied: docker flags, k8s `securityContext`, egress `NetworkPolicy` | `internal/process/bot.go`, `docker.go`, `k8s.go` |
| A bot never gets `CONTROL_PLANE_TOKEN`, `AGENTOVEN_API_KEY` or a provider key; its model calls must use the metered gateway | `applyBotLaunch` |
| An unconfined local process is refused unless `AGENTOVEN_BOT_ALLOW_LOCAL=true` | `Manager.Start` |
| Every invoke is registered (`BotControl.Invoked`) with the caller's identity before it reaches the sandbox, and can be refused | `internal/api/handlers/bot_invoke.go` |
| Bot config is validated when the agent is saved | `checkBotAgent`, `models.BotConfig.Validate` |
| A2A messages and delegations that start a bot run are registered first and can be refused | `proxyA2AGuarded`, `Executor.SetHostedInvoker` |
| A bot gets no tool/skill JSON in its environment; kitchen tools run in the control plane through `contracts.ToolRunner` | `applyBotLaunch`, `internal/executor/tool_run.go` |

## Environment

| Variable | Meaning |
|---|---|
| `AGENTOVEN_BOT_ALLOW_LOCAL` | `true` lets a bot run as an unconfined local process (development only) |
| `AGENTOVEN_BOT_BINARY` | the `ao-bot` executable a local bot runs (default `ao-bot` on `PATH`) |
| `AGENTOVEN_BOT_INVOKE_TIMEOUT` | how long a relayed invoke to a bot may run (default `30m`) |

The manager, gate, ledger, approvals, egress proxy and dashboard are documented in `agentoven-pro/docs/agentic-bots.md`; the sandbox agent in
the `ao-bots` repository.
