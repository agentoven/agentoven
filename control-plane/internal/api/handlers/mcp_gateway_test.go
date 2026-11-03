package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type mcpRig struct {
	h     *handlers.Handlers
	s     store.Store
	calls *int32
	got   *atomic.Value // the last arguments the tool server received
}

func newMCPRig(t *testing.T, rules ...models.Guardrail) *mcpRig {
	t.Helper()
	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	var calls int32
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var in struct {
			Params struct {
				Arguments map[string]interface{} `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		got.Store(in.Params.Arguments)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"done"}]},"id":"1"}`))
	}))
	t.Cleanup(srv.Close)
	if err := s.CreateTool(context.Background(), &models.MCPTool{ID: "t1", Name: "echo", Kitchen: "default", Endpoint: srv.URL, Transport: "http", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	h := handlers.New(s, router.NewModelRouter(s), mcpgw.NewGateway(s), nil, nil, nil, nil)
	h.Guardrails = &guardrails.CommunityGuardrailService{}
	if len(rules) > 0 {
		h.SetGuardrailPolicy(&wsSource{rules: rules})
	}
	return &mcpRig{h: h, s: s, calls: &calls, got: &got}
}

func (m *mcpRig) call(args string) (*models.MCPResponse, int) {
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":` + args + `}}`
	req := withKitchen(httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)), "default")
	req = req.WithContext(pkgmw.SetIdentity(req.Context(), &contracts.Identity{Subject: "u-ana", Email: "ana@x.test", Provider: "oidc", Role: "baker", Kind: "user"}))
	rec := httptest.NewRecorder()
	m.h.MCPEndpoint(rec, req)
	var resp models.MCPResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return &resp, rec.Code
}

func ssnRule(action string) models.Guardrail {
	return models.Guardrail{ID: "ws-ssn", Kind: models.GuardrailPIIDetection, Stage: models.GuardrailStageAll, Enabled: true, Action: models.GuardrailAction(action),
		Config: map[string]interface{}{"patterns": []interface{}{"ssn"}}}
}

// A tool call through the gateway endpoint is judged by the workspace rules like a tool call of an agent
// (review 5, finding 1: it used to skip every guardrail and leave no audit actor).
func TestAToolCalledThroughTheGatewayIsGuardedAndAudited(t *testing.T) {
	m := newMCPRig(t, ssnRule("block"))
	resp, _ := m.call(`{"note":"ssn 123-45-6789"}`)
	if resp.Error == nil || resp.Error.Code != -32005 || atomic.LoadInt32(m.calls) != 0 {
		t.Fatalf("a blocked argument must refuse the call before the tool is reached: %+v, tool calls %d", resp, atomic.LoadInt32(m.calls))
	}
	if resp, _ := m.call(`{"note":"nothing sensitive"}`); resp.Error != nil || atomic.LoadInt32(m.calls) != 1 {
		t.Fatalf("a clean call runs: %+v", resp)
	}
	events, _ := m.s.ListAuditEvents(context.Background(), models.AuditFilter{Kitchen: "default", Limit: 50})
	var outcomes []string
	for _, e := range events {
		if e.Action == "mcp.tool_call" {
			if e.UserID != "u-ana" || e.ResourceID != "echo" {
				t.Errorf("the audit event does not name the caller and the tool: %+v", e)
			}
			outcomes = append(outcomes, e.Details["outcome"].(string))
		}
	}
	if len(outcomes) != 2 {
		t.Fatalf("both calls are audited, got %v", outcomes)
	}
}

func TestARedactingRuleRewritesTheArgumentsTheToolReceives(t *testing.T) {
	m := newMCPRig(t, ssnRule("redact"))
	if resp, _ := m.call(`{"note":"ssn 123-45-6789"}`); resp.Error != nil {
		t.Fatalf("a redacted call still runs: %+v", resp)
	}
	args, _ := m.got.Load().(map[string]interface{})
	if note, _ := args["note"].(string); strings.Contains(note, "123-45-6789") || note == "" {
		t.Fatalf("the tool received %q", note)
	}
}

func TestTheGatewayRefusesWhenTheWorkspaceRulesCannotBeRead(t *testing.T) {
	m := newMCPRig(t)
	m.h.SetGuardrailPolicy(&wsSource{err: context.DeadlineExceeded})
	if resp, _ := m.call(`{"note":"x"}`); resp.Error == nil || atomic.LoadInt32(m.calls) != 0 {
		t.Fatalf("an unreadable policy must stop the call: %+v", resp)
	}
}
