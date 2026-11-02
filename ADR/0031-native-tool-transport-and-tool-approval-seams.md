# ADR-0031: The native tool transport and tool-level approval seams (OSS hooks for Pro tool packs)

**Status:** Accepted  
**Date:** 2026-11-03  
**Authors:** AgentOven Engineering  
**Scope:** OSS (two small, generic seams and one fix). The pack mechanism, the OpenAPI importer, the executors, the egress policy, the API and the dashboard are Pro ([agentoven-pro ADR-0048](https://github.com/agentoven/agentoven-pro)).

---

## Context

The only way to give an agent a tool for an enterprise system is to write an HTTP or MCP server for it and register it (`POST /api/v1/tools`). The
gateway can run a tool in exactly two ways: POST a bare `tools/call` to an endpoint (`http`, `sse`) or talk MCP to a remote server (`mcp`). A tool
for "this REST API", "this database" or "send this mail" has no endpoint to point at: the control plane itself would have to make the call, and
the gateway has nowhere to put the code that does.

Approval has a second gap. `Agent.ApprovalTools` pauses a run before a named tool runs, which is right for an agent author who knows what the tool
does. It is per agent and baked into the agent, so a tool that is dangerous by nature (it writes to a payment system) is safe only in the agents
whose author remembered to list it, and toggling it for the kitchen means editing every agent.

## Decision

1. **A transport `native`** (`models.MCPTransportNative`). A tool of this transport has no endpoint; the gateway hands the call to an installed
   `contracts.NativeToolRunner` (`RunNativeTool(ctx, tool, args)`). The gateway still checks that the tool exists, is enabled and, for a skill's
   tool, that the skill is accepted, and it still scrubs a resolved credential from the result. Without a runner the call fails with
   "this edition cannot run tool packs". The community edition installs none.
2. **`contracts.ToolApprovalPolicy`** (`RequiresApproval(ctx, kitchen, tool)`), installed with `Handlers.SetToolApprovalPolicy`. The executor asks it
   for every tool call in addition to `Agent.ApprovalTools` (`gatedNames`), so a tool can be held for approval for every agent in a kitchen and the
   answer is read per call: switching it takes effect on the next call, not at the next bake. A policy that cannot answer must say yes.
   The realtime path refuses such a tool (a live call cannot wait for a person), and a subagent is never given one (`withPolicyGated` adds the
   policy's tools to the approval set that `buildSubagent` already subtracts).
3. **The gateway refuses a gated tool unless the call went through the executor's gate.** `contracts.WithToolApproved(ctx)` marks the context of a
   call that has passed the gate (the executor sets it where it calls the gateway, which a gated call reaches only after a person approved);
   `Gateway.HandleJSONRPC` answers `-32004 Approval required` to a `tools/call` for a policy-gated tool without the mark. This closes the MCP
   endpoint (`POST /mcp`), which has no way to ask a person, as a way round the approval.
4. **A fix in `reviewSkill`.** Approving a skill replaced `Skill.RegisteredTools` with the tools its manifest's MCP servers registered. A skill that
   owns tools it registered itself (a pack's) lost them from the record, so attaching the skill gave an agent nothing and deleting it left the tools
   behind. The names are now merged.

## Consequences

- Community behaviour is unchanged: no runner, no policy, so `native` tools do not exist and `RequiresApproval` is never asked.
- The per-call policy lookup is one store read per tool call when a policy is installed (Pro: one indexed query).
- A tool that needs approval cannot be called through the MCP endpoint at all. That is deliberate and documented; an MCP client that wants it
  runs it through an agent.
- Nothing here depends on how a runner stores credentials or what it connects to. Egress rules (SSRF), secret resolution, size and time limits are
  the runner's, and are specified in Pro ADR-0048.

## Not done

- The MCP endpoint cannot ask a person for approval, so it refuses instead. A pending-approval flow for direct MCP calls is not designed.
- `Agent.ApprovalTools` and the policy are separate lists; an agent cannot opt a tool out of the policy (only the policy's owner can).

## Verification

`internal/mcpgw/native_test.go` (runner dispatch, no runner, gated tool refused without the mark and served with it),
`internal/executor/tool_policy_test.go` (a policy-gated tool pauses, runs once after approval, the gateway is told it passed the gate, a denial
reaches nothing, an ungated tool is untouched), `internal/api/handlers/skills_test.go` (`TestApproveSkillKeepsToolsTheSkillAlreadyOwns`).
