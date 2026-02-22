# AUTH-PLAN.md — Pluggable Authentication Architecture

> Created: 21 February 2026
> Status: **Design** (not yet implemented)
> Related: [ISSUES.md](ISSUES.md) — ISS-019, ISS-020

---

## 1. Problem Statement

AgentOven needs a **pluggable authentication layer** where:

- **Authentication (authn)** = "who are you?" → handled by an interchangeable identity provider
- **Authorization (authz)** = "what can you do?" → handled internally by AgentOven RBAC

Each enterprise should be able to **choose their identity provider** (Okta, Azure AD,
Google Workspace, corporate LDAP, etc.) without changing any AgentOven code. The auth
layer simply produces a standard `Identity` that flows into RBAC.

### What Exists Today

| Component | State |
|-----------|-------|
| **API key auth** (OSS) | ✅ Working — `middleware/apikey.go` |
| **RBAC roles + permissions** (Pro) | ✅ Defined — 6 roles, 30+ permissions, `HasPermission()` works |
| **Audit logging** (Pro) | ✅ Working — `AuditLogger.Log()` + middleware |
| **SSO/SAML** (Pro) | ❌ Placeholder — `SSOMiddleware` is a no-op |
| **`getUserFromContext()`** | ❌ Returns `nil` always — RBAC can't check anything |
| **Session management** | ❌ Not implemented |
| **OIDC / LDAP / mTLS** | ❌ Not designed |

### The Gap

```
Request → [ ??? authn ??? ] → Identity → [ RBAC authz ✅ ] → Handler
               ↑                              ↑
          NOT IMPLEMENTED              IMPLEMENTED (but unwired)
```

---

## 2. Design: `AuthProvider` Interface

The core abstraction is a single interface in **`pkg/contracts/`** (OSS) that the
enterprise repo can implement with any identity provider.

```go
// pkg/contracts/auth.go

// Identity represents an authenticated user/service.
// Produced by AuthProvider, consumed by RBAC middleware.
type Identity struct {
    Subject     string            // Unique ID (user ID, service account, API key hash)
    Email       string            // User email (may be empty for service accounts)
    DisplayName string            // Human-readable name
    Provider    string            // "apikey", "saml", "oidc", "ldap", "mtls"
    Kitchen     string            // Tenant scope (from token claims or mapping)
    Role        string            // Role hint from IdP (mapped to AgentOven roles)
    Groups      []string          // IdP group memberships (for group→role mapping)
    Claims      map[string]string // Raw claims from the token (for custom policies)
    ExpiresAt   time.Time         // When this identity expires (session TTL)
}

// AuthProvider authenticates an HTTP request and returns an Identity.
// Each provider implements one authentication strategy.
type AuthProvider interface {
    // Name returns the provider identifier (e.g. "saml", "oidc", "ldap").
    Name() string

    // Authenticate inspects the request and returns an Identity.
    // Returns (nil, nil) if this provider doesn't handle the request
    // (allows chaining — the next provider gets a chance).
    // Returns (nil, err) if authentication was attempted but failed.
    Authenticate(ctx context.Context, r *http.Request) (*Identity, error)

    // Enabled returns whether this provider is configured and active.
    Enabled() bool
}

// AuthProviderChain tries providers in order until one returns an Identity.
// This is used by the OSS auth middleware.
type AuthProviderChain interface {
    // Authenticate walks the chain of providers.
    Authenticate(ctx context.Context, r *http.Request) (*Identity, error)

    // RegisterProvider adds a provider to the chain.
    RegisterProvider(provider AuthProvider)
}
```

---

## 3. Provider Options

Each provider is a separate implementation of `AuthProvider`. Enterprises choose
which ones to enable via configuration.

### 3a. API Key (OSS — already exists, needs adapter)

```
                    ┌──────────────┐
Authorization:      │  API Key     │
Bearer <key>    ──► │  Provider    │ ──► Identity{Subject: key_hash, Provider: "apikey"}
X-API-Key: <key>    │  (OSS)       │
                    └──────────────┘
```

- **Config:** `AGENTOVEN_API_KEYS=key1,key2` (existing)
- **Role mapping:** All API keys get a configured default role (default: `baker`)
- **Implementation:** Wrap existing `APIKeyAuth` into `AuthProvider` interface
- **Tier:** OSS (Free)

### 3b. OIDC / OAuth 2.0 (Pro)

