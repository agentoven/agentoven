# ADR-0002: Pluggable Authentication with Provider Chain

- **Status:** Accepted
- **Date:** 2026-02-25
- **Author(s):** Siddartha Kopparapu
- **Related:** [AUTH-PLAN.md](../AUTH-PLAN.md)

## Context

AgentOven needs to support multiple authentication strategies (API keys, OIDC, SAML, LDAP, mTLS, service account tokens) without coupling the core control plane to any specific identity provider. Enterprise customers need to bring their own IdP (Okta, Azure AD, Google Workspace, etc.).

The prior implementation had a single `APIKeyAuth` middleware hardcoded into the router. The Pro repo had placeholder SSO middleware that was a no-op (ISS-019).

## Decision

We implement a **pluggable `AuthProvider` interface** with a **chain-of-responsibility pattern**:

1. **`AuthProvider` interface** defined in `pkg/contracts/auth.go` (OSS):
   - `Authenticate(ctx, r) → (*Identity, error)` — `(identity, nil)` = success, `(nil, nil)` = skip, `(nil, err)` = reject
   - `Enabled() bool` — whether the provider is active

2. **`AuthProviderChain`** walks providers in priority order (mTLS → OIDC → SAML → LDAP → API Key → Service Account)

3. **`Identity` struct** is the contract boundary — handlers never know which provider authenticated the user

4. **Authentication (authn) is pluggable; authorization (authz) is fixed** — RBAC is always AgentOven's built-in role/permission system. IdP groups are *mapped* to AgentOven roles.

### OSS Ships:
- `APIKeyProvider` — wraps existing API key logic
- `ServiceAccountProvider` — HMAC-signed tokens for agent-to-agent calls

### Pro Adds:
- `OIDCProvider` (R8) — JWT validation via JWKS
- `SAMLProvider` (R8) — crewjam/saml with session management
- `LDAPProvider` (Enterprise) — bind + search + group resolution
- `mTLSProvider` (Enterprise) — certificate CN/SAN mapping

## Consequences

- **Easier:** Adding new identity providers requires implementing one interface. No router changes needed. Enterprise customers can mix providers (API keys + OIDC simultaneously).
- **Harder:** Chain ordering matters — misconfiguration can cause silent auth bypass. Session management (SAML) adds state to an otherwise stateless system.
- **Security:** The `(nil, nil)` skip pattern means a provider that doesn't recognize credentials silently defers. The chain must end with a clear reject-if-required policy.

## Alternatives Considered

1. **OAuth2 proxy (e.g., oauth2-proxy)** — Rejected because it doesn't support API key auth or service tokens, and adds operational complexity.
2. **Single configurable auth middleware** — Rejected because switch/case doesn't scale and prevents running multiple auth methods simultaneously.
3. **Embed OIDC library directly** — Rejected for OSS because it adds heavy dependencies; better as a Pro feature.
