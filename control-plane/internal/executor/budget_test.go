package executor_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// The existing scriptedDriver reports a fixed 15 tokens and no cost, so it cannot exercise a
// spend cap. costDriver reports real usage per call and, like a provider, never produces
// more output than max_tokens allows.
type costDriver struct {
	mu       sync.Mutex
	replies  []turnReply
	n        int
	in, out  int64
	inPer1K  float64
	outPer1K float64
	delay    time.Duration
	onCall   func(n int)
	// fn, when set, picks each reply from the request instead of the fixed list, for tests
	// where a parent and its subagent share one driver.
	fn       func(req *models.RouteRequest) turnReply
	requests []*models.RouteRequest
	// failN makes the first failN calls return failErr without using a scripted reply.
	failN   int
	failErr error
}

func (d *costDriver) Kind() string { return "costed" }
func (d *costDriver) Call(ctx context.Context, p *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	d.mu.Lock()
	d.requests = append(d.requests, req)
	if len(d.requests) <= d.failN {
		d.mu.Unlock()
		return nil, d.failErr
	}
	var r turnReply
	if d.fn != nil {
		r = d.fn(req)
	} else {
		if d.n >= len(d.replies) {
			d.mu.Unlock()
			return nil, fmt.Errorf("costDriver: no reply scripted for call %d", d.n+1)
		}
		r = d.replies[d.n]
	}
	d.n++
	n := d.n
	d.mu.Unlock()

	if d.onCall != nil {
		d.onCall(n)
	}
	if d.delay > 0 {
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out := d.out
	if req.MaxTokens != nil && int64(*req.MaxTokens) < out {
		out = int64(*req.MaxTokens)
	}
	finish := "stop"
	if len(r.toolCalls) > 0 {
		finish = "tool_calls"
	}
	return &models.RouteResponse{
		Provider: p.Name, Model: req.Model, Content: r.content, FinishReason: finish, ToolCalls: r.toolCalls,
		Usage: models.TokenUsage{
			InputTokens: d.in, OutputTokens: out, TotalTokens: d.in + out,
			EstimatedCost: float64(d.in)/1000*d.inPer1K + float64(out)/1000*d.outPer1K,
		},
	}, nil
}
func (d *costDriver) HealthCheck(context.Context, *models.ModelProvider) error { return nil }

func (d *costDriver) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}

// newBudgetExecutor builds an executor with a journal. priced gives the provider a
// cost_per_1k price (so a dollar cap is allowed); otherwise the model is unpriced.
func newBudgetExecutor(t *testing.T, d *costDriver, gw *fakeGateway, priced bool) (*executor.Executor, executor.Journal, store.Store) {
	t.Helper()
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	mr := router.NewModelRouter(s)
	mr.RegisterDriver(d)
	prov := &models.ModelProvider{Name: "p", Kitchen: "default", Kind: "costed", Models: []string{"test-model"}, IsDefault: true}
	if priced {
		prov.Config = map[string]interface{}{"cost_per_1k_input": d.inPer1K, "cost_per_1k_output": d.outPer1K}
	}
	if err := s.CreateProvider(context.Background(), prov); err != nil {
		t.Fatal(err)
	}
	e := executor.NewExecutor(s, mr, gw, sessions.NewMemorySessionStore())
	e.SetModelRetry(0, 0) // tests that want retries ask for them
	j, err := executor.NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.SetJournal(j)
	return e, j, s
}

// loopOf scripts n turns that each call lookup_order, then a final answer.
// resolvedP is resolvedWithTools naming the test provider, which a dollar cap needs in order
// to look up a price.
func resolvedP(tools ...models.ResolvedTool) *models.ResolvedIngredients {
	r := resolvedWithTools(tools...)
	r.Model.Provider = "p"
	return r
}

