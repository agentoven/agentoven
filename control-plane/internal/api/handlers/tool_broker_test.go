package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/toolbroker"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/go-chi/chi/v5"
)

type gate map[string]bool

func (g gate) RequiresApproval(_ context.Context, _, tool string) bool { return g[tool] }

type brokerRig struct {
	*mcpRig
	b *toolbroker.Broker
}

func newBrokerRig(t *testing.T, rules ...models.Guardrail) *brokerRig {
	t.Helper()
	t.Setenv("AGENTOVEN_JOURNAL_DIR", t.TempDir()) // held runs of other tests must not be seen here
	m := newMCPRig(t, rules...)
	m.h.Executor.SetGuardrails(m.h.Guardrails) // the rig replaced the service after the executor was given its own
	b := toolbroker.New([]byte("test-secret"), nil)
	m.h.SetToolBroker(b)
	echo := models.ResolvedTool{Name: "echo", Transport: "http", Schema: map[string]interface{}{"type": "object"}}
	for _, kitchen := range []string{"default", "other"} {
		ag := &models.Agent{Name: "triager", Kitchen: kitchen, Status: models.AgentStatusReady, Mode: models.AgentModeManaged,
			ResolvedConfig: &models.ResolvedIngredients{Tools: []models.ResolvedTool{echo}}}
		if kitchen == "other" {
			ag.ResolvedConfig = &models.ResolvedIngredients{} // the same name, no tools
		}
		if err := m.s.CreateAgent(context.Background(), ag); err != nil {
			t.Fatal(err)
		}
	}
	return &brokerRig{mcpRig: m, b: b}
}

func (r *brokerRig) post(tok, body string) (*httptest.ResponseRecorder, map[string]interface{}) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/tool-calls", strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	r.h.BrokeredToolCall(rec, req)
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func errKind(out map[string]interface{}) string {
	e, _ := out["error"].(map[string]interface{})
	k, _ := e["kind"].(string)
	return k
}

func TestAHostedProcessRunsAToolThroughTheControlPlane(t *testing.T) {
	r := newBrokerRig(t)
	tok, done := r.b.Open("default", "triager", "run-1")
	defer done()
	rec, out := r.post(tok, `{"call_id":"c1","name":"echo","arguments":{"x":1}}`)
	if rec.Code != 200 || out["ok"] != true || out["content"] != "done" || atomic.LoadInt32(r.calls) != 1 {
		t.Fatalf("%d %s calls=%d", rec.Code, rec.Body, *r.calls)
	}
}

