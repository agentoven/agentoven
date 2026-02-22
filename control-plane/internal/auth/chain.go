// Package auth provides the authentication provider chain for the AgentOven control plane.
//
// OSS ships:
//   - APIKeyProvider — env-based API key validation
//   - ServiceAccountProvider — HMAC-signed service tokens
//
// Pro adds:
//   - OIDCProvider — JWT validation via JWKS
//   - SAMLProvider — SAML 2.0 assertion validation
//   - LDAPProvider — LDAP bind + search
//   - mTLSProvider — client certificate extraction
//
// See AUTH-PLAN.md for the full architecture.
package auth

import (
	"context"
	"net/http"
	"sync"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/rs/zerolog/log"
)

// ProviderChain implements contracts.AuthProviderChain.
// It walks registered providers in order until one returns an Identity.
//
// Thread-safe: providers can be registered at any time (Pro registers
// enterprise providers after the OSS server is built).
type ProviderChain struct {
	mu        sync.RWMutex
	providers []contracts.AuthProvider
}

// NewProviderChain creates an empty auth provider chain.
func NewProviderChain() *ProviderChain {
	return &ProviderChain{
		providers: make([]contracts.AuthProvider, 0),
	}
}

// RegisterProvider adds a provider to the end of the chain.
// Providers are tried in registration order.
func (c *ProviderChain) RegisterProvider(provider contracts.AuthProvider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.providers = append(c.providers, provider)
	log.Info().
		Str("provider", provider.Name()).
		Bool("enabled", provider.Enabled()).
		Msg("🔑 Auth provider registered")
}

// Authenticate walks the chain of providers in order.
//
// Contract:
//   - (*Identity, nil) → authenticated, stop walking
//   - (nil, nil) → this provider doesn't handle this request, try next
//   - (nil, error) → auth attempted but failed, reject immediately
func (c *ProviderChain) Authenticate(ctx context.Context, r *http.Request) (*contracts.Identity, error) {
	c.mu.RLock()
	providers := make([]contracts.AuthProvider, len(c.providers))
	copy(providers, c.providers)
	c.mu.RUnlock()

	for _, p := range providers {
		if !p.Enabled() {
			continue
		}
		identity, err := p.Authenticate(ctx, r)
		if err != nil {
			// Auth attempted but failed — reject immediately
			log.Debug().
				Str("provider", p.Name()).
				Err(err).
				Msg("Auth provider rejected request")
			return nil, err
		}
		if identity != nil {
			// Authenticated!
			log.Debug().
				Str("provider", p.Name()).
				Str("subject", identity.Subject).
				Str("role", identity.Role).
				Msg("Request authenticated")
			return identity, nil
		}
		// (nil, nil) — this provider doesn't handle this request, try next
	}

	// No provider matched — anonymous request
	return nil, nil
}

// ListProviders returns the names of all registered providers (for diagnostics).
func (c *ProviderChain) ListProviders() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	names := make([]string, len(c.providers))
	for i, p := range c.providers {
		names[i] = p.Name()
	}
	return names
}
