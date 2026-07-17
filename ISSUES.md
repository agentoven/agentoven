# AgentOven — Known Issues & Technical Debt

> Last audited: 03 July 2026 (Post 0.8.6 hotfixes)
>
> This file tracks real bugs, logic errors, and technical debt found via
> code review, `go vet`, and manual inspection. It is referenced from
> [copilot-instructions.md](.github/copilot-instructions.md).

---

## Legend

| Severity | Meaning |
|----------|---------|
| 🔴 Critical | Breaks core functionality at runtime |
| 🟠 High | Data loss, race condition, or silent failure |
| 🟡 Medium | Incorrect behaviour under specific conditions |
| 🟢 Low | Cosmetic, misleading API, or minor correctness |
| ⚪ Infra | CI/CD, Docker, accessibility, non-code |

---

## 🔴 Critical

### ISS-001: Gateway context derived from HTTP request — gateway dies immediately

- **File:** `control-plane/internal/integrations/picoclaw/gateway.go` L142
- **Category:** `BUG`
- **Description:** `startGateway()` calls `context.WithCancel(ctx)` where `ctx` is the
  HTTP request context from `HandleCreateGateway`. The request context is cancelled when
  the HTTP response is written, immediately killing the gateway driver's `Start()` and
  all future `onMessage` callbacks.
- **Fix:** Replace `ctx` with `context.Background()`:
  ```go
  gwCtx, cancel := context.WithCancel(context.Background())
  ```
- **Status:** ✅ Fixed (R10, 3 Mar 2026)

---

## 🟠 High

### ISS-002: PicoClawInstance metadata never persisted

- **File:** `control-plane/internal/integrations/picoclaw/adapter.go` L87–137
- **Category:** `BUG`
- **Description:** `Register()` builds a `PicoClawInstance` with heartbeat config, device
  type, platform, skills, and gateways list, but only persists the `Agent` via
  `store.CreateAgent()`. The instance struct is returned in the HTTP response and then
  discarded. All PicoClaw-specific metadata is lost.
- **Fix:** Either:
  - (A) Embed all PicoClaw metadata into the Agent's `Tags` and `Metadata` fields, or
  - (B) Add a `PicoClawInstanceStore` interface with CRUD operations.
- **Status:** ⬜ Open

### ISS-003: ChatGateway struct never persisted

- **File:** `control-plane/internal/integrations/picoclaw/gateway.go` L80–97
- **Category:** `BUG`
- **Description:** `CreateGateway()` builds a `ChatGateway` struct but only stores the
  cancel function in the in-memory `active` map. There is no `ChatGatewayStore` interface.
  Gateways are **lost on server restart** and cannot be listed or reactivated.
- **Fix:** Add `ChatGatewayStore` to the store interface and persist gateways.
- **Status:** ⬜ Open

### ISS-004: BakeAgent background goroutine races on Agent pointer

- **File:** `control-plane/internal/api/handlers/handlers.go` (BakeAgent method)
- **Category:** `RACE_CONDITION`
- **Description:** `BakeAgent` calls `UpdateAgent(status=baking)`, returns the HTTP
  response, then a background goroutine continues to mutate the same `*Agent` pointer
  (setting status to `ready` or `burnt`) and calls `UpdateAgent` again. Another HTTP
  request could modify the same agent between the two updates, causing stale overwrites.
  The version is also spuriously bumped twice.
- **Status:** ✅ Fixed (R9, 1 Mar 2026) — goroutine re-fetches agent from store, checks status is still `baking` before mutating, captures immutable identifiers instead of `*Agent` pointer.

### ISS-024: A2A provider TLS override lost on framework-native path

- **Files:**
  - `control-plane/internal/api/handlers/handlers.go`
  - `control-plane/internal/process/templates/agent_runner.py`
- **Category:** `BUG`
- **Description:** `tasks/send` accepted `provider_config` and set `WithProviderTLSOverride` in Go context, but framework-native calls proxied via `/invoke` did not serialize TLS override into payload. Python runner then used default trust chain and failed on self-signed certs (`CERTIFICATE_VERIFY_FAILED`).
- **Fix:**
  - Extended `proxyToProcess` payload to include `provider_config`.
  - Updated framework-native call sites to pass TLS override.
  - Added request-scoped SSL context handling in runner and applied it across all outbound `urlopen` calls.
