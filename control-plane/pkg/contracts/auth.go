// Package contracts — Authentication interfaces for the pluggable auth layer.
//
// These types form the boundary between OSS and Pro authentication.
// OSS ships API key + service account providers.
// Pro ships OIDC, SAML, LDAP, mTLS providers.
//
// See AUTH-PLAN.md for the full architecture.
package contracts

import (
	"context"
	"net/http"
	"time"
)

// ── Identity ────────────────────────────────────────────────

// Identity represents an authenticated user or service.
// Produced by an AuthProvider, consumed by RBAC middleware and handlers.
//
// This is the contract boundary between authn (pluggable) and authz (fixed).
// No handler ever knows whether the user came from SAML, OIDC, or an API key.
type Identity struct {
	// Subject is the unique identifier (user ID, service account name, API key hash).
	Subject string `json:"subject"`

	// Email is the user's email address (may be empty for service accounts).
	Email string `json:"email,omitempty"`

	// DisplayName is a human-readable name.
	DisplayName string `json:"display_name,omitempty"`

	// Provider identifies which auth provider authenticated this identity.
	// Values: "apikey", "service_account", "oidc", "saml", "ldap", "mtls"
	Provider string `json:"provider"`

	// Kitchen is the tenant scope extracted from token claims or mapping.
	// Empty means "use the default kitchen from the request header".
	Kitchen string `json:"kitchen,omitempty"`

	// Role is the AgentOven role (mapped from IdP groups or configured default).
	// Values: "admin", "chef", "baker", "auditor", "finance", "viewer"
	Role string `json:"role"`

	// Groups contains IdP group memberships (for group→role mapping in Pro).
	Groups []string `json:"groups,omitempty"`

	// Claims holds raw claims from the token (for custom policies in Enterprise).
	Claims map[string]string `json:"claims,omitempty"`

	// ExpiresAt is when this identity's session expires.
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// ── AuthProvider ────────────────────────────────────────────

// AuthProvider authenticates an HTTP request and returns an Identity.
// Each provider implements one authentication strategy (API key, OIDC, SAML, etc.).
//
// The chain pattern:
//   - Return (*Identity, nil) → authenticated, stop chain
//   - Return (nil, nil) → this provider doesn't handle this request, try next
//   - Return (nil, error) → authentication was attempted but failed, reject
type AuthProvider interface {
	// Name returns the provider identifier (e.g. "apikey", "oidc", "saml", "ldap").
	Name() string

	// Authenticate inspects the request and returns an Identity.
	Authenticate(ctx context.Context, r *http.Request) (*Identity, error)

	// Enabled returns whether this provider is configured and active.
	Enabled() bool
}

// ── AuthProviderChain ───────────────────────────────────────

// AuthProviderChain tries providers in priority order until one returns an Identity.
// This is used by the auth middleware to support multiple concurrent auth strategies.
//
// Pro adds enterprise providers (OIDC, SAML, LDAP, mTLS) to the same chain,
// so API key users and SSO users can both call the same endpoints.
type AuthProviderChain interface {
	// Authenticate walks the chain of providers in order.
	// Returns the first successful Identity, or (nil, nil) if no provider matched.
	Authenticate(ctx context.Context, r *http.Request) (*Identity, error)

	// RegisterProvider adds a provider to the end of the chain.
	// Providers are tried in registration order.
	RegisterProvider(provider AuthProvider)
}
