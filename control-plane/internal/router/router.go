// Package router implements the AgentOven Model Router.
//
// The router selects the optimal model provider based on the configured strategy
// (fallback, cost-optimized, latency-optimized, round-robin), sends the request,
// tracks costs, and handles provider failover transparently.
package router

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/secretref"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/blobstore"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ctxKeySkipTrace is used by the executor to suppress router-level trace creation.
// When the executor calls Route(), it sets this key so the router doesn't create
// orphaned flat traces — the executor persists spans with full hierarchy instead.
type ctxKeySkipTrace struct{}

// ctxKeyProviderTLSOverride carries per-request TLS settings for a specific
// provider. This allows A2A invocations to override provider TLS trust
// settings without mutating persisted provider records.
type ctxKeyProviderTLSOverride struct{}

// ProviderTLSOverride carries optional TLS settings to apply to one provider.
// CABundle should be PEM text; TLSSkipVerify disables cert verification.
type ProviderTLSOverride struct {
	ProviderName  string
	CABundle      string
	TLSSkipVerify bool
}

// ContextSkipTrace returns a context that tells the router to skip trace creation.
// Used by the executor to prevent orphaned router traces when the executor
// already persists rich hierarchical spans.
func ContextSkipTrace(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeySkipTrace{}, true)
}

// WithProviderTLSOverride adds a per-request provider TLS override.
func WithProviderTLSOverride(ctx context.Context, ov ProviderTLSOverride) context.Context {
	if strings.TrimSpace(ov.ProviderName) == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyProviderTLSOverride{}, ov)
}

func providerTLSOverrideFromContext(ctx context.Context) (ProviderTLSOverride, bool) {
	v, ok := ctx.Value(ctxKeyProviderTLSOverride{}).(ProviderTLSOverride)
	if !ok || strings.TrimSpace(v.ProviderName) == "" {
		return ProviderTLSOverride{}, false
	}
	return v, true
}

// shouldSkipTrace checks if trace creation should be skipped for this context.
func shouldSkipTrace(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeySkipTrace{}).(bool)
	return v
}

// SecretResolver resolves external secret references (k8s://, env://) to values.
type SecretResolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// ModelRouter routes LLM requests to configured providers.
type ModelRouter struct {
	store  store.Store
	client *http.Client

	// blobs resolves media parts that reference stored bytes (blob_ref).
	blobs blobstore.Store

	// SecretResolver resolves provider API key references at runtime.
	secretResolver SecretResolver

	// Round-robin counter (atomic)
	rrCounter uint64

	// Key rotation round-robin counter (atomic)
	keyRRCounter uint64

	// Latency tracking: provider name → rolling avg ms
	latencyMu sync.RWMutex
	latencies map[string]int64

	// Cost tracking: kitchen+agent → accumulated cost
	costMu sync.RWMutex
	costs  map[string]*models.CostSummary

	// capturer, when set, is handed every call that gets a span, whole (ADR-0039).
	capturer models.LLMCallCapturer

	// ProviderDriver registry — maps provider kind to its driver.
	// OSS registers openai, azure-openai, anthropic, ollama, gemini at init.
	// Pro calls RegisterDriver("bedrock", ...) etc. to add enterprise drivers.
	driversMu sync.RWMutex
	drivers   map[string]ProviderDriver

	// providerClients caches per-provider *http.Client instances for providers
	// that have a custom CA bundle. Keyed by provider name. The shared client
	// field is used for providers without a custom CA.
	providerClients sync.Map

	// catalog provides model-level pricing and capability lookups.
	// Optional — when nil, falls back to defaultCosts map.
	catalog CatalogLookup

	// circuit tracks per-provider failure streaks so a provider that is
	// clearly down stops eating the retry budget on every subsequent call.
	circuit *circuitBreaker

	// spendGate, when set, is consulted before every model call (see spendgate.go).
	spendGate atomic.Pointer[gateBox]
}

// CatalogLookup is the interface the router needs from the model catalog.
// Defined here to avoid an import cycle (catalog → models ← router).
type CatalogLookup interface {
	Lookup(providerKind, modelName string) *models.ModelCapability
}

// ProviderDriver is the interface for model provider integrations.
// This mirrors contracts.ProviderDriver but lives in the router package
// to avoid import cycles. The contracts package re-exports it for Pro.
type ProviderDriver interface {
	// Kind returns the provider identifier (e.g., "openai", "bedrock").
	Kind() string

	// Call sends a chat completion request to the provider.
	Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error)

	// HealthCheck verifies the provider is reachable.
	HealthCheck(ctx context.Context, provider *models.ModelProvider) error
}

// StreamingProviderDriver is an OPTIONAL interface that drivers can implement
// to support streaming responses. Checked at runtime via type assertion:
//
//	if sd, ok := driver.(StreamingProviderDriver); ok { sd.StreamCall(...) }
//
// Drivers that don't implement this fall back to the non-streaming Call().
type StreamingProviderDriver interface {
	ProviderDriver

	// StreamCall sends a streaming chat completion request.
	// The callback is invoked for each chunk; return non-nil error from callback to abort.
	StreamCall(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest, callback func(chunk *models.StreamChunk) error) error
}

// EmbeddingCapableDriver is an OPTIONAL interface that provider drivers can
// implement to support text embeddings. Checked at runtime via type assertion:
//
//	if ecd, ok := driver.(EmbeddingCapableDriver); ok { ecd.Embed(...) }
//
// When a provider's driver implements this, the server auto-discovers embedding
// capabilities from configured providers — no separate embedding configuration needed.
// This is the "provider-first" embedding model: configure a provider once, and both
// chat completions AND embeddings are available automatically.
type EmbeddingCapableDriver interface {
	ProviderDriver

	// EmbeddingModels returns metadata about the embedding models this provider supports.
	EmbeddingModels() []EmbeddingModelInfo

	// Embed generates vector embeddings using the provider's credentials and endpoint.
	Embed(ctx context.Context, provider *models.ModelProvider, model string, texts []string) ([][]float64, error)
}

// EmbeddingModelInfo describes an embedding model available from a provider kind.
type EmbeddingModelInfo struct {
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	MaxBatch   int    `json:"max_batch"`
}

// ModelDiscoveryDriver is an OPTIONAL interface that provider drivers can
// implement to support model discovery. Checked at runtime via type assertion.
// When a driver implements this, the server can auto-discover available models.
type ModelDiscoveryDriver interface {
	ProviderDriver

	// DiscoverModels queries the provider's API for available models.
	DiscoverModels(ctx context.Context, provider *models.ModelProvider) ([]models.DiscoveredModel, error)
}

// NewModelRouter creates a new model router with built-in drivers registered.
func NewModelRouter(s store.Store) *ModelRouter {
	mr := &ModelRouter{
		store: s,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		latencies: make(map[string]int64),
		costs:     make(map[string]*models.CostSummary),
		drivers:   make(map[string]ProviderDriver),
		circuit:   newCircuitBreaker(),
	}

	// Register built-in OSS drivers
	mr.registerBuiltinDrivers()

	return mr
}

// SetSecretResolver sets the resolver for external secret references.
// Call this after NewModelRouter to enable k8s:// and env:// provider key refs.
func (mr *ModelRouter) SetSecretResolver(r SecretResolver) {
	mr.secretResolver = r
}

// RegisterDriver adds a provider driver to the registry.
// If a driver with the same kind already exists, it is replaced.
// This is the primary extension point for Pro to add enterprise drivers
// (Bedrock, Foundry, Vertex, SageMaker) without modifying OSS code.
func (mr *ModelRouter) RegisterDriver(driver ProviderDriver) {
	mr.driversMu.Lock()
	mr.drivers[driver.Kind()] = driver
	mr.driversMu.Unlock()
	log.Info().Str("kind", driver.Kind()).Msg("Provider driver registered")
}

// SetCatalog wires the model catalog into the router for model-level pricing.
// Called after both router and catalog are initialized in buildServer().
// SetBlobStore lets media parts reference stored bytes by blob_ref.
func (mr *ModelRouter) SetBlobStore(b blobstore.Store) {
	mr.blobs = b
}

func (mr *ModelRouter) SetCatalog(cat CatalogLookup) {
	mr.catalog = cat
	log.Info().Msg("Model catalog wired into router for pricing")
}

// GetDriver returns the registered driver for a provider kind, or nil.
func (mr *ModelRouter) GetDriver(kind string) ProviderDriver {
	mr.driversMu.RLock()
	defer mr.driversMu.RUnlock()
	return mr.drivers[kind]
}

// ListDrivers returns the kinds of all registered drivers.
func (mr *ModelRouter) ListDrivers() []string {
	mr.driversMu.RLock()
	defer mr.driversMu.RUnlock()
	kinds := make([]string, 0, len(mr.drivers))
	for k := range mr.drivers {
		kinds = append(kinds, k)
	}
	return kinds
}