func loopOf(n int, final string) []turnReply {
	var r []turnReply
	for i := 0; i < n; i++ {
		// Distinct arguments each turn: a run repeating one call is what stall detection stops.
		r = append(r, turnReply{toolCalls: []models.ToolCallResult{toolCall(fmt.Sprint("c", i), "lookup_order", map[string]interface{}{"order_id": fmt.Sprint("A", i)})}})
	}
	return append(r, turnReply{content: final})
}

func orderGateway() *fakeGateway {
	gw := newFakeGateway()
	gw.register("lookup_order", func(map[string]interface{}) (string, bool) { return "delivered", false })
	return gw
}

func budgetAgent(b models.RunBudget) *models.Agent {
	a := testAgent()
	a.MaxTurns = 50
	a.Budget = &b
	return a
}

func asBudgetPause(t *testing.T, err error) *models.BudgetPause {
	t.Helper()
	var pe *executor.PausedError
	if !errors.As(err, &pe) || pe.Kind != executor.PauseBudget || pe.Budget == nil {
		t.Fatalf("expected a budget pause, got %v", err)
	}
	return pe.Budget
}

// ── each cap pauses at the right turn ─────────────────────────

func TestTokenCapPausesBeforeAnOverrunningCall(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "done"), in: 90, out: 10} // 100 tokens a call
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)

	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 450}), "go", resolvedP(orderTool()), nil, false)
	p := asBudgetPause(t, err)
	if p.Dimension != models.BudgetDimTokens {
		t.Fatalf("dimension = %q", p.Dimension)
	}
	// 450 cap, 100 a call: after 3 calls 300 are spent; the fourth would need its prompt
	// (about 115 tokens) and a minimum reply on top, which does not fit, so it is not sent.
	if d.calls() != 3 {
		t.Fatalf("expected 3 model calls before the pause, got %d", d.calls())
	}
	if trace.Budget == nil || trace.Budget.SpentTokens != 300 {
		t.Fatalf("trace should report the spend at the pause: %+v", trace.Budget)
	}
}

func TestEveryCallCarriesAMaxTokensWithinWhatIsLeft(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "done"), in: 90, out: 10}
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)

	_, _, _ = e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 450}), "go", resolvedP(orderTool()), nil, false)

	spent := int64(0)
	for i, r := range d.requests {
		if r.MaxTokens == nil {
			t.Fatalf("call %d went out with no max_tokens although a token cap is set", i+1)
		}
		if spent+int64(*r.MaxTokens) > 450 {
			t.Fatalf("call %d may produce %d tokens after %d were spent, past the 450 cap", i+1, *r.MaxTokens, spent)
		}
		spent += 100
	}
}

func TestCostCapPausesAndReportsDollars(t *testing.T) {
	// 1000 in + 500 out at $0.01/$0.02 per 1K is $0.02 a call.
	d := &costDriver{replies: loopOf(10, "done"), in: 1000, out: 500, inPer1K: 0.01, outPer1K: 0.02}
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), true)

	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxCostUSD: 0.05}), "go", resolvedP(orderTool()), nil, false)
	p := asBudgetPause(t, err)
	if p.Dimension != models.BudgetDimCost || p.Cap != 0.05 {
		t.Fatalf("unexpected pause %+v", p)
	}
	// The third call starts at $0.04 spent and is allowed; the overshoot is that one call.
	// The fourth must not go out.
	if d.calls() > 3 {
		t.Fatalf("a call went out after the cap was reached: %d calls", d.calls())
	}
	if trace.Budget.SpentCostUSD < 0.05 {
		t.Fatalf("expected the pause once the cap was reached, spent %v", trace.Budget.SpentCostUSD)
	}
}

func TestTurnCapPausesInsteadOfSayingMaxTurnsReached(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "done"), in: 10, out: 5}
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)

	resp, _, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTurns: 2}), "go", resolvedP(orderTool()), nil, false)
	p := asBudgetPause(t, err)
	if p.Dimension != models.BudgetDimTurns || d.calls() != 2 || resp != "" {
		t.Fatalf("pause %+v after %d calls, response %q", p, d.calls(), resp)
	}
}

