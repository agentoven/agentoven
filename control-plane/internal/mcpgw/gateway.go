// Package mcpgw implements the MCP (Model Context Protocol) Gateway.
//
// The gateway allows agents to discover and invoke tools through the
// standardized MCP protocol. It supports:
//   - Tool registration and discovery
//   - JSON-RPC 2.0 over HTTP and SSE transports
//   - Tool invocation with argument validation
//   - Per-kitchen tool isolation
package mcpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Gateway is the MCP gateway that manages tool registration and invocation.
type Gateway struct {
	store  store.Store
	client *http.Client

	// SSE subscribers: kitchen → channel
	subsMu sync.RWMutex
	subs   map[string][]chan models.MCPResponse
}

// NewGateway creates a new MCP gateway.
func NewGateway(s store.Store) *Gateway {
	return &Gateway{
		store: s,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		subs: make(map[string][]chan models.MCPResponse),
	}
}

// HandleJSONRPC processes an MCP JSON-RPC 2.0 request.
func (gw *Gateway) HandleJSONRPC(ctx context.Context, kitchen string, req *models.MCPRequest) *models.MCPResponse {
	switch req.Method {

	// ── Discovery ────────────────────────────────────
	case "initialize":
		return gw.handleInitialize(req)

	case "tools/list":
		return gw.handleToolsList(ctx, kitchen, req)

	// ── Tool Invocation ──────────────────────────────
	case "tools/call":
		return gw.handleToolsCall(ctx, kitchen, req)

	// ── Notifications (no response) ──────────────────
	case "notifications/initialized":
		log.Debug().Str("kitchen", kitchen).Msg("MCP client initialized")
		return nil // notifications don't get responses

	case "ping":
		return &models.MCPResponse{
			Jsonrpc: "2.0",
			Result:  map[string]string{"status": "pong"},
			ID:      req.ID,
		}

	default:
		return &models.MCPResponse{
			Jsonrpc: "2.0",
			Error: &models.MCPError{
				Code:    -32601,
				Message: "Method not found",
				Data:    fmt.Sprintf("Method '%s' is not supported by the MCP gateway", req.Method),
			},
			ID: req.ID,
		}
	}
}

// handleInitialize responds to the MCP initialize handshake.
func (gw *Gateway) handleInitialize(req *models.MCPRequest) *models.MCPResponse {
	return &models.MCPResponse{
		Jsonrpc: "2.0",
		Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]bool{
					"listChanged": true,
				},
			},
			"serverInfo": map[string]string{
				"name":    "agentoven-mcp-gateway",
				"version": "0.2.0",
			},
		},
		ID: req.ID,
	}
}

// handleToolsList returns all registered tools for the kitchen.
func (gw *Gateway) handleToolsList(ctx context.Context, kitchen string, req *models.MCPRequest) *models.MCPResponse {
	tools, err := gw.store.ListTools(ctx, kitchen)
	if err != nil {
		return &models.MCPResponse{
			Jsonrpc: "2.0",
			Error: &models.MCPError{
				Code:    -32603,
				Message: "Internal error",
				Data:    err.Error(),
			},
			ID: req.ID,
		}
	}

	mcpTools := make([]models.MCPToolInfo, 0, len(tools))
	for _, t := range tools {
		if !t.Enabled {
			continue
		}
		mcpTools = append(mcpTools, models.MCPToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}

	return &models.MCPResponse{
		Jsonrpc: "2.0",
		Result: map[string]interface{}{
			"tools": mcpTools,
		},
		ID: req.ID,
	}
}

// handleToolsCall invokes a registered tool.
func (gw *Gateway) handleToolsCall(ctx context.Context, kitchen string, req *models.MCPRequest) *models.MCPResponse {
	var params models.MCPToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &models.MCPResponse{
			Jsonrpc: "2.0",
			Error: &models.MCPError{
				Code:    -32602,
				Message: "Invalid params",
				Data:    err.Error(),
			},
			ID: req.ID,
		}
	}

	// Look up the tool
	tool, err := gw.store.GetTool(ctx, kitchen, params.Name)
	if err != nil {
		return &models.MCPResponse{
			Jsonrpc: "2.0",
			Error: &models.MCPError{
				Code:    -32001,
				Message: "Tool not found",
				Data:    fmt.Sprintf("Tool '%s' is not registered in kitchen '%s'", params.Name, kitchen),
			},
			ID: req.ID,
		}
	}

	if !tool.Enabled {
		return &models.MCPResponse{
			Jsonrpc: "2.0",
			Error: &models.MCPError{
				Code:    -32002,
				Message: "Tool disabled",
				Data:    fmt.Sprintf("Tool '%s' is currently disabled", params.Name),
			},
			ID: req.ID,
		}
	}

	// Execute the tool based on transport
	result, err := gw.executeTool(ctx, tool, &params)
	if err != nil {
		return &models.MCPResponse{
			Jsonrpc: "2.0",
			Result: models.MCPToolResult{
				Content: []models.MCPContent{{
					Type: "text",
					Text: fmt.Sprintf("Tool execution error: %s", err.Error()),
				}},
				IsError: true,
			},
			ID: req.ID,
		}
	}

	return &models.MCPResponse{
		Jsonrpc: "2.0",
		Result:  result,
		ID:      req.ID,
	}
}

