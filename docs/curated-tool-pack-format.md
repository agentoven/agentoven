# The curated pack format (Pro)

A **curated pack** is a vendor's documented API written down by hand: ServiceNow and Salesforce ship in the catalog today. It sits on top of the tool pack mechanism ([tool-packs.md](tool-packs.md)): the install, the skill that owns the tools, the approval switches, the network allowlist, pinned credentials and result scrubbing are the existing ones. What the format adds is a manifest file that declares the vendor-specific part, so a new vendor is a manifest plus a small handler file, reviewed like code.

A pack is `internal/toolpacks/packs/<name>.pack.json` (embedded in the server) plus named Go handlers (`curated_<vendor>.go`). The manifest is read **strictly**: an unknown key is an error, so a typo in a guard cannot silently turn it off. The server will not start with a manifest that breaks any rule below (`TestEveryShippedPackObeysTheFormat`, `TestAManifestThatBreaksTheFormatIsRefused`).

| Section | Says |
|---|---|
| `name`, `version`, `title`, `description` | What it is. A change of version or content sends an install back for review under skill approval |
| `secrets` | The credential roles the pack knows (`username`, `password`, `client_id`, `client_secret`, `token`). None is "required": which ones are needed depends on the authentication chosen |
| `settings` | Pack-specific knobs (`allowed_objects`, `max_rows`, `api_version`, ...) with a type (`int`, `string` with a pattern, `enum`, `idents`, `strings`), bounds and a default. The pack adds the host label, `environment`, `auth_type`, `rate_limit_*_per_minute` and `timeout_ms` itself |
| `curated.host` | The **host pin template**: `param` (the setting holding the customer's label, such as `instance` or `my_domain`), `pattern` (an anchored regular expression that must not match a dot, slash, colon or at sign), and `environments`, each `{name, template}` with a template of the form `{param}.fixed.domain`. The base URL is `https://` plus the template with the label filled in; nothing else sets it |
| `curated.auth` | The schemes (`basic`, `bearer`, `oauth2_client_credentials`), the roles each needs, and for OAuth the `token_path` on the pinned host and whether the client id and secret go in the form `body` or as `basic` authentication |
| `curated.rate_limit` | Calls per minute per install, reads and writes counted apart (an installer may lower them or raise them up to 600 and 120) |
| `curated.error_parser`, `curated.errors` | How to read the vendor's error body, and rules (`status`, `vendor_code`) that map a failure to a `kind`, whether to retry, and a hint to the model |
| `curated.check` | A read-only request used as the connectivity check (`POST /api/v1/tool-packs/{name}/check`) |
| `tools[]` | `name`, `description`, `risk`, `schema`, `handler`, optional `params` (the table or object the handler works on) and `pagination` |
| `policy.writes_require_approval` | Must be `true` |

## Risk classes

Every tool says `read`, `write` or `destructive`. A **write or destructive tool waits for a person's signed approval by default**; a pack cannot ship them any other way. A Head Chef can switch a write tool's approval off per tool (`toolpack:govern`, audited). A **destructive tool's approval cannot be switched off by anyone** (`409 destructive_requires_approval`), and its audit event carries `risk`. The tool list in the catalog and on the Tool packs page shows the class.

## Schemas and arguments

A tool's input schema must be a closed object schema (`additionalProperties: false`) that compiles. On every call, before anything else, the arguments are validated against that schema (JSON Schema 2020-12 as implemented by `google/jsonschema-go`: types, enums, patterns in RE2, lengths, bounds, `required`). The MCP gateway and the replay paths reach a pack tool without the agent executor's lighter check, so the pack does not rely on it. Handlers then add what a schema cannot say (a filter value must not hold `^`; a SOQL statement must be read-only).

## Pagination

A list tool declares `pagination` (`offset` or `cursor`, a default and a maximum page size); its schema must carry the matching arguments. Answers carry `next_offset` or `next_cursor` (and `total` when the vendor says). A tool with a `limit` argument that does not declare pagination is refused.

## Rate limits

A token bucket per install and per class (read, write), holding up to a minute of calls and refilling continuously. A call over budget returns `rate_limited` with `retry_after_seconds` and is never sent. The buckets are in memory per server process: N replicas admit N times the rate, and the vendor's own limit is the backstop. A Postgres-backed shared bucket is not built.

## Errors

A failure is returned to the model as `{"error": {"kind", "http_status", "vendor_code", "message", "retryable", "hint", "retry_after_seconds"}}`, `kind` being one of `auth_failed forbidden not_found invalid_request conflict rate_limited quota_exceeded vendor_unavailable request_failed`. The vendor's own message is kept (shortened, scrubbed of secrets) because it is usually the precise part. Error rules and rate-limit defaults are read from the binary's manifest at call time, so a fix to either needs no reinstall.

## Authentication, tokens and the host

OAuth 2.0 client credentials is a **token exchange done by the pack**: the client id and secret are read from the vault for the call (never stored on the install), sent only to the token endpoint on the pinned host, the token is held in memory (keyed by kitchen, install and a hash of the credential, so a rotated secret finds no entry), refetched once on a 401, scrubbed from everything a tool returns, and never written to a trace, audit event or table. The token response's own URLs (`instance_url`) are ignored. The credentials are pinned: an install is refused, `secret_not_pinned`, unless each secret's `allowed_hosts` covers the pinned host, and the pin is read again on every call.

For an operator who runs a gateway in front of a vendor, or a test with a fake vendor: `AGENTOVEN_CURATED_PACK_ENDPOINTS="servicenow=https://gw.internal,salesforce=http://127.0.0.1:18602"` replaces the base URL of a pack (and the token URL with it). It is an operator setting; nobody who installs a pack can use it, and the server logs a warning when it is set. The host must still be reachable by the egress policy and the credentials pinned to it.

## Installing and enabling

Through the existing API: `GET /api/v1/tool-packs/catalog` lists the packs (with `curated`), `POST /api/v1/tool-packs` installs one (`{"pack": "servicenow", "name": "sn", "secrets": {...}, "settings": {"instance": "acme", "auth_type": "basic"}}`), `PATCH /api/v1/tool-packs/{name}/tools/{tool}` switches a tool on or off and its approval (Head Chef), `POST /{name}/check`, `POST /{name}/upgrade`, `PUT /{name}/secrets`, `DELETE /{name}`. Install needs `toolpack:install` (Chef and up). Under `AGENTOVEN_SKILL_APPROVAL=required` the install waits for people who did not install it.

## Testing a pack: the conformance fixture

Each pack has a fixture, `internal/toolpacks/testdata/conformance/<pack>.json`: for every tool at least one case with its arguments, the request the vendor must receive (method, path, query, headers, body) and what the answer must hold. The test runs each case against a fresh in-process fake vendor (`internal/toolpacks/vendorfake`, shapes taken from the vendor's public documentation) and fails when a tool has no case. When a sandbox account exists, the same fixture is the checklist to replay against it.

## Known limits

- Text read from a vendor (an incident description, a case body, an article) is untrusted input to the model and can carry instructions. The packs do not filter it; every write waits for a person's approval, and the approver sees the arguments.
- A recipe's agent steps run inside the agent's own process, which calls a tool's endpoint itself: it cannot reach a pack tool (transport `native`) and fails closed. Pack tools are used by agents invoked through the control plane (API, CLI, SDK, tasks, A2A through the executor), where approvals apply. See [examples/claim-triage](../examples/claim-triage/README.md).
- A gated (write) tool cannot be called through `POST /mcp`; it answers `-32004`.
- Connections are made per call (no pool), and no vendor has been exercised with a real account yet.
