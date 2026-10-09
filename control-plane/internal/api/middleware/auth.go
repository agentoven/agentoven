package middleware

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/agentoven/agentoven/control-plane/pkg/a2aauth"
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
		if isAuthPublic(r) {
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

		// The workflow engine and the control plane (relaying to a pod) are not users, but
		// they are who calls the A2A routes in-process, so they are recognised there, and
		// only there, by a secret a user does not have.
		if identity == nil {
			identity = trustedA2ACaller(r)
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

			// If the identity carries a kitchen scope, override the tenant.
			// But if the request explicitly set X-Kitchen to a different value,
			// reject with 403 — prevents silent 404 when token kitchen mismatches.
			if identity.Kitchen != "" {
				requestedKitchen := pkgmw.GetKitchen(ctx)
				if requestedKitchen != "" && requestedKitchen != "default" && requestedKitchen != identity.Kitchen {
					log.Warn().
						Str("provider", identity.Provider).
						Str("subject", identity.Subject).
						Str("token_kitchen", identity.Kitchen).
						Str("requested_kitchen", requestedKitchen).
						Msg("Kitchen mismatch: identity kitchen differs from requested kitchen")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					json.NewEncoder(w).Encode(map[string]string{
						"error":   "kitchen_mismatch",
						"message": "Identity is scoped to kitchen '" + identity.Kitchen + "' but request targets kitchen '" + requestedKitchen + "'. Generate a token for the correct kitchen.",
					})
					return
				}
				ctx = pkgmw.SetKitchen(ctx, identity.Kitchen)
			}
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireIdentity is a route-level middleware that unconditionally rejects any
// request that did not produce an authenticated Identity (nil identity = 401).
// Apply this to specific routes that must always be protected regardless of the
// global AGENTOVEN_REQUIRE_AUTH flag — e.g. /invoke, /invoke/stream.
func RequireIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pkgmw.GetIdentity(r.Context()) == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", `Bearer realm="agentoven"`)
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "authentication_required",
				"message": "Invoking an agent requires authentication. Provide Authorization: Bearer <key>, X-API-Key, or X-Service-Token.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isAuthPublic reports whether a request may skip authentication.
//
// The A2A gateway's public surface is its discovery documents, and only for GET: they
// advertise what an agent can do and are how another agent finds it. Running a task is not
// discovery, so `POST /a2a` is authenticated like every other way of running an agent.
func isAuthPublic(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		switch r.URL.Path {
		case "/.well-known/agent.json", "/a2a/.well-known/agent-card.json":
			return true
		}
	}
	return isAuthPublicPath(r.URL.Path)
}

// isA2AExecutionPath reports whether a path runs an agent over A2A: the gateway root, or a
// per-agent or per-environment endpoint, as opposed to a discovery document under it.
func isA2AExecutionPath(path string) bool {
	if strings.HasSuffix(path, "/.well-known/agent-card.json") || strings.HasSuffix(path, "/.well-known/agent.json") {
		return false
	}
	return path == "/a2a" || strings.HasSuffix(path, "/a2a") || strings.HasSuffix(path, "/a2a/")
}

// trustedA2ACaller recognises the callers that are neither users nor API keys: the workflow
// engine, which holds a secret that never leaves this process, and the control plane, whose
// token for this one agent was handed to this pod when it started. It returns nil for
// everything else, and for every route that is not A2A execution.
func trustedA2ACaller(r *http.Request) *contracts.Identity {
	if r.Method != http.MethodPost || !isA2AExecutionPath(r.URL.Path) {
		return nil
	}
	if a2aauth.Equal(r.Header.Get(a2aauth.InternalHeader), a2aauth.InternalKey()) {
		return &contracts.Identity{Subject: "internal:workflow", Provider: "internal", Kind: "service_account", Role: "baker"}
	}
	podToken := os.Getenv(a2aauth.PodTokenEnv)
	if a2aauth.Equal(r.Header.Get(a2aauth.PodTokenHeader), podToken) {
		return &contracts.Identity{Subject: "internal:control-plane", Provider: "internal", Kind: "service_account", Role: "baker"}
	}
	// An agent pod that was started without a token (one launched by an operator that does
	// not yet hand one out) cannot tell the control plane from anyone else, and refusing every
	// call would stop it working. It keeps accepting them, loudly. This is only ever true in a
	// pod: the control plane has no AGENT_NAME.
	if podToken == "" && os.Getenv("AGENT_NAME") != "" {
		legacyPodWarning.Do(func() {
			log.Warn().Str("agent", os.Getenv("AGENT_NAME")).
				Msg("⚠️  This agent pod has no " + a2aauth.PodTokenEnv + ": it accepts unauthenticated A2A calls from anything that can reach it. Start it with a token (see ADR-0030).")
		})
		return &contracts.Identity{Subject: "internal:unauthenticated-pod", Provider: "internal", Kind: "service_account", Role: "baker"}
	}
	return nil
}

var legacyPodWarning sync.Once

// IsInternalCaller reports whether a request came from a caller recognised by
// trustedA2ACaller. Settings only the platform itself may choose, such as a TLS override
// for a provider connection, are honoured for these callers and no one else.
func IsInternalCaller(r *http.Request) bool {
	id := pkgmw.GetIdentity(r.Context())
	return id != nil && id.Provider == "internal"
}

// isAuthPublicPath returns true for paths that should skip authentication.
func isAuthPublicPath(path string) bool {
	publicPaths := []string{
		"/health",
		"/healthz",
		"/readyz",
		"/version",
	}
	for _, p := range publicPaths {
		if path == p {
			return true
		}
	}
	// Auth login endpoints must be publicly accessible (SSO callbacks too)
	if path == "/auth/login" || strings.HasPrefix(path, "/auth/saml") || strings.HasPrefix(path, "/auth/oidc") {
		return true
	}
	// MCP protocol endpoint (auth handled by MCP layer)
	if strings.HasPrefix(path, "/mcp") {
		return true
	}
	return false
}