// executeTool dispatches tool execution based on the tool's transport.
func (gw *Gateway) executeTool(ctx context.Context, tool *models.MCPTool, params *models.MCPToolCallParams) (*models.MCPToolResult, error) {
	switch tool.Transport {
	case "http":
		return gw.executeHTTPTool(ctx, tool, params)
	case "sse":
		return gw.executeSSETool(ctx, tool, params)
	default:
		return gw.executeHTTPTool(ctx, tool, params)
	}
}

// executeHTTPTool calls a tool over HTTP (POST with JSON body).
func (gw *Gateway) executeHTTPTool(ctx context.Context, tool *models.MCPTool, params *models.MCPToolCallParams) (*models.MCPToolResult, error) {
	// Build the request body — send as MCP tools/call
	rpcReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      params.Name,
			"arguments": params.Arguments,
		},
		"id": uuid.New().String(),
	}
	body, _ := json.Marshal(rpcReq)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", tool.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Apply auth if configured
	gw.applyAuth(httpReq, tool)

	resp, err := gw.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("tool request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// Try to parse as MCP response
	var mcpResp models.MCPResponse
	if err := json.Unmarshal(respBody, &mcpResp); err == nil && mcpResp.Result != nil {
		// It's a proper MCP response — extract the result
		resultBytes, _ := json.Marshal(mcpResp.Result)
		var toolResult models.MCPToolResult
		if err := json.Unmarshal(resultBytes, &toolResult); err == nil {
			return &toolResult, nil
		}
	}

	// Fall back to raw response as text content
	return &models.MCPToolResult{
		Content: []models.MCPContent{{
			Type: "text",
			Text: string(respBody),
		}},
	}, nil
}

// executeSSETool calls a tool over SSE transport.
func (gw *Gateway) executeSSETool(ctx context.Context, tool *models.MCPTool, params *models.MCPToolCallParams) (*models.MCPToolResult, error) {
	// For SSE, we POST the request and read SSE events
	rpcReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      params.Name,
			"arguments": params.Arguments,
		},
		"id": uuid.New().String(),
	}
	body, _ := json.Marshal(rpcReq)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", tool.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create SSE request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	gw.applyAuth(httpReq, tool)

	resp, err := gw.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("SSE request failed: %w", err)
	}
	defer resp.Body.Close()

	// Collect all SSE data events
	var allText string
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			allText += string(buf[:n])
		}
		if err != nil {
			break
		}
	}

	return &models.MCPToolResult{
		Content: []models.MCPContent{{
			Type: "text",
			Text: allText,
		}},
	}, nil
}

// applyAuth adds authentication headers based on tool config.
func (gw *Gateway) applyAuth(req *http.Request, tool *models.MCPTool) {
	if tool.AuthConfig == nil {
		return
	}

	authType, _ := tool.AuthConfig["type"].(string)
	switch authType {
	case "bearer":
		if token, ok := tool.AuthConfig["token"].(string); ok {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	case "api-key":
		header, _ := tool.AuthConfig["header"].(string)
		key, _ := tool.AuthConfig["key"].(string)
		if header != "" && key != "" {
			req.Header.Set(header, key)
		}
	case "basic":
		// basic auth would be set via URL or explicit header
	}
}

// ── SSE Subscription Management ─────────────────────────────

// Subscribe creates an SSE subscription for a kitchen.
func (gw *Gateway) Subscribe(kitchen string) <-chan models.MCPResponse {
	ch := make(chan models.MCPResponse, 32)
	gw.subsMu.Lock()
	gw.subs[kitchen] = append(gw.subs[kitchen], ch)
	gw.subsMu.Unlock()
	return ch
}

// Unsubscribe removes an SSE subscription.
func (gw *Gateway) Unsubscribe(kitchen string, ch <-chan models.MCPResponse) {
	gw.subsMu.Lock()
	defer gw.subsMu.Unlock()

	subs := gw.subs[kitchen]
	for i, s := range subs {
		if s == ch {
			gw.subs[kitchen] = append(subs[:i], subs[i+1:]...)
			close(s)
			break
		}
	}
}

// Broadcast sends a notification to all subscribers of a kitchen.
func (gw *Gateway) Broadcast(kitchen string, resp models.MCPResponse) {
	gw.subsMu.RLock()
	defer gw.subsMu.RUnlock()

	for _, ch := range gw.subs[kitchen] {
		select {
		case ch <- resp:
		default:
			// Drop if subscriber is too slow
		}
	}
}
