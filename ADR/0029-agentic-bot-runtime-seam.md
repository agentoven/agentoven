# ADR-0029: The `agenticbot` runtime seam (OSS hooks for Pro-managed computer-use agents)

**Status:** Accepted  
**Date:** 2026-10-12  
**Authors:** AgentOven Engineering  
**Scope:** OSS (a small, generic seam). The manager, gate, ledger, proxy and UI are Pro ([agentoven-pro ADR-0037](https://github.com/agentoven/agentoven-pro)); the sandbox agent is the separate repo `ao-bots`.

---

## Context

Customers want agents that operate a browser or desktop (computer-use agents, "CUA"). Such an agent renders hostile web pages and acts on
them, so it needs three things no other hosted runtime needs: it must run confined, every action it takes must be decided by something
outside its own process, and it must not hold a general control-plane credential or a provider key. AgentOven already has the pieces
(hosted-process contract, platform-boundary guardrails, journals, audit, agent identity, metered model gateway) but no sandbox, and
OSS ADR-0018 deferred sandboxing of untrusted code.

## Decision

1. **A runtime `agenticbot`** (`models.RuntimeAgenticBot`) beside `langchain`, `langgraph`, `deepagents`. A bot is framework-native: its own
   process (`ao-bot`, from `ao-bots`) owns the loop, so the existing invoke/stream/session paths and their guardrails apply unchanged.
2. **`models.Agent.Bot *BotConfig`**: tier, start URL, domain allow/block lists, limits, approval mode, credential references, sandbox sizing.
   Validated when the agent is saved (`checkBotAgent`).
3. **`contracts.BotControl`** (`Prepare`, `Invoked`, `Stopped`) is the only way a bot is started, invoked or stopped. The community
   implementation refuses; Pro's is the sole manager and tracker. Without one installed, `process.Manager.Start` refuses a bot.
4. **Confinement is applied by the executors, decided by the manager.** `models.SandboxProfile` (read-only rootfs, caps dropped, non-root,
   memory/cpu/pids/shm limits, runtime class, extra hosts, allowed egress) is mapped to `docker run` flags and to a Kubernetes pod
   `securityContext` plus an egress `NetworkPolicy`. A Kubernetes bot whose NetworkPolicy cannot be applied is not started. The applied
   profile is recorded on `ProcessInfo.Sandbox`.
5. **A bot is narrower than other hosted agents.** The process manager drops `CONTROL_PLANE_TOKEN`, `AGENTOVEN_API_KEY` and
   `AGENTOVEN_PRO_TOKEN` from its environment and refuses to start it unless its model calls go through the metered model gateway
   (no provider key in the sandbox). An unconfined local bot is refused unless `AGENTOVEN_BOT_ALLOW_LOCAL=true` (development only).
6. **Every run is registered before it starts.** `proxyToProcess` and its stream twin assign the run ID (the trace ID) and call
   `BotControl.Invoked` with the caller's identity and task; the manager can refuse (kill switch, concurrency cap). Relayed invokes to
   a bot get `AGENTOVEN_BOT_INVOKE_TIMEOUT` (default 30m) instead of 30s.
7. **A2A and delegation register runs too.** A2A messages that start work (`tasks/send`, `message/stream`) are registered with
   `BotControl.Invoked` under the run ID the process is sent (`-32007` if the manager refuses); `tasks/get` and `tasks/cancel` pass through.
   `agentoven_delegate` to a bot invokes its process through `Executor.SetHostedInvoker` (the same `proxyToProcess` path) instead of
   running the bot's configuration in the Go loop; without an invoker the delegation fails. So scheduled recipes, workflow steps and
   other agents reach a bot only through the control plane.
8. **Kitchen tools run in the control plane.** `contracts.ToolRunner` (`ToolsFor`, `RunToolCall`, implemented by `*executor.Executor`, reached
   through `Handlers.Tools()`) runs one tool call with the executor's schema check, guardrails on arguments and result, timeout, MCP
   gateway and stored credentials. It enforces the agent's own tool list, and leaves approval to the caller (the bot gate). A bot's
   environment no longer carries `AGENT_TOOLS_JSON`, `AGENT_DATA_SOURCES_JSON` or `AGENT_SKILLS`: the gate tells it what it may call.
9. **`BotConfig` carries the whole-computer settings** (`Shell`, `Files`, `Persistence`, `Takeover`, `Tools`); the kitchen's bot policy in Pro is
   the ceiling. The OSS side only stores and validates them.

## Consequences

- OSS gains no sandbox runtime of its own and no bot UI beyond a disabled runtime option; the edition boundary is one interface.
- The control plane, not the bot, is the policy decision point; OSS only guarantees the bot cannot start or be invoked around it.
- Pending: a Kubernetes path through the Pro operator CRD (separate from this executor), CLI/Rust SDK exposure of `runtime`/`bot`, inbound event triggers, and
  the same hosted-process delegation for LangChain/LangGraph/Deep Agents targets (today `agentoven_delegate` runs any managed target's *configuration* in the Go loop; only bots are routed to their process).

## Alternatives considered

- Run the computer-use loop in the Go executor and drive the sandbox as a tool provider: gives journal v2 and `ApprovalTools` for free, but
  the user chose a sandboxed agent process managed by Pro, and a hosted loop keeps the model-facing logic next to the screen it reads.
  The cost is that journaling and gating are re-implemented on the Pro side of the gate rather than inherited.
- A generic "sandboxed runtime" instead of a bot-specific one: more general, but nothing else needs a per-action gate yet.