// clientFor returns the *http.Client to use when calling the given provider.
// If the provider has a non-empty CABundle, a custom client that trusts that
// CA chain (in addition to the system pool) is returned and cached by provider
// name. If TLSSkipVerify is set, certificate verification is disabled entirely.
// Otherwise the shared router client is returned.
func (mr *ModelRouter) clientFor(p *models.ModelProvider) *http.Client {
	if p == nil || (p.CABundle == "" && !p.TLSSkipVerify) {
		return mr.client
	}
	if v, ok := mr.providerClients.Load(p.Name); ok {
		log.Debug().
			Str("provider", p.Name).
			Bool("has_ca_bundle", p.CABundle != "").
			Int("ca_bundle_len", len(p.CABundle)).
			Bool("tls_skip_verify", p.TLSSkipVerify).
			Msg("Reusing cached provider HTTP client with custom TLS settings")
		return v.(*http.Client)
	}
	tlsCfg := &tls.Config{}
	if p.TLSSkipVerify {
		tlsCfg.InsecureSkipVerify = true // #nosec G402 — user-opted-in for private endpoints
		log.Warn().Str("provider", p.Name).Msg("Provider TLS verification disabled by configuration")
	} else {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if ok := pool.AppendCertsFromPEM([]byte(p.CABundle)); !ok {
			log.Warn().
				Str("provider", p.Name).
				Int("ca_bundle_len", len(p.CABundle)).
				Msg("Provider CA bundle could not be parsed as PEM; proceeding with system trust store")
		} else {
			log.Info().
				Str("provider", p.Name).
				Int("ca_bundle_len", len(p.CABundle)).
				Msg("Provider CA bundle loaded into TLS client")
		}
		tlsCfg.RootCAs = pool
	}
	c := &http.Client{
		Timeout: mr.client.Timeout,
		Transport: &http.Transport{
			TLSClientConfig:       tlsCfg,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
	mr.providerClients.Store(p.Name, c)
	return c
}

// InvalidateProviderClient removes the cached custom HTTP client for a provider.
// Call this after updating a provider's CA bundle so the new bundle is picked up.
func (mr *ModelRouter) InvalidateProviderClient(name string) {
	mr.providerClients.Delete(name)
}

// ListEmbeddingCapableDrivers returns all registered drivers that implement
// EmbeddingCapableDriver. Used by server.go to auto-discover embedding
// capabilities from configured providers.
func (mr *ModelRouter) ListEmbeddingCapableDrivers() map[string]EmbeddingCapableDriver {
	mr.driversMu.RLock()
	defer mr.driversMu.RUnlock()
	result := make(map[string]EmbeddingCapableDriver)
	for kind, driver := range mr.drivers {
		if ecd, ok := driver.(EmbeddingCapableDriver); ok {
			result[kind] = ecd
		}
	}
	return result
}

// DiscoverEmbeddingsForProvider checks if a provider's driver supports embeddings
// and returns the capability info. Returns nil if not embedding-capable.
func (mr *ModelRouter) DiscoverEmbeddingsForProvider(provider *models.ModelProvider) (EmbeddingCapableDriver, []EmbeddingModelInfo) {
	driver := mr.GetDriver(provider.Kind)
	if driver == nil {
		return nil, nil
	}
	ecd, ok := driver.(EmbeddingCapableDriver)
	if !ok {
		return nil, nil
	}
	return ecd, ecd.EmbeddingModels()
}

// DiscoverModelsForProvider checks if a provider's driver supports model discovery
// and queries the provider's API for available models.
func (mr *ModelRouter) DiscoverModelsForProvider(ctx context.Context, provider *models.ModelProvider) ([]models.DiscoveredModel, error) {
	driver := mr.GetDriver(provider.Kind)
	if driver == nil {
		return nil, fmt.Errorf("no driver registered for kind: %s", provider.Kind)
	}
	mdd, ok := driver.(ModelDiscoveryDriver)
	if !ok {
		return nil, fmt.Errorf("driver %q does not support model discovery", provider.Kind)
	}
	p := *provider
	mr.applyKey(&p)
	return mdd.DiscoverModels(ctx, &p)
}

// ListDiscoveryCapableDrivers returns all registered drivers that implement
// ModelDiscoveryDriver.
func (mr *ModelRouter) ListDiscoveryCapableDrivers() map[string]ModelDiscoveryDriver {
	mr.driversMu.RLock()
	defer mr.driversMu.RUnlock()
	result := make(map[string]ModelDiscoveryDriver)
	for kind, driver := range mr.drivers {
		if mdd, ok := driver.(ModelDiscoveryDriver); ok {
			result[kind] = mdd
		}
	}
	return result
}

// HealthCheck pings all configured providers and returns their status.
func (mr *ModelRouter) HealthCheck(ctx context.Context) map[string]string {
	// Cross-kitchen admin view — empty kitchen means "no filter" in the store layer.
	providers, err := mr.store.ListProviders(ctx, "")
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	result := make(map[string]string, len(providers))
	for _, p := range providers {
		driver := mr.GetDriver(p.Kind)
		if driver == nil {
			result[p.Name] = "no driver registered for kind: " + p.Kind
			continue
		}
		mr.applyKey(&p)
		if err := driver.HealthCheck(ctx, &p); err != nil {
			result[p.Name] = "unhealthy: " + err.Error()
		} else {
			result[p.Name] = "healthy"
		}
	}
	return result
}

// applyKey selects the key to use for provider (see SelectAPIKey) and puts it in provider.Config, which
// is where every driver reads it. A provider that keeps its key in an external secret, or only in its
// rotation pool, has no config api_key of its own, so a driver handed the provider as stored would find
// no key. Every path that hands a stored provider to a driver (a call, a stream, a health check, a test,
// model discovery) goes through here.
//
// Config is replaced with a fresh map rather than mutated in place. The store hands out providers as a
// shallow copy of its internal struct: the struct is new, but Config is a map, a reference type, so
// that copy still points at the exact map the store's own background snapshot goroutine reads. Writing
// into it raced that goroutine under -race; writing into a new map and only then pointing
// provider.Config at it never touches the store's map at all. Callers pass a copy they own.
func (mr *ModelRouter) applyKey(provider *models.ModelProvider) {
	selectedKey := mr.SelectAPIKey(provider)
	if selectedKey == "" {
		return
	}
	withKey := make(map[string]interface{}, len(provider.Config)+1)
	for k, v := range provider.Config {
		withKey[k] = v
	}
	withKey["api_key"] = selectedKey
	provider.Config = withKey
}

// FreshProvider reads a provider from the store as it is now, with the key to use already applied.
// Long-lived holders of a provider (an embedding adapter) call this on each use instead of keeping the
// copy they were created with, so a rotated key or a changed secret reference takes effect at once.
func (mr *ModelRouter) FreshProvider(ctx context.Context, kitchen, name string) (*models.ModelProvider, error) {
	p, err := mr.store.GetProvider(ctx, kitchen, name)
	if err != nil {
		return nil, fmt.Errorf("provider %q not found", name)
	}
	mr.applyKey(p)
	return p, nil
}

// ProviderKey reads the key to use for a provider, now: from the provider as it is stored, through the
// same selection a model call uses (an external secret, a key from the rotation pool, or the single
// stored key). Callers that need a key (a hosted process starting) read it here instead of keeping a
// copy, so a rotated or revoked key takes effect at once and no copy can leak.
func (mr *ModelRouter) ProviderKey(ctx context.Context, kitchen, name string) (string, error) {
	p, err := mr.store.GetProvider(ctx, kitchen, name)
	if err != nil {
		return "", fmt.Errorf("provider %q not found", name)
	}
	return mr.SelectAPIKey(p), nil
}

// SelectAPIKey picks an API key from a provider's key pool using the configured
// rotation strategy. If no API keys are configured in the pool, falls back to
// the provider's Config["api_key"] field.
//
// Resolution priority:
//  1. Provider-level SecretRef (k8s://, env:// or vault://) → single dynamic key
//  2. APIKeys pool with per-key SecretRef support → rotation with dynamic keys
//  3. APIKeys pool with inline keys → rotation with static keys
//  4. Legacy Config["api_key"] → single static key
//
// Strategies:
//   - "round-robin" (default): rotates through enabled keys sequentially
//   - "random": picks a random enabled key
//   - "weighted": weighted random selection based on key weights
func (mr *ModelRouter) SelectAPIKey(provider *models.ModelProvider) string {
	// A vault:// reference is looked up in the provider's own kitchen, never another.
	ctx := secretref.WithKitchen(context.Background(), provider.Kitchen)
	// Priority 1: Provider-level SecretRef (dynamic key from external store)
	if provider.SecretRef != "" && mr.secretResolver != nil {
		resolved, err := mr.secretResolver.Resolve(ctx, provider.SecretRef)
		if err != nil {
			log.Warn().Err(err).Str("provider", provider.Name).Str("ref", provider.SecretRef).Msg("Failed to resolve provider secret ref, falling back")
		} else if resolved != "" {
			return resolved
		}
	}

	// Filter to enabled keys only, resolving SecretRefs for each
	var enabled []models.APIKeyEntry
	for _, k := range provider.APIKeys {
		if !k.Enabled {
			continue
		}
		// Resolve SecretRef for this key entry if set
		if k.SecretRef != "" && mr.secretResolver != nil {
			resolved, err := mr.secretResolver.Resolve(ctx, k.SecretRef)
			if err != nil {
				log.Warn().Err(err).Str("provider", provider.Name).Str("label", k.Label).Str("ref", k.SecretRef).Msg("Failed to resolve key secret ref, skipping")
				continue
			}
			if resolved != "" {
				k.Key = resolved
			}
		}
		if k.Key != "" {
			enabled = append(enabled, k)
		}
	}

	// No rotation keys configured — use legacy single key
	if len(enabled) == 0 {
		apiKey, _ := provider.Config["api_key"].(string)
		return apiKey
	}

	strategy := provider.RotationStrategy
	if strategy == "" {
		strategy = "round-robin"
	}

	switch strategy {
	case "random":
		return enabled[rand.Intn(len(enabled))].Key

	case "weighted":
		totalWeight := 0
		for _, k := range enabled {
			w := k.Weight
			if w <= 0 {
				w = 1
			}
			totalWeight += w
		}
		pick := rand.Intn(totalWeight)
		cumulative := 0
		for _, k := range enabled {
			w := k.Weight
			if w <= 0 {
				w = 1
			}
			cumulative += w
			if pick < cumulative {
				return k.Key
			}
		}
		return enabled[0].Key // fallback

	default: // round-robin
		idx := atomic.AddUint64(&mr.keyRRCounter, 1)
		return enabled[int(idx)%len(enabled)].Key
	}
}

// Route sends a request through the router using the specified strategy.
func (mr *ModelRouter) Route(ctx context.Context, req *models.RouteRequest) (*models.RouteResponse, error) {
	started := time.Now()
	resp, attempts, err := mr.routeCore(ctx, req)
	mr.finishRoute(ctx, req, resp, attempts, started, err)
	return resp, err
}

// routeCore tries the providers in order and returns the first answer, with the providers that failed
// before it. It records nothing about the call (finishRoute does), so a caller that goes on to try
// something else, a backup provider, records one trace for the whole request.
func (mr *ModelRouter) routeCore(ctx context.Context, req *models.RouteRequest) (*models.RouteResponse, []models.RouteAttempt, error) {
	// Get configured providers
	providers, err := mr.store.ListProviders(ctx, req.Kitchen)
	if err != nil {
		return nil, nil, fmt.Errorf("list providers: %w", err)
	}
	if ov, ok := providerTLSOverrideFromContext(ctx); ok {
		matched := false
		for i := range providers {
			if providers[i].Name != ov.ProviderName {
				continue
			}
			matched = true
			providers[i].CABundle = ov.CABundle
			providers[i].TLSSkipVerify = ov.TLSSkipVerify
			log.Info().
				Str("provider", providers[i].Name).
				Bool("has_ca_bundle", ov.CABundle != "").
				Int("ca_bundle_len", len(ov.CABundle)).
				Bool("tls_skip_verify", ov.TLSSkipVerify).
				Msg("Applied per-request provider TLS override")
			break
		}
		if !matched {
			log.Warn().Str("provider", ov.ProviderName).Msg("Provider TLS override provided but provider not found")
		}
	}
	if len(providers) == 0 {
		return nil, nil, fmt.Errorf("no model providers configured")
	}

	if req.PinnedProvider != "" {
		return mr.routePinned(ctx, req, providers)
	}

	strategy := req.Strategy
	if strategy == "" {
		strategy = models.RoutingFallback
	}

	// Select providers in order based on strategy
	ordered := mr.orderProviders(providers, strategy, req.Model)

	// Try each provider in order (fallback behavior)
	var lastErr error
	var attempts []models.RouteAttempt
	for _, provider := range ordered {
		t0 := time.Now()
		resp, err := mr.callWithRetry(ctx, &provider, req)
		if err != nil {
			log.Warn().
				Str("provider", provider.Name).
				Str("model", provider.Kind).
				Err(err).
				Msg("Provider call failed (after retries), trying next")
			lastErr = err
			attempts = append(attempts, mr.attemptOf(&provider, req, err, time.Since(t0)))
			continue
		}

		// Track cost
		mr.trackCost(req.Kitchen, req.AgentRef, resp)
		return resp, attempts, nil
	}

	return nil, attempts, fmt.Errorf("all providers failed, last error: %w", lastErr)
}

// attemptOf describes one provider try for a trace.
func (mr *ModelRouter) attemptOf(p *models.ModelProvider, req *models.RouteRequest, err error, took time.Duration) models.RouteAttempt {
	model := req.Model
	if model == "" && len(p.Models) > 0 {
		model = p.Models[0]
	}
	a := models.RouteAttempt{Provider: p.Name, Model: model, LatencyMs: took.Milliseconds()}
	if err != nil {
		a.Error = err.Error()
	}
	return a
}

// routePinned serves a RouteRequest with PinnedProvider set: exactly one
// named provider is tried, with no fallback to any other — see
// RouteRequest.PinnedProvider for why that's the point, not a limitation.
func (mr *ModelRouter) routePinned(ctx context.Context, req *models.RouteRequest, providers []models.ModelProvider) (*models.RouteResponse, []models.RouteAttempt, error) {
	for i := range providers {
		if providers[i].Name != req.PinnedProvider {
			continue
		}
		t0 := time.Now()
		resp, err := mr.callWithRetry(ctx, &providers[i], req)
		if err != nil {
			return nil, []models.RouteAttempt{mr.attemptOf(&providers[i], req, err, time.Since(t0))}, fmt.Errorf("pinned provider %q failed: %w", req.PinnedProvider, err)
		}
		mr.trackCost(req.Kitchen, req.AgentRef, resp)
		return resp, nil, nil
	}
	return nil, nil, fmt.Errorf("pinned provider %q not found in kitchen %q", req.PinnedProvider, req.Kitchen)
}

// RouteWithBackup routes a request through the primary provider, and if all
// primary providers fail, automatically falls back to the backup provider.
// This is used by InvokeAgent/SendSessionMessage when an agent has a BackupProvider configured.
func (mr *ModelRouter) RouteWithBackup(ctx context.Context, req *models.RouteRequest, backupProvider, backupModel string) (*models.RouteResponse, error) {
	started := time.Now()
	// Try primary route first
	resp, attempts, err := mr.routeCore(ctx, req)
	if err == nil {
		mr.finishRoute(ctx, req, resp, attempts, started, nil)
		return resp, nil
	}

	// Primary failed — try backup provider if configured
	if backupProvider == "" {
		mr.finishRoute(ctx, req, nil, attempts, started, err)
		return nil, err // no backup, return original error
	}

	log.Warn().
		Str("backup_provider", backupProvider).
		Str("backup_model", backupModel).
		Err(err).
		Msg("Primary providers failed, attempting backup provider")

	backup, bErr := mr.store.GetProvider(ctx, req.Kitchen, backupProvider)
	if bErr != nil {
		err = fmt.Errorf("primary providers failed (%w) and backup provider %q not found: %v", err, backupProvider, bErr)
		mr.finishRoute(ctx, req, nil, attempts, started, err)
		return nil, err
	}
	if ov, ok := providerTLSOverrideFromContext(ctx); ok && backup != nil && backup.Name == ov.ProviderName {
		backup.CABundle = ov.CABundle
		backup.TLSSkipVerify = ov.TLSSkipVerify
		log.Info().
			Str("provider", backup.Name).
			Bool("has_ca_bundle", ov.CABundle != "").
			Int("ca_bundle_len", len(ov.CABundle)).
			Bool("tls_skip_verify", ov.TLSSkipVerify).
			Msg("Applied per-request provider TLS override (backup)")
	}

	// Build a request targeting the backup model
	backupReq := *req
	if backupModel != "" {
		backupReq.Model = backupModel
	} else if len(backup.Models) > 0 {
		backupReq.Model = backup.Models[0]
	}

	t0 := time.Now()
	resp, bErr = mr.callWithRetry(ctx, backup, &backupReq)
	if bErr != nil {
		attempts = append(attempts, mr.attemptOf(backup, &backupReq, bErr, time.Since(t0)))
		err = fmt.Errorf("primary providers failed (%w) and backup provider %q also failed: %v", err, backupProvider, bErr)
		mr.finishRoute(ctx, req, nil, attempts, started, err)
		return nil, err
	}

	// Tag the response as coming from backup
	resp.Provider = backup.Name + " (backup)"
	mr.trackCost(req.Kitchen, req.AgentRef, resp)
	mr.finishRoute(ctx, req, resp, attempts, started, nil)

	return resp, nil
}

// RouteStream routes a request to a provider that supports streaming.
// Falls back to non-streaming Route() if the selected driver doesn't implement
// StreamingProviderDriver, buffering the full response and sending it as one chunk.
func (mr *ModelRouter) RouteStream(ctx context.Context, req *models.RouteRequest, callback func(chunk *models.StreamChunk) error) error {
	providers, err := mr.store.ListProviders(ctx, req.Kitchen)
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	if len(providers) == 0 {
		return fmt.Errorf("no model providers configured")
	}

	strategy := req.Strategy
	if strategy == "" {
		strategy = models.RoutingFallback
	}

	ordered := mr.orderProviders(providers, strategy, req.Model)

	started := time.Now()
	var attempts []models.RouteAttempt
	var lastErr error
	for _, provider := range ordered {
		// Look up driver
		mr.driversMu.RLock()
		driver, ok := mr.drivers[provider.Kind]
		mr.driversMu.RUnlock()

		if !ok {
			lastErr = fmt.Errorf("no driver for kind %q", provider.Kind)
			continue
		}
		mr.applyKey(&provider)

		// Check if driver supports streaming
		if sd, ok := driver.(StreamingProviderDriver); ok {
			err := sd.StreamCall(ctx, &provider, req, callback)
			if err != nil {
				log.Warn().Str("provider", provider.Name).Err(err).Msg("Streaming call failed, trying next")
				lastErr = err
				continue
			}
			return nil
		}

		// Fallback: non-streaming call → emit as single chunk
		t0 := time.Now()
		resp, err := mr.callProvider(ctx, &provider, req)
		if err != nil {
			lastErr = err
			attempts = append(attempts, mr.attemptOf(&provider, req, err, time.Since(t0)))
			continue
		}
		mr.trackCost(req.Kitchen, req.AgentRef, resp)
		mr.finishRoute(ctx, req, resp, attempts, started, nil)

		// The reasoning goes out first, then the answer: a streaming client sees it the way it would
		// from a provider that streams its thinking.
		for _, tb := range resp.ThinkingBlocks {
			if tb.Content == "" {
				continue
			}
			if err := callback(&models.StreamChunk{ThinkingContent: tb.Content}); err != nil {
				return err
			}
		}
		return callback(&models.StreamChunk{
			Content: resp.Content,
			Done:    true,
			Usage:   &resp.Usage,
		})
	}

	err = fmt.Errorf("all providers failed (stream), last error: %w", lastErr)
	mr.finishRoute(ctx, req, nil, attempts, started, err)
	return err
}

// orderProviders sorts providers based on the routing strategy.
func (mr *ModelRouter) orderProviders(providers []models.ModelProvider, strategy models.RoutingStrategy, requestedModel string) []models.ModelProvider {
	// Filter by model if specified
	if requestedModel != "" {
		var filtered []models.ModelProvider
		for _, p := range providers {
			for _, m := range p.Models {
				if m == requestedModel {
					filtered = append(filtered, p)
					break
				}
			}
		}
		if len(filtered) > 0 {
			providers = filtered
		}
	}

	switch strategy {
	case models.RoutingCostOptimized:
		sort.Slice(providers, func(i, j int) bool {
			ci := mr.getProviderCostPer1K(providers[i], requestedModel)
			cj := mr.getProviderCostPer1K(providers[j], requestedModel)
			return ci < cj
		})

	case models.RoutingLatencyOptimized:
		mr.latencyMu.RLock()
		sort.Slice(providers, func(i, j int) bool {
			li := mr.latencies[providers[i].Name]
			lj := mr.latencies[providers[j].Name]
			if li == 0 {
				li = 1000 // default 1s for unknown
			}
			if lj == 0 {
				lj = 1000
			}
			return li < lj
		})
		mr.latencyMu.RUnlock()

	case models.RoutingRoundRobin:
		idx := atomic.AddUint64(&mr.rrCounter, 1)
		n := len(providers)
		// Rotate the list so index is at front
		rotated := make([]models.ModelProvider, n)
		for i := 0; i < n; i++ {
			rotated[i] = providers[(int(idx)+i)%n]
		}
		return rotated

	case models.RoutingFallback:
		// Default providers first, then by name
		sort.Slice(providers, func(i, j int) bool {
			if providers[i].IsDefault != providers[j].IsDefault {
				return providers[i].IsDefault
			}
			return providers[i].Name < providers[j].Name
		})
	}

	return providers
}

// callProvider sends the request to a specific provider.
func (mr *ModelRouter) callProvider(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	start := time.Now()

	// When thinking is enabled, ensure the context has enough headroom —
	// thinking models (Claude extended, o-series) can take 30-120s per turn.
	if req.ThinkingEnabled {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
		}
	}

	model := req.Model
	if model == "" && len(provider.Models) > 0 {
		model = provider.Models[0]
	}

	mr.applyKey(provider)

	// Look up registered driver
	driver := mr.GetDriver(provider.Kind)
	if driver == nil {
		// Fallback: try OpenAI-compatible driver for unknown kinds
		driver = mr.GetDriver("openai")
		if driver == nil {
			return nil, fmt.Errorf("no driver registered for provider kind: %s", provider.Kind)
		}
	}

	// Build a request with the resolved model
	driverReq := *req
	driverReq.Model = model

	if err := mr.spendAllow(ctx, provider, &driverReq); err != nil {
		return nil, err
	}
	resp, err := driver.Call(ctx, provider, &driverReq)
	if err != nil {
		return nil, err
	}
	// A driver that did not price its own call (one registered from outside, Pro's cloud drivers)
	// gets the model's price applied here, so its cost is not recorded as zero.
	if resp.Usage.PriceSource == "" && resp.Usage.EstimatedCost == 0 && (resp.Usage.InputTokens > 0 || resp.Usage.OutputTokens > 0) {
		priced := model
		if resp.Model != "" {
			priced = resp.Model
		}
		mr.settle(provider, priced, &resp.Usage)
	}
	mr.spendRecord(ctx, provider, &driverReq, resp)

	latencyMs := time.Since(start).Milliseconds()
	resp.LatencyMs = latencyMs
	resp.Strategy = req.Strategy
	if resp.Strategy == "" {
		resp.Strategy = models.RoutingFallback
	}

	// Update latency tracking
	mr.latencyMu.Lock()
	prev := mr.latencies[provider.Name]
	if prev == 0 {
		mr.latencies[provider.Name] = latencyMs
	} else {
		// Exponential moving average
		mr.latencies[provider.Name] = (prev*7 + latencyMs*3) / 10
	}
	mr.latencyMu.Unlock()

	return resp, nil
}

// ── OpenAI / Azure OpenAI Provider ──────────────────────────

type openAIRequest struct {
	Model          string                  `json:"model"`
	Messages       []openAIMessage         `json:"messages"`
	Tools          []models.ToolDefinition `json:"tools,omitempty"`
	ToolChoice     interface{}             `json:"tool_choice,omitempty"`
	ResponseFormat *models.ResponseFormat  `json:"response_format,omitempty"`
	// ReasoningEffort asks a reasoning model to think ("low", "medium", "high"). Reasoning is
	// OpenRouter's form of the same request.
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
	Reasoning       *openRouterReasoning `json:"reasoning,omitempty"`
}

type openRouterReasoning struct {
	MaxTokens int `json:"max_tokens,omitempty"`
}

// openAIMessage is the wire shape of one chat message. Content is a string for
// text-only messages and a parts array when media is attached.
type openAIMessage struct {
	Role       string                  `json:"role"`
	Content    interface{}             `json:"content"`
	ToolCalls  []models.ToolCallResult `json:"tool_calls,omitempty"`
	ToolCallID string                  `json:"tool_call_id,omitempty"`
	Name       string                  `json:"name,omitempty"`
}

// openAIMessages converts chat history into OpenAI wire messages, mapping any
// media parts and refusing modalities the model cannot take.
func (mr *ModelRouter) openAIMessages(ctx context.Context, prov *models.ModelProvider, model string, msgs []models.ChatMessage) ([]openAIMessage, error) {
	out := make([]openAIMessage, 0, len(msgs))
	for _, m := range msgs {
		content, err := mr.openAIContent(ctx, prov, model, m)
		if err != nil {
			return nil, err
		}
		out = append(out, openAIMessage{
			Role: m.Role, Content: content, ToolCalls: m.ToolCalls,
			ToolCallID: m.ToolCallID, Name: m.Name,
		})
	}
	return out, nil
}

type openAIResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message      openAIResponseMessage `json:"message"`
		FinishReason string                `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
		ReasoningTokens  int64 `json:"reasoning_tokens,omitempty"` // some compatible servers put it here
		// OpenAI's own breakdown: cached prompt tokens, and the thinking part of the completion.
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens,omitempty"` // DeepSeek
	} `json:"usage"`
}

