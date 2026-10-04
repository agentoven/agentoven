package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func outputSchema() map[string]interface{} {
	return map[string]interface{}{
		"name":   "answer",
		"strict": true,
		"schema": map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"verdict": map[string]interface{}{"type": "string"}},
			"required":   []interface{}{"verdict"},
		},
	}
}

func TestOpenAIPassesResponseFormatThrough(t *testing.T) {
	var captured map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","choices":[{"message":{"content":"{\"verdict\":\"ok\"}"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	mr := router.NewModelRouter(s)
	ctx := t.Context()
	if err := s.CreateProvider(ctx, &models.ModelProvider{
		Name: "p", Kitchen: "default", Kind: "openai", Endpoint: srv.URL, Models: []string{"m"}, IsDefault: true,
		Config: map[string]interface{}{"api_key": "k"},
	}); err != nil {
		t.Fatal(err)
	}

	format := &models.ResponseFormat{Type: "json_schema", JSONSchema: outputSchema()}
	resp, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "m", ResponseFormat: format})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != `{"verdict":"ok"}` {
		t.Fatalf("unexpected content %q", resp.Content)
	}
	rf, ok := captured["response_format"].(map[string]interface{})
	if !ok {
		t.Fatalf("response_format was not sent on the wire: %v", captured)
	}
	if rf["type"] != "json_schema" {
		t.Fatalf("expected type json_schema, got %v", rf["type"])
	}
}

func TestAnthropicForcesAToolForStructuredOutputAndSurfacesItAsContent(t *testing.T) {
	var captured map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"emit_structured_output","input":{"verdict":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	mr := router.NewModelRouter(s)
	ctx := t.Context()
	if err := s.CreateProvider(ctx, &models.ModelProvider{
		Name: "p", Kitchen: "default", Kind: "anthropic", Endpoint: srv.URL, Models: []string{"claude-x"}, IsDefault: true,
		Config: map[string]interface{}{"api_key": "k"},
	}); err != nil {
		t.Fatal(err)
	}

	format := &models.ResponseFormat{Type: "json_schema", JSONSchema: outputSchema()}
	resp, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "claude-x", ResponseFormat: format})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != `{"verdict":"ok"}` {
		t.Fatalf("expected the tool's arguments surfaced as Content, got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("the synthetic tool call must not appear as a real tool call, got %+v", resp.ToolCalls)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("expected finish_reason stop for a resolved structured answer, got %q", resp.FinishReason)
	}

	tools, ok := captured["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected exactly one synthetic tool on the wire, got %v", captured["tools"])
	}
	choice, ok := captured["tool_choice"].(map[string]interface{})
	if !ok || choice["name"] != "emit_structured_output" {
		t.Fatalf("expected tool_choice forcing the synthetic tool, got %v", captured["tool_choice"])
	}
}

func TestAnthropicSkipsStructuredOutputWhenTheAgentHasRealTools(t *testing.T) {
	var captured map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	mr := router.NewModelRouter(s)
	ctx := t.Context()
	if err := s.CreateProvider(ctx, &models.ModelProvider{
		Name: "p", Kitchen: "default", Kind: "anthropic", Endpoint: srv.URL, Models: []string{"claude-x"}, IsDefault: true,
		Config: map[string]interface{}{"api_key": "k"},
	}); err != nil {
		t.Fatal(err)
	}

	format := &models.ResponseFormat{Type: "json_schema", JSONSchema: outputSchema()}
	realTool := models.ToolDefinition{Type: "function", Function: models.ToolFunction{Name: "lookup", Parameters: map[string]interface{}{"type": "object"}}}
	if _, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "claude-x", ResponseFormat: format, Tools: []models.ToolDefinition{realTool}}); err != nil {
		t.Fatal(err)
	}

	tools, _ := captured["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("expected only the agent's real tool, got %v", captured["tools"])
	}
	first, _ := tools[0].(map[string]interface{})
	if first["name"] != "lookup" {
		t.Fatalf("the agent's own tool must not be replaced by the synthetic one, got %v", first)
	}
	if captured["tool_choice"] != nil {
		t.Fatalf("tool_choice must not be forced when the agent has its own tools, got %v", captured["tool_choice"])
	}
}
