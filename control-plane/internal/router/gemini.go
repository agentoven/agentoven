// Package router — Gemini (Google AI) provider driver.
//
// Talks to the Gemini API at generativelanguage.googleapis.com.
// Auth: API key passed as query param (?key=...).
// Supports: chat completions, tool calling, model discovery, embeddings.
package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ── Gemini Request / Response types ─────────────────────────

// geminiContent is a single message in the Gemini conversation.
type geminiContent struct {
	Role  string       `json:"role"` // "user" or "model"
	Parts []geminiPart `json:"parts"`
}

// geminiPart represents a content part (text, function call, or function response).
type geminiPart struct {
	Text             string                `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall   `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResult `json:"functionResponse,omitempty"`
}

// geminiFunctionCall is returned by the model when it wants to call a tool.
type geminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

// geminiFunctionResult is sent back to the model with tool output.
type geminiFunctionResult struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

// geminiTool wraps function declarations for the Gemini API.
type geminiTool struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations"`
}

// geminiFunctionDecl describes a tool the model can use.
type geminiFunctionDecl struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// geminiToolConfig controls tool calling behavior.
type geminiToolConfig struct {
	FunctionCallingConfig *geminiFCConfig `json:"functionCallingConfig,omitempty"`
}

type geminiFCConfig struct {
	Mode string `json:"mode"` // "AUTO", "ANY", "NONE"
}

// geminiRequest is the body for generateContent.
type geminiRequest struct {
	Contents          []geminiContent   `json:"contents"`
	Tools             []geminiTool      `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig `json:"toolConfig,omitempty"`
	SystemInstruction *geminiContent    `json:"systemInstruction,omitempty"`
}

// geminiResponse is the response from generateContent.
type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"` // "STOP", "MAX_TOKENS", "TOOL_CALLS" (not standard — Gemini just stops)
}

// ── Gemini Driver ───────────────────────────────────────────

type GeminiDriver struct{ router *ModelRouter }

func (d *GeminiDriver) Kind() string { return "gemini" }

func (d *GeminiDriver) Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	return d.router.callGemini(ctx, provider, req)
}

func (d *GeminiDriver) HealthCheck(ctx context.Context, provider *models.ModelProvider) error {
	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return fmt.Errorf("gemini: api_key not configured")
	}

	endpoint := geminiEndpoint(provider)
	url := endpoint + "/models?key=" + apiKey
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := d.router.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("gemini unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gemini: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ── Gemini Model Discovery ──────────────────────────────────

func (d *GeminiDriver) DiscoverModels(ctx context.Context, provider *models.ModelProvider) ([]models.DiscoveredModel, error) {
	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("gemini discover: api_key not configured")
	}

	endpoint := geminiEndpoint(provider)
	url := endpoint + "/models?key=" + apiKey
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := d.router.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini discover: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini discover: status %d: %s", resp.StatusCode, string(body))
	}

	var listResp struct {
		Models []struct {
			Name                       string   `json:"name"` // "models/gemini-2.0-flash"
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, fmt.Errorf("gemini discover: decode: %w", err)
	}

	var result []models.DiscoveredModel
	for _, m := range listResp.Models {
		// Only include models that support generateContent (chat)
		supportsChat := false
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				supportsChat = true
				break
			}
		}
		if !supportsChat {
			continue
		}

		// Strip "models/" prefix for cleaner IDs
		id := m.Name
		if len(id) > 7 && id[:7] == "models/" {
			id = id[7:]
		}

		result = append(result, models.DiscoveredModel{
			ID:       id,
			Provider: provider.Name,
			Kind:     "gemini",
			OwnedBy:  "google",
			Metadata: map[string]string{"display_name": m.DisplayName},
		})
	}
	return result, nil
}

// ── Gemini Embedding Capability ─────────────────────────────

func (d *GeminiDriver) EmbeddingModels() []EmbeddingModelInfo {
	return []EmbeddingModelInfo{
		{Model: "text-embedding-004", Dimensions: 768, MaxBatch: 100},
	}
}