func TestActiveTimeCapPauses(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "done"), in: 10, out: 5, delay: 600 * time.Millisecond}
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)

	_, _, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxActiveSeconds: 1}), "go", resolvedP(orderTool()), nil, false)
	p := asBudgetPause(t, err)
	if p.Dimension != models.BudgetDimActive {
		t.Fatalf("dimension = %q", p.Dimension)
	}
	if d.calls() > 2 {
		t.Fatalf("calls kept going past the time cap: %d", d.calls())
	}
}

// ── merge ─────────────────────────────────────────────────────

func TestARequestBudgetCanTightenButNeverLoosenTheAgents(t *testing.T) {
	cases := []struct {
		name         string
		agent, req   int64
		wantMaxCalls int
	}{
		{"request is smaller", 1000, 450, 3},  // 100 a call: the request's 450 stops it after three
		{"request is larger", 450, 100000, 3}, // the agent's own 450 still holds
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &costDriver{replies: loopOf(20, "done"), in: 90, out: 10}
			e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)
			ctx := contracts.WithBudget(context.Background(), models.RunBudget{MaxTokens: c.req})
			_, _, err := e.Execute(ctx, budgetAgent(models.RunBudget{MaxTokens: c.agent}), "go", resolvedP(orderTool()), nil, false)
			asBudgetPause(t, err)
			if d.calls() != c.wantMaxCalls {
				t.Fatalf("calls = %d, want %d", d.calls(), c.wantMaxCalls)
			}
		})
	}
}

func TestNoBudgetMeansNoBudgetBehaviour(t *testing.T) {
	d := &costDriver{replies: loopOf(4, "all done"), in: 100000, out: 100000}
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)
	a := testAgent()
	a.MaxTurns = 50
	resp, trace, err := e.Execute(context.Background(), a, "go", resolvedP(orderTool()), nil, false)
	if err != nil || resp != "all done" || trace.Budget != nil {
		t.Fatalf("an agent with no budget must run exactly as before: %q %v %+v", resp, err, trace.Budget)
	}
	for _, r := range d.requests {
		if r.MaxTokens != nil {
			t.Fatal("no budget, so no max_tokens should be imposed")
		}
	}
}

func TestADollarCapOnAnUnpricedModelIsRefusedBeforeAnythingStarts(t *testing.T) {
	d := &costDriver{replies: []turnReply{{content: "hi"}}, in: 10, out: 5}
	e, j, _ := newBudgetExecutor(t, d, orderGateway(), false) // no price on the provider

	_, _, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxCostUSD: 1}), "go", resolvedP(), nil, false)
	if !errors.Is(err, executor.ErrUnpricedModel) {
		t.Fatalf("expected ErrUnpricedModel, got %v", err)
	}
	if d.calls() != 0 {
		t.Fatal("no model call may be made under a cap that cannot be enforced")
	}
	if un, _ := j.ListUnfinished(context.Background(), "default"); len(un) != 0 {
		t.Fatalf("a refused run must leave no journal behind: %+v", un)
	}

	// Tokens are exact for any model, so a token cap on the same model is fine.
	d2 := &costDriver{replies: []turnReply{{content: "hi"}}, in: 10, out: 5}
	e2, _, _ := newBudgetExecutor(t, d2, orderGateway(), false)
	if _, _, err := e2.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 1000}), "go", resolvedP(), nil, false); err != nil {
		t.Fatalf("a token cap must work on an unpriced model: %v", err)
	}
}

// ── pause, extend, resume ─────────────────────────────────────

func pausedAtTokens(t *testing.T, replies []turnReply) (*executor.Executor, executor.Journal, *costDriver, string) {
	t.Helper()
	d := &costDriver{replies: replies, in: 90, out: 10}
	e, j, _ := newBudgetExecutor(t, d, orderGateway(), false)
	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 350}), "go", resolvedP(orderTool()), nil, false)
	asBudgetPause(t, err)
	return e, j, d, trace.TraceID
}

