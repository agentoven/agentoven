package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
)

// ServiceAccountProvider validates HMAC-signed service account tokens.
// Used for agent-to-agent calls, CI/CD pipelines, and internal services.
//
// Token format: base64(JSON payload) + "." + base64(HMAC-SHA256 signature)
// Payload: {"sub": "ci-pipeline", "kitchen": "default", "role": "baker", "exp": 1234567890}
//
// Config: AGENTOVEN_SA_SECRET env var (HMAC secret key).
type ServiceAccountProvider struct {
	secret  []byte
	enabled bool
}

// serviceAccountPayload is the JWT-like payload for service account tokens.
type serviceAccountPayload struct {
	Subject string `json:"sub"`
	Kitchen string `json:"kitchen,omitempty"`
	Role    string `json:"role"`
	Exp     int64  `json:"exp"` // Unix timestamp
}

// NewServiceAccountProvider creates a service account provider from environment config.
func NewServiceAccountProvider() *ServiceAccountProvider {
	secret := os.Getenv("AGENTOVEN_SA_SECRET")
	if secret == "" {
		return &ServiceAccountProvider{enabled: false}
	}
	return &ServiceAccountProvider{
		secret:  []byte(secret),
		enabled: true,
	}
}

func (p *ServiceAccountProvider) Name() string { return "service_account" }
func (p *ServiceAccountProvider) Enabled() bool { return p.enabled }

// Authenticate validates the service account token from X-Service-Token header.
// Returns (nil, nil) if no service token is present.
// Returns (nil, error) if the token is present but invalid.
func (p *ServiceAccountProvider) Authenticate(_ context.Context, r *http.Request) (*contracts.Identity, error) {
	token := r.Header.Get("X-Service-Token")
	if token == "" {
		return nil, nil // not our concern
	}

	payload, err := p.validateToken(token)
	if err != nil {
		return nil, fmt.Errorf("invalid service account token: %w", err)
	}

	return &contracts.Identity{
		Subject:     "svc:" + payload.Subject,
		Provider:    "service_account",
		Kitchen:     payload.Kitchen,
		Role:        payload.Role,
		DisplayName: payload.Subject,
		ExpiresAt:   time.Unix(payload.Exp, 0),
	}, nil
}

func (p *ServiceAccountProvider) validateToken(token string) (*serviceAccountPayload, error) {
	// Split token into payload.signature
	parts := splitToken(token)
	if len(parts) != 2 {
		return nil, fmt.Errorf("malformed token: expected payload.signature")
	}

	payloadB64, sigB64 := parts[0], parts[1]

	// Verify HMAC signature
	mac := hmac.New(sha256.New, p.secret)
	mac.Write([]byte(payloadB64))
	expectedSig := mac.Sum(nil)

	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, fmt.Errorf("invalid signature encoding: %w", err)
	}

	if !hmac.Equal(sig, expectedSig) {
		return nil, fmt.Errorf("signature mismatch")
	}

	// Decode payload
	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, fmt.Errorf("invalid payload encoding: %w", err)
	}

	var payload serviceAccountPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("invalid payload JSON: %w", err)
	}

	// Check expiry
	if payload.Exp > 0 && time.Now().Unix() > payload.Exp {
		return nil, fmt.Errorf("token expired")
	}

	// Validate required fields
	if payload.Subject == "" {
		return nil, fmt.Errorf("missing subject")
	}
	if payload.Role == "" {
		payload.Role = "baker" // default role
	}

	return &payload, nil
}

func splitToken(token string) []string {
	for i := len(token) - 1; i >= 0; i-- {
		if token[i] == '.' {
			return []string{token[:i], token[i+1:]}
		}
	}
	return []string{token}
}

// GenerateToken creates a signed service account token.
// This is a helper for CLI tools and tests — not called by the server.
func GenerateToken(secret []byte, subject, kitchen, role string, ttl time.Duration) (string, error) {
	payload := serviceAccountPayload{
		Subject: subject,
		Kitchen: kitchen,
		Role:    role,
		Exp:     time.Now().Add(ttl).Unix(),
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payloadB64))
	sig := mac.Sum(nil)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	return payloadB64 + "." + sigB64, nil
}
