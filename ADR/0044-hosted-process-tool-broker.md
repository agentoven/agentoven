# ADR-0044: Tool calls of a hosted agent process go through the control plane (the tool broker)

**Status:** Accepted (built 2026-11-03; the design was written before the code and amended where the build differed)
**Date:** 2026-11-03
**Scope:** OSS. `internal/toolbroker` (token and scope registry), `internal/executor/hosted_tools.go` (the governed call and the hold for approval), `internal/api/handlers/tool_broker.go` (the route), small edits to `a2a_trust.go`, `a2a_guard.go`, `handlers.go` (the three proxy sinks), `run.go` (`resume` refuses an orphaned held call), `middleware/auth.go` (one public path), `process/templates/agent_runner.py`, `process/manager.go` (a process is told a tool's name and schema, not its endpoint), `pkg/server/server.go` (the broker, and `AGENTOVEN_CONTROL_PLANE_URL` defaulting to loopback), `workflow/engine.go` (`stepClient`). Pro adds a route rule, tests and an ADR ([agentoven-pro ADR-0062](https://github.com/agentoven/agentoven-pro)).
**Follows:** [ADR-0031](0031-native-tool-transport-and-tool-approval-seams.md), [ADR-0037](0037-tool-egress-guard-store-errors-and-sdk-route-contract.md), [ADR-0043](0043-restart-safe-recipe-gates.md); Pro ADR-0030 finding C3 (tool calls bypass the gateway), Pro ADR-0061 finding 1.

## Context

A recipe agent step is a POST to `/agents/{name}/a2a`, which proxies to the agent's process: the embedded `agent_runner.py` for a managed `agentoven`-runtime agent. `POST /agents/{x}/invoke` proxies to the same process whenever one is running (a recipe step starts it, so what governs an agent depends on what ran before). The runner calls every tool's `endpoint` itself (`_call_mcp_tool`). The Go executor does a great deal the runner does not:

| Go executor | Python runner |
|---|---|
| tool allowlist (`ToolsFor`) | any name in its own `AGENT_TOOLS_JSON` |
| approval: `Agent.ApprovalTools`, the installed `ToolApprovalPolicy`, guardrail holds; pause in the journal, signed decision | none: a write tool runs at once |
| argument schema check, guardrails on arguments and on the result | none |
| the MCP gateway: tool enabled, skill accepted, `native` transport (`pack://`), credential resolution and scrubbing, egress guard (`pkg/netguard`) | direct `urllib` call: no egress guard, endpoint and credentials in the process; `pack://` fails ("unknown url type") |
| per-call timeout, result cap, OTel span, run audit | 30 s, none, none |

Both consequences are real: a recipe cannot use pack tools, and the runner is a way round approval, guardrails and audit for any tool. Framework-native processes (LangGraph and others) run their own code and are out of scope.

## Decision

1. **The runner never calls a tool endpoint.** It posts the call to `POST /api/v1/internal/tool-calls` with a token the control plane gave it for the call it is handling. Without a token or control plane URL the call fails closed with an error result.
2. **The token** is minted per relayed call to a process the platform launched (not an external agent, not a bot, whose gate is its own). It is sent in `X-AgentOven-Tool-Token` by `signPodRequest`, never put in the environment and never logged. Format `aotc_<payload>.<sig>`: HMAC-SHA256 under a key derived from `A2ASecret` with its own label, so it cannot validate as a pod token, a gateway credential or a bot token. Claims: kitchen, agent, run id, jti, expiry (default 15 minutes, `AGENTOVEN_TOOL_TOKEN_TTL`, at most one hour).
3. **A scope registry** (`internal/toolbroker`) opens a scope when the relayed call starts and closes it when the call returns, in the three proxy sinks (`proxyA2AGuarded`, `proxyToProcess`, `proxyToProcessStream`). A call under a closed or unknown-but-expired token is refused. A scope has a call cap (`AGENTOVEN_TOOL_SCOPE_MAX_CALLS`, default 200, tighter if the agent budget names tool calls), a concurrency cap (8) and a total result byte cap. The registry is **per replica**: on a replica that never opened the scope, signature and expiry alone decide (see threats).
4. **The route is not user authentication.** It is public to the auth middleware (`isAuthPublicPath`) and is valid only with a run token; it needs no `AGENTOVEN_REQUIRE_AUTH`. Kitchen, agent and run come from the token, never from the body. The body is `{call_id, name, arguments}`, at most 1 MiB, strict JSON.
5. **Governed execution.** `Executor.RunHostedToolCall` loads the agent from the store (its stored snapshot, so a retired tool disappears immediately), applies the allowlist (`ToolsFor`), runs `guardToolCalls` (redaction, block, hold), asks `gatedNames` (agent list, installed policy, guardrail holds), and for an ungated call goes through `dispatchToolCall`: schema, guardrails, fence, timeout, gateway (`native` transport, credential resolution and scrubbing, egress guard), result guardrails, result cap, span. Audit event `tool.brokered_call` (agent, run, tool, decision, outcome, argument digest, no argument values).
6. **Approval.** A gated call is journaled as a one-call paused run (`MarkPaused` with the call, `run.paused` observer, same signed `tool_approval` gate, same `POST /agents/{a}/runs/{trace}/resume`) with the header marked `Hosted: true`. The broker request waits, polling the journal, so any replica can record the decision: `ResumeRun` on a held run claims it (`ClaimResume`, one winner) and records the decision as the run's outcome instead of resuming a model loop. Approved: the call is dispatched with the approval mark and the result returned to the process. Denied, expired (`AGENTOVEN_TOOL_APPROVAL_WAIT`, default 4 minutes, which is below the five minutes the relayed call itself may last, `AGENTOVEN_BACKEND_CALL_TIMEOUT`; the recipe engine's two minute client limit gives way to a step's `timeout_secs` when that is longer) or the caller gone: an error result, nothing ran. `resume` refuses a hosted run nobody waits for (after a restart) and ends it cancelled, so a late approval never runs a call whose asker is gone.
7. **The process sees typed results** `{ok, content, is_error, error: {kind}}` with kinds `unknown_tool`, `invalid_arguments`, `guardrail_blocked`, `approval_denied`, `approval_expired`, `budget`, `scope_closed`.

## Threat analysis (a compromised or malicious agent process)

| Attack | Defence |
|---|---|
| Forge a call for another kitchen, agent or run | identity comes only from the signed token; HMAC under a labelled key; constant-time compare; body fields ignored |
| Call a tool not in its allowlist (it holds the whole `AGENT_TOOLS_JSON`) | allowlist recomputed from the stored agent on every call, as the Go loop does |
| Replay after the run ended | scope closed on return of the relayed call: refused with `scope_closed`; expiry bounds it on a replica that did not open the scope |
| Use a token for a second run of the same agent | token is bound to one run id and one jti; the scope is per relayed call |
| Skip approval, or approve itself | the gate is evaluated server-side; the decision is a person's signed decision on the journal; the process holds no route to decide |
| Exhaust budget or the control plane | per-scope call, concurrency and byte caps; per-call timeout; body cap; model spend is the gateway's (Pro) |
| SSRF through a tool | the tool's endpoint is stored server-side; egress guard and pinning apply inside the gateway; the process supplies no URL |
| Smuggle instructions in a tool result | result guardrails as in the Go loop (input stage on results) |
| Token theft from the process | lives in one request header of one relayed call, a few minutes at most; not in env, files or logs |
| Approval spam to flood approvers | one pending per call, scope call cap, expiry |

## Consequences

Recipe agent steps run pack tools, and approval, guardrails, egress, secret handling and audit apply to every tool of a managed process. The runner has no direct network path to a tool any more (it still reaches the model: gateway or provider).

## Not done

- A held call is **in flight, not restart-safe**: a control plane restart ends the relayed call and the step fails (ADR-0043). The held run stays in the queue until its deadline (4 minutes by default) and a decision on it in that time is recorded but acts on nothing (the answer says so); after the deadline a decision ends it. A recipe step cannot wait hours for an approver; the wait is bounded. A long wait is a `human_gate`.
- A container or pod must be given `AGENTOVEN_CONTROL_PLANE_URL`; the loopback default serves processes on the server's machine only. A docker or k8s agent whose tools it used to reach directly now reaches them only through the control plane.
- The pod's environment still carries the long-lived `CONTROL_PLANE_TOKEN` (baker role) and the model credential; this ADR removes the tool bypass, not the process's other means.
- The registry is per replica; replay protection after the run ended is exact only on the replica that opened the scope.
- `agentoven_delegate` in the runner still calls `/api/v1/agents/{x}/invoke` with the pod's `CONTROL_PLANE_TOKEN`, and that long-lived baker token stays in the pod environment.
- Framework-native processes and external agents are not covered.
- A parallel tool-call turn is not batched into one approval.