func TestAPausedRunIsListedAsBudgetPausedAndFinishesAfterAnExtension(t *testing.T) {
	e, _, d, id := pausedAtTokens(t, loopOf(3, "finished"))

	un, err := e.ListUnfinished(context.Background(), "default")
	if err != nil || len(un) != 1 || un[0].Status != executor.RunPaused || un[0].PauseKind != string(executor.PauseBudget) {
		t.Fatalf("expected one budget-paused run, got %+v (%v)", un, err)
	}
	callsAtPause := d.calls()

	resp, trace, err := e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 1000, Reason: "worth it"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp != "finished" {
		t.Fatalf("resp = %q", resp)
	}
	if d.calls() <= callsAtPause {
		t.Fatal("the resumed run made no further progress")
	}
	if trace.Budget == nil || trace.Budget.Limits.MaxTokens != 1350 {
		t.Fatalf("the extension should have raised the cap to 1350: %+v", trace.Budget)
	}
	if left, _ := e.ListUnfinished(context.Background(), "default"); len(left) != 0 {
		t.Fatalf("a finished run is not unfinished: %+v", left)
	}
}

func TestApprovingABudgetPauseWithoutAnExtensionIsRefusedAndKeepsThePause(t *testing.T) {
	e, _, _, id := pausedAtTokens(t, loopOf(3, "finished"))

	for _, dec := range []*executor.ResumeDecision{
		{Approved: true},
		{Approved: true, Extend: &models.BudgetExtension{}},
		{Approved: true, Extend: &models.BudgetExtension{AddTokens: -5}},
	} {
		if _, _, err := e.Resume(context.Background(), "default", id, dec); err == nil {
			t.Fatalf("approving with no real extension must fail: %+v", dec)
		}
	}
	// Still paused, still resumable with a proper extension.
	if _, _, err := e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 5000}}); err != nil {
		t.Fatalf("the pause should have survived the refused attempts: %v", err)
	}
}

func TestAPersonCanEndAPausedRun(t *testing.T) {
	e, _, _, id := pausedAtTokens(t, loopOf(3, "finished"))

	_, _, err := e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: false, Note: "not worth more"})
	if !errors.Is(err, executor.ErrBudgetExhausted) {
		t.Fatalf("expected ErrBudgetExhausted, got %v", err)
	}
	if left, _ := e.ListUnfinished(context.Background(), "default"); len(left) != 0 {
		t.Fatalf("an ended run must not stay listed: %+v", left)
	}
	if _, _, err := e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 99}}); err == nil {
		t.Fatal("an ended run cannot be resumed")
	}
}

func TestOnlyOneOfTwoConcurrentResumesRuns(t *testing.T) {
	e, _, d, id := pausedAtTokens(t, loopOf(3, "finished"))
	before := d.calls()

	var wg sync.WaitGroup
	var ok, refused int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 100000}})
			if err == nil {
				atomic.AddInt32(&ok, 1)
			} else {
				atomic.AddInt32(&refused, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || refused != 7 {
		t.Fatalf("exactly one resume may win, got %d ok / %d refused", ok, refused)
	}
	// The run needs 3 more calls (loop turns + final); a second resume running would add more.
	if got := d.calls() - before; got > 4 {
		t.Fatalf("the work ran more than once: %d calls after the pause", got)
	}
}

func TestAnExtensionIdIsAppliedOnceAcrossARestart(t *testing.T) {
	// A budget pause, then the extension is granted, then the process dies before finishing.
	// A fresh executor reading the journal must see the raised cap exactly once.
	d := &costDriver{replies: loopOf(2, "x"), in: 90, out: 10}
	e1, j, _ := newBudgetExecutor(t, d, orderGateway(), false)
	_, trace, err := e1.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 350}), "go", resolvedP(orderTool()), nil, false)
	asBudgetPause(t, err)

	ext := &models.BudgetExtension{ID: "grant-1", AddTokens: 100}
	if ok, err := j.ClaimResume(context.Background(), trace.TraceID, ext); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	// Claiming the same extension again must not stack it.
	_, _ = j.ClaimResume(context.Background(), trace.TraceID, ext)

	js, err := j.Load(context.Background(), trace.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(js.Extensions) != 1 {
		t.Fatalf("extension recorded %d times, want once", len(js.Extensions))
	}
}

