package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func runRig(t *testing.T) (*handlers.Handlers, executor.Journal) {
	t.Helper()
	t.Setenv("AGENTOVEN_JOURNAL_DIR", t.TempDir())
	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	h := handlers.New(s, router.NewModelRouter(s), mcpgw.NewGateway(s), nil, nil, nil, nil)
	j := h.Executor.Journal()
	if j == nil {
		t.Fatal("the executor has no journal")
	}
	return h, j
}

func beginRun(t *testing.T, j executor.Journal, id string) {
	t.Helper()
	if err := j.Start(context.Background(), executor.JournalHeader{
		TraceID: id, Kitchen: "default", Agent: &models.Agent{Name: "billing", Kitchen: "default"}, StartedAt: timeNow(), StartedBy: "ana@x.test", Schema: 2,
	}); err != nil {
		t.Fatal(err)
	}
}

func resume(h *handlers.Handlers, traceID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req = withChiParam(withChiParam(withKitchen(req, "default"), "agentName", "billing"), "traceID", traceID)
	req = req.WithContext(pkgmw.SetIdentity(req.Context(), &contracts.Identity{Subject: "u-bo", Email: "bo@x.test", Role: "chef", Kind: "user"}))
	rec := httptest.NewRecorder()
	h.ResumeRun(rec, req)
	return rec
}

