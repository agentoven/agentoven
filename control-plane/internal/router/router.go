// Package router implements the AgentOven Model Router.
//
// The router selects the optimal model provider based on the configured strategy
// (fallback, cost-optimized, latency-optimized, round-robin), sends the request,
// tracks costs, and handles provider failover transparently.
package router

import (
	"bytes"
	"context"
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

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ModelRouter routes LLM requests to configured providers.
type ModelRouter struct {
	store   store.Store
	client  *http.Client

	// Round-robin counter (atomic)
	rrCounter uint64

	// Latency tracking: provider name → rolling avg ms
	latencyMu sync.RWMutex
	latencies map[string]int64

	// Cost tracking: kitchen+agent → accumulated cost
	costMu sync.RWMutex
	costs  map[string]*models.CostSummary

	// ProviderDriver registry — maps provider kind to its driver.
	// OSS registers openai, azure-openai, anthropic, ollama at init.
	// Pro calls RegisterDriver("bedrock", ...) etc. to add enterprise drivers.
	driversMu sync.RWMutex
	drivers   map[string]ProviderDriver
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
	}

	// Register built-in OSS drivers
	mr.registerBuiltinDrivers()

	return mr
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
	return mdd.DiscoverModels(ctx, provider)
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
	providers, err := mr.store.ListProviders(ctx)
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
		if err := driver.HealthCheck(ctx, &p); err != nil {
			result[p.Name] = "unhealthy: " + err.Error()
		} else {
			result[p.Name] = "healthy"
		}
	}
	return result
}

// Route sends a request through the router using the specified strategy.
func (mr *ModelRouter) Route(ctx context.Context, req *models.RouteRequest) (*models.RouteResponse, error) {
	// Get configured providers
	providers, err := mr.store.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("no model providers configured")
	}

	strategy := req.Strategy
	if strategy == "" {
		strategy = models.RoutingFallback
	}

	// Select providers in order based on strategy
	ordered := mr.orderProviders(providers, strategy, req.Model)

	// Try each provider in order (fallback behavior)
	var lastErr error
	for _, provider := range ordered {
		resp, err := mr.callProvider(ctx, &provider, req)
		if err != nil {
			log.Warn().
				Str("provider", provider.Name).
				Str("model", provider.Kind).
				Err(err).
				Msg("Provider call failed, trying next")
			lastErr = err
			continue
		}

		// Track cost
		mr.trackCost(req.Kitchen, req.AgentRef, resp)

		// Record trace
		mr.recordTrace(ctx, req, resp)

		return resp, nil
	}

	return nil, fmt.Errorf("all providers failed, last error: %w", lastErr)
}

// RouteStream routes a request to a provider that supports streaming.
// Falls back to non-streaming Route() if the selected driver doesn't implement
// StreamingProviderDriver, buffering the full response and sending it as one chunk.
func (mr *ModelRouter) RouteStream(ctx context.Context, req *models.RouteRequest, callback func(chunk *models.StreamChunk) error) error {
	providers, err := mr.store.ListProviders(ctx)
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
		resp, err := driver.Call(ctx, &provider, req)
		if err != nil {
			lastErr = err
			continue
		}
		mr.trackCost(req.Kitchen, req.AgentRef, resp)
		mr.recordTrace(ctx, req, resp)

		return callback(&models.StreamChunk{
			Content: resp.Content,
			Done:    true,
			Usage:   &resp.Usage,
		})
	}

	return fmt.Errorf("all providers failed (stream), last error: %w", lastErr)
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
			ci := mr.getProviderCostPer1K(providers[i])
			cj := mr.getProviderCostPer1K(providers[j])
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

	model := req.Model
	if model == "" && len(provider.Models) > 0 {
		model = provider.Models[0]
	}

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

	resp, err := driver.Call(ctx, provider, &driverReq)
	if err != nil {
		return nil, err
	}

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
	Model    string                `json:"model"`
	Messages []models.ChatMessage  `json:"messages"`
}

type openAIResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content,omitempty"` // OpenAI o-series reasoning
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
		ReasoningTokens  int64 `json:"reasoning_tokens,omitempty"` // o-series reasoning token count
	} `json:"usage"`
}

func (mr *ModelRouter) callOpenAI(ctx context.Context, provider *models.ModelProvider, model string, messages []models.ChatMessage) (*models.RouteResponse, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}

	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("openai: api_key not configured for provider %s", provider.Name)
	}

	body, _ := json.Marshal(openAIRequest{Model: model, Messages: messages})

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

	httpResp, err := mr.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("openai: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}

	content := ""
	var thinkingBlocks []models.ThinkingBlock
	if len(oaiResp.Choices) > 0 {
		content = oaiResp.Choices[0].Message.Content
		// Capture reasoning_content from OpenAI o-series models
		if rc := oaiResp.Choices[0].Message.ReasoningContent; rc != "" {
			thinkingBlocks = append(thinkingBlocks, models.ThinkingBlock{
				Content:    rc,
				TokenCount: oaiResp.Usage.ReasoningTokens,
				Model:      model,
				Provider:   provider.Name,
				Timestamp:  time.Now().UTC(),
			})
		}
	}

	costPer1KInput := mr.getModelCost(provider, model, "input")
	costPer1KOutput := mr.getModelCost(provider, model, "output")
	estimatedCost := float64(oaiResp.Usage.PromptTokens)/1000*costPer1KInput +
		float64(oaiResp.Usage.CompletionTokens)/1000*costPer1KOutput

	return &models.RouteResponse{
		ID:             oaiResp.ID,
		Provider:       provider.Name,
		Model:          model,
		Content:        content,
		ThinkingBlocks: thinkingBlocks,
		Usage: models.TokenUsage{
			InputTokens:    oaiResp.Usage.PromptTokens,
			OutputTokens:   oaiResp.Usage.CompletionTokens,
			TotalTokens:    oaiResp.Usage.TotalTokens,
			ThinkingTokens: oaiResp.Usage.ReasoningTokens,
			EstimatedCost:  estimatedCost,
		},
	}, nil
}

// ── Anthropic Provider ──────────────────────────────────────

type anthropicRequest struct {
	Model     string                `json:"model"`
	Messages  []models.ChatMessage  `json:"messages"`
	MaxTokens int                   `json:"max_tokens"`
}