func TestAResumeIsRefusedWhenThePreviousOneDiedAfterClaiming(t *testing.T) {
	d := &costDriver{replies: loopOf(2, "x"), in: 90, out: 10}
	e, j, _ := newBudgetExecutor(t, d, orderGateway(), false)
	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 350}), "go", resolvedP(orderTool()), nil, false)
	asBudgetPause(t, err)
	if ok, _ := j.ClaimResume(context.Background(), trace.TraceID, &models.BudgetExtension{ID: "a", AddTokens: 10}); !ok {
		t.Fatal("claim failed")
	}
	// The claimant crashed: nothing in the journal says what it did, so it is not re-run
	// blindly.
	if _, _, err := e.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 10}}); err == nil {
		t.Fatal("a run whose resume was claimed and never finished must not auto-resume")
	}
}

func TestResumeForRefusesAnotherAgentsRun(t *testing.T) {
	e, _, _, id := pausedAtTokens(t, loopOf(3, "finished"))
	if _, _, err := e.ResumeFor(context.Background(), "default", "some-other-agent", id, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 1000}}); err == nil {
		t.Fatal("a run addressed through a different agent must not resume")
	}
	if _, _, err := e.ResumeFor(context.Background(), "default", "test-agent", id, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 1000}}); err != nil {
		t.Fatalf("the right agent resumes it: %v", err)
	}
}

// ── spend is not lost ─────────────────────────────────────────

func TestThePausedTurnsModelCallIsCountedAfterAnApprovalResume(t *testing.T) {
	d := &costDriver{
		replies: []turnReply{
			{toolCalls: []models.ToolCallResult{toolCall("1", "issue_refund", map[string]interface{}{})}},
			{content: "ok"},
		},
		in: 90, out: 10,
	}
	gw := newFakeGateway()
	gw.register("issue_refund", func(map[string]interface{}) (string, bool) { return "refunded", false })
	e, j, _ := newBudgetExecutor(t, d, gw, false)

	agent := budgetAgent(models.RunBudget{MaxTokens: 10000})
	agent.ApprovalTools = []string{"issue_refund"}
	tool := models.ResolvedTool{Name: "issue_refund", Schema: map[string]interface{}{"type": "object"}}

	_, trace, err := e.Execute(context.Background(), agent, "refund", resolvedP(tool), nil, false)
	if err != executor.ErrPausedForApproval {
		t.Fatalf("expected the approval pause, got %v", err)
	}
	if _, _, err := e.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: true}); err != nil {
		t.Fatal(err)
	}
	js, err := j.Load(context.Background(), trace.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	// Two paid calls of 100 tokens. Before this was journaled the first (the paused turn) was
	// recorded as zero, so a run could pause and resume its way past any cap.
	if js.Usage.TotalTokens != 200 {
		t.Fatalf("journaled usage = %d tokens, want 200", js.Usage.TotalTokens)
	}
}

func TestAResumedRunStartsFromWhatItAlreadySpent(t *testing.T) {
	e, _, d, id := pausedAtTokens(t, loopOf(6, "finished"))
	callsAtPause := d.calls() // 2 calls = 200 tokens under a 350 cap

	// +100 tokens is room for only one more call from where it stood (450 total cap), not a
	// fresh 350.
	_, _, err := e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 100}})
	asBudgetPause(t, err)
	if got := d.calls() - callsAtPause; got != 1 {
		t.Fatalf("expected 1 more call within the 450-token cap, got %d", got)
	}
}

