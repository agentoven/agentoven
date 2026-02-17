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
}

// NewModelRouter creates a new model router.
func NewModelRouter(s store.Store) *ModelRouter {
	return &ModelRouter{
		store: s,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		latencies: make(map[string]int64),
		costs:     make(map[string]*models.CostSummary),
	}
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

	var resp *models.RouteResponse
	var err error

	switch provider.Kind {
	case "openai", "azure-openai":
		resp, err = mr.callOpenAI(ctx, provider, model, req.Messages)
	case "anthropic":
		resp, err = mr.callAnthropic(ctx, provider, model, req.Messages)
	case "ollama":
		resp, err = mr.callOllama(ctx, provider, model, req.Messages)
	default:
		// Generic OpenAI-compatible endpoint
		resp, err = mr.callOpenAI(ctx, provider, model, req.Messages)
	}

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
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
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
	if len(oaiResp.Choices) > 0 {
		content = oaiResp.Choices[0].Message.Content
	}

	costPer1KInput := mr.getModelCost(provider, model, "input")
	costPer1KOutput := mr.getModelCost(provider, model, "output")
	estimatedCost := float64(oaiResp.Usage.PromptTokens)/1000*costPer1KInput +
		float64(oaiResp.Usage.CompletionTokens)/1000*costPer1KOutput

	return &models.RouteResponse{
		ID:       oaiResp.ID,
		Provider: provider.Name,
		Model:    model,
		Content:  content,
		Usage: models.TokenUsage{
			InputTokens:   oaiResp.Usage.PromptTokens,
			OutputTokens:  oaiResp.Usage.CompletionTokens,
			TotalTokens:   oaiResp.Usage.TotalTokens,
			EstimatedCost: estimatedCost,
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
		Type string `json:"type"`
		Text string `json:"text"`
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
	for _, c := range anthResp.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}

	totalTokens := anthResp.Usage.InputTokens + anthResp.Usage.OutputTokens
	costPer1KInput := mr.getModelCost(provider, model, "input")
	costPer1KOutput := mr.getModelCost(provider, model, "output")
	estimatedCost := float64(anthResp.Usage.InputTokens)/1000*costPer1KInput +
		float64(anthResp.Usage.OutputTokens)/1000*costPer1KOutput

	return &models.RouteResponse{
		ID:       anthResp.ID,
		Provider: provider.Name,
		Model:    model,
		Content:  content,
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
		return nil, fmt.Errorf("status %d: %s", httpResp.StatusCode, string(respBody))
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
		ID:          uuid.New().String(),
		AgentName:   agentName,
		Kitchen:     kitchen,
		Status:      "completed",
		DurationMs:  resp.LatencyMs,
		TotalTokens: resp.Usage.TotalTokens,
		CostUSD:     resp.Usage.EstimatedCost,
		Metadata: map[string]interface{}{
			"provider": resp.Provider,
			"model":    resp.Model,
			"strategy": resp.Strategy,
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
