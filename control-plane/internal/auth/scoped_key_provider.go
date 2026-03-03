package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
)

// ScopedKeyProvider authenticates requests using scoped API keys.
// These keys grant access to specific agents only, with usage quotas
// and traceability. Used by the Agent Viewer for consumer access.
//
// Header: X-Agent-Key: <key>
// Query param: agent_key=<key> (for SSE/WebSocket connections)
//
// Returns Identity with Role="viewer", Provider="scoped-key",
// Kitchen from the key, and the allowed agent names in Claims["agents"].
type ScopedKeyProvider struct {
	store store.ScopedKeyStore
}

// NewScopedKeyProvider creates a scoped key auth provider backed by the store.
func NewScopedKeyProvider(s store.ScopedKeyStore) *ScopedKeyProvider {
	return &ScopedKeyProvider{store: s}
}

func (p *ScopedKeyProvider) Name() string { return "scoped-key" }

func (p *ScopedKeyProvider) Enabled() bool { return true }

// Authenticate validates the scoped API key and returns an Identity.
// Returns (nil, nil) if no scoped key is present (let next provider try).
// Returns (nil, error) if a key is present but invalid/revoked/expired/over-quota.
func (p *ScopedKeyProvider) Authenticate(ctx context.Context, r *http.Request) (*contracts.Identity, error) {
	// Extract scoped key from request
	key := r.Header.Get("X-Agent-Key")
	if key == "" {
		key = r.URL.Query().Get("agent_key")
	}
	if key == "" {
		// No scoped key in request — not our concern, let next provider try
		return nil, nil
	}

	// Hash the key to look up in store
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))

	scopedKey, err := p.store.GetScopedKeyByHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("invalid agent key")
	}

	// Validate key state
	if scopedKey.Revoked {
		return nil, fmt.Errorf("agent key has been revoked")
	}
	if scopedKey.IsExpired() {
		return nil, fmt.Errorf("agent key has expired")
	}
	if scopedKey.IsQuotaExceeded() {
		return nil, fmt.Errorf("agent key quota exceeded (%d/%d calls)", scopedKey.CallCount, scopedKey.MaxCalls)
	}

	// Increment usage count
	if err := p.store.IncrementScopedKeyUsage(ctx, scopedKey.ID); err != nil {
		// Log but don't fail auth — usage tracking is best-effort
		fmt.Printf("WARN: failed to increment scoped key usage: %v\n", err)
	}

	// Build identity — role is always "viewer" for scoped keys
	identity := &contracts.Identity{
		Subject:     "scopedkey:" + scopedKey.ID,
		Provider:    "scoped-key",
		Kitchen:     scopedKey.Kitchen,
		Role:        "viewer",
		DisplayName: scopedKey.Label,
		ExpiresAt:   time.Now().Add(1 * time.Hour), // session-level expiry
		Claims: map[string]string{
			"key_id":  scopedKey.ID,
			"label":   scopedKey.Label,
			"kitchen": scopedKey.Kitchen,
		},
	}

	return identity, nil
}
