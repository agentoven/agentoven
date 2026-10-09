// Package a2aauth authenticates the callers of the A2A execution routes that are not users.
//
// Two callers need it. The control plane's own workflow engine calls the gateway over HTTP,
// and proves who it is with a secret that exists only in this process. The control plane
// also relays a call to an agent pod, and proves who it is with a token derived for that one
// agent; the pod holds the token, never the secret it came from.
//
// Neither is a user, so neither belongs in the identity chain, and neither is ever accepted
// anywhere but the A2A execution routes.
package a2aauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// InternalHeader carries the in-process secret on the workflow engine's calls.
	InternalHeader = "X-AgentOven-Internal"
	// PodTokenHeader carries the per-agent token on the control plane's calls to a pod.
	PodTokenHeader = "X-AgentOven-A2A-Token"
	// PodTokenEnv is the pod's copy of its own token.
	PodTokenEnv = "AGENT_A2A_TOKEN"
)

var (
	mu        sync.RWMutex
	shared    []byte
	randOnce  sync.Once
	randomKey string
)

// UseSecret sets the key the internal key is derived from. Replicas that share one secret
// (the production setup) then agree on it, so the workflow engine's call to its own base URL
// is recognised whichever replica a load balancer lands it on. Call it once at startup.
func UseSecret(secret []byte) {
	mu.Lock()
	shared = append([]byte(nil), secret...)
	mu.Unlock()
}

// InternalKey is what the workflow engine presents on its calls to the A2A gateway. With a
// shared secret it is derived from it; without one it is a random value created once per
// process, which is right for a single instance and, by design, wrong across replicas (they
// would not recognise each other), so a multi-replica deployment must configure a secret.
func InternalKey() string {
	mu.RLock()
	secret := shared
	mu.RUnlock()
	if len(secret) > 0 {
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte("agentoven-a2a-internal-v1"))
		return hex.EncodeToString(mac.Sum(nil))
	}
	randOnce.Do(func() {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			panic("a2aauth: no randomness: " + err.Error())
		}
		randomKey = hex.EncodeToString(b)
	})
	return randomKey
}

// Equal compares two secrets in constant time. An empty value never matches, so a missing
// header cannot equal a missing configuration.
func Equal(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Secret returns the key pod tokens are derived from. It is AGENTOVEN_A2A_SECRET, or else
// AGENTOVEN_SA_SECRET (the secret the control plane already signs pod tokens with); with
// neither it is a random key kept in dataDir, so it stays the same across restarts and a pod
// started before a restart still recognises the control plane.
func Secret(dataDir string) ([]byte, error) {
	for _, env := range []string{"AGENTOVEN_A2A_SECRET", "AGENTOVEN_SA_SECRET"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return []byte(v), nil
		}
	}
	if dataDir == "" {
		return nil, fmt.Errorf("no AGENTOVEN_A2A_SECRET or AGENTOVEN_SA_SECRET, and no data directory to keep one in")
	}
	path := filepath.Join(dataDir, "a2a.secret")
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	key := hex.EncodeToString(b)
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		return nil, err
	}
	return []byte(key), nil
}

// PodToken derives the token one agent's pod accepts from the control plane. It is bound to
// the kitchen and the agent, so a token for one agent opens no other.
func PodToken(secret []byte, kitchen, agent string) string {
	if len(secret) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("agentoven-a2a-pod-v1\x00" + kitchen + "\x00" + agent))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