- **Status:** ✅ Fixed (0.8.6 hotfix, 2 Jul 2026)

---

## 🟡 Medium

### ISS-005: HeartbeatMonitor cannot restart after Stop()

- **File:** `control-plane/internal/integrations/picoclaw/heartbeat.go` L49–66
- **Category:** `BUG`
- **Description:** `Stop()` closes `stopCh`. A subsequent `Start()` sets `running=true`
  and launches `loop()`, but `loop()` immediately reads from the already-closed channel
  and returns. The monitor never actually runs again.
- **Fix:** Recreate `stopCh` inside `Start()`:
  ```go
  m.stopCh = make(chan struct{})
  ```
- **Status:** ⬜ Open

### ISS-006: Double UpdateAgent call in heartbeat processResult

- **File:** `control-plane/internal/integrations/picoclaw/heartbeat.go` L117–163
- **Category:** `LOGIC_ERROR`
- **Description:** `processResult()` calls `store.UpdateAgent()` up to **twice** per
  heartbeat: once for status change and once for tag updates. Each call bumps the agent
  version, inflating version history and creating phantom version entries.
- **Fix:** Consolidate all mutations into a single `UpdateAgent` call using a
  `needsUpdate` flag.
- **Status:** ⬜ Open

### ISS-007: Heartbeat monitor only checks "default" kitchen

- **File:** `control-plane/internal/integrations/picoclaw/heartbeat.go` L94
- **Category:** `BUG`
- **Description:** `checkAll()` hardcodes `kitchens := []string{"default"}`. PicoClaw
  agents registered in any other kitchen via the `X-Kitchen` header are never health-checked.
- **Fix:** Use `store.ListKitchens()` to iterate all kitchens (once available).
- **Status:** ⬜ Open (blocked on ListKitchens API)

### ISS-008: HeartbeatMonitor not wired into server lifecycle

- **File:** `control-plane/internal/api/router.go` L278–295
- **Category:** `RESOURCE_LEAK`
- **Description:** `NewRouter` creates `pcAdapter` and `pcGateway` but never starts a
  `HeartbeatMonitor`. The monitor is defined but unused. There's also no shutdown hook
  for `pcGateway.StopAll()`.
- **Fix:** Wire `HeartbeatMonitor` into `main.go`. Call `Start()` on server startup and
  `Stop()`/`StopAll()` on graceful shutdown via `os.Signal`.
- **Status:** ✅ Fixed (R10, 3 Mar 2026) — PicoClaw components created in `buildServer()`, HeartbeatMonitor started, shutdown hooks added.

### ISS-009: resolveEmbedding uses provider Kind instead of Name

- **File:** `control-plane/internal/resolver/resolver.go` (resolveEmbedding / resolveRetriever)
- **Category:** `LOGIC_ERROR`
- **Description:** `resolveEmbedding` sets `Provider` to the provider's Kind (e.g.
  `"openai"`) but the retriever cross-reference validation checks against it using the
  provider **name**. If a user names their provider `"my-openai"` and sets
  `embedding_ref: "my-openai"`, the match fails.
- **Fix:** Store both `ProviderKind` and `ProviderName` in `ResolvedEmbedding`, or
  match on name.
- **Status:** ✅ Fixed (R10, 3 Mar 2026) — added `ProviderName` field, cross-reference matches on name, kind, and kind/model.

---

## 🟢 Low

### ISS-010: GET /picoclaw/gateways returns drivers, not gateways

- **File:** `control-plane/internal/api/router.go` L291
- **Category:** `LOGIC_ERROR`
- **Description:** `GET /picoclaw/gateways` is bound to `HandleListDrivers` which returns
  registered driver **kinds** (e.g. `["telegram"]`), not actual gateway instances. API
  consumers expect to see created gateways.