func TestSubagentSpendIsChargedToTheParentAndCarriedInItsTotals(t *testing.T) {
	d := &costDriver{
		replies: []turnReply{
			{toolCalls: []models.ToolCallResult{toolCall("1", "agentoven_delegate", map[string]interface{}{"agent": "helper", "message": "do it"})}},
			{content: "helper says hi"}, // the helper's only call
			{content: "all done"},       // the parent's second call
		},
		in: 90, out: 10,
	}
	e, j, s := newBudgetExecutor(t, d, orderGateway(), false)
	helper := testAgent()
	helper.Name = "helper"
	helper.Status = models.AgentStatusReady
	helper.ResolvedConfig = resolvedP()
	if err := s.CreateAgent(context.Background(), helper); err != nil {
		t.Fatal(err)
	}

	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 100000}), "go", resolvedP(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if trace.Budget.SpentTokens != 300 { // parent 100 + helper 100 + parent 100
		t.Fatalf("the parent's budget should include the helper's spend, got %d", trace.Budget.SpentTokens)
	}
	if trace.Usage.TotalTokens != 300 {
		t.Fatalf("the trace total should include the helper's spend, got %d", trace.Usage.TotalTokens)
	}
	js, _ := j.Load(context.Background(), trace.TraceID)
	if js.Usage.TotalTokens != 300 {
		t.Fatalf("the journal must record the delegation's cost too, or a resume forgets it: %d", js.Usage.TotalTokens)
	}
}

func TestASubagentThatOutrunsItsShareStopsWithAnErrorResultNotAPause(t *testing.T) {
	// One driver serves parent and helper; the helper is the run whose first user message
	// is "loop forever" and it never stops asking for tools.
	var parentCalls int32
	d := &costDriver{in: 90, out: 10}
	d.fn = func(req *models.RouteRequest) turnReply {
		for _, m := range req.Messages {
			if m.Role == "user" && m.Content == "loop forever" {
				return turnReply{toolCalls: []models.ToolCallResult{toolCall("h", "lookup_order", map[string]interface{}{"order_id": "A"})}}
			}
		}
		if atomic.AddInt32(&parentCalls, 1) == 1 {
			return turnReply{toolCalls: []models.ToolCallResult{toolCall("1", "agentoven_delegate", map[string]interface{}{"agent": "helper", "message": "loop forever"})}}
		}
		return turnReply{content: "parent carried on"}
	}
	e, _, s := newBudgetExecutor(t, d, orderGateway(), false)
	helper := testAgent()
	helper.Name = "helper"
	helper.MaxTurns = 50
	helper.Status = models.AgentStatusReady
	helper.ResolvedConfig = resolvedWithTools(orderTool())
	if err := s.CreateAgent(context.Background(), helper); err != nil {
		t.Fatal(err)
	}

	// 1200 tokens: the parent spends 100, so the helper is granted half of the 1100 left.
	resp, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 1200}), "go", resolvedWithTools(), nil, false)
	if err != nil || resp != "parent carried on" {
		t.Fatalf("the parent must carry on when its child runs out, got %q %v", resp, err)
	}
	r := trace.Turns[0].ToolResults[0]
	if !r.IsError {
		t.Fatalf("the child's exhaustion should come back as an error result, got %+v", r)
	}
	if r.ChildUsage.TotalTokens == 0 || r.ChildUsage.TotalTokens > 550 {
		t.Fatalf("the child should have spent something but no more than its 550 share, spent %d", r.ChildUsage.TotalTokens)
	}
	if trace.Budget.SpentTokens > 1200 {
		t.Fatalf("the parent's cap was exceeded by its child: %d", trace.Budget.SpentTokens)
	}
}

// ── cancellation and events ───────────────────────────────────