func TestAToolApprovalOnAPausedRunIsSignedAgainstTheCallsItWasShown(t *testing.T) {
	h, j := runRig(t)
	h.RegisterOwnDecisionGates()
	beginRun(t, j, "run-1")
	pending := executor.PendingApproval{Turn: 2, GatedNames: []string{"refund"},
		ToolCalls: []executor.ToolCall{{ID: "c1", Name: "refund", Arguments: map[string]interface{}{"amount": 40, "to": "acct-9"}}}}
	if err := j.MarkPaused(context.Background(), "run-1", pending, nil); err != nil {
		t.Fatal(err)
	}

	// What the challenge shows is what the handler will ask to have signed.
	shown, err := h.DescribeDecision(context.Background(), contracts.GateToolApproval, "default", "run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if shown.Title == "" || shown.Requester != "ana@x.test" || shown.Scope != "billing" || len(shown.Digest) != 64 {
		t.Fatalf("subject = %+v", shown)
	}
	calls, _ := shown.Display["tool_calls"].([]map[string]interface{})
	if len(calls) != 1 || calls[0]["name"] != "refund" {
		t.Fatalf("display = %+v", shown.Display)
	}
	// The run is waiting for a tool decision, not a budget one.
	if _, err := h.DescribeDecision(context.Background(), contracts.GateBudgetExtension, "default", "run-1", nil); err == nil {
		t.Fatal("a tool pause is not a budget pause")
	}

	fs := &fakeSigner{refuse: &contracts.AuthError{Status: 403, Code: "signature_required", Message: "sign it"}}
	h.SetDecisionSigner(fs)
	rec := resume(h, "run-1", `{"approved":true}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "signature_required") {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	if len(fs.applied) != 0 {
		t.Fatal("a refused decision is not applied")
	}
	if js, _ := j.Load(context.Background(), "run-1"); js.Status != executor.RunPaused {
		t.Fatalf("the run must still be paused: %s", js.Status)
	}

	fs.refuse, fs.signer = nil, "ana"
	// The note is part of what is signed, so the challenge for a decision with a note carries it (the dashboard
	// sends the same payload it sends with the resume call).
	shown, err = h.DescribeDecision(context.Background(), contracts.GateToolApproval, "default", "run-1", map[string]interface{}{"note": "too much"})
	if err != nil {
		t.Fatal(err)
	}
	rec = resume(h, "run-1", `{"approved":false,"note":"too much"}`)
	// There is no model behind this run, so applying the rejection fails after the signature: that is
	// reported to the signer as not applied, and the signature stays on record.
	got := fs.got[len(fs.got)-1]
	if got.Digest != shown.Digest || got.Gate != contracts.GateToolApproval || fs.meaning[len(fs.meaning)-1] != contracts.MeaningRejected {
		t.Fatalf("the handler asked to have %+v signed (%v), the challenge showed %+v", got, fs.meaning, shown)
	}
	if len(fs.applied) != 1 {
		t.Fatalf("the signer must be told how the decision went (rec %d %s)", rec.Code, rec.Body)
	}
}

func TestABudgetExtensionIsSignedWithItsAmounts(t *testing.T) {
	h, j := runRig(t)
	h.RegisterOwnDecisionGates()
	beginRun(t, j, "run-2")
	if err := j.MarkBudgetPaused(context.Background(), "run-2", models.BudgetPause{Turn: 5, Dimension: "cost_usd", Spent: 4.9, Cap: 5, Scope: "run"}); err != nil {
		t.Fatal(err)
	}
	// The extension the client sends in the challenge and the one the handler decodes digest alike,
	// whatever zero fields the client wrote.
	fromClient := map[string]interface{}{"extend": map[string]interface{}{"add_cost_usd": 2.5, "add_tokens": 0, "reason": "finish the report", "by": "someone"}}
	a, err := h.DescribeDecision(context.Background(), contracts.GateBudgetExtension, "default", "run-2", fromClient)
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeSigner{signer: "ana"}
	h.SetDecisionSigner(fs)
	resume(h, "run-2", `{"approved":true,"extend":{"add_cost_usd":2.5,"reason":"finish the report"}}`)
	if len(fs.got) == 0 || fs.got[0].Gate != contracts.GateBudgetExtension {
		t.Fatalf("handler subjects = %+v", fs.got)
	}
	if fs.got[0].Digest != a.Digest {
		t.Fatal("the digest the handler asked to have signed differs from the one the challenge showed")
	}
	// A different amount is a different signature.
	b, _ := h.DescribeDecision(context.Background(), contracts.GateBudgetExtension, "default", "run-2",
		map[string]interface{}{"extend": map[string]interface{}{"add_cost_usd": 25, "reason": "finish the report"}})
	if b.Digest == a.Digest {
		t.Fatal("the amount is part of what is signed")
	}
}

func TestAnUnpausedRunHasNothingToSign(t *testing.T) {
	h, j := runRig(t)
	h.RegisterOwnDecisionGates()
	beginRun(t, j, "run-3")
	if _, err := h.DescribeDecision(context.Background(), contracts.GateToolApproval, "default", "run-3", nil); err == nil {
		t.Fatal("a running run is not waiting for a decision")
	}
	if _, err := h.DescribeDecision(context.Background(), contracts.GateToolApproval, "other", "run-3", nil); err == nil {
		t.Fatal("another kitchen cannot see it")
	}
}

func timeNow() time.Time { return time.Now().UTC() }

// The free-text note of a decision is what the model is told on a refusal ("Human denied this tool call: <note>"),
// so it is part of what is signed: the same decision with another note is another signature, and a decision
// with no note digests as it always did.
func TestTheNoteOfAToolOrBudgetDecisionIsSigned(t *testing.T) {
	h, j := runRig(t)
	h.RegisterOwnDecisionGates()
	ctx := context.Background()
	beginRun(t, j, "run-n1")
	if err := j.MarkPaused(ctx, "run-n1", executor.PendingApproval{Turn: 1, GatedNames: []string{"refund"},
		ToolCalls: []executor.ToolCall{{ID: "c1", Name: "refund", Arguments: map[string]interface{}{"amount": 40}}}}, nil); err != nil {
		t.Fatal(err)
	}
	beginRun(t, j, "run-n2")
	if err := j.MarkBudgetPaused(ctx, "run-n2", models.BudgetPause{Turn: 5, Dimension: "cost_usd", Spent: 4.9, Cap: 5, Scope: "run"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ gate, run string }{{contracts.GateToolApproval, "run-n1"}, {contracts.GateBudgetExtension, "run-n2"}} {
		digest := func(payload map[string]interface{}) string {
			s, err := h.DescribeDecision(ctx, c.gate, "default", c.run, payload)
			if err != nil {
				t.Fatal(err)
			}
			return s.Digest
		}
		none, blank := digest(nil), digest(map[string]interface{}{"note": "  "})
		wrong, other := digest(map[string]interface{}{"note": "wrong order"}), digest(map[string]interface{}{"note": "wrong customer"})
		if none != blank {
			t.Errorf("%s: a blank note is no note", c.gate)
		}
		if wrong == none || wrong == other {
			t.Errorf("%s: the note is part of what is signed", c.gate)
		}
		if again := digest(map[string]interface{}{"note": "wrong order"}); again != wrong {
			t.Errorf("%s: the same note digests the same", c.gate)
		}
	}
	// The handler asks for the same digest the challenge showed, whichever way the decision goes.
	shown, _ := h.DescribeDecision(ctx, contracts.GateToolApproval, "default", "run-n1", map[string]interface{}{"note": "wrong order"})
	fs := &fakeSigner{signer: "ana"}
	h.SetDecisionSigner(fs)
	resume(h, "run-n1", `{"approved":false,"note":"wrong order"}`)
	if len(fs.got) != 1 || fs.got[0].Digest != shown.Digest || fs.got[0].Display["note"] != "wrong order" {
		t.Fatalf("subjects: %+v", fs.got)
	}
}