```
                    ┌──────────────┐
Authorization:      │  OIDC        │
Bearer <JWT>    ──► │  Provider    │ ──► Identity{Subject: sub, Email: email, Groups: [...]}
                    │  (Pro)       │
                    └──────────────┘
                          │
                          ▼
                    ┌──────────────┐
                    │ JWKS         │
                    │ Validation   │
                    └──────────────┘
```

- **Supported IdPs:** Okta, Azure AD (Entra ID), Google Workspace, Auth0, Keycloak, any OIDC-compliant
- **Config:**
  ```yaml
  auth:
    oidc:
      enabled: true
      issuer_url: "https://login.microsoftonline.com/{tenant}/v2.0"
      client_id: "abc-123"
      audience: "api://agentoven"
      # Optional: map IdP groups to AgentOven roles
      group_role_mapping:
        "AgentOven-Admins": "admin"
        "AgentOven-Developers": "baker"
        "AgentOven-Viewers": "viewer"
  ```
- **Flow:**
  1. Client obtains JWT from IdP (outside AgentOven)
  2. Client sends `Authorization: Bearer <JWT>` to AgentOven
  3. OIDC provider validates JWT signature via JWKS endpoint
  4. Extracts `sub`, `email`, `groups` claims → `Identity`
  5. Maps IdP groups to AgentOven roles via `group_role_mapping`
- **Library:** `github.com/coreos/go-oidc/v3`
- **Tier:** Pro

### 3c. SAML 2.0 (Pro)

```
                    ┌──────────────┐
Browser redirect    │  SAML        │
from IdP        ──► │  Provider    │ ──► Identity{Subject: nameID, Email: email}
SAML assertion      │  (Pro)       │
                    └──────────────┘
                          │
                          ▼
                    ┌──────────────┐
                    │ Session      │
                    │ Cookie/Store │
                    └──────────────┘
```

- **Supported IdPs:** Okta, Azure AD, OneLogin, PingFederate, ADFS
- **Config:**
  ```yaml
  auth:
    saml:
      enabled: true
      idp_metadata_url: "https://login.microsoftonline.com/{tenant}/federationmetadata/saml"
      entity_id: "urn:agentoven:prod"
      acs_url: "https://api.agentoven.dev/auth/saml/callback"
      certificate: "/etc/agentoven/saml.crt"
      private_key: "/etc/agentoven/saml.key"
  ```
- **Flow:**
  1. Unauthenticated browser request → redirect to IdP
  2. User authenticates at IdP
  3. IdP POSTs SAML assertion to ACS URL
  4. SAML provider validates assertion, creates session
  5. Subsequent requests use session cookie → `Identity`
- **Library:** `github.com/crewjam/saml`
- **Session store:** Redis or encrypted cookie
- **Tier:** Pro

### 3d. LDAP / Active Directory (Enterprise)

```
                    ┌──────────────┐
Authorization:      │  LDAP        │
Basic <b64>     ──► │  Provider    │ ──► Identity{Subject: dn, Email: mail, Groups: memberOf}
                    │  (Enterprise)│
                    └──────────────┘
                          │
                          ▼
                    ┌──────────────┐
                    │ LDAP Server  │
                    │ (bind+search)│
                    └──────────────┘
```

- **Supported:** Active Directory, OpenLDAP, FreeIPA
- **Config:**
  ```yaml
  auth:
    ldap:
      enabled: true
      url: "ldaps://ldap.corp.example.com:636"
      bind_dn: "cn=agentoven,ou=service,dc=corp,dc=example,dc=com"
      bind_password_env: "LDAP_BIND_PASSWORD"
      user_search_base: "ou=users,dc=corp,dc=example,dc=com"
      user_search_filter: "(sAMAccountName={username})"
      group_search_base: "ou=groups,dc=corp,dc=example,dc=com"
      group_search_filter: "(member={dn})"
      group_role_mapping:
        "CN=AgentOven-Admin,OU=Groups,...": "admin"
        "CN=Developers,OU=Groups,...": "baker"
  ```
- **Flow:**
  1. Client sends `Authorization: Basic <base64(user:pass)>`
  2. LDAP provider binds to LDAP server with service account
  3. Searches for user DN, then binds as user to verify password
  4. Searches for group memberships → `Identity`
- **Library:** `github.com/go-ldap/ldap/v3`
- **Tier:** Enterprise

### 3e. mTLS / Client Certificate (Enterprise)

```
                    ┌──────────────┐
TLS handshake       │  mTLS        │
client cert     ──► │  Provider    │ ──► Identity{Subject: CN, Email: SAN}
                    │  (Enterprise)│
                    └──────────────┘
```