type openAIResponseMessage struct {
	Content   string                  `json:"content"`
	ToolCalls []models.ToolCallResult `json:"tool_calls,omitempty"`
	openAIReasoning
}

func (mr *ModelRouter) callOpenAI(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	model := req.Model
	messages := req.Messages

	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}

	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("openai: api_key not configured for provider %s", provider.Name)
	}

	oaiMsgs, err := mr.openAIMessages(ctx, provider, model, messages)
	if err != nil {
		return nil, err
	}
	oaiReq := openAIRequest{Model: model, Messages: oaiMsgs, ResponseFormat: req.ResponseFormat}
	// Include tool definitions if provided by the executor
	if len(req.Tools) > 0 {
		oaiReq.Tools = req.Tools
		if req.ToolChoice != nil {
			oaiReq.ToolChoice = req.ToolChoice
		}
	}
	mr.applyOpenAIThinking(&oaiReq, provider, model, req)
	body, _ := json.Marshal(oaiReq)

	url := endpoint + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Azure OpenAI uses a different auth header
	if provider.Kind == "azure-openai" {
		httpReq.Header.Set("api-key", apiKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	// Optional attribution headers used by OpenRouter (and any provider that sets them
	// via provider.Config["http_referer"] / provider.Config["x_title"]).
	if referer, ok := provider.Config["http_referer"].(string); ok && referer != "" {
		httpReq.Header.Set("HTTP-Referer", referer)
	}
	if title, ok := provider.Config["x_title"].(string); ok && title != "" {
		httpReq.Header.Set("X-Title", title)
	}

	httpResp, err := mr.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, parseOpenAIError("openai", httpResp.StatusCode, respBody, req.Model)
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}

	content, finishReason, thinkingBlocks, toolCalls, usage := openAIResult(oaiResp, model, provider.Name)
	mr.settle(provider, model, &usage)

	return &models.RouteResponse{
		ID:             oaiResp.ID,
		Provider:       provider.Name,
		Model:          model,
		Content:        content,
		ThinkingBlocks: thinkingBlocks,
		FinishReason:   finishReason,
		ToolCalls:      toolCalls,
		Usage:          usage,
	}, nil
}

