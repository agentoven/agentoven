package mcpgw

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/toolauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TransportMCP is the transport of a tool served by a remote MCP server over
// streamable HTTP: the session handshake, session id and JSON-or-SSE
// responses are handled by the official MCP client. The plain "http" and
// "sse" transports POST a bare tools/call and only suit servers that accept
// that.
const TransportMCP = models.MCPTransportStreamableHTTP

// ListRemoteTools connects to a remote MCP server and returns the tools it
// offers. auth is a resolved authentication (see pkg/toolauth), or nil.
func (gw *Gateway) ListRemoteTools(ctx context.Context, endpoint string, auth map[string]interface{}) ([]models.RemoteTool, error) {
	cs, err := gw.connectRemote(ctx, endpoint, auth)
	if err != nil {
		return nil, err
	}
	defer cs.Close()

	var out []models.RemoteTool
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		rt := models.RemoteTool{Name: t.Name, Description: t.Description}
		// The schema crosses the SDK as an untyped value; round-trip it to a plain map.
		if raw, err := json.Marshal(t.InputSchema); err == nil {
			_ = json.Unmarshal(raw, &rt.InputSchema)
		}
		out = append(out, rt)
	}
	return out, nil
}

// executeMCPTool calls one tool of a remote MCP server. Each call opens its
// own session and closes it again, so nothing is held between calls and no
// instance owns a session, which keeps it correct when the control plane
// scales out or runs serverless. The cost is the handshake's extra round trips.
func (gw *Gateway) executeMCPTool(ctx context.Context, tool *models.MCPTool, params *models.MCPToolCallParams, auth map[string]interface{}) (*models.MCPToolResult, error) {
	cs, err := gw.connectRemote(ctx, tool.Endpoint, auth)
	if err != nil {
		return nil, err
	}
	defer cs.Close()

	name := tool.RemoteName
	if name == "" {
		name = params.Name
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: params.Arguments})
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", name, err)
	}

	out := &models.MCPToolResult{IsError: res.IsError}
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			out.Content = append(out.Content, models.MCPContent{Type: "text", Text: t.Text})
		}
	}
	// A tool that answers only with structured content still reaches the model.
	if len(out.Content) == 0 && res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			out.Content = []models.MCPContent{{Type: "text", Text: string(raw)}}
		}
	}
	return out, nil
}

func (gw *Gateway) connectRemote(ctx context.Context, endpoint string, auth map[string]interface{}) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "agentoven-mcp-gateway", Version: "0.2.0"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Timeout: gw.client.Timeout, Transport: authTransport{auth: auth, next: http.DefaultTransport}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server: %w", err)
	}
	return cs, nil
}

// authTransport applies a tool's AuthConfig to every request the MCP client sends.
type authTransport struct {
	auth map[string]interface{}
	next http.RoundTripper
}

func (a authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	toolauth.Apply(req, a.auth)
	return a.next.RoundTrip(req)
}