func TestACancelledCallerStopsTheRunAtTheNextTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &costDriver{replies: loopOf(10, "done"), in: 10, out: 5, onCall: func(n int) {
		if n == 1 {
			cancel()
		}
	}}
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)
	a := testAgent()
	a.MaxTurns = 50

	_, _, err := e.Execute(ctx, a, "go", resolvedP(orderTool()), nil, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the run to stop with the caller's cancellation, got %v", err)
	}
	if d.calls() != 1 {
		t.Fatalf("a caller that went away kept costing money: %d model calls", d.calls())
	}
}

func TestBudgetWarningsFireOncePerThresholdAndThePauseIsAnEvent(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "done"), in: 90, out: 30} // 120 a call
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)

	var warns []executor.BudgetWarning
	var paused int
	_, _, err := e.ExecuteStream(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 430}), "go", resolvedP(orderTool()), nil, false,
		func(ev executor.Event) error {
			if ev.BudgetWarning != nil {
				warns = append(warns, *ev.BudgetWarning)
			}
			if ev.Type == executor.EventBudgetPaused {
				paused++
			}
			return nil
		})
	asBudgetPause(t, err)
	if paused != 1 {
		t.Fatalf("expected one budget_paused event, got %d", paused)
	}
	seen := map[string]int{}
	for _, w := range warns {
		seen[fmt.Sprint(w.Dimension, w.Pct)]++
	}
	if seen["tokens50"] != 1 || seen["tokens80"] != 1 || len(warns) != 2 {
		t.Fatalf("expected one 50%% and one 80%% token warning, got %+v", warns)
	}
}

func TestWithoutAJournalTheRunStopsAtItsCapInsteadOfPausing(t *testing.T) {
	// A Runner built with no journal (as Pro's agentexec users get) has nowhere to
	// checkpoint, so waiting for a decision is not an option: the cap must still hold.
	d := &costDriver{replies: loopOf(10, "done"), in: 90, out: 10}
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)
	e.SetJournal(nil)

	_, _, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 450}), "go", resolvedP(orderTool()), nil, false)
	if !errors.Is(err, executor.ErrBudgetExhausted) {
		t.Fatalf("expected ErrBudgetExhausted, got %v", err)
	}
	var pe *executor.PausedError
	if errors.As(err, &pe) {
		t.Fatal("a run with no journal cannot be paused")
	}
	if d.calls() != 3 {
		t.Fatalf("calls = %d, want 3", d.calls())
	}
}

type auditLog struct {
	mu     sync.Mutex
	events []string
	last   map[string]map[string]interface{}
}

func (a *auditLog) sink(_ context.Context, _ string, action string, details map[string]interface{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, action)
	if a.last == nil {
		a.last = map[string]map[string]interface{}{}
	}
	a.last[action] = details
}

func TestBudgetPausesAndExtensionsAreAudited(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "finished"), in: 90, out: 10}
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)
	audit := &auditLog{}
	e.SetAudit(audit.sink)

	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 350}), "go", resolvedP(orderTool()), nil, false)
	asBudgetPause(t, err)
	if len(audit.events) != 1 || audit.events[0] != "run.budget_paused" {
		t.Fatalf("a pause at the budget is audited, got %v", audit.events)
	}
	if audit.last["run.budget_paused"]["dimension"] != models.BudgetDimTokens || audit.last["run.budget_paused"]["trace_id"] != trace.TraceID {
		t.Fatalf("the audit event should say what was hit and which run: %v", audit.last["run.budget_paused"])
	}

	_, _, err = e.Resume(context.Background(), "default", trace.TraceID,
		&executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 5000, Reason: "worth it", By: "dana@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	ext := audit.last["run.budget_extended"]
	if ext == nil || ext["add_tokens"] != int64(5000) || ext["by"] != "dana@example.com" || ext["reason"] != "worth it" {
		t.Fatalf("a raised budget is audited with who and why: %v", ext)
	}
}