// applyOpenAIThinking adds the request's thinking setting in the form the provider takes. OpenRouter
// takes a reasoning budget; the others take an effort. A model not known to reason is left alone.
func (mr *ModelRouter) applyOpenAIThinking(oaiReq *openAIRequest, provider *models.ModelProvider, model string, req *models.RouteRequest) {
	if !req.ThinkingEnabled {
		return
	}
	switch provider.Kind {
	case "openrouter":
		oaiReq.Reasoning = &openRouterReasoning{MaxTokens: thinkingBudget(req)}
	case "ollama":
		oaiReq.ReasoningEffort = reasoningEffort(thinkingBudget(req)) // an older Ollama ignores it
	default:
		if mr.canThink(provider.Kind, model) {
			oaiReq.ReasoningEffort = reasoningEffort(thinkingBudget(req))
		}
	}
}

// ── Anthropic Provider ──────────────────────────────────────

// anthropicTool describes a tool in Anthropic's format.
type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicRequest struct {
	Model      string             `json:"model"`
	System     string             `json:"system,omitempty"`
	Messages   []anthropicMessage `json:"messages"`
	MaxTokens  int                `json:"max_tokens"`
	Tools      []anthropicTool    `json:"tools,omitempty"`
	ToolChoice interface{}        `json:"tool_choice,omitempty"`
	Thinking   *anthropicThinking `json:"thinking,omitempty"`
}

// anthropicThinking turns on extended thinking with a token budget.
type anthropicThinking struct {
	Type         string `json:"type"` // "enabled"
	BudgetTokens int    `json:"budget_tokens"`
}

// anthropicStructuredOutputTool is the name of the tool structuredOutputTool
// synthesizes. Anthropic has no response_format — a forced tool call is the
// standard workaround: the model "calls" this tool with the schema as its
// arguments instead of writing free text, and callAnthropic moves those
// arguments into RouteResponse.Content so a caller reading .Content sees the
// same shape of structured JSON it would get from OpenAI's native
// response_format, regardless of which provider actually answered.
const anthropicStructuredOutputTool = "emit_structured_output"

// structuredOutputTool builds the forced tool for a json_schema ResponseFormat.
// Returns ok=false when format doesn't ask for one.
func structuredOutputTool(format *models.ResponseFormat) (anthropicTool, bool) {
	if format == nil || format.Type != "json_schema" {
		return anthropicTool{}, false
	}
	schema := format.JSONSchema
	// The executor wraps the schema as {"name":..., "schema": {...}, "strict":...}
	// to match OpenAI's wire shape (router.go callOpenAI passes it through
	// verbatim) — unwrap that here so Anthropic's tool gets the bare schema.
	if inner, ok := schema["schema"].(map[string]interface{}); ok {
		schema = inner
	}
	return anthropicTool{
		Name:        anthropicStructuredOutputTool,
		Description: "Emit the final answer as JSON matching the required schema. Always call this exactly once to give your answer; never answer in plain text.",
		InputSchema: schema,
	}, true
}

// anthropicMessage is one turn in Anthropic's wire format. Content is a plain
// string for an ordinary text turn, or a list of anthropicContentBlock for a
// turn that carries tool_use/tool_result blocks — Anthropic accepts either
// shape per message, so anthropicMessagesFrom only builds the block form when
// a message actually needs one.
type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// anthropicContentBlock is a "text", "tool_use" or "tool_result" block.
// Exactly the fields for one of those three are set; json:",omitempty"
// leaves the other kinds out of the wire payload.
type anthropicContentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// tool_use (an assistant message asking to call a tool)
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	// tool_result (a user message answering a tool_use)
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// thinking and redacted_thinking: the model's own reasoning, sent back unchanged (signature
	// included) in the turn that follows a tool call.
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

// anthropicMessagesFrom translates the router's provider-agnostic message
// history — OpenAI-shaped: an assistant message's tool calls in ToolCalls,
// each answered by a follow-up message with Role "tool" and a ToolCallID —
// into Anthropic's Messages API shape, which has no "tool" role at all.
//
// An assistant message with ToolCalls becomes an assistant turn whose content
// is a text block (if it also said something) followed by one tool_use block
// per call. Anthropic requires every tool_use in one assistant turn to be
// answered together, in one following user turn, so consecutive "tool"
// messages are merged into a single user message with one tool_result block
// each — passing them through as separate consecutive messages the way the
// generic history stores them is exactly what produced "Unexpected role
// \"tool\"" once, and would still violate Anthropic's turn-pairing even after
// the role were merely renamed.
func (mr *ModelRouter) anthropicMessagesFrom(ctx context.Context, prov *models.ModelProvider, model string, msgs []models.ChatMessage, withThinking bool) ([]anthropicMessage, error) {
	var out []anthropicMessage
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]

		if hasMediaParts(m) && m.Role != "tool" {
			content, err := mr.anthropicContent(ctx, prov, model, m)
			if err != nil {
				return nil, err
			}
			out = append(out, anthropicMessage{Role: m.Role, Content: content})
			continue
		}

		if m.Role == "tool" {
			var blocks []anthropicContentBlock
			for i < len(msgs) && msgs[i].Role == "tool" {
				blocks = append(blocks, anthropicContentBlock{
					Type:      "tool_result",
					ToolUseID: msgs[i].ToolCallID,
					Content:   msgs[i].Content,
				})
				i++
			}
			i--
			out = append(out, anthropicMessage{Role: "user", Content: blocks})
			continue
		}

		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			var blocks []anthropicContentBlock
			// With thinking on, Anthropic rejects a tool_use turn that does not start with the
			// thinking blocks the model produced for it.
			if withThinking {
				for _, tb := range m.Thinking {
					switch {
					case tb.Redacted && tb.Data != "":
						blocks = append(blocks, anthropicContentBlock{Type: "redacted_thinking", Data: tb.Data})
					case tb.Signature != "":
						blocks = append(blocks, anthropicContentBlock{Type: "thinking", Thinking: tb.Content, Signature: tb.Signature})
					}
				}
			}
			if m.Content != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				var input map[string]interface{}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
				blocks = append(blocks, anthropicContentBlock{
					Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: input,
				})
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
			continue
		}

		out = append(out, anthropicMessage{Role: m.Role, Content: m.Content})
	}
	return out, nil
}

// hasMediaParts reports whether a message carries any non-text content part.
func hasMediaParts(m models.ChatMessage) bool {
	for _, p := range m.ContentParts {
		if k := partKind(p); k != "text" && k != "" {
			return true
		}
	}
	return false
}