func TestTheRouteAcceptsNothingButARunToken(t *testing.T) {
	r := newBrokerRig(t)
	other := toolbroker.New([]byte("a different secret"), nil)
	foreign, d1 := other.Open("default", "triager", "r")
	defer d1()
	tok, d2 := r.b.Open("default", "triager", "r")
	defer d2()
	for name, bearer := range map[string]string{"none": "", "garbage": "x", "another server's token": foreign, "user api key": "e2e-key", "a pod token": "podtoken"} {
		rec, out := r.post(bearer, `{"name":"echo","arguments":{}}`)
		if rec.Code != 401 || errKind(out) != "invalid_token" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// Identity headers and identity-bearing contexts do not stand in for the token.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/tool-calls", strings.NewReader(`{"name":"echo","arguments":{}}`))
	req = withKitchen(req, "default")
	req = req.WithContext(pkgmw.SetIdentity(req.Context(), &contracts.Identity{Subject: "u", Role: "head_chef", Kind: "user"}))
	req.Header.Set("X-Service-Token", "anything")
	rec := httptest.NewRecorder()
	r.h.BrokeredToolCall(rec, req)
	if rec.Code != 401 {
		t.Fatalf("a signed-in user is not a run token: %d", rec.Code)
	}
	if atomic.LoadInt32(r.calls) != 0 {
		t.Fatal("a refused call reached the tool")
	}
	_ = tok
}

func TestAReplayAfterTheCallEndedAndAnExpiredTokenAreRefused(t *testing.T) {
	r := newBrokerRig(t)
	tok, done := r.b.Open("default", "triager", "run-1")
	done()
	rec, out := r.post(tok, `{"name":"echo","arguments":{}}`)
	if rec.Code != 403 || errKind(out) != "scope_closed" {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	clk := time.Now().Add(2 * time.Hour)
	late := toolbroker.New([]byte("test-secret"), func() time.Time { return clk })
	r.h.SetToolBroker(late)
	rec, out = r.post(tok, `{"name":"echo","arguments":{}}`)
	if rec.Code != 401 || errKind(out) != "expired_token" {
		t.Fatalf("expired: %d %s", rec.Code, rec.Body)
	}
	if atomic.LoadInt32(r.calls) != 0 {
		t.Fatal("a refused call reached the tool")
	}
}

func TestAProcessCannotNameAnotherKitchenOrAgentInTheBody(t *testing.T) {
	r := newBrokerRig(t)
	// The token is for the agent in the kitchen that has no tools: the other kitchen's echo is out of reach.
	tok, done := r.b.Open("other", "triager", "run-1")
	defer done()
	_, out := r.post(tok, `{"name":"echo","arguments":{}}`)
	if errKind(out) != "unknown_tool" {
		t.Fatalf("the allowlist is the token's agent's: %v", out)
	}
	for _, body := range []string{
		`{"name":"echo","arguments":{},"kitchen":"default"}`,
		`{"name":"echo","arguments":{},"agent":"triager"}`,
		`{"name":"","arguments":{}}`,
		`not json`,
		`{"name":"echo","arguments":` + strings.Repeat("[", 10) + `}`,
	} {
		rec, _ := r.post(tok, body)
		if rec.Code != 400 {
			t.Errorf("%q: %d", body, rec.Code)
		}
	}
	big := `{"name":"echo","arguments":{"x":"` + strings.Repeat("a", 2<<20) + `"}}`
	if rec, _ := r.post(tok, big); rec.Code != 400 {
		t.Errorf("an oversized body: %d", rec.Code)
	}
	if atomic.LoadInt32(r.calls) != 0 {
		t.Fatal("a refused call reached the tool")
	}
}

func TestATokenOfADeletedAgentOpensNothing(t *testing.T) {
	r := newBrokerRig(t)
	tok, done := r.b.Open("default", "ghost", "run-1")
	defer done()
	rec, out := r.post(tok, `{"name":"echo","arguments":{}}`)
	if rec.Code != 403 || errKind(out) != "scope_closed" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestBrokeredCallsAreJudgedByTheGuardrailsLikeALoopsCalls(t *testing.T) {
	r := newBrokerRig(t, ssnRule("block"))
	tok, done := r.b.Open("default", "triager", "run-1")
	defer done()
	_, out := r.post(tok, `{"name":"echo","arguments":{"note":"ssn 123-45-6789"}}`)
	if errKind(out) != "guardrail_blocked" || atomic.LoadInt32(r.calls) != 0 {
		t.Fatalf("%v calls=%d", out, *r.calls)
	}
}

func TestAHeldBrokeredCallIsDecidedThroughResumeRun(t *testing.T) {
	r := newBrokerRig(t)
	r.h.Executor.SetToolApprovalPolicy(gate{"echo": true})
	tok, done := r.b.Open("default", "triager", "run-1")
	defer done()
	res := make(chan map[string]interface{}, 1)
	go func() { _, out := r.post(tok, `{"name":"echo","arguments":{"x":1}}`); res <- out }()
	var id string
	for i := 0; i < 100 && id == ""; i++ {
		runs, _ := r.h.Executor.ListUnfinished(context.Background(), "default")
		if len(runs) > 0 {
			id = runs[0].TraceID
		}
		time.Sleep(50 * time.Millisecond)
	}
	if id == "" || atomic.LoadInt32(r.calls) != 0 {
		t.Fatalf("the call must be held, run=%q calls=%d", id, *r.calls)
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"approved":true}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("agentName", "triager")
	rctx.URLParams.Add("traceID", id)
	req = withKitchen(req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)), "default")
	rec := httptest.NewRecorder()
	r.h.ResumeRun(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "decision_recorded") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	select {
	case out := <-res:
		if out["ok"] != true || out["approval"] != "approved" || atomic.LoadInt32(r.calls) != 1 {
			t.Fatalf("%v calls=%d", out, *r.calls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held call did not return")
	}
}

var _ = handlers.New
