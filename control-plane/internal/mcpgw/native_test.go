package mcpgw

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type fakeRunner struct{ calls int }

func (f *fakeRunner) RunNativeTool(_ context.Context, tool *models.MCPTool, args map[string]interface{}) (*models.MCPToolResult, error) {
	f.calls++
	return &models.MCPToolResult{Content: []models.MCPContent{{Type: "text", Text: tool.Name + ":" + args["x"].(string)}}}, nil
}

type gate map[string]bool

func (g gate) RequiresApproval(_ context.Context, _, tool string) bool { return g[tool] }

func nativeFixture(t *testing.T) (*Gateway, *fakeRunner) {
	t.Helper()
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	for _, name := range []string{"pack_read", "pack_write"} {
		if err := s.CreateTool(context.Background(), &models.MCPTool{ID: name, Name: name, Kitchen: "default",
			Transport: models.MCPTransportNative, Endpoint: "pack://x/" + name, Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	gw := NewGateway(s)
	r := &fakeRunner{}
	gw.SetNativeToolRunner(r)
	return gw, r
}

func call(t *testing.T, gw *Gateway, ctx context.Context, tool string) *models.MCPResponse {
	t.Helper()
	params, _ := json.Marshal(models.MCPToolCallParams{Name: tool, Arguments: map[string]interface{}{"x": "1"}})
	return gw.HandleJSONRPC(ctx, "default", &models.MCPRequest{Jsonrpc: "2.0", Method: "tools/call", Params: params, ID: "1"})
}

func TestNativeToolRunsThroughTheInstalledRunner(t *testing.T) {
	gw, r := nativeFixture(t)
	resp := call(t, gw, context.Background(), "pack_read")
	if resp.Error != nil || r.calls != 1 {
		t.Fatalf("expected one run through the runner: %+v calls=%d", resp, r.calls)
	}
	raw, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(raw), "pack_read:1") {
		t.Fatalf("result should come from the runner: %s", raw)
	}
}

func TestNativeToolWithoutARunnerFailsClearly(t *testing.T) {
	gw, _ := nativeFixture(t)
	gw.SetNativeToolRunner(nil)
	resp := call(t, gw, context.Background(), "pack_read")
	raw, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(raw), "cannot run tool packs") {
		t.Fatalf("expected a clear refusal, got %s", raw)
	}
}

func TestApprovalGatedToolIsRefusedUnlessItPassedTheGate(t *testing.T) {
	gw, r := nativeFixture(t)
	gw.SetToolApprovalPolicy(gate{"pack_write": true})

	resp := call(t, gw, context.Background(), "pack_write")
	if resp.Error == nil || resp.Error.Code != -32004 || r.calls != 0 {
		t.Fatalf("a direct call to a gated tool must be refused without running it: %+v calls=%d", resp, r.calls)
	}
	if resp := call(t, gw, context.Background(), "pack_read"); resp.Error != nil || r.calls != 1 {
		t.Fatalf("an ungated tool is unaffected: %+v", resp)
	}
	if resp := call(t, gw, contracts.WithToolApproved(context.Background()), "pack_write"); resp.Error != nil || r.calls != 2 {
		t.Fatalf("a call that passed the gate runs: %+v", resp)
	}
}