- **Use case:** Service-to-service auth, IoT devices, PicoClaw instances
- **Config:**
  ```yaml
  auth:
    mtls:
      enabled: true
      ca_cert: "/etc/agentoven/ca.crt"
      # Map certificate CN/OU to roles
      cn_role_mapping:
        "picoclaw-*": "baker"
        "ci-pipeline": "chef"
  ```
- **Flow:**
  1. TLS handshake validates client certificate against CA
  2. mTLS provider extracts CN, SANs, OU from verified cert
  3. Maps CN/OU to role via `cn_role_mapping` → `Identity`
- **Tier:** Enterprise

### 3f. Service Account Token (OSS)

```
                    ┌──────────────┐
X-Service-Token:    │  Service     │
<signed-token>  ──► │  Account     │ ──► Identity{Subject: svc_name, Provider: "service"}
                    │  Provider    │
                    └──────────────┘
```

- **Use case:** Agent-to-agent calls, CI/CD pipelines, internal services
- **Config:** Kitchen-scoped service accounts managed via API
- **Flow:** HMAC-signed tokens with kitchen + role + expiry
- **Tier:** OSS

---

## 4. Provider Chain Architecture

Multiple providers can be enabled simultaneously. The auth middleware walks them
in priority order until one returns an `Identity`:

```
Request ──► [ mTLS ] ──► [ OIDC/JWT ] ──► [ SAML/Session ] ──► [ LDAP ] ──► [ API Key ] ──► [ Anonymous ]
              │              │                  │                  │              │               │
              ▼              ▼                  ▼                  ▼              ▼               ▼
         Identity?      Identity?          Identity?          Identity?      Identity?       nil, nil
```

```go
// internal/auth/chain.go (OSS)

type ProviderChain struct {
    providers []contracts.AuthProvider
}

func (c *ProviderChain) Authenticate(ctx context.Context, r *http.Request) (*contracts.Identity, error) {
    for _, p := range c.providers {
        if !p.Enabled() {
            continue
        }
        identity, err := p.Authenticate(ctx, r)
        if err != nil {
            return nil, err // auth attempted but failed — reject
        }
        if identity != nil {
            return identity, nil // authenticated!
        }
        // (nil, nil) = this provider doesn't handle this request, try next
    }
    return nil, nil // no provider matched — anonymous
}
```

---

## 5. Middleware Pipeline (OSS)

The auth middleware is defined in `pkg/contracts` (OSS) so Pro can override it:

```go
// pkg/contracts/auth.go

// AuthMiddleware is the HTTP middleware interface for pluggable authentication.
type AuthMiddleware interface {
    // Middleware returns an http.Handler that authenticates requests.
    Middleware(next http.Handler) http.Handler
}
```

**OSS implementation** (in `internal/api/middleware/auth.go`):

```go
func NewAuthMiddleware(chain contracts.AuthProviderChain, requireAuth bool) AuthMiddleware {
    return &authMiddleware{chain: chain, requireAuth: requireAuth}
}

func (am *authMiddleware) Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Skip auth for public endpoints
        if isPublicPath(r.URL.Path) {
            next.ServeHTTP(w, r)
            return
        }

        identity, err := am.chain.Authenticate(r.Context(), r)
        if err != nil {
            http.Error(w, `{"error":"authentication_failed"}`, 401)
            return
        }
        if identity == nil && am.requireAuth {
            http.Error(w, `{"error":"authentication_required"}`, 401)
            return
        }

        // Store identity in context for RBAC and handlers
        ctx := SetIdentity(r.Context(), identity)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

**Pro overrides** the chain with additional providers (OIDC, SAML, LDAP, mTLS).

---

## 6. The Complete Flow

```
                        ┌─────────────────────────────────────────┐
                        │              HTTP Request                 │
                        └──────────────┬──────────────────────────┘
                                       │
                        ┌──────────────▼──────────────────────────┐
                        │          Auth Middleware (OSS)            │
                        │     walks AuthProviderChain              │
                        │                                          │
                        │  mTLS → OIDC → SAML → LDAP → APIKey     │
                        │            ↓                             │
                        │     Identity{Sub, Email, Role, Groups}   │
                        └──────────────┬──────────────────────────┘
                                       │
                        ┌──────────────▼──────────────────────────┐
                        │      Role Mapping Middleware (Pro)        │
                        │                                          │
                        │  IdP groups → AgentOven Role             │
                        │  "Developers" → "baker"                  │
                        │  "Platform-Team" → "admin"               │
                        └──────────────┬──────────────────────────┘
                                       │
                        ┌──────────────▼──────────────────────────┐
                        │       RBAC Middleware (Pro)               │
                        │                                          │
                        │  HasPermission(role, "agent:bake") ?     │
                        │  Yes → proceed                           │
                        │  No  → 403 Forbidden                    │
                        └──────────────┬──────────────────────────┘
                                       │
                        ┌──────────────▼──────────────────────────┐
                        │       Audit Middleware (Pro)              │
                        │                                          │
                        │  Log: who, what, when, where             │
                        └──────────────┬──────────────────────────┘
                                       │
                        ┌──────────────▼──────────────────────────┐
                        │          Handler                         │
                        └──────────────────────────────────────────┘
