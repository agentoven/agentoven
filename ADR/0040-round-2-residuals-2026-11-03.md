# ADR-0040: Round 2 residuals: the scheduler's door, tool authentication shapes, channel signing secrets, signed notes, kitchen-required stores

**Status:** Accepted (built 2026-11-03)
**Date:** 2026-11-03
**Scope:** OSS (`pkg/toolauth`, `pkg/contracts`, `pkg/models`, `internal/api/handlers`, `internal/notify`, `internal/store`, `internal/router`). Pro counterpart: [ADR-0053](../../agentoven-pro/ADR/0053-round-2-residuals-2026-11-03.md), which has the reasons and the tests of the Pro half.

## Decisions

1. **Tool authentication has three shapes.** `toolauth.Validate` accepts `bearer`, `api-key` and `basic` (with `credential_ref`, or a literal secret that Pro seals) and refuses everything else with a 400 (`*toolauth.InvalidError`, answered by `respondCreateError`) at register, bulk register and update, in every edition: an unsupported type (OAuth client credentials, custom header sets), a key the type does not take, a non-text value, a header name that is not one (`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`, not `Host`, `Content-Length` and the other framing headers), a line break, a value over 4096 characters. Nothing sends an OAuth secret or a header set, so storing one is a liability. `basic` is implemented: `Resolve` reads the password from the credential, `Apply` sets basic auth, `Values` scrubs it from results, `Redacted` keeps the user name and any `*_ref`. `toolauth.LiteralSecret` finds the one literal secret of a supported shape; `toolauth.StrayFields` lists what an older, now refused shape holds. `Handlers.SealToolAuth` is exported for callers that register a tool themselves.
2. **The vault migration moves them.** `SealAllTools` also seals a literal basic password and, for a tool whose auth shape is now refused, every field with a value into `tool-<tool>-<field>` (the row keeps `<field>_ref`). Idempotent; a dry run lists them.
3. **A channel's signing secret is write-only.** `NotificationChannel` gains `secret_ref` (set by the server, refused from a client) and `has_secret`. Create, get, list and update never return `secret`. An update that omits the secret keeps it, an explicit `""` removes it. `notify.Service.withSigningSecret` reads the credential named by `secret_ref` for one delivery, on a copy, on every send path, and a secret that cannot be read stops the send rather than sending unsigned. A store that keeps secrets itself (the memory store) is unchanged.
4. **The note of a decision is signed.** `describeRun` puts a tool-approval and a budget decision's `note` in the digest and the display, `describeSkill` and `reviewSkill` a skill review's. Blank adds nothing.
5. **An empty kitchen is an error in the memory store's lists** (`ListAgents`, `ListRecipes`, `ListTraces`, `ListTracesFiltered`, `TraceStats`, `ListProviders`, `ListTools`, `ListSkills`, `ListPrompts`, `ListChannels`, `ListAuditEvents`, `CountAuditEvents`): `ErrKitchenRequired`, never "all". `ModelRouterService.HealthCheck(ctx, kitchen)` takes a kitchen (it scanned every kitchen's providers with an empty one and nothing outside tests called it).
6. **Exported for the scheduler:** `StartRequest.Origin` and `triggers.AccountFor`/`MayActAs` are Pro; the OSS side of the scheduler is the unchanged `POST /api/v1/recipes/{name}/bake`.

## Tests

`pkg/toolauth/validate_test.go`, `internal/api/handlers/tool_security_test.go` (refused shapes, sealed basic password, the migration), `channel_secret_test.go`, `internal/notify/retry_test.go` (signed from a credential, unreadable stops), `signing_run_test.go` and `signing_test.go` (the note is signed), `internal/store/memory_kitchen_required_test.go`, `internal/router/router_test.go`.

## Not done

OAuth client credentials and custom header sets are refused, not built. The community edition keeps a literal tool secret in its row (validated, redacted on read). No CLI or SDK surface for the channel secret beyond the existing `secret` field; the dashboard channel form has no secret field. The other strictly-filtered memory lists still return an empty list for an empty kitchen.
