// Package ragas provides a Go helper for registering the RAGAS evaluation
// tool with the AgentOven MCP Gateway. The actual RAGAS evaluation runs in
// a Python sidecar (server.py), and this package registers an MCP tool that
// proxies requests to it.
package ragas

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// DefaultEndpoint is the default RAGAS sidecar URL.
const DefaultEndpoint = "http://localhost:8400"

// Client communicates with the RAGAS Python sidecar.
type Client struct {
	endpoint string
	client   *http.Client
}

// NewClient creates a RAGAS client.
func NewClient() *Client {
	endpoint := os.Getenv("AGENTOVEN_RAGAS_URL")
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return &Client{
		endpoint: endpoint,
		client:   &http.Client{Timeout: 120 * time.Second},
	}
}

// EvalRequest matches the Python server's request schema.
type EvalRequest struct {
	Question    string   `json:"question"`
	Answer      string   `json:"answer"`
	Contexts    []string `json:"contexts"`
	GroundTruth string   `json:"ground_truth,omitempty"`
	Metrics     []string `json:"metrics,omitempty"`
}

// EvalResponse matches the Python server's response schema.
type EvalResponse struct {
	Scores  map[string]float64 `json:"scores"`
	Details map[string]any     `json:"details,omitempty"`
}

// Evaluate sends an evaluation request to the RAGAS sidecar.
func (c *Client) Evaluate(ctx context.Context, req EvalRequest) (*EvalResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/evaluate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RAGAS sidecar returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result EvalResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &result, nil
}

// HealthCheck verifies the RAGAS sidecar is reachable.
func (c *Client) HealthCheck(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/health", nil)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("RAGAS sidecar unreachable: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("RAGAS sidecar unhealthy: status %d", resp.StatusCode)
	}
	return nil
}

// MCPToolDefinition returns the MCP tool definition for ragas.evaluate.
// Use this to auto-register the tool with the MCP Gateway.
func MCPToolDefinition() models.MCPTool {
	return models.MCPTool{
		Name:        "ragas.evaluate",
		Description: "Evaluate RAG pipeline quality using RAGAS metrics (faithfulness, relevancy, precision, recall, correctness, similarity)",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"question":     map[string]string{"type": "string", "description": "The user question"},
				"answer":       map[string]string{"type": "string", "description": "The RAG-generated answer"},
				"contexts":     map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "Retrieved context passages"},
				"ground_truth": map[string]string{"type": "string", "description": "Expected correct answer (optional)"},
				"metrics":      map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "Specific metrics to evaluate"},
			},
			"required": []string{"question", "answer", "contexts"},
		},
		Transport:    "http",
		Capabilities: []string{"tool"},
		Enabled:      true,
	}
}

// TryRegisterMCPTool attempts to register the RAGAS tool with the MCP Gateway.
// Silently skips if the sidecar is not reachable.
func TryRegisterMCPTool(ctx context.Context) *models.MCPTool {
	c := NewClient()
	if err := c.HealthCheck(ctx); err != nil {
		log.Debug().Err(err).Msg("RAGAS sidecar not available — ragas.evaluate tool not registered")
		return nil
	}
	tool := MCPToolDefinition()
	log.Info().Msg("✅ RAGAS evaluation tool available (ragas.evaluate)")
	return &tool
}
