package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// rawDriver answers every call and counts them, so a test can say whether a provider was reached at all.
type rawDriver struct{ calls int32 }

func (d *rawDriver) Kind() string                                             { return "raw" }
func (d *rawDriver) HealthCheck(context.Context, *models.ModelProvider) error { return nil }
func (d *rawDriver) Call(_ context.Context, p *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	atomic.AddInt32(&d.calls, 1)
	return &models.RouteResponse{Provider: p.Name, Model: req.Model, Content: "ok", Usage: models.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
}

type rawRig struct {
	h *handlers.Handlers
	s store.Store
	d *rawDriver
}

func newRawRig(t *testing.T) *rawRig {
	t.Helper()
	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	d := &rawDriver{}
	mr := router.NewModelRouter(s)
	mr.RegisterDriver(d)
	ctx := context.Background()
	// The provider and the agent exist in kitchen "default" only; the caller below belongs to "solo".
	_ = s.CreateProvider(ctx, &models.ModelProvider{Name: "p", Kitchen: "default", Kind: "raw", Models: []string{"m"}, IsDefault: true})
	_ = s.CreateAgent(ctx, &models.Agent{Name: "victim-agent", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady})
	h := handlers.New(s, mr, nil, nil, nil, sessions.NewMemorySessionStore(), nil)
	h.Guardrails = &guardrails.CommunityGuardrailService{}
	mr.SetContentGuard(h.ModelGuard())
	return &rawRig{h: h, s: s, d: d}
}

func (r *rawRig) route(kitchen, body string) *httptest.ResponseRecorder {
	req := withKitchen(httptest.NewRequest(http.MethodPost, "/models/route", strings.NewReader(body)), kitchen)
	rec := httptest.NewRecorder()
	r.h.RouteModel(rec, req)
	return rec
}

// Review 5, finding 2: the kitchen came from the body, so a member of one kitchen spent another's provider.
func TestTheRawRouteIgnoresAKitchenNamedInTheBody(t *testing.T) {
	r := newRawRig(t)
	rec := r.route("solo", `{"kitchen":"default","model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code == http.StatusOK || atomic.LoadInt32(&r.d.calls) != 0 {
		t.Fatalf("a caller in kitchen solo reached kitchen default's provider: %d %s (provider calls %d)", rec.Code, rec.Body.String(), r.d.calls)
	}
	if rec := r.route("default", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("the caller's own kitchen works: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTheRawRouteOnlyAttributesACallToAnAgentOfTheCallersKitchen(t *testing.T) {
	r := newRawRig(t)
	for _, ref := range []string{"invented-name", "victim-agent"} {
		if rec := r.route("solo", `{"agent_ref":"`+ref+`","model":"m","messages":[{"role":"user","content":"hi"}]}`); rec.Code != http.StatusNotFound {
			t.Errorf("agent_ref %q from kitchen solo: %d, want 404", ref, rec.Code)
		}
	}
	if rec := r.route("default", `{"agent_ref":"victim-agent","model":"m","messages":[{"role":"user","content":"hi"}]}`); rec.Code != http.StatusOK {
		t.Errorf("an agent of the caller's own kitchen: %d %s", rec.Code, rec.Body.String())
	}
}

// Review 5, finding 3: the raw route sent what the kitchen forbids to the model.
func TestTheRawRouteIsHeldToTheWorkspaceGuardrails(t *testing.T) {
	r := newRawRig(t)
	r.h.SetGuardrailPolicy(&wsSource{rules: []models.Guardrail{
		{ID: "ws-ssn", Kind: models.GuardrailPIIDetection, Stage: models.GuardrailStageAll, Enabled: true, Action: models.GuardrailActionBlock,
			Config: map[string]interface{}{"patterns": []interface{}{"ssn"}}}}})
	rec := r.route("default", `{"model":"m","messages":[{"role":"user","content":"my ssn is 123-45-6789"}]}`)
	if rec.Code != http.StatusForbidden || atomic.LoadInt32(&r.d.calls) != 0 {
		t.Fatalf("a blocked prompt must be a 403 and never reach the provider: %d %s (calls %d)", rec.Code, rec.Body.String(), r.d.calls)
	}
	// The same through a role that is not "user", and through a tool result.
	for _, body := range []string{
		`{"model":"m","messages":[{"role":"system","content":"ssn 123-45-6789"},{"role":"user","content":"hi"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"ssn 123-45-6789","tool_call_id":"1"}]}`,
		`{"model":"m","messages":[{"role":"user","content_parts":[{"type":"text","text":"ssn 123-45-6789"}]}]}`,
	} {
		if rec := r.route("default", body); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	if atomic.LoadInt32(&r.d.calls) != 0 {
		t.Fatalf("the provider was reached %d times", r.d.calls)
	}
	if rec := r.route("default", `{"model":"m","messages":[{"role":"user","content":"hello"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("a clean prompt goes through: %d %s", rec.Code, rec.Body.String())
	}
}

// A budget that is used up is a 402 and a rate limit a 429 on the raw route, not a 502 (review 5, finding 11).
type fixedRefusalGate struct{ err error }

func (g fixedRefusalGate) Allow(context.Context, models.SpendQuery) error { return g.err }
func (fixedRefusalGate) Record(context.Context, models.SpendRecord)       {}

func TestTheRawRouteAnswersARefusalWithItsOwnStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"budget":     {&models.SpendRefusedError{Scope: "kitchen", Reason: "used up", Spent: 1, Cap: 1}, http.StatusPaymentRequired},
		"rate limit": {&models.RateLimitedError{Scope: "kitchen", Limit: "requests_per_minute", RetryAfter: 5e9, Reason: "slow down"}, http.StatusTooManyRequests},
	} {
		r := newRawRig(t)
		r.h.Router.SetSpendGate(fixedRefusalGate{tc.err})
		rec := r.route("default", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
		if rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body.String(), tc.want)
		}
		if tc.want == http.StatusTooManyRequests && rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s: no Retry-After", name)
		}
		// The stream answers the same way, before it is committed to a 200.
		req := withKitchen(httptest.NewRequest(http.MethodPost, "/models/route/stream", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)), "default")
		srec := httptest.NewRecorder()
		r.h.RouteModelStream(srec, req)
		if srec.Code != tc.want {
			t.Errorf("%s stream: %d %s, want %d", name, srec.Code, srec.Body.String(), tc.want)
		}
	}
}

// The community edition keeps its opt-in authentication: with no identity on the request (authentication is not
// required) the raw route and the MCP gateway work as they always did, in the default kitchen. The always-on
// identity requirement is the enterprise route table's (agentoven-pro routeguard).
func TestTheRawRouteAndTheGatewayStillWorkAnonymouslyWhenAuthIsNotRequired(t *testing.T) {
	r := newRawRig(t)
	req := httptest.NewRequest(http.MethodPost, "/models/route", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	r.h.RouteModel(rec, req) // no identity, no kitchen: the default kitchen
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous /models/route: %d %s", rec.Code, rec.Body.String())
	}
	m := newMCPRig(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"a":"b"}}}`
	mreq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	mrec := httptest.NewRecorder()
	m.h.MCPEndpoint(mrec, mreq)
	if mrec.Code != http.StatusOK || strings.Contains(mrec.Body.String(), `"error"`) || atomic.LoadInt32(m.calls) != 1 {
		t.Fatalf("anonymous /mcp tools/call: %d %s (tool calls %d)", mrec.Code, mrec.Body.String(), atomic.LoadInt32(m.calls))
	}
}
