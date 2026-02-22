package middleware

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
)

// APIKeyAuth is middleware that validates API key authentication.
//
// When enabled (AGENTOVEN_API_KEYS is set), all requests to /api/v1/*
// must include a valid API key via:
//   - Authorization: Bearer <key>
//   - X-API-Key: <key>
//
// The following paths are always public:
//   - /health
//   - /version
//   - /.well-known/agent.json (A2A discovery)
//   - /a2a (A2A protocol endpoint)
//
// API keys are configured via the AGENTOVEN_API_KEYS environment variable
// as a comma-separated list: "key1,key2,key3"
//
// This is the OSS auth layer. The Pro repo overrides this with
// SSO/SAML/RBAC middleware for enterprise deployments.
type APIKeyAuth struct {
	mu      sync.RWMutex
	keys    map[string]bool
	enabled bool
}

// NewAPIKeyAuth creates an API key auth middleware from environment config.
func NewAPIKeyAuth() *APIKeyAuth {
	auth := &APIKeyAuth{
		keys: make(map[string]bool),
	}

	keysEnv := os.Getenv("AGENTOVEN_API_KEYS")
	if keysEnv == "" {
		// No API keys configured — auth disabled
		auth.enabled = false
		return auth
	}

	for _, key := range strings.Split(keysEnv, ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			auth.keys[key] = true
			auth.enabled = true
		}
	}

	return auth
}

// Enabled returns whether API key auth is active.
func (a *APIKeyAuth) Enabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.enabled
}

// AddKey adds a new API key at runtime.
func (a *APIKeyAuth) AddKey(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keys[key] = true
	a.enabled = true
}

// RemoveKey removes an API key at runtime.
func (a *APIKeyAuth) RemoveKey(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.keys, key)
	if len(a.keys) == 0 {
		a.enabled = false
	}
}

// Middleware returns an http.Handler middleware that enforces API key auth.
func (a *APIKeyAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always allow unauthenticated requests if auth is disabled
		if !a.Enabled() {
			next.ServeHTTP(w, r)
			return
		}

		// Public endpoints — no auth required
		path := r.URL.Path
		if isPublicPath(path) {
			next.ServeHTTP(w, r)
			return
		}

		// Extract API key from request
		apiKey := extractAPIKey(r)
		if apiKey == "" {
			respondUnauthorized(w, "API key required. Set Authorization: Bearer <key> or X-API-Key header.")
			return
		}

		// Validate the key (constant-time comparison)
		if !a.validateKey(apiKey) {
			respondUnauthorized(w, "Invalid API key.")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (a *APIKeyAuth) validateKey(candidate string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	for key := range a.keys {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

func extractAPIKey(r *http.Request) string {
	// Check Authorization: Bearer <key>
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}

	// Check X-API-Key header
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}

	// Check api_key query parameter (for SSE/WebSocket connections)
	if key := r.URL.Query().Get("api_key"); key != "" {
		return key
	}

	return ""
}

func isPublicPath(path string) bool {
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

	// A2A discovery and MCP endpoints may need special handling
	if strings.HasPrefix(path, "/a2a") {
		return true
	}

	return false
}

func respondUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="agentoven"`)
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   "unauthorized",
		"message": msg,
	})
}
