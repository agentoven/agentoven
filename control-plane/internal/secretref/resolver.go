// Package secretref resolves external secret references at runtime.
//
// Supported URI formats:
//
//	k8s://namespace/secret-name/key  → Kubernetes Secret data field (requires K8sReader)
//	env://ENV_VAR_NAME               → Environment variable
//
// This allows provider API keys to be stored as references rather than
// plaintext values in the database. When keys rotate in the external
// secret store, AgentOven picks up the new value on next request —
// no restart or redeployment needed.
package secretref

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// K8sSecretReader reads a key from a Kubernetes Secret.
// This interface is implemented in the Pro repo using client-go.
// In OSS mode, it's nil and k8s:// refs return an error.
type K8sSecretReader interface {
	ReadSecret(ctx context.Context, namespace, name, key string) (string, error)
}

// Resolver resolves secret references to their actual values.
type Resolver struct {
	k8s   K8sSecretReader
	cache sync.Map // map[string]cacheEntry
	ttl   time.Duration
}

type cacheEntry struct {
	value     string
	expiresAt time.Time
}

// New creates a Resolver with env:// support only.
// Call SetK8sReader to enable k8s:// support.
func New() *Resolver {
	return &Resolver{
		ttl: 60 * time.Second,
	}
}

// SetK8sReader enables k8s:// secret resolution.
// Called by Pro server init with a real K8s client implementation.
func (r *Resolver) SetK8sReader(reader K8sSecretReader) {
	r.k8s = reader
	log.Info().Msg("secretref: K8s secret reader enabled")
}

// Resolve resolves a secret reference URI to its current value.
// Returns empty string if the ref is empty or resolution fails.
func (r *Resolver) Resolve(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}

	// Check cache first
	if cached, ok := r.cache.Load(ref); ok {
		entry := cached.(cacheEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.value, nil
		}
		r.cache.Delete(ref)
	}

	var value string
	var err error

	switch {
	case strings.HasPrefix(ref, "env://"):
		value, err = r.resolveEnv(ref)
	case strings.HasPrefix(ref, "k8s://"):
		value, err = r.resolveK8s(ctx, ref)
	default:
		return "", fmt.Errorf("unsupported secret ref scheme: %s", ref)
	}

	if err != nil {
		return "", err
	}

	// Cache the resolved value
	r.cache.Store(ref, cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(r.ttl),
	})

	return value, nil
}

// resolveEnv resolves env://VAR_NAME references.
func (r *Resolver) resolveEnv(ref string) (string, error) {
	varName := strings.TrimPrefix(ref, "env://")
	if varName == "" {
		return "", fmt.Errorf("empty env var name in ref: %s", ref)
	}

	value := os.Getenv(varName)
	if value == "" {
		return "", fmt.Errorf("env var %s is not set or empty", varName)
	}
	return value, nil
}

// resolveK8s resolves k8s://namespace/secret-name/key references.
func (r *Resolver) resolveK8s(ctx context.Context, ref string) (string, error) {
	if r.k8s == nil {
		return "", fmt.Errorf("K8s secret reader not configured (k8s:// refs require Pro with in-cluster deployment)")
	}

	path := strings.TrimPrefix(ref, "k8s://")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid k8s secret ref format, expected k8s://namespace/secret-name/key, got: %s", ref)
	}

	namespace, secretName, key := parts[0], parts[1], parts[2]
	return r.k8s.ReadSecret(ctx, namespace, secretName, key)
}

// InvalidateCache removes a specific ref from the cache, forcing re-resolution on next call.
func (r *Resolver) InvalidateCache(ref string) {
	r.cache.Delete(ref)
}

// InvalidateAll clears the entire cache.
func (r *Resolver) InvalidateAll() {
	r.cache = sync.Map{}
}