type anthropicResponse struct {
	ID      string `json:"id"`
	Content []struct {
		Type     string `json:"type"`      // "text", "thinking", "tool_use"
		Text     string `json:"text"`
		Thinking string `json:"thinking"` // Anthropic extended thinking content
	} `json:"content"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (mr *ModelRouter) callAnthropic(ctx context.Context, provider *models.ModelProvider, model string, messages []models.ChatMessage) (*models.RouteResponse, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.anthropic.com"
	}

	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("anthropic: api_key not configured for provider %s", provider.Name)
	}

	maxTokens := 4096
	if mt, ok := provider.Config["max_tokens"].(float64); ok {
		maxTokens = int(mt)
	}

	body, _ := json.Marshal(anthropicRequest{Model: model, Messages: messages, MaxTokens: maxTokens})

	url := endpoint + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := mr.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("anthropic: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var anthResp anthropicResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&anthResp); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}

	content := ""
	var thinkingBlocks []models.ThinkingBlock
	for _, c := range anthResp.Content {
		switch c.Type {
		case "text":
			content += c.Text
		case "thinking":
			// Anthropic extended thinking block
			thinkingBlocks = append(thinkingBlocks, models.ThinkingBlock{
				Content:   c.Thinking,
				Model:     model,
				Provider:  provider.Name,
				Timestamp: time.Now().UTC(),
			})
		}
	}

	totalTokens := anthResp.Usage.InputTokens + anthResp.Usage.OutputTokens
	costPer1KInput := mr.getModelCost(provider, model, "input")
	costPer1KOutput := mr.getModelCost(provider, model, "output")
	estimatedCost := float64(anthResp.Usage.InputTokens)/1000*costPer1KInput +
		float64(anthResp.Usage.OutputTokens)/1000*costPer1KOutput

	return &models.RouteResponse{
		ID:             anthResp.ID,
		Provider:       provider.Name,
		Model:          model,
		Content:        content,
		ThinkingBlocks: thinkingBlocks,
		Usage: models.TokenUsage{
			InputTokens:   anthResp.Usage.InputTokens,
			OutputTokens:  anthResp.Usage.OutputTokens,
			TotalTokens:   totalTokens,
			EstimatedCost: estimatedCost,
		},
	}, nil
}

// ── Ollama Provider ─────────────────────────────────────────

func (mr *ModelRouter) callOllama(ctx context.Context, provider *models.ModelProvider, model string, messages []models.ChatMessage) (*models.RouteResponse, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}

	body, _ := json.Marshal(openAIRequest{Model: model, Messages: messages})

	url := endpoint + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := mr.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("ollama: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	content := ""
	if len(oaiResp.Choices) > 0 {
		content = oaiResp.Choices[0].Message.Content
	}

	return &models.RouteResponse{
		ID:       uuid.New().String(),
		Provider: provider.Name,
		Model:    model,
		Content:  content,
		Usage: models.TokenUsage{
			InputTokens:  oaiResp.Usage.PromptTokens,
			OutputTokens: oaiResp.Usage.CompletionTokens,
			TotalTokens:  oaiResp.Usage.TotalTokens,
		},
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
		resp, err := mr.client.Do(req)
		if err != nil {
			result.Error = fmt.Sprintf("ollama unreachable: %v", err)
			result.LatencyMs = time.Since(start).Milliseconds()
			return result
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			result.Error = fmt.Sprintf("ollama: status %d: %s", resp.StatusCode, string(body))
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
				model = "claude-3-5-haiku-20241022"
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
		Messages            []models.ChatMessage  `json:"messages"`
		MaxTokens           *int                  `json:"max_tokens,omitempty"`
		MaxCompletionTokens *int                  `json:"max_completion_tokens,omitempty"`
	}

	limit := 5
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

	httpResp, err := mr.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		errStr := string(respBody)
		// A max_tokens truncation error means the model IS reachable — treat as healthy
		if httpResp.StatusCode == 400 && (strings.Contains(errStr, "max_tokens") || strings.Contains(errStr, "max_completion_tokens")) {
			return &models.RouteResponse{Provider: provider.Name, Model: model}, nil
		}
		return nil, fmt.Errorf("status %d: %s", httpResp.StatusCode, errStr)
	}

	return &models.RouteResponse{Provider: provider.Name, Model: model}, nil
}

// callAnthropicTest sends a minimal 1-token request to validate Anthropic credentials.
func (mr *ModelRouter) callAnthropicTest(ctx context.Context, provider *models.ModelProvider, model string) (*models.RouteResponse, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.anthropic.com"
	}

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

	httpResp, err := mr.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("status %d: %s", httpResp.StatusCode, string(respBody))
	}

	return &models.RouteResponse{Provider: provider.Name, Model: model}, nil
}

// recordTrace creates a trace record for the routed request.
func (mr *ModelRouter) recordTrace(ctx context.Context, req *models.RouteRequest, resp *models.RouteResponse) {
	kitchen := req.Kitchen
	if kitchen == "" {
		kitchen = "default"
	}
	agentName := req.AgentRef
	if agentName == "" {
		agentName = "router"
	}

	trace := &models.Trace{
		ID:             uuid.New().String(),
		AgentName:      agentName,
		Kitchen:        kitchen,
		Status:         "completed",
		DurationMs:     resp.LatencyMs,
		TotalTokens:    resp.Usage.TotalTokens,
		CostUSD:        resp.Usage.EstimatedCost,
		ThinkingBlocks: resp.ThinkingBlocks,
		Metadata: map[string]interface{}{
			"provider":        resp.Provider,
			"model":           resp.Model,
			"strategy":        resp.Strategy,
			"thinking_tokens": resp.Usage.ThinkingTokens,
		},
		CreatedAt: time.Now().UTC(),
	}

	if err := mr.store.CreateTrace(ctx, trace); err != nil {
		log.Warn().Err(err).Msg("Failed to record trace for routed request")
	}
}

// ── Cost Helpers ────────────────────────────────────────────

// Known cost per 1K tokens (USD) — sensible defaults
var defaultCosts = map[string]map[string]float64{
	"gpt-4o":           {"input": 0.0025, "output": 0.01},
	"gpt-4o-mini":      {"input": 0.00015, "output": 0.0006},
	"gpt-4-turbo":      {"input": 0.01, "output": 0.03},
	"claude-sonnet-4-20250514":    {"input": 0.003, "output": 0.015},
	"claude-3-5-haiku-20241022":   {"input": 0.001, "output": 0.005},
	"claude-opus-4-20250514":     {"input": 0.015, "output": 0.075},
}

func (mr *ModelRouter) getModelCost(provider *models.ModelProvider, model, direction string) float64 {
	// Check provider config first
	key := "cost_per_1k_" + direction
	if v, ok := provider.Config[key].(float64); ok {
		return v
	}
	// Fall back to defaults
	if costs, ok := defaultCosts[model]; ok {
		return costs[direction]
	}
	return 0.001 // generic fallback
}

func (mr *ModelRouter) getProviderCostPer1K(provider models.ModelProvider) float64 {
	if len(provider.Models) > 0 {
		return mr.getModelCost(&provider, provider.Models[0], "input")
	}
	return 1.0
}

// Ensure rand is seeded (Go 1.20+ does this automatically)
var _ = rand.Int

// ══════════════════════════════════════════════════════════════
// ── Built-in Provider Drivers ────────────────────────────────
// ══════════════════════════════════════════════════════════════

// registerBuiltinDrivers registers the four OSS community drivers.
func (mr *ModelRouter) registerBuiltinDrivers() {
	mr.RegisterDriver(&OpenAIDriver{router: mr})
	mr.RegisterDriver(&AzureOpenAIDriver{router: mr})
	mr.RegisterDriver(&AnthropicDriver{router: mr})
	mr.RegisterDriver(&OllamaDriver{router: mr})
	mr.RegisterDriver(&LiteLLMDriver{router: mr})
}

// ── OpenAI Driver ───────────────────────────────────────────

type OpenAIDriver struct{ router *ModelRouter }

func (d *OpenAIDriver) Kind() string { return "openai" }

func (d *OpenAIDriver) Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	return d.router.callOpenAI(ctx, provider, req.Model, req.Messages)
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

	httpResp, err := d.router.client.Do(httpReq)
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

	httpResp, err := d.router.client.Do(httpReq)
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
	return d.router.callOpenAI(ctx, provider, req.Model, req.Messages)
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

	httpResp, err := d.router.client.Do(httpReq)
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
	return d.router.callAnthropic(ctx, provider, req.Model, req.Messages)
}

func (d *AnthropicDriver) HealthCheck(ctx context.Context, provider *models.ModelProvider) error {
	model := ""
	if len(provider.Models) > 0 {
		model = provider.Models[0]
	} else {
		model = "claude-3-5-haiku-20241022"
	}
	_, err := d.router.callAnthropicTest(ctx, provider, model)
	return err
}

// ── Ollama Driver ───────────────────────────────────────────

type OllamaDriver struct{ router *ModelRouter }

func (d *OllamaDriver) Kind() string { return "ollama" }

func (d *OllamaDriver) Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	return d.router.callOllama(ctx, provider, req.Model, req.Messages)
}

func (d *OllamaDriver) HealthCheck(ctx context.Context, provider *models.ModelProvider) error {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	url := endpoint + "/api/tags"
	httpReq, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := d.router.client.Do(httpReq)
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

	httpResp, err := d.router.client.Do(httpReq)
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

	httpResp, err := d.router.client.Do(httpReq)
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
	return d.router.callOpenAI(ctx, provider, req.Model, req.Messages)
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

	resp, err := d.router.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("litellm unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("litellm: status %d: %s", resp.StatusCode, string(body))
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

	httpResp, err := d.router.client.Do(httpReq)
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