type anthropicResponse struct {
	ID         string `json:"id"`
	StopReason string `json:"stop_reason,omitempty"` // "end_turn", "tool_use", "max_tokens"
	Content    []struct {
		Type     string                 `json:"type"` // "text", "thinking", "tool_use"
		Text     string                 `json:"text"`
		Thinking string                 `json:"thinking"`        // Anthropic extended thinking content
		ID       string                 `json:"id,omitempty"`    // tool_use block ID
		Name     string                 `json:"name,omitempty"`  // tool_use function name
		Input    map[string]interface{} `json:"input,omitempty"` // tool_use arguments
		// thinking blocks carry a signature; redacted ones an opaque payload instead of text.
		Signature string `json:"signature,omitempty"`
		Data      string `json:"data,omitempty"`
	} `json:"content"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		// Prompt tokens read from and written to the cache are reported apart from input_tokens.
		CacheReadTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func (mr *ModelRouter) callAnthropic(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	model := normalizeAnthropicModel(req.Model)
	messages := req.Messages

	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.anthropic.com"
	}
	// Normalize: strip trailing slash and /v1 suffix so we can always append /v1/...
	endpoint = strings.TrimRight(endpoint, "/")
	endpoint = strings.TrimSuffix(endpoint, "/v1")

	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("anthropic: api_key not configured for provider %s", provider.Name)
	}

	maxTokens := 4096
	if mt, ok := provider.Config["max_tokens"].(float64); ok {
		maxTokens = int(mt)
	}

	// Anthropic Messages API requires system messages as a top-level "system"
	// parameter, not as a message with role "system" in the messages array.
	var systemText string
	var filteredMessages []models.ChatMessage
	for _, m := range messages {
		if m.Role == "system" {
			if systemText != "" {
				systemText += "\n\n"
			}
			systemText += m.Content
		} else {
			filteredMessages = append(filteredMessages, m)
		}
	}

	// Extended thinking: a budget of at least 1,024 tokens that max_tokens must leave room beyond.
	// It cannot be combined with a forced tool_choice, so a structured-output call (below) goes
	// without it.
	_, forcedOutput := structuredOutputTool(req.ResponseFormat)
	thinkingOn := req.ThinkingEnabled && !(forcedOutput && len(req.Tools) == 0)
	budget := 0
	if thinkingOn {
		budget = thinkingBudget(req)
		if budget < minAnthropicThinking {
			budget = minAnthropicThinking
		}
		if maxTokens <= budget {
			maxTokens = budget + 4096
		}
	}

	anthMsgs, err := mr.anthropicMessagesFrom(ctx, provider, model, filteredMessages, thinkingOn)
	if err != nil {
		return nil, err
	}
	anthReq := anthropicRequest{Model: model, System: systemText, Messages: anthMsgs, MaxTokens: maxTokens}
	if thinkingOn {
		anthReq.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
	}
	// Convert tool definitions to Anthropic format
	if len(req.Tools) > 0 {
		for _, td := range req.Tools {
			anthReq.Tools = append(anthReq.Tools, anthropicTool{
				Name:        td.Function.Name,
				Description: td.Function.Description,
				InputSchema: td.Function.Parameters,
			})
		}
	}
	// Structured output only engages when the agent declares no real tools of
	// its own — forcing a single tool_choice would otherwise silently disable
	// every tool the agent actually needs to call. An agent that wants both
	// tool use and a final structured answer gets OpenAI/Ollama's native
	// response_format instead, which composes with tool calls; this is
	// Anthropic's narrower, tool-only path.
	if len(req.Tools) == 0 {
		if sot, ok := structuredOutputTool(req.ResponseFormat); ok {
			anthReq.Tools = []anthropicTool{sot}
			anthReq.ToolChoice = map[string]string{"type": "tool", "name": anthropicStructuredOutputTool}
		}
	}

	body, _ := json.Marshal(anthReq)

	url := endpoint + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("anthropic-beta", anthropicFilesBeta)

	httpResp, err := mr.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, parseAnthropicError("anthropic", httpResp.StatusCode, respBody, model)
	}

	var anthResp anthropicResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&anthResp); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}

	content := ""
	var thinkingBlocks []models.ThinkingBlock
	var toolCalls []models.ToolCallResult
	for _, c := range anthResp.Content {
		switch c.Type {
		case "text":
			content += c.Text
		case "thinking":
			// Anthropic extended thinking block
			thinkingBlocks = append(thinkingBlocks, models.ThinkingBlock{
				Content:   c.Thinking,
				Signature: c.Signature,
				Model:     model,
				Provider:  provider.Name,
				Timestamp: time.Now().UTC(),
			})
		case "redacted_thinking":
			// Reasoning the API withheld: nothing to read, but it must be sent back with the tool result.
			thinkingBlocks = append(thinkingBlocks, models.ThinkingBlock{
				Content:   "[redacted by the provider]",
				Redacted:  true,
				Data:      c.Data,
				Model:     model,
				Provider:  provider.Name,
				Timestamp: time.Now().UTC(),
			})
		case "tool_use":
			// A forced structured-output call carries the answer as its
			// arguments — surface it as Content, exactly like OpenAI's native
			// response_format would, instead of as a tool call the executor
			// would otherwise try to dispatch to a nonexistent tool.
			if c.Name == anthropicStructuredOutputTool {
				argsJSON, _ := json.Marshal(c.Input)
				content += string(argsJSON)
				continue
			}
			// Anthropic tool_use block → convert to ToolCallResult
			argsJSON, _ := json.Marshal(c.Input)
			toolCalls = append(toolCalls, models.ToolCallResult{
				ID:   c.ID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{
					Name:      c.Name,
					Arguments: string(argsJSON),
				},
			})
		}
	}

	// Map Anthropic stop_reason to OpenAI-style finish_reason. A stop_reason of
	// "tool_use" whose only tool_use block was the synthetic structured-output
	// call is reported as "stop", not "tool_calls" — toolCalls is empty for
	// that case (its arguments went to Content instead), and a caller that
	// trusts finish_reason over the empty slice should see an ordinary final
	// answer, not a dangling signal that a tool call is still pending.
	finishReason := anthResp.StopReason
	if finishReason == "tool_use" {
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	} else if finishReason == "end_turn" {
		finishReason = "stop"
	}

	// Anthropic reports cached and cache-written prompt tokens apart from input_tokens. The usage the
	// router returns counts the whole prompt, with the cached parts broken out, so the cost is
	// computed from the parts and context and budget arithmetic sees the real prompt size.
	u := anthResp.Usage
	usage := models.TokenUsage{
		InputTokens:   u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens,
		OutputTokens:  u.OutputTokens, // includes the thinking tokens, which Anthropic does not report apart
		CachedTokens:  u.CacheReadTokens,
		CacheCreation: u.CacheCreationTokens,
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	if u.CacheReadTokens > 0 {
		usage.CacheHits = 1
	}
	mr.settle(provider, model, &usage)

	return &models.RouteResponse{
		ID:             anthResp.ID,
		Provider:       provider.Name,
		Model:          model,
		Content:        content,
		ThinkingBlocks: thinkingBlocks,
		FinishReason:   finishReason,
		ToolCalls:      toolCalls,
		Usage:          usage,
	}, nil
}

// ── Ollama Provider ─────────────────────────────────────────

func (mr *ModelRouter) callOllama(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	model := req.Model
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}

	oaiMsgs, err := mr.openAIMessages(ctx, provider, model, req.Messages)
	if err != nil {
		return nil, err
	}
	oaiReq := openAIRequest{Model: model, Messages: oaiMsgs, ResponseFormat: req.ResponseFormat}
	mr.applyOpenAIThinking(&oaiReq, provider, model, req)

	// Include tools if provided
	if len(req.Tools) > 0 {
		oaiReq.Tools = req.Tools
		if req.ToolChoice != nil {
			oaiReq.ToolChoice = req.ToolChoice
		} else {
			oaiReq.ToolChoice = "auto"
		}
	}

	body, _ := json.Marshal(oaiReq)

	url := endpoint + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := mr.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		// Ollama returns {"error": "model 'x' not found, try pulling it first"}
		var ollamaErr struct {
			Error string `json:"error"`
		}
		var msg error
		if json.Unmarshal(respBody, &ollamaErr) == nil && ollamaErr.Error != "" {
			msg = fmt.Errorf("ollama: %s", ollamaErr.Error)
		} else {
			msg = fmt.Errorf("ollama: status %d: %s", httpResp.StatusCode, string(respBody))
		}
		if retryableStatus(httpResp.StatusCode) {
			return nil, Retryable(httpResp.StatusCode, msg)
		}
		return nil, msg
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	content, finishReason, thinkingBlocks, toolCalls, usage := openAIResult(oaiResp, model, provider.Name)
	mr.settle(provider, model, &usage)

	return &models.RouteResponse{
		ID:             uuid.New().String(),
		Provider:       provider.Name,
		Model:          model,
		Content:        content,
		ThinkingBlocks: thinkingBlocks,
		FinishReason:   finishReason,
		ToolCalls:      toolCalls,
		Usage:          usage,
	}, nil
}

// ── Cost Tracking ───────────────────────────────────────────

func (mr *ModelRouter) trackCost(kitchen, agentRef string, resp *models.RouteResponse) {
	mr.costMu.Lock()
	defer mr.costMu.Unlock()

	key := kitchen
	if key == "" {
		key = "default"
	}

	summary, ok := mr.costs[key]
	if !ok {
		summary = &models.CostSummary{
			Period:     "session",
			ByAgent:    make(map[string]float64),
			ByModel:    make(map[string]float64),
			ByProvider: make(map[string]float64),
		}
		mr.costs[key] = summary
	}

	summary.TotalCostUSD += resp.Usage.EstimatedCost
	summary.TotalTokens += resp.Usage.TotalTokens

	if agentRef != "" {
		summary.ByAgent[agentRef] += resp.Usage.EstimatedCost
	}
	summary.ByModel[resp.Model] += resp.Usage.EstimatedCost
	summary.ByProvider[resp.Provider] += resp.Usage.EstimatedCost
}

// GetCostSummary returns the cost summary for a kitchen.
func (mr *ModelRouter) GetCostSummary(kitchen string) *models.CostSummary {
	mr.costMu.RLock()
	defer mr.costMu.RUnlock()

	if kitchen == "" {
		kitchen = "default"
	}

	summary, ok := mr.costs[kitchen]
	if !ok {
		return &models.CostSummary{
			Period:     "session",
			ByAgent:    make(map[string]float64),
			ByModel:    make(map[string]float64),
			ByProvider: make(map[string]float64),
		}
	}
	return summary
}

// ── Provider Test ───────────────────────────────────────────

// TestProvider performs a real credential-validating call to a provider.
// For openai/azure-openai/anthropic it sends a 1-token chat completion.
// For ollama it calls /api/tags to list available models.
// Returns a structured result with latency and error info.
func (mr *ModelRouter) TestProvider(ctx context.Context, provider *models.ModelProvider) *models.ProviderTestResult {
	tested := *provider // a copy: the key applied for the test is not left on the caller's provider
	mr.applyKey(&tested)
	provider = &tested
	result := &models.ProviderTestResult{
		Provider: provider.Name,
		Kind:     provider.Kind,
	}

	testCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	start := time.Now()

	switch provider.Kind {
	case "ollama":
		// For Ollama, just verify it's running and list models
		endpoint := provider.Endpoint
		if endpoint == "" {
			endpoint = "http://localhost:11434"
		}
		url := endpoint + "/api/tags"
		req, _ := http.NewRequestWithContext(testCtx, "GET", url, nil)
		resp, err := mr.clientFor(provider).Do(req)
		if err != nil {
			result.Error = fmt.Sprintf("ollama unreachable: %v", err)
			result.LatencyMs = time.Since(start).Milliseconds()
			return result
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			var ollamaErr struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(body, &ollamaErr) == nil && ollamaErr.Error != "" {
				result.Error = fmt.Sprintf("ollama: %s", ollamaErr.Error)
			} else {
				result.Error = fmt.Sprintf("ollama: status %d", resp.StatusCode)
			}
			result.LatencyMs = time.Since(start).Milliseconds()
			return result
		}

		// Parse Ollama tags response to confirm models exist
		var tagsResp struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
			result.Error = fmt.Sprintf("ollama: decode response: %v", err)
			result.LatencyMs = time.Since(start).Milliseconds()
			return result
		}

		result.Healthy = true
		result.LatencyMs = time.Since(start).Milliseconds()
		if len(tagsResp.Models) > 0 {
			result.Model = tagsResp.Models[0].Name
		}
		return result

	case "openai", "azure-openai", "anthropic":
		// Send a minimal 1-token chat completion to validate credentials
		model := ""
		if len(provider.Models) > 0 {
			model = provider.Models[0]
		} else {
			// Sensible defaults per kind
			switch provider.Kind {
			case "openai":
				model = "gpt-4o-mini"
			case "azure-openai":
				model = "gpt-4o-mini"
			case "anthropic":
				model = "claude-haiku-4-5"
			}
		}

		testMessages := []models.ChatMessage{
			{Role: "user", Content: "Say OK"},
		}

		// Build a minimal request with max_tokens=1 for the cheapest possible validation
		var resp *models.RouteResponse
		var err error

		switch provider.Kind {
		case "openai", "azure-openai":
			resp, err = mr.callOpenAITest(testCtx, provider, model)
		case "anthropic":
			resp, err = mr.callAnthropicTest(testCtx, provider, model)
		default:
			resp, err = mr.callOpenAITest(testCtx, provider, model)
		}
		_ = testMessages // used implicitly in callXxxTest

		result.LatencyMs = time.Since(start).Milliseconds()
		if err != nil {
			result.Error = err.Error()
			return result
		}

		result.Healthy = true
		result.Model = resp.Model
		return result

	default:
		// A kind with its own wire format (Gemini, Vertex, Bedrock, ...) is tested
		// by its driver. Sending it an OpenAI-style call, as every unknown kind used
		// to get, fails for a valid key — and a failed test burns the agent at bake.
		if d := mr.GetDriver(provider.Kind); d != nil && !openAICompatibleKinds[provider.Kind] {
			err := d.HealthCheck(testCtx, provider)
			result.LatencyMs = time.Since(start).Milliseconds()
			if err != nil {
				result.Error = err.Error()
				return result
			}
			result.Healthy = true
			if len(provider.Models) > 0 {
				result.Model = provider.Models[0]
			}
			return result
		}

		// Unknown provider kind — try OpenAI-compatible test
		model := ""
		if len(provider.Models) > 0 {
			model = provider.Models[0]
		}
		_, err := mr.callOpenAITest(testCtx, provider, model)
		result.LatencyMs = time.Since(start).Milliseconds()
		if err != nil {
			result.Error = err.Error()
			return result
		}
		result.Healthy = true
		result.Model = model
		return result
	}
}

// openAICompatibleKinds speak the OpenAI chat API, so TestProvider checks them
// with the same minimal chat call as OpenAI itself.
var openAICompatibleKinds = map[string]bool{"litellm": true, "openrouter": true}

// parseGoogleAPIError extracts a human-readable message from a Google API JSON
// error body. Falls back to the raw string if the body is not JSON.
func parseGoogleAPIError(prefix string, status int, body []byte, model string) error {
	var gErr struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &gErr) == nil && gErr.Error.Message != "" {
		if gErr.Error.Status == "NOT_FOUND" && model != "" {
			return fmt.Errorf("%s: model %q not found — check the model name in your provider config (e.g. gemini-2.0-flash)", prefix, model)
		}
		if gErr.Error.Status != "" {
			return fmt.Errorf("%s: status %d: %s (%s)", prefix, status, gErr.Error.Message, gErr.Error.Status)
		}
		return fmt.Errorf("%s: status %d: %s", prefix, status, gErr.Error.Message)
	}
	return fmt.Errorf("%s: status %d: %s", prefix, status, string(body))
}

// parseOpenAIError extracts a human-readable message from an OpenAI-compatible JSON error body.
// Falls back to the raw string if the body is not JSON or has no message.
func parseOpenAIError(prefix string, status int, body []byte, model string) error {
	var apiErr struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	var msg error
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
		if apiErr.Error.Code == "model_not_found" || (status == 404 && model != "") {
			// Never retryable: the model name itself is wrong.
			return fmt.Errorf("%s: model %q not found — check the model name in your provider config", prefix, model)
		}
		if apiErr.Error.Code != "" {
			msg = fmt.Errorf("%s: status %d: %s (%s)", prefix, status, apiErr.Error.Message, apiErr.Error.Code)
		} else {
			msg = fmt.Errorf("%s: status %d: %s", prefix, status, apiErr.Error.Message)
		}
	} else {
		msg = fmt.Errorf("%s: status %d: %s", prefix, status, string(body))
	}
	if retryableStatus(status) {
		return Retryable(status, msg)
	}
	return msg
}

// parseAnthropicError extracts a human-readable message from an Anthropic JSON error body.
func parseAnthropicError(prefix string, status int, body []byte, model string) error {
	var apiErr struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
		if apiErr.Error.Type == "not_found_error" {
			return fmt.Errorf("%s: model %q not found — check the model name (e.g. claude-opus-4-7, claude-sonnet-4-6, claude-haiku-4-5). Note: Claude 4.6+ uses dateless IDs — do not append a date suffix", prefix, model)
		}
		if retryableStatus(status) {
			return Retryable(status, fmt.Errorf("%s: status %d: %s (%s)", prefix, status, apiErr.Error.Message, apiErr.Error.Type))
		}
		return fmt.Errorf("%s: status %d: %s (%s)", prefix, status, apiErr.Error.Message, apiErr.Error.Type)
	}
	if retryableStatus(status) {
		return Retryable(status, fmt.Errorf("%s: status %d: %s", prefix, status, string(body)))
	}
	return fmt.Errorf("%s: status %d: %s", prefix, status, string(body))
}

// callOpenAITest sends a minimal 1-token request to validate OpenAI/Azure credentials.
func (mr *ModelRouter) callOpenAITest(ctx context.Context, provider *models.ModelProvider, model string) (*models.RouteResponse, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}

	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("api_key not configured for provider %s", provider.Name)
	}

	// Newer OpenAI models (gpt-4o, gpt-5, o-series) require max_completion_tokens
	// instead of max_tokens. We send max_completion_tokens for OpenAI (not Azure)
	// and fall back to max_tokens for Azure and older models.
	type testReq struct {
		Model               string               `json:"model"`
		Messages            []models.ChatMessage `json:"messages"`
		MaxTokens           *int                 `json:"max_tokens,omitempty"`
		MaxCompletionTokens *int                 `json:"max_completion_tokens,omitempty"`
	}

	limit := 200
	req := testReq{
		Model:    model,
		Messages: []models.ChatMessage{{Role: "user", Content: "Say OK"}},
	}
	if provider.Kind == "azure-openai" {
		req.MaxTokens = &limit
	} else {
		req.MaxCompletionTokens = &limit
	}

	body, _ := json.Marshal(req)

	url := endpoint + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	if provider.Kind == "azure-openai" {
		httpReq.Header.Set("api-key", apiKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpResp, err := mr.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		errStr := string(respBody)
		// A max_tokens truncation error means the model IS reachable — treat as healthy
		if httpResp.StatusCode == 400 && (strings.Contains(errStr, "max_tokens") || strings.Contains(errStr, "max_completion_tokens") || strings.Contains(errStr, "max_output_tokens")) {
			return &models.RouteResponse{Provider: provider.Name, Model: model}, nil
		}
		// Extract human-readable message from OpenAI-compatible JSON error bodies
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			if apiErr.Error.Code != "" {
				return nil, fmt.Errorf("status %d: %s (%s)", httpResp.StatusCode, apiErr.Error.Message, apiErr.Error.Code)
			}
			return nil, fmt.Errorf("status %d: %s", httpResp.StatusCode, apiErr.Error.Message)
		}
		return nil, fmt.Errorf("status %d: %s", httpResp.StatusCode, errStr)
	}

	return &models.RouteResponse{Provider: provider.Name, Model: model}, nil
}

// callAnthropicTest sends a minimal 1-token request to validate Anthropic credentials.
func (mr *ModelRouter) callAnthropicTest(ctx context.Context, provider *models.ModelProvider, model string) (*models.RouteResponse, error) {
	model = normalizeAnthropicModel(model)
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.anthropic.com"
	}
	// Normalize: strip trailing slash and /v1 suffix so we can always append /v1/...
	endpoint = strings.TrimRight(endpoint, "/")
	endpoint = strings.TrimSuffix(endpoint, "/v1")

	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("api_key not configured for provider %s", provider.Name)
	}

	type testReq struct {
		Model     string               `json:"model"`
		Messages  []models.ChatMessage `json:"messages"`
		MaxTokens int                  `json:"max_tokens"`
	}

	body, _ := json.Marshal(testReq{
		Model:     model,
		Messages:  []models.ChatMessage{{Role: "user", Content: "Say OK"}},
		MaxTokens: 1,
	})

	url := endpoint + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := mr.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		// Extract human-readable message from Anthropic JSON error bodies
		var apiErr struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			if apiErr.Error.Type == "not_found_error" {
				return nil, fmt.Errorf("model %q not found — check the model name (e.g. claude-opus-4-7, claude-sonnet-4-6, claude-haiku-4-5). Note: Claude 4.6+ uses dateless IDs — do not append a date suffix", model)
			}
			return nil, fmt.Errorf("status %d: %s (%s)", httpResp.StatusCode, apiErr.Error.Message, apiErr.Error.Type)
		}
		return nil, fmt.Errorf("status %d: %s", httpResp.StatusCode, string(respBody))
	}

	return &models.RouteResponse{Provider: provider.Name, Model: model}, nil
}

// SetCallCapturer installs the hook that receives every routed call, complete. Pass nil to remove it.
// It is set once at start-up, before the router serves a request.
func (mr *ModelRouter) SetCallCapturer(c models.LLMCallCapturer) { mr.capturer = c }

// CallCapturer is the installed hook, so the executor, which records its own spans, can use the same one.
func (mr *ModelRouter) CallCapturer() models.LLMCallCapturer { return mr.capturer }

// finishRoute records what a routed request did: one trace, with a root span for the request and a
// child span for every provider tried (the failures with their errors, the one that answered with its
// model, usage, cost, the model's reasoning and its answer). The executor passes ContextSkipTrace,
// since it records richer spans of its own, and gets the attempts on the response to add to them.
func (mr *ModelRouter) finishRoute(ctx context.Context, req *models.RouteRequest, resp *models.RouteResponse, attempts []models.RouteAttempt, started time.Time, routeErr error) {
	if resp != nil {
		resp.Attempts = attempts
	}
	if shouldSkipTrace(ctx) {
		return
	}

	kitchen := req.Kitchen
	if kitchen == "" {
		kitchen = "default"
	}
	agentName := req.AgentRef
	if agentName == "" {
		agentName = "router"
	}
	now := time.Now().UTC()
	total := now.Sub(started.UTC()).Milliseconds()
	traceID := uuid.New().String()
	rootID := uuid.New().String()

	strategy := req.Strategy
	if strategy == "" {
		strategy = models.RoutingFallback
	}
	input := lastUserText(req.Messages)

	trace := &models.Trace{
		ID:         traceID,
		AgentName:  agentName,
		Kitchen:    kitchen,
		Status:     "completed",
		DurationMs: total,
		InputText:  input,
		CreatedAt:  now,
		Metadata: map[string]interface{}{
			"strategy": strategy,
			"model":    req.Model,
			"attempts": len(attempts),
		},
	}
	for k, v := range req.Labels {
		trace.Metadata[k] = v
		if k == "purpose" {
			trace.Tags = append(trace.Tags, v)
		}
	}
	rootStatus, rootErr := "completed", ""
	if routeErr != nil {
		trace.Status, rootStatus, rootErr = "failed", "failed", routeErr.Error()
		trace.Metadata["error"] = rootErr
	}

	inJSON, _ := json.Marshal(req.Messages)
	spans := []models.Span{{
		ID: rootID, TraceID: traceID, Name: "router.route", Kind: models.SpanKindChain, Status: rootStatus,
		StartTime: started.UTC(), EndTime: now, DurationMs: total, Error: rootErr,
		Input: inJSON,
		Metadata: map[string]interface{}{
			"strategy": strategy, "requested_model": req.Model, "attempts": len(attempts),
			"thinking_enabled": req.ThinkingEnabled,
		},
	}}
	for k, v := range req.Labels {
		spans[0].Metadata[k] = v
	}

	// Each provider tried is a span, in order: the failed ones, then the one that answered.
	cursor := started.UTC()
	var calls []models.LLMCall
	for _, a := range attempts {
		end := cursor.Add(time.Duration(a.LatencyMs) * time.Millisecond)
		attemptID := uuid.New().String()
		calls = append(calls, models.LLMCall{SpanID: attemptID, StartedAt: cursor, Error: a.Error})
		spans = append(spans, models.Span{
			ID: attemptID, TraceID: traceID, ParentSpanID: rootID,
			Name: "llm." + a.Provider, Kind: models.SpanKindLLM, Status: "failed",
			StartTime: cursor, EndTime: end, DurationMs: a.LatencyMs,
			Model: a.Model, Provider: a.Provider, Error: a.Error,
			Metadata: map[string]interface{}{"fallback": true},
		})
		cursor = end
	}

	if resp != nil {
		trace.OutputText = resp.Content
		trace.TotalTokens = resp.Usage.TotalTokens
		trace.CostUSD = resp.Usage.EstimatedCost
		trace.ThinkingBlocks = resp.ThinkingBlocks
		usage := resp.Usage
		trace.Usage = &usage
		trace.Metadata["provider"] = resp.Provider
		trace.Metadata["model"] = resp.Model
		trace.Metadata["strategy"] = resp.Strategy
		trace.Metadata["thinking_tokens"] = resp.Usage.ThinkingTokens
		trace.Metadata["price_source"] = resp.Usage.PriceSource

		out, _ := json.Marshal(map[string]interface{}{
			"content":         resp.Content,
			"thinking_blocks": resp.ThinkingBlocks,
			"tool_calls":      resp.ToolCalls,
			"finish_reason":   resp.FinishReason,
		})
		end := cursor.Add(time.Duration(resp.LatencyMs) * time.Millisecond)
		if end.After(now) {
			end = now
		}
		answerID := uuid.New().String()
		calls = append(calls, models.LLMCall{SpanID: answerID, StartedAt: cursor, Response: resp})
		spans = append(spans, models.Span{
			ID: answerID, TraceID: traceID, ParentSpanID: rootID,
			Name: "llm." + resp.Provider, Kind: models.SpanKindLLM, Status: "completed",
			StartTime: cursor, EndTime: end, DurationMs: resp.LatencyMs,
			Input: inJSON, Output: out, Usage: &usage, Model: resp.Model, Provider: resp.Provider,
			Metadata: map[string]interface{}{
				"finish_reason":   resp.FinishReason,
				"thinking_blocks": len(resp.ThinkingBlocks),
				"thinking_tokens": resp.Usage.ThinkingTokens,
				"cached_tokens":   resp.Usage.CachedTokens,
				"price_source":    resp.Usage.PriceSource,
			},
		})
		spans[0].Usage = &usage
		spans[0].Output = out
	}

	if err := mr.store.CreateTrace(ctx, trace); err != nil {
		log.Warn().Err(err).Msg("Failed to record trace for routed request")
		return
	}
	if err := mr.store.CreateSpans(ctx, spans); err != nil {
		log.Warn().Err(err).Str("trace_id", traceID).Msg("Failed to record spans for routed request")
		return
	}
	if mr.capturer != nil {
		for _, c := range calls {
			c.TraceID, c.Kitchen, c.AgentName, c.Request = traceID, kitchen, agentName, req
			if err := mr.capturer.CaptureLLMCall(ctx, c); err != nil {
				log.Warn().Err(err).Str("trace_id", traceID).Str("span_id", c.SpanID).Msg("Failed to capture routed call")
			}
		}
	}
}

// lastUserText is the text of the last user message, what a trace shows as the request.
func lastUserText(msgs []models.ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// ── Cost Helpers ────────────────────────────────────────────

// Known cost per 1K tokens (USD) — sensible defaults (fallback when catalog is unavailable)
var defaultCosts = map[string]map[string]float64{
	"gpt-4o":                    {"input": 0.0025, "output": 0.01},
	"gpt-4o-mini":               {"input": 0.00015, "output": 0.0006},
	"gpt-4-turbo":               {"input": 0.01, "output": 0.03},
	"claude-sonnet-4-20250514":  {"input": 0.003, "output": 0.015},
	"claude-3-5-haiku-20241022": {"input": 0.001, "output": 0.005},
	"claude-opus-4-20250514":    {"input": 0.015, "output": 0.075},
	// Gemini
	"gemini-2.5-pro":        {"input": 0.00125, "output": 0.01},
	"gemini-2.5-flash":      {"input": 0.00015, "output": 0.0006},
	"gemini-2.0-flash":      {"input": 0.0001, "output": 0.0004},
	"gemini-2.0-flash-lite": {"input": 0.000075, "output": 0.0003},
}

// getProviderCostPer1K is what to rank a provider by under cost-optimised routing: the price of the
// model being asked for (input plus output) when there is one, else of the provider's first model.
// Never an average over the provider's models, which would rank a provider by models nobody asked for.
func (mr *ModelRouter) getProviderCostPer1K(provider models.ModelProvider, requestedModel string) float64 {
	model := requestedModel
	if model == "" && len(provider.Models) > 0 {
		model = provider.Models[0]
	}
	if model == "" {
		return 1.0
	}
	p := mr.priceOf(&provider, model)
	return p.InputPer1K + p.OutputPer1K
}

// Ensure rand is seeded (Go 1.20+ does this automatically)
var _ = rand.Int

// ══════════════════════════════════════════════════════════════
// ── Built-in Provider Drivers ────────────────────────────────
// ══════════════════════════════════════════════════════════════

// registerBuiltinDrivers registers the OSS community drivers.
func (mr *ModelRouter) registerBuiltinDrivers() {
	mr.RegisterDriver(&OpenAIDriver{router: mr})
	mr.RegisterDriver(&AzureOpenAIDriver{router: mr})
	mr.RegisterDriver(&AnthropicDriver{router: mr})
	mr.RegisterDriver(&OllamaDriver{router: mr})
	mr.RegisterDriver(&GeminiDriver{router: mr})
	mr.RegisterDriver(&LiteLLMDriver{router: mr})
	mr.RegisterDriver(&OpenRouterDriver{router: mr})
}

// ── OpenAI Driver ───────────────────────────────────────────

type OpenAIDriver struct{ router *ModelRouter }

func (d *OpenAIDriver) Kind() string { return "openai" }

func (d *OpenAIDriver) Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	return d.router.callOpenAI(ctx, provider, req)
}

func (d *OpenAIDriver) HealthCheck(ctx context.Context, provider *models.ModelProvider) error {
	model := ""
	if len(provider.Models) > 0 {
		model = provider.Models[0]
	} else {
		model = "gpt-4o-mini"
	}
	_, err := d.router.callOpenAITest(ctx, provider, model)
	return err
}

// ── OpenAI Model Discovery ──────────────────────────────────

func (d *OpenAIDriver) DiscoverModels(ctx context.Context, provider *models.ModelProvider) ([]models.DiscoveredModel, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}
	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("openai discover: api_key not configured")
	}

	url := endpoint + "/models"
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	httpResp, err := d.router.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai discover: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("openai discover: status %d: %s", httpResp.StatusCode, string(body))
	}

	var resp struct {
		Data []struct {
			ID        string `json:"id"`
			OwnedBy   string `json:"owned_by"`
			CreatedAt int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("openai discover: decode: %w", err)
	}

	var result []models.DiscoveredModel
	for _, m := range resp.Data {
		result = append(result, models.DiscoveredModel{
			ID:        m.ID,
			Provider:  provider.Name,
			Kind:      "openai",
			OwnedBy:   m.OwnedBy,
			CreatedAt: m.CreatedAt,
		})
	}
	return result, nil
}

// Compile-time assertion: OpenAIDriver implements ModelDiscoveryDriver.
var _ ModelDiscoveryDriver = (*OpenAIDriver)(nil)

// ── OpenAI Embedding Capability ─────────────────────────────

func (d *OpenAIDriver) EmbeddingModels() []EmbeddingModelInfo {
	return []EmbeddingModelInfo{
		{Model: "text-embedding-3-small", Dimensions: 1536, MaxBatch: 2048},
		{Model: "text-embedding-3-large", Dimensions: 3072, MaxBatch: 2048},
		{Model: "text-embedding-ada-002", Dimensions: 1536, MaxBatch: 2048},
	}
}

func (d *OpenAIDriver) Embed(ctx context.Context, provider *models.ModelProvider, model string, texts []string) ([][]float64, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}
	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("openai embed: api_key not configured for provider %s", provider.Name)
	}

	type embedReq struct {
		Input []string `json:"input"`
		Model string   `json:"model"`
	}
	type embedData struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	}
	type embedResp struct {
		Data  []embedData `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	body, _ := json.Marshal(embedReq{Input: texts, Model: model})
	url := endpoint + "/embeddings"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai embed: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	httpResp, err := d.router.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai embed: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai embed: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var result embedResp
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("openai embed: unmarshal: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("openai embed: %s", result.Error.Message)
	}

	vectors := make([][]float64, len(texts))
	for _, d := range result.Data {
		if d.Index < len(vectors) {
			vectors[d.Index] = d.Embedding
		}
	}
	return vectors, nil
}

