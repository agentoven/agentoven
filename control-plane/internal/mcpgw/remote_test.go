package mcpgw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoArgs struct {
	Text string `json:"text" jsonschema:"text to echo back"`
}

// fakeMCPServer serves a real MCP server over streamable HTTP that insists on
// a bearer token, so the test proves the handshake and the auth header both work.
func fakeMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Echo the text"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + a.Text}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "fail", Description: "Always fails"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "nope"}}}, nil, nil
		})

	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

var bearer = map[string]interface{}{"type": "bearer", "token": "secret"}

func TestListRemoteTools(t *testing.T) {
	ts := fakeMCPServer(t)
	gw := NewGateway(store.NewMemoryStore())

	tools, err := gw.ListRemoteTools(context.Background(), ts.URL, bearer)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]models.RemoteTool{}
	for _, rt := range tools {
		byName[rt.Name] = rt
	}
	echo, ok := byName["echo"]
	if !ok || len(byName) != 2 {
		t.Fatalf("tools = %v", tools)
	}
	if echo.Description != "Echo the text" {
		t.Errorf("description = %q", echo.Description)
	}
	if props, _ := echo.InputSchema["properties"].(map[string]interface{}); props["text"] == nil {
		t.Errorf("input schema lost its properties: %v", echo.InputSchema)
	}
}

func TestListRemoteToolsWrongToken(t *testing.T) {
	ts := fakeMCPServer(t)
	gw := NewGateway(store.NewMemoryStore())
	if _, err := gw.ListRemoteTools(context.Background(), ts.URL, map[string]interface{}{"type": "bearer", "token": "bad"}); err == nil {
		t.Fatal("expected an error with the wrong token")
	}
	if _, err := gw.ListRemoteTools(context.Background(), ts.URL, nil); err == nil {
		t.Fatal("expected an error with no token")
	}
}

// callTool goes through HandleJSONRPC, the same entry the executor uses.
func callTool(t *testing.T, gw *Gateway, name string, args map[string]interface{}) models.MCPToolResult {
	t.Helper()
	params, _ := json.Marshal(models.MCPToolCallParams{Name: name, Arguments: args})
	resp := gw.HandleJSONRPC(context.Background(), "default", &models.MCPRequest{Jsonrpc: "2.0", Method: "tools/call", Params: params, ID: "1"})
	if resp.Error != nil {
		t.Fatalf("rpc error: %v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var res models.MCPToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestExecuteMCPToolUsesRemoteName(t *testing.T) {
	ts := fakeMCPServer(t)
	st := store.NewMemoryStore()
	gw := NewGateway(st)
	for _, tool := range []*models.MCPTool{
		{ID: "1", Name: "demo_echo", RemoteName: "echo", Kitchen: "default", Endpoint: ts.URL, Transport: TransportMCP, AuthConfig: bearer, Enabled: true},
		{ID: "2", Name: "demo_fail", RemoteName: "fail", Kitchen: "default", Endpoint: ts.URL, Transport: TransportMCP, AuthConfig: bearer, Enabled: true},
	} {
		if err := st.CreateTool(context.Background(), tool); err != nil {
			t.Fatal(err)
		}
	}

	res := callTool(t, gw, "demo_echo", map[string]interface{}{"text": "hi"})
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text != "echo: hi" {
		t.Fatalf("result = %+v", res)
	}

	// A tool-level failure comes back as an error result the model can read.
	res = callTool(t, gw, "demo_fail", map[string]interface{}{"text": "x"})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "nope") {
		t.Fatalf("result = %+v", res)
	}
}

func TestExecuteMCPToolUnreachable(t *testing.T) {
	st := store.NewMemoryStore()
	gw := NewGateway(st)
	_ = st.CreateTool(context.Background(), &models.MCPTool{ID: "1", Name: "t", Kitchen: "default", Endpoint: "http://127.0.0.1:1/mcp", Transport: TransportMCP, Enabled: true})
	res := callTool(t, gw, "t", nil)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "connect to MCP server") {
		t.Fatalf("result = %+v", res)
	}
}
