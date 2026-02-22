// Package langchain provides an adapter that exposes AgentOven agents
// as LangChain-compatible tools via the Tool Calling interface.
//
// This enables bidirectional integration:
//   - LangChain agents can call AgentOven agents as tools
//   - AgentOven recipes can invoke LangChain agents via A2A
//
// The adapter exposes a /langchain/tools endpoint that returns the agent
// catalog in LangChain tool schema format, and a /langchain/invoke endpoint
// that proxies tool calls to the appropriate AgentOven agent via A2A.
package langchain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// ── LangChain Tool Schema ────────────────────────────────────

// ToolSchema represents a LangChain-compatible tool definition.
type ToolSchema struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Parameters  ToolParameters    `json:"parameters"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// ToolParameters describes the JSON Schema for tool inputs.
type ToolParameters struct {
	Type       string                    `json:"type"`
	Properties map[string]ToolProperty   `json:"properties"`
	Required   []string                  `json:"required"`
}

// ToolProperty is a single parameter in the tool schema.
type ToolProperty struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// InvokeRequest is the payload to invoke an AgentOven agent as a LangChain tool.
type InvokeRequest struct {
	ToolName  string                 `json:"tool_name"`
	Arguments map[string]interface{} `json:"arguments"`
	Kitchen   string                 `json:"kitchen,omitempty"`
}

// InvokeResponse is the response from a LangChain tool invocation.
type InvokeResponse struct {
	Output   string                 `json:"output"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

// ── Adapter ──────────────────────────────────────────────────

// Adapter exposes AgentOven agents as LangChain-compatible tools.
type Adapter struct {
	store  store.Store
	client *http.Client
}

// NewAdapter creates a new LangChain adapter.
func NewAdapter(s store.Store) *Adapter {
	return &Adapter{
		store: s,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// ListTools returns all baked agents in a kitchen as LangChain tool schemas.
func (a *Adapter) ListTools(ctx context.Context, kitchen string) ([]ToolSchema, error) {
	agents, err := a.store.ListAgents(ctx, kitchen)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}

	tools := make([]ToolSchema, 0, len(agents))
	for _, agent := range agents {
		if agent.Status != models.AgentStatusReady {
			continue // only expose ready (deployed) agents
		}

		tool := ToolSchema{
			Name:        agent.Name,
			Description: agent.Description,
			Parameters: ToolParameters{
				Type: "object",
				Properties: map[string]ToolProperty{
					"message": {
						Type:        "string",
						Description: fmt.Sprintf("The input message to send to the %s agent", agent.Name),
					},
				},
				Required: []string{"message"},
			},
			Metadata: map[string]string{
				"agent_id":  agent.ID,
				"framework": string(agent.Framework),
				"kitchen":   kitchen,
				"source":    "agentoven",
			},
		}
		tools = append(tools, tool)
	}

	return tools, nil
}

// Invoke calls an AgentOven agent via A2A and returns the result.
func (a *Adapter) Invoke(ctx context.Context, req InvokeRequest) InvokeResponse {
	kitchen := req.Kitchen
	if kitchen == "" {
		kitchen = "default"
	}

	agent, err := a.store.GetAgent(ctx, kitchen, req.ToolName)
	if err != nil {
		return InvokeResponse{Error: fmt.Sprintf("agent not found: %s", req.ToolName)}
	}

	if agent.Status != models.AgentStatusReady {
		return InvokeResponse{Error: fmt.Sprintf("agent %s is not ready (current: %s)", req.ToolName, agent.Status)}
	}

	// Build A2A message
	message, _ := req.Arguments["message"].(string)
	if message == "" {
		return InvokeResponse{Error: "missing required argument: message"}
	}

	// Call agent via A2A endpoint
	a2aReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tasks/send",
		"id":      fmt.Sprintf("lc-%d", time.Now().UnixMilli()),
		"params": map[string]interface{}{
			"message": map[string]interface{}{
				"role": "user",
				"parts": []map[string]interface{}{
					{"type": "text", "text": message},
				},
			},
		},
	}

	a2aBody, _ := json.Marshal(a2aReq)
	endpoint := agent.A2AEndpoint
	if endpoint == "" {
		return InvokeResponse{Error: fmt.Sprintf("agent %s has no A2A endpoint configured", req.ToolName)}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(a2aBody))
	if err != nil {
		return InvokeResponse{Error: fmt.Sprintf("build request: %s", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return InvokeResponse{Error: fmt.Sprintf("A2A call failed: %s", err)}
	}
	defer resp.Body.Close()

	var a2aResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&a2aResp); err != nil {
		return InvokeResponse{Error: fmt.Sprintf("decode A2A response: %s", err)}
	}

	// Extract text from A2A response
	output := extractA2AOutput(a2aResp)
	return InvokeResponse{
		Output: output,
		Metadata: map[string]interface{}{
			"agent":    req.ToolName,
			"kitchen":  kitchen,
			"a2a_resp": a2aResp,
		},
	}
}

// ── HTTP Handlers ────────────────────────────────────────────

// HandleListTools handles GET /langchain/tools
func (a *Adapter) HandleListTools(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	if kitchen == "" {
		kitchen = "default"
	}

	tools, err := a.ListTools(r.Context(), kitchen)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tools)
}

// HandleInvoke handles POST /langchain/invoke
func (a *Adapter) HandleInvoke(w http.ResponseWriter, r *http.Request) {
	var req InvokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Kitchen == "" {
		req.Kitchen = r.Header.Get("X-Kitchen")
	}

	resp := a.Invoke(r.Context(), req)
	w.Header().Set("Content-Type", "application/json")
	if resp.Error != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	json.NewEncoder(w).Encode(resp)
}

// ── Helpers ──────────────────────────────────────────────────

func extractA2AOutput(resp map[string]interface{}) string {
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		return fmt.Sprintf("%v", resp)
	}

	// Try to extract from artifacts first
	if artifacts, ok := result["artifacts"].([]interface{}); ok && len(artifacts) > 0 {
		if artifact, ok := artifacts[0].(map[string]interface{}); ok {
			if parts, ok := artifact["parts"].([]interface{}); ok {
				for _, p := range parts {
					if part, ok := p.(map[string]interface{}); ok {
						if text, ok := part["text"].(string); ok {
							return text
						}
					}
				}
			}
		}
	}

	// Fallback: extract from message
	if msg, ok := result["message"].(map[string]interface{}); ok {
		if parts, ok := msg["parts"].([]interface{}); ok {
			for _, p := range parts {
				if part, ok := p.(map[string]interface{}); ok {
					if text, ok := part["text"].(string); ok {
						return text
					}
				}
			}
		}
	}

	log.Warn().Interface("response", resp).Msg("Could not extract text from A2A response")
	return fmt.Sprintf("%v", result)
}
