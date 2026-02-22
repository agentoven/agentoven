// Package catalog provides a live model capability database for AgentOven.
//
// The catalog merges three data sources:
//
//  1. **LiteLLM enrichment** — MIT-licensed model_prices_and_context_window.json
//     from github.com/BerriAI/litellm. Vendored locally and auto-refreshed
//     every 24 hours (configurable via AGENTOVEN_CATALOG_REFRESH_INTERVAL).
//
//  2. **Provider discovery** — live queries to provider model-listing APIs
//     (OpenAI GET /v1/models, Anthropic GET /v1/models, Ollama GET /api/tags).
//     Triggered on provider create/update and periodically.
//
//  3. **Manual overrides** — users can set per-model capabilities via API.
//
// The catalog exposes a thread-safe in-memory lookup used by the Model Router
// to enrich requests (choose correct token param, enforce context limits) and
// by the dashboard to show model capability badges.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

const (
	// LiteLLM model pricing data (MIT licensed, maintained by BerriAI).
	litellmURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

	// Default refresh interval for fetching updated catalog data.
	defaultRefreshInterval = 24 * time.Hour

	// Local cache file for offline operation.
	defaultCacheFile = "model_catalog_cache.json"
)

// Catalog is a thread-safe, auto-refreshing model capability database.
type Catalog struct {
	mu       sync.RWMutex
	models   map[string]*models.ModelCapability // key: "provider/model" or model_id

	client   *http.Client
	cacheDir string
	stopCh   chan struct{}
	running  bool
}

// NewCatalog creates a new model catalog.
// Call Start() to begin background refresh.
func NewCatalog(cacheDir string) *Catalog {
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".agentoven")
	}
	return &Catalog{
		models:   make(map[string]*models.ModelCapability),
		client:   &http.Client{Timeout: 30 * time.Second},
		cacheDir: cacheDir,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the background refresh goroutine.
func (c *Catalog) Start(ctx context.Context) {
	if c.running {
		return
	}
	c.running = true

	// Load from local cache first (instant startup)
	if err := c.loadCache(); err != nil {
		log.Debug().Err(err).Msg("Catalog: no local cache, will fetch fresh data")
	}

	// Load built-in defaults
	c.loadBuiltinDefaults()

	// Fetch fresh data on startup
	go func() {
		if err := c.fetchLiteLLMData(ctx); err != nil {
			log.Warn().Err(err).Msg("Catalog: failed to fetch LiteLLM data on startup")
		}
	}()

	// Background refresh loop
	interval := defaultRefreshInterval
	if v := os.Getenv("AGENTOVEN_CATALOG_REFRESH_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			interval = d
		}
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := c.fetchLiteLLMData(ctx); err != nil {
					log.Warn().Err(err).Msg("Catalog: refresh failed")
				}
			case <-c.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	log.Info().Dur("refresh_interval", interval).Msg("Model catalog started")
}

// Stop halts the background refresh.
func (c *Catalog) Stop() {
	if c.running {
		close(c.stopCh)
		c.running = false
	}
}

// Refresh forces an immediate re-fetch of the LiteLLM catalog data.
func (c *Catalog) Refresh(ctx context.Context) error {
	return c.fetchLiteLLMData(ctx)
}

// Lookup returns the capability data for a model.
// Tries: "provider/model", then just "model".
func (c *Catalog) Lookup(providerKind, modelName string) *models.ModelCapability {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Try provider-qualified key first
	if cap, ok := c.models[providerKind+"/"+modelName]; ok {
		return cap
	}
	// Try just model name
	if cap, ok := c.models[modelName]; ok {
		return cap
	}
	return nil
}

// LookupByID returns capability data by canonical model_id.
func (c *Catalog) LookupByID(modelID string) *models.ModelCapability {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.models[modelID]
}

// ListAll returns all known model capabilities.
func (c *Catalog) ListAll() []*models.ModelCapability {
	c.mu.RLock()
	defer c.mu.RUnlock()

	seen := make(map[string]bool)
	var result []*models.ModelCapability
	for _, cap := range c.models {
		if !seen[cap.ModelID] {
			seen[cap.ModelID] = true
			result = append(result, cap)
		}
	}
	return result
}

// ListByProvider returns models for a specific provider kind.
func (c *Catalog) ListByProvider(providerKind string) []*models.ModelCapability {
	c.mu.RLock()
	defer c.mu.RUnlock()

	seen := make(map[string]bool)
	var result []*models.ModelCapability
	for _, cap := range c.models {
		if cap.ProviderKind == providerKind && !seen[cap.ModelID] {
			seen[cap.ModelID] = true
			result = append(result, cap)
		}
	}
	return result
}

// Register adds or updates a model capability entry.
func (c *Catalog) Register(cap *models.ModelCapability) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.models[cap.ModelID] = cap
	// Also index by bare model name
	c.models[cap.ModelName] = cap
}