func TestAnExtensionThatLosesTheRaceIsNotAudited(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "finished"), in: 90, out: 10}
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)
	_, trace, _ := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 350}), "go", resolvedP(orderTool()), nil, false)
	audit := &auditLog{}
	e.SetAudit(audit.sink)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = e.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 5000}})
		}()
	}
	wg.Wait()
	n := 0
	for _, ev := range audit.events {
		if ev == "run.budget_extended" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the budget was raised once, so it is audited once, got %d", n)
	}
}

// ── budgets shared beyond one run ─────────────────────────────

type fakeGate struct {
	mu      sync.Mutex
	refuse  bool
	allowed int
	records []models.SpendRecord
}

func (g *fakeGate) Allow(_ context.Context, q models.SpendQuery) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refuse {
		return &models.SpendRefusedError{Scope: "kitchen", Reason: "monthly budget used up", Spent: 100, Cap: 100}
	}
	g.allowed++
	return nil
}
func (g *fakeGate) Record(_ context.Context, r models.SpendRecord) {
	g.mu.Lock()
	g.records = append(g.records, r)
	g.mu.Unlock()
}

func newGatedExecutor(t *testing.T, d *costDriver, gate *fakeGate) *executor.Executor {
	t.Helper()
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	mr := router.NewModelRouter(s)
	mr.RegisterDriver(d)
	mr.SetSpendGate(gate)
	if err := s.CreateProvider(context.Background(), &models.ModelProvider{Name: "p", Kitchen: "default", Kind: "costed", Models: []string{"test-model"}, IsDefault: true}); err != nil {
		t.Fatal(err)
	}
	e := executor.NewExecutor(s, mr, orderGateway(), sessions.NewMemorySessionStore())
	e.SetModelRetry(3, time.Millisecond)
	j, err := executor.NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.SetJournal(j)
	return e
}

func TestARunPausesWhenASharedBudgetIsUsedUpAndCarriesOnWhenItIsNot(t *testing.T) {
	d := &costDriver{replies: loopOf(2, "finished"), in: 10, out: 5}
	gate := &fakeGate{}
	e := newGatedExecutor(t, d, gate)
	a := testAgent()
	a.MaxTurns = 10

	// The kitchen's budget runs out before the second call.
	d.onCall = func(n int) {
		if n == 1 {
			gate.mu.Lock()
			gate.refuse = true
			gate.mu.Unlock()
		}
	}
	_, trace, err := e.Execute(context.Background(), a, "go", resolvedWithTools(orderTool()), nil, false)
	p := asBudgetPause(t, err)
	if p.Scope != "kitchen" || p.Cap != 100 {
		t.Fatalf("the pause should say whose budget it was: %+v", p)
	}
	if len(d.requests) != 1 {
		t.Fatalf("a refused call must never reach the provider, saw %d requests", len(d.requests))
	}
	if len(gate.records) != 1 || gate.records[0].Usage.TotalTokens != 15 {
		t.Fatalf("the gate is told about the call that was made: %+v", gate.records)
	}

	// Still refused: resuming just pauses again, without needing (or inventing) an extension.
	_, _, err = e.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: true})
	asBudgetPause(t, err)

	// The budget is raised elsewhere; resuming now carries on.
	gate.mu.Lock()
	gate.refuse = false
	gate.mu.Unlock()
	resp, _, err := e.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: true})
	if err != nil || resp != "finished" {
		t.Fatalf("%q %v", resp, err)
	}
}

func TestARefusalByTheGateIsNotRetried(t *testing.T) {
	d := &costDriver{replies: []turnReply{{content: "x"}}, in: 10, out: 5}
	gate := &fakeGate{refuse: true}
	e := newGatedExecutor(t, d, gate)
	_, _, err := e.Execute(taskCtx(), testAgent(), "go", resolvedWithTools(), nil, false)
	asBudgetPause(t, err)
	if gate.allowed != 0 {
		t.Fatal("nothing was allowed")
	}
}