func (d *GeminiDriver) Embed(ctx context.Context, provider *models.ModelProvider, model string, texts []string) ([][]float64, error) {
	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("gemini embed: api_key not configured")
	}

	endpoint := geminiEndpoint(provider)

	results := make([][]float64, 0, len(texts))
	// Gemini embedContent handles one text at a time; batch via batchEmbedContents
	batchReq := struct {
		Requests []struct {
			Model   string `json:"model"`
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"requests"`
	}{}

	for _, text := range texts {
		entry := struct {
			Model   string `json:"model"`
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		}{
			Model: "models/" + model,
		}
		entry.Content.Parts = []struct {
			Text string `json:"text"`
		}{{Text: text}}
		batchReq.Requests = append(batchReq.Requests, entry)
	}

	body, _ := json.Marshal(batchReq)
	url := fmt.Sprintf("%s/models/%s:batchEmbedContents?key=%s", endpoint, model, apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gemini embed: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := d.router.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini embed: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("gemini embed: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var embedResp struct {
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("gemini embed: decode: %w", err)
	}

	for _, e := range embedResp.Embeddings {
		results = append(results, e.Values)
	}
	return results, nil
}

// ── Gemini API Call ─────────────────────────────────────────

func (mr *ModelRouter) callGemini(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	apiKey, _ := provider.Config["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("gemini: api_key not configured")
	}

	model := req.Model
	endpoint := geminiEndpoint(provider)

	// Convert ChatMessages → Gemini contents
	var contents []geminiContent
	var systemInstruction *geminiContent

	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			// Gemini uses systemInstruction instead of a system message
			systemInstruction = &geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: msg.Content}},
			}
		case "user":
			contents = append(contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: msg.Content}},
			})
		case "assistant":
			parts := []geminiPart{}
			if msg.Content != "" {
				parts = append(parts, geminiPart{Text: msg.Content})
			}
			// Include function calls from assistant messages (multi-turn tool use)
			for _, tc := range msg.ToolCalls {
				var args map[string]interface{}
				if tc.Function.Arguments != "" {
					json.Unmarshal([]byte(tc.Function.Arguments), &args)
				}
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{
						Name: tc.Function.Name,
						Args: args,
					},
				})
			}
			contents = append(contents, geminiContent{
				Role:  "model",
				Parts: parts,
			})
		case "tool":
			// Tool result → Gemini functionResponse
			contents = append(contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{{
					FunctionResponse: &geminiFunctionResult{
						Name: msg.Name,
						Response: map[string]interface{}{
							"result": msg.Content,
						},
					},
				}},
			})
		}
	}

	// Build Gemini request
	gemReq := geminiRequest{
		Contents:          contents,
		SystemInstruction: systemInstruction,
	}

	// Convert tool definitions to Gemini format
	if len(req.Tools) > 0 {
		var funcDecls []geminiFunctionDecl
		for _, td := range req.Tools {
			funcDecls = append(funcDecls, geminiFunctionDecl{
				Name:        td.Function.Name,
				Description: td.Function.Description,
				Parameters:  td.Function.Parameters,
			})
		}
		gemReq.Tools = []geminiTool{{FunctionDeclarations: funcDecls}}
		gemReq.ToolConfig = &geminiToolConfig{
			FunctionCallingConfig: &geminiFCConfig{Mode: "AUTO"},
		}
	}

	body, _ := json.Marshal(gemReq)

	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", endpoint, model, apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gemini: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	httpResp, err := mr.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: request failed: %w", err)
	}
	defer httpResp.Body.Close()
	latency := time.Since(start).Milliseconds()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("gemini: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var gemResp geminiResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&gemResp); err != nil {
		return nil, fmt.Errorf("gemini: decode response: %w", err)
	}

	// Extract content and tool calls from response
	content := ""
	var toolCalls []models.ToolCallResult
	finishReason := ""

	if len(gemResp.Candidates) > 0 {
		candidate := gemResp.Candidates[0]
		finishReason = mapGeminiFinishReason(candidate.FinishReason)

		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				content += part.Text
			}
			if part.FunctionCall != nil {
				argsJSON, _ := json.Marshal(part.FunctionCall.Args)
				toolCalls = append(toolCalls, models.ToolCallResult{
					ID:   fmt.Sprintf("call_%s", uuid.New().String()[:8]),
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      part.FunctionCall.Name,
						Arguments: string(argsJSON),
					},
				})
			}
		}
	}

	if len(toolCalls) > 0 && finishReason == "" {
		finishReason = "tool_calls"
	}

	// Calculate cost
	costPer1KInput := mr.getModelCost(provider, model, "input")
	costPer1KOutput := mr.getModelCost(provider, model, "output")
	estimatedCost := float64(gemResp.UsageMetadata.PromptTokenCount)/1000*costPer1KInput +
		float64(gemResp.UsageMetadata.CandidatesTokenCount)/1000*costPer1KOutput

	log.Debug().
		Str("provider", provider.Name).
		Str("model", model).
		Int("input_tokens", gemResp.UsageMetadata.PromptTokenCount).
		Int("output_tokens", gemResp.UsageMetadata.CandidatesTokenCount).
		Int("tool_calls", len(toolCalls)).
		Int64("latency_ms", latency).
		Msg("Gemini call complete")

	return &models.RouteResponse{
		ID:           uuid.New().String(),
		Provider:     provider.Name,
		Model:        model,
		Content:      content,
		FinishReason: finishReason,
		ToolCalls:    toolCalls,
		Usage: models.TokenUsage{
			InputTokens:   int64(gemResp.UsageMetadata.PromptTokenCount),
			OutputTokens:  int64(gemResp.UsageMetadata.CandidatesTokenCount),
			TotalTokens:   int64(gemResp.UsageMetadata.TotalTokenCount),
			EstimatedCost: estimatedCost,
		},
	}, nil
}

// ── Helpers ─────────────────────────────────────────────────

// geminiEndpoint returns the base Gemini API URL.
func geminiEndpoint(provider *models.ModelProvider) string {
	if provider.Endpoint != "" {
		return provider.Endpoint
	}
	return "https://generativelanguage.googleapis.com/v1beta"
}

// mapGeminiFinishReason converts Gemini finish reasons to OpenAI-style.
func mapGeminiFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	case "RECITATION":
		return "content_filter"
	default:
		return reason
	}
}

// Compile-time assertions for GeminiDriver.
var (
	_ ProviderDriver         = (*GeminiDriver)(nil)
	_ ModelDiscoveryDriver   = (*GeminiDriver)(nil)
	_ EmbeddingCapableDriver = (*GeminiDriver)(nil)
)