- **Fix:** Move driver listing to `GET /picoclaw/gateways/drivers` and add a real
  `HandleListGateways` endpoint.
- **Status:** ⬜ Open

### ISS-011: checkHealth doesn't handle trailing-slash endpoints

- **File:** `control-plane/internal/integrations/picoclaw/adapter.go` L413
- **Category:** `BUG`
- **Description:** `checkHealth` builds `agent.A2AEndpoint + "/status"`. If the endpoint
  is `http://device:8080/`, this produces `http://device:8080//status`.
- **Fix:** `strings.TrimRight(agent.A2AEndpoint, "/") + "/status"`
- **Status:** ✅ Fixed (R10, 3 Mar 2026)

### ISS-012: HandleStopGateway comment/implementation mismatch

- **File:** `control-plane/internal/integrations/picoclaw/gateway.go` L242
- **Category:** `BUG`
- **Description:** Comment says `DELETE /picoclaw/gateways/{id}` (path param) but the
  handler reads `r.URL.Query().Get("id")` (query param) and the route has no path param.
- **Fix:** Update comment to `DELETE /picoclaw/gateways?id=<gateway_id>`.
- **Status:** ⬜ Open

### ISS-013: resolveVectorStore requires index for embedded backend

- **File:** `control-plane/internal/resolver/resolver.go` (resolveVectorStore)
- **Category:** `LOGIC_ERROR`
- **Description:** `resolveVectorStore` requires a non-empty `index` config field even for
  the `embedded` (in-memory) backend, where there is only one store per kitchen. This
  forces users to supply a meaningless index name for the simplest use case.
- **Fix:** Default `index` to `"default"` when backend is `embedded`.
- **Status:** ✅ Fixed (R10, 3 Mar 2026)

---

## ⚪ Infra / Non-code

### ISS-014: Dashboard accessibility violations

- **Files:**
  - `control-plane/dashboard/src/pages/Agents.tsx` — 7 `<select>` without accessible names, 1 `<button>` without text, 1 `<input>` without label
  - `control-plane/dashboard/src/pages/Tools.tsx` — 2 `<select>`, 2 `<button>` issues
  - `control-plane/dashboard/src/pages/Providers.tsx` — 1 `<select>` issue
  - `control-plane/dashboard/src/pages/RAGPipelines.tsx` — 1 `<select>`, 3 `<input>` issues
  - `control-plane/dashboard/src/pages/AgentTest.tsx` — 1 `<button>` issue
- **Category:** `ACCESSIBILITY`
- **Fix:** Add `title` or `aria-label` attributes to all form elements and buttons.
- **Status:** ⬜ Open

### ISS-015: Docker base image vulnerabilities

- **Files:**
  - `control-plane/Dockerfile` L2 — `node:22-alpine` has 6 high vulnerabilities
  - `control-plane/internal/integrations/ragas/Dockerfile` L1 — `python:3.11-slim` has 1 high vulnerability
- **Category:** `SECURITY`
- **Fix:** Update to latest patch versions or use distroless images.
- **Status:** ⬜ Open

### ISS-016: GitHub Actions secrets not configured

- **File:** `.github/workflows/release.yml` L269, L309
- **Category:** `CI/CD`
- **Description:** `NPM_TOKEN` and `HOMEBREW_TAP_TOKEN` secrets are referenced but not
  set in the repository. These jobs will fail on actual release.
- **Fix:** Add secrets in GitHub → Settings → Secrets.
- **Status:** ⬜ Open

### ISS-017: Landing page / docs inline CSS violations

- **Files:**
  - `agentoven-main/agentoven/src/components/CodeBlock.tsx`
  - `agentoven-main/agentoven-docs/src/components/CodeBlock.tsx`
  - `agentoven-main/agentoven-docs/src/app/layout.tsx`
- **Category:** `LINT`
- **Description:** CSS inline styles flagged by linter. Not functional bugs.
- **Status:** ⬜ Open (low priority)

### ISS-018: `meta[name=theme-color]` not supported in Firefox

- **File:** `site/index.html` L8
- **Category:** `COMPAT`
- **Description:** `<meta name="theme-color">` is not supported by Firefox, Firefox for
  Android, or Opera.
