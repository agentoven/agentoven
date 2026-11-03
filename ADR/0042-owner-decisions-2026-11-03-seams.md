# ADR-0042: The owner's decisions of 2026-11-03, community seams

**Status:** Accepted (built 2026-11-03)
**Date:** 2026-11-03
**Scope:** OSS control plane (`pkg/models`, `pkg/netguard`, `pkg/contracts`, `internal/api/handlers`, `internal/guardrails`, `internal/notify`), the SDKs and the Rust client. The enterprise side and the full list of decisions: Pro ADR-0057.

## Decisions (what the community edition does)

1. **A3: approval inputs are content.** `models.ApprovalRecord` joins the content types (`internal/api/handlers/content_guard.go`). `GET /api/v1/approvals` and `/approvals/{gateKey}` hand a reader without `trace:read:content` (the edition's `SetTraceContentGuard`) `models.PublicApproval`: that the approval exists, its step, status and who decided it, and the metadata labels a trace's public view keeps (`agent_name`, `from_env`, `to_env`, `version`, `requested_by`), not the gate's inputs or the approver's comment (`content_withheld: true`). With no guard installed everyone reads everything, as before.
2. **A4: `null` clears an optional field.** `models.AgentEdit.Cleared` lists the fields a PUT or re-cook body sent as JSON `null` (`models.NullFields`); it is kept in the draft, merged (a later set removes the field from the list) and applied by `Apply`. Clearable: `description`, `framework`, `backup_provider`, `backup_model`, `execution_mode`, `max_turns`, `bot`, `ingredients`, `context_budget`, `reasoning_strategy`, `summary_provider`, `summary_model`. `null` for `model_provider`, `model_name`, `mode`, `behavior`, `runtime` is `400`. `null` for a list stays "no opinion" (a client that marshals a nil slice would otherwise wipe guardrails). The SDK edit mirrors (Python `apply_edit`, TypeScript `applyEdit`, Rust `apply_edit`) follow, run against one shared fixture, `sdk/fixtures/agent-edit-cases.json`, which the Go models test also runs; the mirrors now clear `skills` with `[]` as the server does.
3. **A7: a bake tells the edition where it landed.** `Handlers.BakeDeployments` (`BakeDeploymentRecorder.RecordBakeDeployment(kitchen, agent, version, env, by)`) is called once the baked agent is ready, with the environment the bake targeted. The community edition has no deployment records and installs none.
4. **A8: a retired agent is not touched.** `refuseRetired` answers 409 for an edit, bake, re-cook, cool and rewarm, and the cool's store write carries `AgentLifecycleGuard.Not = retired` so a delete between the read and the write cannot be undone by a cool.
5. **S7: an agent's own custom endpoint.** `Handlers.SetOwnEndpointPolicy` installs `guardrails.OwnEndpointPolicy`, asked by `Effective` before every run and by bake and re-cook (`409 guardrail_endpoint_not_approved`). Every rule is stamped with `KitchenID` and `AgentName` by `Effective`, overwriting what a client sent.
6. **S10: channels have their own egress allowlist.** `netguard.Channels()` is a second guard with its own operator list (`AGENTOVEN_CHANNEL_EGRESS_ALLOW`) and kitchen list (`SetChannelKitchenAllow`). The webhook driver and the save-time check use it. `AGENTOVEN_TOOL_EGRESS_ALLOW` no longer admits a channel, and the channel list admits no tool. Metadata addresses are refused whatever the list says.
7. **Gate names for the edition's signed decisions:** `contracts.GateTriggerChange` (S12) and `contracts.GateDatasetCaseErase` (S14).

## Settled as "no change"

A5 (check strictness: warnings on write, errors on bake), A6 (an unknown agent on a raw route is 404), S9 (a gated tool over `/mcp` is refused), S11 (sealing provider keys stays opt-in, `AGENTOVEN_SEAL_PROVIDER_KEYS`), P6 (replay windows 24 hours for a webhook, 30 days for a mailbox).

## Not done

- A channel's URL is checked at save time and at dial time; a name that resolves to a private address and is allowlisted by the kitchen is still the kitchen's choice.
- No dashboard control for clearing a field (the dashboard sends only what the form changed); no CLI flag (`agentoven agent update --json '{"backup_provider": null}'` works).
