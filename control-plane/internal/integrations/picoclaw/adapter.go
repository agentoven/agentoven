// Package picoclaw provides an A2A adapter that wraps PicoClaw instances
// as AgentOven-managed agents. PicoClaw is an ultra-lightweight personal AI
// assistant built in Go that runs on $10 hardware with <10MB RAM.
//
// This adapter enables:
//   - PicoClaw instances to register in the AgentOven agent registry
//   - AgentOven to relay tasks to PicoClaw via A2A protocol
//   - Chat platform gateways (Telegram, Discord, etc.) to route through AgentOven
//   - Heartbeat health monitoring for IoT edge agents
//
// PicoClaw reference: https://github.com/sipeed/picoclaw
package picoclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ── A2A Adapter ──────────────────────────────────────────────

// Adapter wraps PicoClaw instances and exposes them as A2A-compliant agents
// in the AgentOven registry. It handles instance registration, task relay,
// and agent card proxying.
type Adapter struct {
	store  store.Store
	client *http.Client
}

// NewAdapter creates a new PicoClaw A2A adapter.
func NewAdapter(s store.Store) *Adapter {
	return &Adapter{
		store: s,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ── Instance Registration ────────────────────────────────────

// RegisterRequest is the payload to register a PicoClaw instance.
type RegisterRequest struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Endpoint    string                 `json:"endpoint"` // http://device-ip:port
	DeviceType  string                 `json:"device_type,omitempty"` // "risc-v", "arm", "x86"
	Platform    string                 `json:"platform,omitempty"` // "linux", "android"
	Skills      []string               `json:"skills,omitempty"`
	Gateways    []string               `json:"gateways,omitempty"` // ["telegram", "discord"]
	Heartbeat   *models.HeartbeatConfig `json:"heartbeat,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// RegisterResponse is returned after registering a PicoClaw instance.
type RegisterResponse struct {
	Instance  *models.PicoClawInstance `json:"instance"`
	AgentName string                    `json:"agent_name"`
	A2AEndpoint string                  `json:"a2a_endpoint"`
}

// Register registers a PicoClaw instance and creates a corresponding
// AgentOven agent entry with mode=external and framework=picoclaw.
func (a *Adapter) Register(ctx context.Context, kitchen string, req RegisterRequest) (*RegisterResponse, error) {
	if req.Name == "" || req.Endpoint == "" {
		return nil, fmt.Errorf("name and endpoint are required")
	}

	// Probe the PicoClaw instance to verify it's reachable
	status, err := a.probeInstance(ctx, req.Endpoint)
	if err != nil {
		log.Warn().Err(err).Str("endpoint", req.Endpoint).Msg("PicoClaw instance probe failed (registering anyway)")
	}

	// Create PicoClaw instance record
	instance := &models.PicoClawInstance{
		ID:          uuid.New().String(),
		Name:        req.Name,
		Description: req.Description,
		Kitchen:     kitchen,
		Endpoint:    req.Endpoint,
		DeviceType:  req.DeviceType,
		Platform:    req.Platform,
		Version:     status.model, // discovered from probe
		Status:      models.PicoClawStatusOnline,
		Skills:      req.Skills,
		Gateways:    req.Gateways,
		Metadata:    req.Metadata,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	if req.Heartbeat != nil {
		instance.Heartbeat = *req.Heartbeat
	} else {
		instance.Heartbeat = models.HeartbeatConfig{
			Enabled:      true,
			IntervalSecs: 60,
			TimeoutSecs:  180,
		}
	}

	if err != nil {
		instance.Status = models.PicoClawStatusUnknown
	}

	now := time.Now().UTC()
	instance.LastSeen = &now

	// Create a corresponding AgentOven agent (external mode)
	agentName := fmt.Sprintf("picoclaw-%s", req.Name)
	instance.AgentName = agentName

	skills := req.Skills
	if len(skills) == 0 {
		skills = []string{"general-assistant", "code-execution", "web-search"}
	}

	agent := &models.Agent{
		ID:          uuid.New().String(),
		Name:        agentName,
		Description: fmt.Sprintf("PicoClaw IoT agent: %s (%s/%s)", req.Name, req.DeviceType, req.Platform),
		Framework:   "picoclaw",
		Mode:        models.AgentModeExternal,
		Status:      models.AgentStatusReady,
		Kitchen:     kitchen,
		Version:     "1.0.0",
		A2AEndpoint: req.Endpoint,
		Skills:      skills,
		Tags: map[string]string{
			"picoclaw":       "true",
			"picoclaw_id":    instance.ID,
			"device_type":    req.DeviceType,
			"platform":       req.Platform,
			"iot":            "true",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := a.store.CreateAgent(ctx, agent); err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	log.Info().
		Str("instance", instance.Name).
		Str("agent", agentName).
		Str("endpoint", req.Endpoint).
		Str("device", req.DeviceType).
		Msg("PicoClaw instance registered")

	return &RegisterResponse{
		Instance:    instance,
		AgentName:   agentName,
		A2AEndpoint: req.Endpoint,
	}, nil
}

// ── Task Relay ───────────────────────────────────────────────

// RelayRequest sends a task to a PicoClaw instance via its agent endpoint.
type RelayRequest struct {
	InstanceName string `json:"instance_name"`
	Message      string `json:"message"`
	Model        string `json:"model,omitempty"` // override PicoClaw's default model
	MaxTokens    int    `json:"max_tokens,omitempty"`
}

// RelayResponse is the result of a task relayed to PicoClaw.
type RelayResponse struct {
	Output   string                 `json:"output"`
	Model    string                 `json:"model,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

// Relay sends a message to a PicoClaw instance and returns the response.
// This uses PicoClaw's agent mode CLI-compatible HTTP endpoint.
func (a *Adapter) Relay(ctx context.Context, kitchen string, req RelayRequest) RelayResponse {
	// Find the linked agent
	agentName := fmt.Sprintf("picoclaw-%s", req.InstanceName)
	agent, err := a.store.GetAgent(ctx, kitchen, agentName)
	if err != nil {
		return RelayResponse{Error: fmt.Sprintf("PicoClaw instance not found: %s", req.InstanceName)}
	}

	if agent.Status != models.AgentStatusReady {
		return RelayResponse{Error: fmt.Sprintf("PicoClaw agent %s is not ready (status: %s)", agentName, agent.Status)}
	}

	endpoint := agent.A2AEndpoint
	if endpoint == "" {
		return RelayResponse{Error: "PicoClaw endpoint not configured"}
	}

	// Build A2A tasks/send request (PicoClaw-compatible JSON-RPC)
	a2aReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tasks/send",
		"id":      fmt.Sprintf("pc-%d", time.Now().UnixMilli()),
		"params": map[string]interface{}{
			"message": map[string]interface{}{
				"role": "user",
				"parts": []map[string]interface{}{
					{"type": "text", "text": req.Message},
				},
			},
		},
	}

	body, _ := json.Marshal(a2aReq)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return RelayResponse{Error: fmt.Sprintf("build request: %s", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return RelayResponse{Error: fmt.Sprintf("PicoClaw call failed: %s", err)}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// PicoClaw may respond with plain text or JSON
	var a2aResp map[string]interface{}
	if err := json.Unmarshal(respBody, &a2aResp); err != nil {
		// Plain text response (PicoClaw agent mode)
		return RelayResponse{
			Output: string(respBody),
			Metadata: map[string]interface{}{
				"agent":    agentName,
				"instance": req.InstanceName,
				"format":   "plain",
			},
		}
	}

	// Extract text from A2A JSON-RPC response
	output := extractOutput(a2aResp)
	return RelayResponse{
		Output: output,
		Metadata: map[string]interface{}{
			"agent":    agentName,
			"instance": req.InstanceName,
			"format":   "a2a",
		},
	}
}

// ── HTTP Handlers ────────────────────────────────────────────

// HandleRegister handles POST /picoclaw/instances
func (a *Adapter) HandleRegister(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	if kitchen == "" {
		kitchen = "default"
	}

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	resp, err := a.Register(r.Context(), kitchen, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// HandleListInstances handles GET /picoclaw/instances
func (a *Adapter) HandleListInstances(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	if kitchen == "" {
		kitchen = "default"
	}

	// List agents with picoclaw framework tag
	agents, err := a.store.ListAgents(r.Context(), kitchen)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	picoAgents := make([]models.Agent, 0)
	for _, agent := range agents {
		if agent.Framework == "picoclaw" {
			picoAgents = append(picoAgents, agent)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(picoAgents)
}

// HandleRelay handles POST /picoclaw/relay
func (a *Adapter) HandleRelay(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	if kitchen == "" {
		kitchen = "default"
	}

	var req RelayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	resp := a.Relay(r.Context(), kitchen, req)
	w.Header().Set("Content-Type", "application/json")
	if resp.Error != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	json.NewEncoder(w).Encode(resp)
}

// HandleHealthCheck handles GET /picoclaw/instances/{name}/health
func (a *Adapter) HandleHealthCheck(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	if kitchen == "" {
		kitchen = "default"
	}

	// Extract instance name from URL
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name query parameter required", http.StatusBadRequest)
		return
	}

	agentName := fmt.Sprintf("picoclaw-%s", name)
	agent, err := a.store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		http.Error(w, fmt.Sprintf("instance not found: %s", name), http.StatusNotFound)
		return
	}

	result := a.checkHealth(r.Context(), agent)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// ── Internal Helpers ─────────────────────────────────────────

type probeResult struct {
	model string
}

// probeInstance pings a PicoClaw endpoint to check if it's alive.
func (a *Adapter) probeInstance(ctx context.Context, endpoint string) (probeResult, error) {
	statusURL := endpoint + "/status"
	if endpoint[len(endpoint)-1] == '/' {
		statusURL = endpoint + "status"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return probeResult{}, err
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return probeResult{}, fmt.Errorf("probe failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return probeResult{}, fmt.Errorf("probe returned %d", resp.StatusCode)
	}

	var status struct {
		Status string `json:"status"`
		Model  string `json:"model"`
		Uptime int64  `json:"uptime"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return probeResult{}, nil // non-JSON status is OK
	}

	return probeResult{model: status.Model}, nil
}

// checkHealth performs a heartbeat health check on a PicoClaw agent.
func (a *Adapter) checkHealth(ctx context.Context, agent *models.Agent) models.HeartbeatResult {
	result := models.HeartbeatResult{
		InstanceID: agent.Tags["picoclaw_id"],
		CheckedAt:  time.Now().UTC(),
	}

	if agent.A2AEndpoint == "" {
		result.Status = models.PicoClawStatusOffline
		result.Error = "no endpoint configured"
		return result
	}

	statusURL := agent.A2AEndpoint + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		result.Status = models.PicoClawStatusOffline
		result.Error = err.Error()
		return result
	}

	resp, err := a.client.Do(req)
	if err != nil {
		result.Status = models.PicoClawStatusOffline
		result.Error = fmt.Sprintf("unreachable: %s", err)
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		result.Status = models.PicoClawStatusDegraded
		result.Error = fmt.Sprintf("status %d", resp.StatusCode)
		return result
	}

	var status struct {
		Status string   `json:"status"`
		Model  string   `json:"model"`
		Uptime int64    `json:"uptime"`
		Memory float64  `json:"memory_mb"`
		Skills []string `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err == nil {
		result.Uptime = status.Uptime
		result.MemoryMB = status.Memory
		result.Model = status.Model
		result.Skills = status.Skills
	}

	result.Status = models.PicoClawStatusOnline
	return result
}

// extractOutput extracts text from an A2A JSON-RPC response.
func extractOutput(resp map[string]interface{}) string {
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		return fmt.Sprintf("%v", resp)
	}

	// Try artifacts first (A2A standard)
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

	// Try message parts (fallback)
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

	// Try plain "output" field (PicoClaw CLI format)
	if output, ok := result["output"].(string); ok {
		return output
	}

	return fmt.Sprintf("%v", result)
}