```

---

## 7. Tier Matrix

| Provider | OSS (Free) | Pro | Enterprise |
|----------|-----------|-----|------------|
| **API Key** | ✅ | ✅ | ✅ |
| **Service Account Token** | ✅ | ✅ | ✅ |
| **OIDC / OAuth 2.0** | ❌ | ✅ | ✅ |
| **SAML 2.0** | ❌ | ✅ | ✅ |
| **LDAP / Active Directory** | ❌ | ❌ | ✅ |
| **mTLS / Client Certificate** | ❌ | ❌ | ✅ |
| **RBAC enforcement** | ❌ | ✅ | ✅ |
| **Audit logging** | ❌ | ✅ | ✅ |
| **Group → Role mapping** | ❌ | ✅ | ✅ |
| **Multi-IdP (chain multiple)** | ❌ | ❌ | ✅ |

---

## 8. Configuration Format

All auth config lives in a single YAML block (or env vars):

```yaml
# agentoven.yaml
auth:
  require_auth: false          # OSS default: false (open access)
  default_role: "baker"        # Role for authenticated users without mapping

  apikey:
    enabled: true              # OSS: set AGENTOVEN_API_KEYS env var
    default_role: "baker"

  service_account:
    enabled: true
    hmac_secret_env: "AGENTOVEN_SA_SECRET"

  oidc:                        # Pro
    enabled: true
    issuer_url: "https://login.microsoftonline.com/{tenant}/v2.0"
    client_id: "..."
    audience: "api://agentoven"
    group_claim: "groups"      # JWT claim containing group list
    group_role_mapping:
      "550e8400-...": "admin"  # Azure AD group ID → role
      "6ba7b810-...": "baker"

  saml:                        # Pro
    enabled: false
    idp_metadata_url: "..."
    entity_id: "urn:agentoven"
    acs_url: "https://api.agentoven.dev/auth/saml/callback"
    session_ttl: "8h"

  ldap:                        # Enterprise
    enabled: false
    url: "ldaps://ldap.corp.example.com:636"
    bind_dn: "..."
    user_search_base: "ou=users,dc=corp"
    user_search_filter: "(sAMAccountName={username})"
    group_role_mapping:
      "CN=Admins,...": "admin"

  mtls:                        # Enterprise
    enabled: false
    ca_cert: "/etc/agentoven/ca.crt"
    cn_role_mapping:
      "picoclaw-*": "baker"