// Compile-time assertion: OpenAIDriver implements EmbeddingCapableDriver.
var _ EmbeddingCapableDriver = (*OpenAIDriver)(nil)

// ── Azure OpenAI Driver ─────────────────────────────────────

type AzureOpenAIDriver struct{ router *ModelRouter }

func (d *AzureOpenAIDriver) Kind() string { return "azure-openai" }

func (d *AzureOpenAIDriver) Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	return d.router.callOpenAI(ctx, provider, req)
}

func (d *AzureOpenAIDriver) HealthCheck(ctx context.Context, provider *models.ModelProvider) error {
	model := ""
	if len(provider.Models) > 0 {
		model = provider.Models[0]
	} else {
		model = "gpt-4o-mini"
	}
	_, err := d.router.callOpenAITest(ctx, provider, model)
	return err
}

// ── Azure OpenAI Embedding Capability ───────────────────────

func (d *AzureOpenAIDriver) EmbeddingModels() []EmbeddingModelInfo {
	// Azure OpenAI deployments are custom; return common defaults.
	// Users deploy specific models to their Azure OpenAI resource.
	return []EmbeddingModelInfo{
		{Model: "text-embedding-3-small", Dimensions: 1536, MaxBatch: 2048},
		{Model: "text-embedding-3-large", Dimensions: 3072, MaxBatch: 2048},
		{Model: "text-embedding-ada-002", Dimensions: 1536, MaxBatch: 2048},
	}
}

