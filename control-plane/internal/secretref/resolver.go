// Package secretref resolves external secret references at runtime.
//
// Supported URI formats:
//
//	k8s://namespace/secret-name/key  → Kubernetes Secret data field (requires K8sReader)
//	env://ENV_VAR_NAME               → Environment variable
//	vault://credential-name          → A named credential in the kitchen's vault (requires a Vault)
//
// This allows provider API keys to be stored as references rather than
// plaintext values in the database. When keys rotate in the external
// secret store, AgentOven picks up the new value on next request —
// no restart or redeployment needed.
package secretref

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
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

// Vault is a kitchen's store of named credentials. The community edition has none (a vault:// ref
// there is an error); Pro installs one backed by its encrypted credential vault. Every call is for
// one kitchen: a reference can only ever reach the credentials of the kitchen that holds it.
type Vault interface {
	// Read returns the current value of a credential.
	Read(ctx context.Context, kitchen, name string) (string, error)
	// Write creates the credential, or gives an existing one a new value.
	Write(ctx context.Context, kitchen, name, value, label string) error
	// Delete removes a credential. Deleting one that is not there is not an error.
	Delete(ctx context.Context, kitchen, name string) error
}

// VaultScheme is the prefix of a reference into the vault.
const VaultScheme = "vault://"

// VaultRef is the reference to a vault credential.
func VaultRef(name string) string { return VaultScheme + name }

// VaultName returns the credential a vault:// reference names.
func VaultName(ref string) (string, bool) {
	name, ok := strings.CutPrefix(ref, VaultScheme)
	return name, ok && name != ""
}

// ErrNotKitchenRef is returned for a reference a kitchen may not supply.
var ErrNotKitchenRef = errors.New("a secret reference must look like vault://<name>: env:// and k8s:// references name the installation's own secrets and are not available to a kitchen")

var kitchenCredName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

// KitchenVaultName returns the credential a reference supplied by a kitchen (a trigger, a tool pack, a
// channel, a bot, a judge: anything a Chef or Head Chef configures) names. A kitchen reaches its own
// vault and nothing else: env:// would read the control plane's process environment (its database URL,
// signing keys, every provider key) and k8s:// the cluster's secrets, and hand the value to a host the
// kitchen chose. The error never repeats the reference: someone who pasted a literal secret must not
// see it echoed into a response or a log.
func KitchenVaultName(ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, VaultScheme)
	if !ok || !kitchenCredName.MatchString(name) {
		return "", ErrNotKitchenRef
	}
	return name, nil
}

type kitchenKey struct{}

// WithKitchen says which kitchen a reference is being resolved for. A vault:// reference is looked up
// in that kitchen only.
func WithKitchen(ctx context.Context, kitchen string) context.Context {
	return context.WithValue(ctx, kitchenKey{}, kitchen)
}

func kitchenFrom(ctx context.Context) string {
	if k, ok := ctx.Value(kitchenKey{}).(string); ok && k != "" {
		return k
	}
	return "default"
}

// Resolver resolves secret references to their actual values.
type Resolver struct {
	k8s   K8sSecretReader
	vault Vault
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

// SetVault enables vault:// references and lets the provider handlers keep keys there. Called by
// Pro server init.
func (r *Resolver) SetVault(v Vault) {
	r.vault = v
	log.Info().Msg("secretref: vault enabled")
}

// Vault returns the installed vault, or nil in the community edition.
func (r *Resolver) Vault() Vault {
	if r == nil {
		return nil
	}
	return r.vault
}

// Resolve resolves a secret reference URI to its current value.
// Returns empty string if the ref is empty or resolution fails.
func (r *Resolver) Resolve(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}

	// A vault credential is read every time and never cached, here or in the tool-auth path: rotating
	// it takes effect on the very next call, and a deleted one stops working at once.
	if name, ok := VaultName(ref); ok {
		return r.resolveVault(ctx, name)
	}
	if strings.HasPrefix(ref, VaultScheme) {
		return "", fmt.Errorf("empty credential name in ref: %s", ref)
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

// resolveVault resolves vault://credential-name references in the kitchen on the context.
func (r *Resolver) resolveVault(ctx context.Context, name string) (string, error) {
	if r.vault == nil {
		return "", fmt.Errorf("vault:// refs need a credential vault (available in Pro); %q cannot be resolved", VaultScheme+name)
	}
	value, err := r.vault.Read(ctx, kitchenFrom(ctx), name)
	if err != nil {
		return "", fmt.Errorf("vault credential %q: %w", name, err)
	}
	if value == "" {
		return "", fmt.Errorf("vault credential %q is empty", name)
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

// ResolveFor resolves a reference a kitchen supplied, on behalf of that kitchen: only a vault:// reference
// is accepted and it reads that kitchen's credential, never the default kitchen's (see KitchenVaultName
// for why env:// and k8s:// are refused). Resolve, which takes any scheme, is for references an operator
// set (a platform provider's key); anything a person in a kitchen typed goes through here.
func (r *Resolver) ResolveFor(ctx context.Context, kitchen, ref string) (string, error) {
	if _, err := KitchenVaultName(ref); err != nil {
		return "", err
	}
	return r.Resolve(WithKitchen(ctx, kitchen), ref)
}
