package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
)

// APIKeyProvider wraps the existing API key validation as an AuthProvider.
// It validates keys from the Authorization: Bearer <key> or X-API-Key headers.
//
// Config: AGENTOVEN_API_KEYS env var (comma-separated list).
// Default role: AGENTOVEN_API_KEY_ROLE env var (default: "baker").
type APIKeyProvider struct {
	mu          sync.RWMutex
	keys        map[string]bool
	enabled     bool
	defaultRole string
}

// NewAPIKeyProvider creates an API key auth provider from environment config.
func NewAPIKeyProvider() *APIKeyProvider {
	p := &APIKeyProvider{
		keys:        make(map[string]bool),
		defaultRole: "baker",
	}

	if role := os.Getenv("AGENTOVEN_API_KEY_ROLE"); role != "" {
		p.defaultRole = role
	}

	keysEnv := os.Getenv("AGENTOVEN_API_KEYS")
	if keysEnv == "" {
		p.enabled = false
		return p
	}

	for _, key := range strings.Split(keysEnv, ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			p.keys[key] = true
			p.enabled = true
		}
	}

	return p
}

func (p *APIKeyProvider) Name() string { return "apikey" }

func (p *APIKeyProvider) Enabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.enabled
}

// Authenticate validates the API key and returns an Identity.
// Returns (nil, nil) if no API key is present (let next provider try).
// Returns (nil, error) if an API key is present but invalid.
func (p *APIKeyProvider) Authenticate(_ context.Context, r *http.Request) (*contracts.Identity, error) {
	// Extract key from request
	apiKey := extractAPIKeyFromRequest(r)
	if apiKey == "" {
		// No API key in request — not our concern, let next provider try
		return nil, nil
	}

	// Validate the key
	if !p.validateKey(apiKey) {
		return nil, fmt.Errorf("invalid API key")
	}

	// Build identity from the validated key
	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(apiKey)))

	return &contracts.Identity{
		Subject:     "apikey:" + keyHash[:16],
		Provider:    "apikey",
		Role:        p.defaultRole,
		DisplayName: "API Key User",
		ExpiresAt:   time.Now().Add(24 * time.Hour), // API keys don't expire per-request
	}, nil
}

func (p *APIKeyProvider) validateKey(candidate string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for key := range p.keys {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

// AddKey adds a new API key at runtime.
func (p *APIKeyProvider) AddKey(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys[key] = true
	p.enabled = true
}

// RemoveKey removes an API key at runtime.
func (p *APIKeyProvider) RemoveKey(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.keys, key)
	if len(p.keys) == 0 {
		p.enabled = false
	}
}

func extractAPIKeyFromRequest(r *http.Request) string {
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
