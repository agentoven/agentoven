package middleware

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/rs/zerolog/log"
)

// AuthMiddleware is the HTTP middleware that authenticates requests using
// the pluggable AuthProviderChain and stores the resulting Identity in context.
//
// This replaces the old APIKeyAuth middleware with a chain-based approach
// that supports multiple concurrent auth strategies (API key + OIDC + SAML + ...).
//
// See AUTH-PLAN.md for the full architecture.
type AuthMiddleware struct {
	chain       contracts.AuthProviderChain
	requireAuth bool
}

// NewAuthMiddleware creates the auth middleware.
//
// If requireAuth is true, unauthenticated requests to non-public paths are rejected.
// Config: AGENTOVEN_REQUIRE_AUTH env var (default: false for OSS).
func NewAuthMiddleware(chain contracts.AuthProviderChain) *AuthMiddleware {
	requireAuth := os.Getenv("AGENTOVEN_REQUIRE_AUTH") == "true"
	return &AuthMiddleware{
		chain:       chain,
		requireAuth: requireAuth,
	}
}

// Handler returns the HTTP handler middleware that authenticates requests.
func (am *AuthMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Public paths — skip auth
		if isAuthPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Walk the provider chain
		identity, err := am.chain.Authenticate(r.Context(), r)
		if err != nil {
			log.Debug().Err(err).Str("path", r.URL.Path).Msg("Authentication failed")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", `Bearer realm="agentoven"`)
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "authentication_failed",
				"message": err.Error(),
			})
			return
		}

		// No identity and auth is required → reject
		if identity == nil && am.requireAuth {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", `Bearer realm="agentoven"`)
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "authentication_required",
				"message": "This endpoint requires authentication. Set Authorization: Bearer <key>, X-API-Key, or X-Service-Token header.",
			})
			return
		}

		// Store identity in context (nil is fine — means anonymous)
		ctx := r.Context()
		if identity != nil {
			ctx = pkgmw.SetIdentity(ctx, identity)

			// If the identity carries a kitchen scope, override the tenant
			if identity.Kitchen != "" {
				ctx = pkgmw.SetKitchen(ctx, identity.Kitchen)
			}
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isAuthPublicPath returns true for paths that should skip authentication.
func isAuthPublicPath(path string) bool {
	publicPaths := []string{
		"/health",
		"/version",
		"/.well-known/agent.json",
	}
	for _, p := range publicPaths {
		if path == p {
			return true
		}
	}
	// A2A discovery endpoints
	if strings.HasPrefix(path, "/a2a") {
		return true
	}
	// MCP protocol endpoint (auth handled by MCP layer)
	if strings.HasPrefix(path, "/mcp") {
		return true
	}
	return false
}