```

---

## 9. Implementation Plan

### Phase 1: Foundation (R7 scope)

**Goal:** Get the `AuthProvider` interface + chain into OSS, wrap existing API key auth.

| # | Task | Repo | Effort |
|---|------|------|--------|
| 1 | Define `Identity`, `AuthProvider`, `AuthProviderChain` in `pkg/contracts/auth.go` | OSS | 1 day |
| 2 | Implement `ProviderChain` in `internal/auth/chain.go` | OSS | 1 day |
| 3 | Wrap existing `APIKeyAuth` as `AuthProvider` | OSS | 0.5 day |
| 4 | Create `AuthMiddleware` in `internal/api/middleware/auth.go` | OSS | 1 day |
| 5 | Add `SetIdentity()` / `GetIdentity()` to `pkg/middleware/` | OSS | 0.5 day |
| 6 | Wire `AuthMiddleware` into `router.go` (replace current `apiKeyAuth.Middleware`) | OSS | 0.5 day |
| 7 | Implement `ServiceAccountProvider` (HMAC-signed tokens) | OSS | 1 day |
| 8 | Wire Pro `getUserFromContext()` to read `Identity` from context | Pro | 0.5 day |
| 9 | Wire Pro `RBACMiddleware` into router after `AuthMiddleware` | Pro | 0.5 day |
| **Total** | | | **~6 days** |

### Phase 2: Enterprise IdPs (R7-R8 scope)

| # | Task | Repo | Effort |
|---|------|------|--------|
| 10 | Implement `OIDCProvider` (JWT validation, JWKS, group claim extraction) | Pro | 2 days |
| 11 | Implement `SAMLProvider` (crewjam/saml, session management, ACS callback) | Pro | 3 days |
| 12 | Add session store interface (Redis driver + encrypted cookie fallback) | Pro | 1.5 days |
| 13 | Add `/auth/saml/login`, `/auth/saml/callback`, `/auth/saml/metadata` routes | Pro | 1 day |
| 14 | Group → Role mapping engine (config-driven, regex support) | Pro | 1 day |
| 15 | Add OIDC/SAML config to kitchen settings (per-kitchen IdP config) | Pro | 1 day |
| **Total** | | | **~9.5 days** |

### Phase 3: Enterprise+ (R8+ scope)

| # | Task | Repo | Effort |
|---|------|------|--------|
| 16 | Implement `LDAPProvider` (bind, search, group resolution) | Pro | 2 days |
| 17 | Implement `mTLSProvider` (cert extraction, CN/SAN mapping) | Pro | 1.5 days |
| 18 | Multi-IdP chain configuration (per-kitchen provider selection) | Pro | 1 day |
| 19 | Token exchange / token refresh for long-running agent tasks | Pro | 2 days |
| 20 | Dashboard: login page, session management UI, role viewer | OSS | 3 days |
| **Total** | | | **~9.5 days** |

---

## 10. Where Code Lives

| Component | File | Repo |
|-----------|------|------|
| `Identity` struct | `pkg/contracts/auth.go` | OSS |
| `AuthProvider` interface | `pkg/contracts/auth.go` | OSS |
| `AuthProviderChain` interface | `pkg/contracts/auth.go` | OSS |
| `ProviderChain` (implementation) | `internal/auth/chain.go` | OSS |
| `APIKeyProvider` | `internal/auth/apikey_provider.go` | OSS |
| `ServiceAccountProvider` | `internal/auth/service_account.go` | OSS |
| `AuthMiddleware` | `internal/api/middleware/auth.go` | OSS |
| `SetIdentity` / `GetIdentity` | `pkg/middleware/identity.go` | OSS |
| `OIDCProvider` | `internal/auth/oidc.go` | Pro |
| `SAMLProvider` | `internal/auth/saml.go` | Pro |
| `LDAPProvider` | `internal/auth/ldap.go` | Pro |
| `mTLSProvider` | `internal/auth/mtls.go` | Pro |
| `GroupRoleMapper` | `internal/auth/rolemapper.go` | Pro |
| `SessionStore` interface | `internal/auth/session.go` | Pro |
| `RedisSessionStore` | `internal/auth/session_redis.go` | Pro |
| `CookieSessionStore` | `internal/auth/session_cookie.go` | Pro |

---

## 11. Key Design Decisions

### D1: Authn is pluggable, Authz is fixed

Authentication providers are interchangeable. Authorization (RBAC) is always
AgentOven's built-in role/permission system. IdP groups are **mapped** to
AgentOven roles, not used directly.

### D2: Chain pattern, not if/else

Multiple providers can be active simultaneously. The chain pattern means
an API key user and an OIDC user can both call the same endpoint.

### D3: (nil, nil) means "not my request"

A provider returning `(nil, nil)` means it doesn't recognize the credentials
in this request. The chain moves to the next provider. `(nil, error)` means
"I tried to authenticate this but it failed" — reject immediately.

### D4: Identity is the contract boundary

OSS defines `Identity`. Pro populates it. Handlers only see `Identity`.
No handler ever knows whether the user came from SAML, OIDC, or an API key.

### D5: Per-kitchen IdP configuration (Enterprise)

Enterprise customers with multiple teams can configure different IdPs per
kitchen. Kitchen "team-alpha" uses Okta; kitchen "team-beta" uses Azure AD.

### D6: Session management is a Pro concern

OSS is stateless (API keys, service tokens). Pro adds session state for
browser-based flows (SAML, OIDC code flow). Session store is pluggable
(Redis or encrypted cookie).

---

## 12. Migration Path from Current State

1. **API key users** — zero changes. `APIKeyProvider` wraps existing behavior.
2. **Pro SSO users** — replace placeholder `SSOMiddleware` with `SAMLProvider` + `OIDCProvider`.
3. **Pro RBAC** — `getUserFromContext()` reads `Identity` from context instead of returning `nil`.
4. **Existing handlers** — no changes needed. They don't touch auth directly.
5. **PicoClaw/IoT** — uses mTLS or service account tokens for device auth.