func (d *AzureOpenAIDriver) Embed(ctx context.Context, provider *models.ModelProvider, model string, texts []string) ([][]float64, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		return nil, fmt.Errorf("azure-openai embed: endpoint required")
	}
	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("azure-openai embed: api_key not configured for provider %s", provider.Name)
	}

	type embedReq struct {
		Input []string `json:"input"`
	}
	type embedData struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	}
	type embedResp struct {
		Data  []embedData `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	body, _ := json.Marshal(embedReq{Input: texts})

	// Azure OpenAI uses deployment-based URLs:
	// {endpoint}/openai/deployments/{model}/embeddings?api-version=2024-02-01
	apiVersion := "2024-02-01"
	if v, ok := provider.Config["api_version"].(string); ok && v != "" {
		apiVersion = v
	}
	url := fmt.Sprintf("%s/openai/deployments/%s/embeddings?api-version=%s", endpoint, model, apiVersion)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("azure-openai embed: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-key", apiKey)

	httpResp, err := d.router.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("azure-openai embed: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("azure-openai embed: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var result embedResp
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("azure-openai embed: unmarshal: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("azure-openai embed: %s", result.Error.Message)
	}

	vectors := make([][]float64, len(texts))
	for _, d := range result.Data {
		if d.Index < len(vectors) {
			vectors[d.Index] = d.Embedding
		}
	}
	return vectors, nil
}

// Compile-time assertion: AzureOpenAIDriver implements EmbeddingCapableDriver.
var _ EmbeddingCapableDriver = (*AzureOpenAIDriver)(nil)

// ── Anthropic Driver ────────────────────────────────────────

type AnthropicDriver struct{ router *ModelRouter }

func (d *AnthropicDriver) Kind() string { return "anthropic" }

func (d *AnthropicDriver) Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	return d.router.callAnthropic(ctx, provider, req)
}

func (d *AnthropicDriver) HealthCheck(ctx context.Context, provider *models.ModelProvider) error {
	model := ""
	if len(provider.Models) > 0 {
		model = provider.Models[0]
	} else {
		model = "claude-haiku-4-5"
	}
	_, err := d.router.callAnthropicTest(ctx, provider, model)
	return err
}

// ── Anthropic Model Discovery ───────────────────────────────

// DiscoverModels queries Anthropic's GET /v1/models endpoint to return the
// list of models available to the configured API key.
func (d *AnthropicDriver) DiscoverModels(ctx context.Context, provider *models.ModelProvider) ([]models.DiscoveredModel, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.anthropic.com"
	}
	// Normalize: strip trailing slash and /v1 suffix so we can always append /v1/...
	endpoint = strings.TrimRight(endpoint, "/")
	endpoint = strings.TrimSuffix(endpoint, "/v1")
	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("anthropic discover: api_key not configured")
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := d.router.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic discover: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("anthropic discover: status %d: %s", httpResp.StatusCode, string(body))
	}

	var resp struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			CreatedAt   string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("anthropic discover: decode: %w", err)
	}

	result := make([]models.DiscoveredModel, 0, len(resp.Data))
	for _, m := range resp.Data {
		md := map[string]string{}
		if m.DisplayName != "" {
			md["display_name"] = m.DisplayName
		}
		result = append(result, models.DiscoveredModel{
			ID:       m.ID,
			Provider: provider.Name,
			Kind:     "anthropic",
			Metadata: md,
		})
	}
	return result, nil
}

// Compile-time assertion: AnthropicDriver implements ModelDiscoveryDriver.
var _ ModelDiscoveryDriver = (*AnthropicDriver)(nil)

// ── Ollama Driver ───────────────────────────────────────────

type OllamaDriver struct{ router *ModelRouter }

func (d *OllamaDriver) Kind() string { return "ollama" }

func (d *OllamaDriver) Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	return d.router.callOllama(ctx, provider, req)
}

func (d *OllamaDriver) HealthCheck(ctx context.Context, provider *models.ModelProvider) error {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	url := endpoint + "/api/tags"
	httpReq, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := d.router.clientFor(provider).Do(httpReq)
	if err != nil {
		return fmt.Errorf("ollama unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama: status %d", resp.StatusCode)
	}
	return nil
}

// ── Ollama Model Discovery ──────────────────────────────────

func (d *OllamaDriver) DiscoverModels(ctx context.Context, provider *models.ModelProvider) ([]models.DiscoveredModel, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	url := endpoint + "/api/tags"
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	httpResp, err := d.router.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama discover: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama discover: status %d", httpResp.StatusCode)
	}

	var resp struct {
		Models []struct {
			Name       string `json:"name"`
			ModifiedAt string `json:"modified_at"`
			Size       int64  `json:"size"`
		} `json:"models"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("ollama discover: decode: %w", err)
	}

	var result []models.DiscoveredModel
	for _, m := range resp.Models {
		result = append(result, models.DiscoveredModel{
			ID:       m.Name,
			Provider: provider.Name,
			Kind:     "ollama",
			Metadata: map[string]string{"size": fmt.Sprintf("%d", m.Size)},
		})
	}
	return result, nil
}