// RegisterDiscovered merges discovered models from a provider's list-models API.
func (c *Catalog) RegisterDiscovered(discovered []models.DiscoveredModel) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, dm := range discovered {
		key := dm.Kind + "/" + dm.ID
		existing, ok := c.models[key]
		if ok {
			// Merge: keep enrichment data, update discovery metadata
			existing.Source = "catalog+discovery"
			continue
		}
		// New model — create a basic entry
		c.models[key] = &models.ModelCapability{
			ModelID:      key,
			ProviderKind: dm.Kind,
			ModelName:    dm.ID,
			Source:       "discovery",
		}
		c.models[dm.ID] = c.models[key]
	}
}

// Count returns the number of unique models in the catalog.
func (c *Catalog) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	seen := make(map[string]bool)
	for _, cap := range c.models {
		seen[cap.ModelID] = true
	}
	return len(seen)
}

// ── LiteLLM Data Fetcher ────────────────────────────────────

// litellmEntry is the structure from LiteLLM's model_prices_and_context_window.json.
type litellmEntry struct {
	MaxTokens          int      `json:"max_tokens"`
	MaxInputTokens     int      `json:"max_input_tokens"`
	MaxOutputTokens    int      `json:"max_output_tokens"`
	InputCostPerToken  float64  `json:"input_cost_per_token"`
	OutputCostPerToken float64  `json:"output_cost_per_token"`
	LitellmProvider    string   `json:"litellm_provider"`
	Mode               string   `json:"mode"`
	SupportsVision     bool     `json:"supports_vision"`
	SupportsToolChoice bool     `json:"supports_tool_choice"`
	SupportsResponseSchema bool `json:"supports_response_schema"`
	SupportsAssistantPrefill bool `json:"supports_assistant_prefill"`
	SupportsFunctionCalling bool `json:"supports_function_calling"`
}

func (c *Catalog) fetchLiteLLMData(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", litellmURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch litellm data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("litellm returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("unmarshal litellm data: %w", err)
	}

	c.mu.Lock()
	count := 0
	for modelKey, data := range raw {
		// Skip the "sample_spec" key
		if modelKey == "sample_spec" {
			continue
		}

		var entry litellmEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue // skip malformed entries
		}

		// Skip non-chat models (embedding, image generation, etc.)
		if entry.Mode != "" && entry.Mode != "chat" && entry.Mode != "completion" {
			continue
		}

		// Map LiteLLM provider to AgentOven provider kind
		providerKind := mapLiteLLMProvider(entry.LitellmProvider)
		modelName := modelKey
		// Strip provider prefix if present (e.g., "openai/gpt-5" → "gpt-5")
		if parts := strings.SplitN(modelKey, "/", 2); len(parts) == 2 {
			modelName = parts[1]
		}

		contextWindow := entry.MaxInputTokens
		if contextWindow == 0 {
			contextWindow = entry.MaxTokens
		}

		// Determine token param name
		tokenParam := "max_tokens"
		if providerKind == "openai" && !strings.HasPrefix(modelName, "gpt-3.5") {
			tokenParam = "max_completion_tokens"
		}

		cap := &models.ModelCapability{
			ModelID:          modelKey,
			ProviderKind:     providerKind,
			ModelName:        modelName,
			ContextWindow:    contextWindow,
			MaxOutputTokens:  entry.MaxOutputTokens,
			InputCostPer1K:   entry.InputCostPerToken * 1000,
			OutputCostPer1K:  entry.OutputCostPerToken * 1000,
			SupportsTools:    entry.SupportsFunctionCalling || entry.SupportsToolChoice,
			SupportsVision:   entry.SupportsVision,
			SupportsStreaming: true, // most chat models support streaming
			SupportsJSON:     entry.SupportsResponseSchema,
			TokenParamName:   tokenParam,
			Source:           "catalog",
		}

		// Check for thinking/reasoning support
		if strings.Contains(modelName, "o1") || strings.Contains(modelName, "o3") ||
			strings.Contains(modelName, "claude-3-5-opus") || strings.Contains(modelName, "claude-opus-4") {
			cap.SupportsThinking = true
		}

		c.models[modelKey] = cap
		c.models[modelName] = cap
		count++
	}
	c.mu.Unlock()

	// Save to cache for offline use
	_ = c.saveCache(body)

	log.Info().Int("models", count).Msg("Catalog: loaded LiteLLM enrichment data")
	return nil
}

// mapLiteLLMProvider maps LiteLLM provider names to AgentOven provider kinds.
func mapLiteLLMProvider(litellmProvider string) string {
	switch strings.ToLower(litellmProvider) {
	case "openai":
		return "openai"
	case "azure", "azure_ai":
		return "azure-openai"
	case "anthropic":
		return "anthropic"
	case "ollama", "ollama_chat":
		return "ollama"
	case "bedrock", "amazon":
		return "bedrock"
	case "vertex_ai", "vertex_ai_beta", "google":
		return "vertex"
	case "sagemaker":
		return "sagemaker"
	case "together_ai":
		return "together"
	case "groq":
		return "groq"
	case "deepseek":
		return "deepseek"
	case "mistral":
		return "mistral"
	case "cohere", "cohere_chat":
		return "cohere"
	default:
		return litellmProvider
	}
}

