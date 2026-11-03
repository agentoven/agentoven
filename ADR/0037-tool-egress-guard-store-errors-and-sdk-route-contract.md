# ADR-0037: Tool egress guard, store errors, retired agents and the SDK route contract: fixes from the 2026-11-03 adversarial review

**Status:** Accepted (built 2026-11-03)
**Date:** 2026-11-03
**Scope:** OSS control plane (`pkg/netguard`, `internal/api/handlers`, `internal/mcpgw`, `internal/notify`), the three SDKs and the Rust CLI.
**Pro counterpart:** [ADR-0052](../../agentoven-pro/ADR/0052-identity-and-surface-hardening-2026-11-03.md)

## Decisions

1. **A tool cannot be pointed at the control plane's own network.** `pkg/netguard` refuses endpoints that resolve to loopback, private, link-local, carrier-grade NAT or cloud-metadata addresses. It is applied when a tool is registered or updated (`POST/PUT /tools`, `/tools/bulk`: 400) and at every call (MCP gateway HTTP/SSE, remote MCP over streamable HTTP, notification tools) by dialling only the address it checked, so DNS rebinding gets no further. `AGENTOVEN_TOOL_EGRESS_ALLOW` (names, addresses, `*.suffix`) allows private hosts for every kitchen; an edition adds a per-kitchen list with `netguard.SetKitchenAllow` (Pro wires the kitchen's tool-pack egress allowlist). Metadata is never allowed. A failing allowlist lookup allows nothing. Redirects are followed only to the same host. Proxy environment variables are ignored.
2. **Tool registration.** `enabled:false` is honoured on create and bulk, an update without `enabled` leaves it, the masked `auth_config` written back keeps the stored secret, and with a vault (Pro) a literal token or key is sealed into a kitchen credential (`tool-<name>`, `credential_ref` in the row; `Handlers.SealAllTools` migrates older tools). A bulk request is validated whole before anything is created.
3. **Store errors.** `respondCreateError`: `ErrNotFound` 404, `ErrConflict` or a raw unique violation (recognised through `SQLState()`, no driver imported) 409, anything else a 500 without the driver's text. Used by agent, recipe, provider, tool, prompt, channel and scoped-key creates.
4. **Retired agents.** `GET /agents` omits retired agents unless `status=retired` or `include_retired=true`; `PUT` on one is 409; creating one again is the store's decision (Pro revives it under its id, ADR-0052). The SDKs' `register` creates when the agent is absent or retired.
5. **SDK route contract.** `sdk/fixtures/server-routes.json` is the server's route list, generated from the real server's chi tree by Pro's `internal/routeguard` test (`UPDATE_SDK_ROUTES=1 go test ./internal/routeguard -run SDKRoute`); OSS `pkg/server` test `TestSDKRouteFixtureHasEveryCommunityRoute` keeps it from missing a community route. Each SDK has a test that reads its call sites and fails for a verb and path the server lacks: `sdk/python/tests/test_route_contract.py`, `sdk/typescript/test/route-contract.test.ts`, `crates/agentoven-core/tests/route_contract.rs`. Fixed by it: Rust `retire_agent` (was `POST /agents/{name}/retire`, now `DELETE /agents/{name}`), Python and TypeScript `delete_world_schema` (removed: the server has no such route). The guardrail methods (`toggle_guardrail`, `get_guardrail`, exception removal by id) were corrected by ADR-0032's work.
6. **SDK transport.** Python and Rust time out (default 120 s; `timeout=` / `with_timeout`, `AGENTOVEN_TIMEOUT`, `timeout_secs`), TypeScript too (`timeoutMs`); Python and Rust refuse a redirect instead of sending the key to it; TypeScript reads an error body once (a proxy's HTML page is an `AgentOvenAPIError`, not "Body is unusable"); every id and name is escaped as one path segment in all three; the Rust config file is written 0600 (directory 0700) and `Debug` of the client and config never prints the key or token.

## Not done

- The route contract reads call sites: a path held in a variable is not seen, and the Rust scanner finds a verb by the next calls in the source (66 calls today). The CLI crate's own HTTP calls and the dashboard's `api.ts` are not covered.
- `internal/api/handlers` webhook-style channels and the guardrail webhook keep their own address checks; they are not folded into `pkg/netguard`. (Channels are now: see the 2026-11-03 amendment on notification channels.)
- The Python SDK's native `register` is the Rust core: it picks up the retired-agent rule when the extension is rebuilt (`maturin`), which was not done here; the Rust test covers it.
- `delete_world_schema` is removed rather than the server gaining a delete route: a delete needs a store method, a permission and an ADR of its own.

## Amendment 2026-11-03: adversarial review 4 (the guard, secret references, managed tools)

- **`pkg/netguard` is the one SSRF implementation for tools, tool packs and triggers.** It now refuses IPv6 zone identifiers (`fd00:ec2::254%eth0` walked past `netip.Prefix.Contains` into the dial), inet_aton number spellings (`2130706433`, `0x7f.0.0.1`, `127.1`), userinfo and other non-host characters, and the ranges that carry an IPv4 address inside an IPv6 one (`::/96`, `64:ff9b::/96`, `64:ff9b:1::/48`, `2002::/16`, `2001::/32`), strips a trailing dot and lower-cases before deciding, and always refuses the metadata addresses, with or without a zone. `Guard.Vet` is exported (resolve and judge a host, what a registration-time check and every dial go through); a guard can carry its own list (`Allow`, with CIDR ranges, for an installation's private hosts) and ignore the tools' environment variable (`IgnoreEnv`); `ClientWith(HTTPOptions)` takes a redirect budget, never follows a redirect off the host or from https to http, and can keep no connection alive. `corpus_test.go` is the one test corpus (IPv4-mapped, zone ids, decimal/octal/hex, trailing dots, userinfo, IDN look-alikes, redirects, rebinding).
- **A reference a kitchen types is `vault://<name>` of that kitchen, and nothing else** (`internal/secretref.KitchenVaultName`, exported to editions as `contracts.KitchenSecretName`). `Resolver.ResolveFor`, which every edition uses for such references, refuses any other scheme; `Resolve`, which takes every scheme, is for operator-set references. A provider's `secret_ref` or pool-key reference that is `env://` or `k8s://` can be set only by a platform administrator when the edition has a vault (`provider_vault.go`, `403`); one the provider already holds is kept.
- **A tool whose transport is `native` is managed by the edition that runs it** (tool packs): `PUT` and `DELETE /tools/{name}` answer `403 tool_managed`, and a native tool cannot be registered through `POST /tools`, `/tools/bulk` or by changing a tool's transport.

Not done: the guardrail webhook's own address check is still outside the guard (unchanged; the notification channels' URLs are inside it now, see the amendment below); the provider driver's `secret_ref` still resolves with `Resolve` because it is set by an operator (or by a platform administrator through the API).

## Amendment 2026-11-03: notification channels follow the egress guard

The end-to-end scripts that start a loopback receiver for a channel (`e2e_channel_secret.py`, `e2e_approval_notify.py` in Pro) forced
the question whether a channel may point at a private address. It could: a channel's `url` was posted to by a plain `http.Client`,
so a person who may create channels could aim one at `http://169.254.169.254/` or any internal service, and a Pro e-mail channel
dialled whatever `smtp_host` it was given. That was the open item "found, not fixed" in Pro ADR-0050. The rule is the one for tools:

- **At delivery.** `WebhookChannelDriver.Send` (OSS) and Pro's Slack, Teams, Discord, Zapier and e-mail drivers send through
  `netguard.Default()` (`ClientWith` / `Dial`): the destination is resolved once, vetted, and dialled by address; loopback, private, link-local and
  the cloud metadata ranges are refused unless `AGENTOVEN_TOOL_EGRESS_ALLOW` or the kitchen's allowlist names the host (the metadata range is
  refused even then). No redirect is followed and no connection is kept between deliveries. A refusal (`netguard.ErrRefused`) is a policy decision: it is
  returned at once, not retried three times with a pause, and Pro's `postJSON` leaves the URL out of it (an incoming-webhook URL is itself the secret).
- **At save.** `POST /channels` and `PUT /channels/{name}` run `netguard.CheckURL` on the `url` of every kind that has one (webhook, Slack, Teams, Discord, Zapier;
  not e-mail, which names an SMTP server in its config, and not WebSocket, which is listened to) and answer `400` with `url: ...`. A name that does not resolve yet is
  accepted, as for tools; the dial still checks it. `handlers/channel_egress.go`.
- Tests: `internal/notify/channel_egress_test.go`, `internal/api/handlers/channel_egress_test.go`; Pro `internal/channels/egress_test.go` and the end-to-end
  `e2e_channel_secret.py`. Test servers on loopback allowlist `127.0.0.1` (the handlers and Pro channels packages do it in `TestMain`).

Not done: an existing channel at a private address is not removed or flagged by a migration; it keeps its row and its deliveries fail with the refusal on the
approval's timeline until the operator allowlists the host or edits the channel. The e-mail channel's SMTP host is checked at delivery only (it is not in a URL).