// Compile-time assertion: OllamaDriver implements ModelDiscoveryDriver.
var _ ModelDiscoveryDriver = (*OllamaDriver)(nil)

// ── Ollama Embedding Capability ─────────────────────────────

func (d *OllamaDriver) EmbeddingModels() []EmbeddingModelInfo {
	return []EmbeddingModelInfo{
		{Model: "nomic-embed-text", Dimensions: 768, MaxBatch: 512},
		{Model: "mxbai-embed-large", Dimensions: 1024, MaxBatch: 512},
		{Model: "all-minilm", Dimensions: 384, MaxBatch: 512},
		{Model: "snowflake-arctic-embed", Dimensions: 1024, MaxBatch: 512},
	}
}

func (d *OllamaDriver) Embed(ctx context.Context, provider *models.ModelProvider, model string, texts []string) ([][]float64, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}

	type embedReq struct {
		Model string `json:"model"`
		Input any    `json:"input"`
	}
	type embedResp struct {
		Embeddings [][]float64 `json:"embeddings"`
	}

	body, _ := json.Marshal(embedReq{Model: model, Input: texts})
	url := endpoint + "/api/embed"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embed: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := d.router.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var result embedResp
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("ollama embed: unmarshal: %w", err)
	}
	if len(result.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embed: expected %d embeddings, got %d", len(texts), len(result.Embeddings))
	}
	return result.Embeddings, nil
}

// Compile-time assertion: OllamaDriver implements EmbeddingCapableDriver.
var _ EmbeddingCapableDriver = (*OllamaDriver)(nil)

// ══════════════════════════════════════════════════════════════
// ── LiteLLM Proxy Driver ─────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// LiteLLMDriver forwards requests to a user's LiteLLM proxy using the
// OpenAI-compatible API format. This lets AgentOven users who already run
// LiteLLM get all the AgentOven value-add (lifecycle, versioning, recipes,
// observability) on top of their existing proxy.
//
// Provider config:
//
//	{
//	  "kind": "litellm",
//	  "endpoint": "http://localhost:4000",
//	  "config": {"api_key": "sk-optional-litellm-key"}
//	}
type LiteLLMDriver struct{ router *ModelRouter }

func (d *LiteLLMDriver) Kind() string { return "litellm" }

func (d *LiteLLMDriver) Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	// LiteLLM exposes an OpenAI-compatible /chat/completions endpoint
	return d.router.callOpenAI(ctx, provider, req)
}

func (d *LiteLLMDriver) HealthCheck(ctx context.Context, provider *models.ModelProvider) error {
	endpoint := provider.Endpoint
	if endpoint == "" {
		return fmt.Errorf("litellm: endpoint required (e.g. http://localhost:4000)")
	}

	// LiteLLM exposes GET /health
	url := endpoint + "/health"
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := d.router.clientFor(provider).Do(httpReq)
	if err != nil {
		return fmt.Errorf("litellm unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return parseOpenAIError("litellm", resp.StatusCode, body, "")
	}
	return nil
}

// ── LiteLLM Model Discovery ─────────────────────────────────

func (d *LiteLLMDriver) DiscoverModels(ctx context.Context, provider *models.ModelProvider) ([]models.DiscoveredModel, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		return nil, fmt.Errorf("litellm: endpoint required")
	}

	// LiteLLM exposes OpenAI-compatible GET /v1/models
	url := endpoint + "/v1/models"
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpResp, err := d.router.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("litellm discover: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("litellm discover: status %d: %s", httpResp.StatusCode, string(body))
	}

	var resp struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("litellm discover: decode: %w", err)
	}

	var result []models.DiscoveredModel
	for _, m := range resp.Data {
		result = append(result, models.DiscoveredModel{
			ID:       m.ID,
			Provider: provider.Name,
			Kind:     "litellm",
			OwnedBy:  m.OwnedBy,
		})
	}
	return result, nil
}

// Compile-time assertions for LiteLLMDriver.
var (
	_ ProviderDriver       = (*LiteLLMDriver)(nil)
	_ ModelDiscoveryDriver = (*LiteLLMDriver)(nil)
)

// ── OpenRouter Driver ───────────────────────────────────────
//
// OpenRouter is an OpenAI-compatible gateway that routes to 200+ models.
// It uses the same /chat/completions wire format as OpenAI but:
//   - Base URL is https://openrouter.ai/api/v1
//   - Model names use "provider/model" convention (e.g. "openai/gpt-4.1")
//   - Optionally sends HTTP-Referer + X-Title headers for attribution
//
// Provider config:
//
//	{
//	  "kind": "openrouter",
//	  "endpoint": "https://openrouter.ai/api/v1",
//	  "config": {
//	    "api_key": "sk-or-...",
//	    "http_referer": "https://your-app.com",   // optional
//	    "x_title": "Your App Name"                // optional
//	  }
//	}
type OpenRouterDriver struct{ router *ModelRouter }

func (d *OpenRouterDriver) Kind() string { return "openrouter" }

func (d *OpenRouterDriver) Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	// Inject OpenRouter attribution headers via provider config override.
	// callOpenAI respects provider.Endpoint and provider.Config["api_key"].
	// For HTTP-Referer and X-Title we piggyback on the existing extra-headers
	// convention by cloning the provider and adding them.
	p := *provider // shallow copy — safe to mutate local fields
	if p.Endpoint == "" {
		p.Endpoint = "https://openrouter.ai/api/v1"
	}
	if p.Config == nil {
		p.Config = map[string]interface{}{}
	}
	// Copy config so we don't mutate the shared provider object.
	cfg := make(map[string]interface{}, len(provider.Config))
	for k, v := range provider.Config {
		cfg[k] = v
	}
	if _, ok := cfg["http_referer"]; !ok {
		cfg["http_referer"] = "https://agentoven.dev"
	}
	if _, ok := cfg["x_title"]; !ok {
		cfg["x_title"] = "AgentOven"
	}
	p.Config = cfg
	return d.router.callOpenAI(ctx, &p, req)
}

func (d *OpenRouterDriver) HealthCheck(ctx context.Context, provider *models.ModelProvider) error {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://openrouter.ai/api/v1"
	}
	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return fmt.Errorf("openrouter: api_key not configured")
	}
	// OpenRouter exposes GET /api/v1/auth/key to validate a key.
	url := endpoint + "/auth/key"
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := d.router.clientFor(provider).Do(httpReq)
	if err != nil {
		return fmt.Errorf("openrouter unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return parseOpenAIError("openrouter", resp.StatusCode, body, "")
	}
	return nil
}

func (d *OpenRouterDriver) DiscoverModels(ctx context.Context, provider *models.ModelProvider) ([]models.DiscoveredModel, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://openrouter.ai/api/v1"
	}
	apiKey, _ := provider.Config["api_key"].(string)

	// OpenRouter's model list is at GET /api/v1/models — no auth required but
	// the key filters to models the account can use.
	url := endpoint + "/models"
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpResp, err := d.router.clientFor(provider).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter discover: %w", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("openrouter discover: status %d: %s", httpResp.StatusCode, string(body))
	}

	var resp struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"data"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("openrouter discover: decode: %w", err)
	}

	result := make([]models.DiscoveredModel, 0, len(resp.Data))
	for _, m := range resp.Data {
		result = append(result, models.DiscoveredModel{
			ID:       m.ID,
			Provider: provider.Name,
			Kind:     "openrouter",
		})
	}
	return result, nil
}

// Compile-time assertions for OpenRouterDriver.
var (
	_ ProviderDriver       = (*OpenRouterDriver)(nil)
	_ ModelDiscoveryDriver = (*OpenRouterDriver)(nil)
)

// normalizeAnthropicModel strips the trailing date suffix from Claude model IDs.
// Claude 4.6+ uses dateless IDs (e.g. "claude-sonnet-4-6"), but older stored
// agents may have date-suffixed names like "claude-opus-4-7-20260416". Anthropic's
// API rejects the date-suffixed form, so we strip "-YYYYMMDD" before calling.
func normalizeAnthropicModel(model string) string {
	// A date suffix is a "-" followed by exactly 8 ASCII digits at the end.
	if len(model) > 9 {
		suffix := model[len(model)-9:] // e.g. "-20260416"
		if suffix[0] == '-' {
			digits := suffix[1:]
			allDigits := true
			for _, c := range digits {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return model[:len(model)-9]
			}
		}
	}
	return model
}