- **Status:** ⬜ Open (cosmetic)

---

## � High (Codex Findings — Release 7)

### ISS-021: Tier quota matching uses strings.Contains — too broad

- **File:** `agentoven-pro/internal/tier/enforcer.go` L67
- **Category:** `LOGIC_ERROR`
- **Description:** `TierEnforcer.Middleware` uses `strings.Contains(path, "/agents")` to
  detect agent-creation requests. This is a substring match, so it incorrectly triggers
  the agent quota check on any path containing `/agents` — including sub-resource
  operations like `POST /api/v1/agents/{name}/bake`, PicoClaw endpoints, and future
  nested routes. Same issue for `/recipes`, `/providers`, `/tools`, `/prompts`.
- **Fix:** Replace `strings.Contains` with exact path matching:
  ```go
  case path == "/api/v1/agents" && r.Method == http.MethodPost:
  ```
- **Status:** ✅ Fixed (R7, 21 Feb 2026)

### ISS-022: CORS AllowedOrigins "*" with AllowCredentials true — security risk

- **File:** `control-plane/internal/api/router.go` L44, L48
- **Category:** `SECURITY`
- **Description:** `AllowedOrigins: []string{"*"}` combined with `AllowCredentials: true`
  causes go-chi/cors to reflect any `Origin` header back with credentials allowed. This
  means any website can make credentialed cross-origin requests to the API — a CSRF/credential-leak vector.
- **Fix:** Make CORS origins configurable via `AGENTOVEN_CORS_ORIGINS` env var. When
  wildcard is used, disable `AllowCredentials`. Default to localhost + agentoven.dev.
- **Status:** ✅ Fixed (R7, 21 Feb 2026)

### ISS-023: BakeAgent silently ignores JSON decode errors

- **File:** `control-plane/internal/api/handlers/handlers.go` L212
- **Category:** `BUG`
- **Description:** `json.NewDecoder(r.Body).Decode(&req)` discards the error return value.
  Malformed JSON (typos, wrong content type, garbled body) silently proceeds with
  zero-value fields. This is separate from ISS-004 (BakeAgent race condition).
- **Fix:** Check decode error and return 400 (allow `io.EOF` since body is optional).
- **Status:** ✅ Fixed (R7, 21 Feb 2026)

---

## 🔒 Authentication Architecture

### ISS-019: SSO middleware is a placeholder (SAML not implemented)

- **File:** `agentoven-pro/internal/auth/sso.go`
- **Category:** `NOT_IMPLEMENTED`
- **Description:** `SSOMiddleware` is a no-op placeholder. The SAML flow (IdP metadata
  parsing, assertion validation, session management) is not implemented. The `TODO` comment
  lists requirements but no code exists.
- **Impact:** Enterprise SSO authentication does not work. All requests pass through.
- **Status:** ✅ Partially fixed (R7) — SSO now rejects when enabled but not configured
  (closes fail-open). Full SAML/OIDC providers in R8 per [AUTH-PLAN.md](AUTH-PLAN.md).

### ISS-020: RBAC middleware not wired into router

- **File:** `agentoven-pro/internal/auth/rbac.go`
- **Category:** `NOT_IMPLEMENTED`
- **Description:** `RBACMiddleware` is defined with proper role/permission mapping but
  `getUserFromContext()` always returns `nil`, so RBAC is effectively a no-op. It's not
  wired into the API router.
- **Impact:** Authorization is not enforced. All authenticated users have full access.
- **Status:** ✅ Partially fixed (R7) — `getUserFromContext()` now reads `Identity` from
  context (set by auth chain). Per-route RBAC wiring deferred to R8.

---

## Summary

| Severity | Count | Fixed |
|----------|-------|-------|
| 🔴 Critical | 1 | 1 |
| 🟠 High | 7 | 5 |
| 🟡 Medium | 5 | 4 |
| 🟢 Low | 4 | 2 |
| ⚪ Infra | 5 | 0 |
| 🔒 Auth | 2 | 2 (partial) |
| **Total** | **24** | **14** |