// ── Cache Management ────────────────────────────────────────

func (c *Catalog) loadCache() error {
	path := filepath.Join(c.cacheDir, defaultCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var entries map[string]*models.ModelCapability
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("unmarshal cache: %w", err)
	}

	c.mu.Lock()
	for k, v := range entries {
		c.models[k] = v
	}
	c.mu.Unlock()

	log.Debug().Int("entries", len(entries)).Msg("Catalog: loaded from local cache")
	return nil
}

func (c *Catalog) saveCache(rawLiteLLM []byte) error {
	_ = os.MkdirAll(c.cacheDir, 0o755)
	path := filepath.Join(c.cacheDir, defaultCacheFile)

	// Save a processed cache (not the raw LiteLLM JSON) for faster loading
	c.mu.RLock()
	data, err := json.Marshal(c.models)
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ── Built-in Defaults ───────────────────────────────────────

// loadBuiltinDefaults registers a small set of well-known models so the
// catalog works immediately even without a LiteLLM fetch.
func (c *Catalog) loadBuiltinDefaults() {
	defaults := []*models.ModelCapability{
		// OpenAI
		{ModelID: "openai/gpt-5", ProviderKind: "openai", ModelName: "gpt-5",
			ContextWindow: 128000, MaxOutputTokens: 16384,
			InputCostPer1K: 0.005, OutputCostPer1K: 0.015,
			SupportsTools: true, SupportsVision: true, SupportsStreaming: true, SupportsJSON: true,
			TokenParamName: "max_completion_tokens", Source: "builtin"},
		{ModelID: "openai/gpt-5-mini", ProviderKind: "openai", ModelName: "gpt-5-mini",
			ContextWindow: 128000, MaxOutputTokens: 16384,
			InputCostPer1K: 0.0004, OutputCostPer1K: 0.0016,
			SupportsTools: true, SupportsVision: true, SupportsStreaming: true, SupportsJSON: true,
			TokenParamName: "max_completion_tokens", Source: "builtin"},
		{ModelID: "openai/gpt-4o", ProviderKind: "openai", ModelName: "gpt-4o",
			ContextWindow: 128000, MaxOutputTokens: 16384,
			InputCostPer1K: 0.0025, OutputCostPer1K: 0.01,
			SupportsTools: true, SupportsVision: true, SupportsStreaming: true, SupportsJSON: true,
			TokenParamName: "max_completion_tokens", Source: "builtin"},
		{ModelID: "openai/gpt-4o-mini", ProviderKind: "openai", ModelName: "gpt-4o-mini",
			ContextWindow: 128000, MaxOutputTokens: 16384,
			InputCostPer1K: 0.00015, OutputCostPer1K: 0.0006,
			SupportsTools: true, SupportsVision: true, SupportsStreaming: true, SupportsJSON: true,
			TokenParamName: "max_completion_tokens", Source: "builtin"},

		// Anthropic
		{ModelID: "anthropic/claude-sonnet-4-20250514", ProviderKind: "anthropic", ModelName: "claude-sonnet-4-20250514",
			ContextWindow: 200000, MaxOutputTokens: 8192,
			InputCostPer1K: 0.003, OutputCostPer1K: 0.015,
			SupportsTools: true, SupportsVision: true, SupportsStreaming: true,
			TokenParamName: "max_tokens", Source: "builtin"},
		{ModelID: "anthropic/claude-opus-4-20250514", ProviderKind: "anthropic", ModelName: "claude-opus-4-20250514",
			ContextWindow: 200000, MaxOutputTokens: 32000,
			InputCostPer1K: 0.015, OutputCostPer1K: 0.075,
			SupportsTools: true, SupportsVision: true, SupportsStreaming: true, SupportsThinking: true,
			TokenParamName: "max_tokens", Source: "builtin"},
		{ModelID: "anthropic/claude-3-5-haiku-20241022", ProviderKind: "anthropic", ModelName: "claude-3-5-haiku-20241022",
			ContextWindow: 200000, MaxOutputTokens: 8192,
			InputCostPer1K: 0.001, OutputCostPer1K: 0.005,
			SupportsTools: true, SupportsStreaming: true,
			TokenParamName: "max_tokens", Source: "builtin"},
	}

	c.mu.Lock()
	for _, d := range defaults {
		// Don't overwrite if already loaded from cache/litellm
		if _, exists := c.models[d.ModelID]; !exists {
			c.models[d.ModelID] = d
			c.models[d.ModelName] = d
		}
	}
	c.mu.Unlock()
}
