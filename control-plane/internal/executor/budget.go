package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// ErrBudgetExhausted ends a run that hit its budget and cannot pause for a decision: it
// has no journal to checkpoint into, it is a subagent whose parent is mid-tool, or a person
// declined to extend it.
var ErrBudgetExhausted = errors.New("executor: run budget exhausted")

// ErrUnpricedModel is returned when a run asks for a dollar cap on a model the router has no
// real price for. The cap would be measured against a placeholder, so it is refused rather
// than quietly enforced wrongly; a token cap is exact and always allowed.
var ErrUnpricedModel = errors.New("executor: cannot enforce a dollar budget on a model with no known price")

// PauseKind says why a run stopped and what unblocks it.
type PauseKind string

const (
	// PauseApproval waits on a person's decision about a tool call (Agent.ApprovalTools).
	PauseApproval PauseKind = "approval"
	// PauseBudget waits on a person extending the run's budget, or ending it.
	PauseBudget PauseKind = "budget"
	// PauseStall waits on a person after the run kept repeating the same tool call.
	PauseStall PauseKind = "stall"
)

// PausedError is returned by Execute, ExecuteStream and Resume when a run stopped to wait
// for a person for a reason other than an approval gate. It is a different type from
// ErrPausedForApproval on purpose: a caller that only knows about approvals should fail
// loudly on a budget pause, not treat "stopped" as "finished".
type PausedError struct {
	Kind   PauseKind
	Budget *models.BudgetPause
	Stall  *StallInfo
}

func (e *PausedError) Error() string {
	if e.Stall != nil {
		return fmt.Sprintf("executor: paused, %s was called %d times with the same arguments", e.Stall.Tool, e.Stall.Count)
	}
	if e.Budget != nil {
		return fmt.Sprintf("executor: paused at the run budget (%s: %.4g of %.4g)", e.Budget.Dimension, e.Budget.Spent, e.Budget.Cap)
	}
	return "executor: paused"
}

// BudgetWarning is raised once as a run's use of a dimension crosses a configured percentage.
type BudgetWarning struct {
	Dimension string  `json:"dimension"`
	Pct       int     `json:"pct"`
	Spent     float64 `json:"spent"`
	Cap       float64 `json:"cap"`
}

const (
	defaultChildShare = 0.5
	// minOutputTokens is the smallest reply worth paying for. A call whose remaining
	// headroom cannot fit it is blocked rather than sent to produce a truncated answer.
	minOutputTokens = 64
	// maxHeadroomTokens stops an almost-unlimited budget from being sent to a provider as
	// an absurd max_tokens.
	maxHeadroomTokens = 1 << 20
)

var defaultWarnPct = []int{50, 80}

// grant is what a delegating run gives a subagent: limits for the child, a ledger entry on
// the parent so concurrent children cannot be promised more than the parent has left, and
// the parent itself so the child's spend is charged to it as it happens.
type grant struct {
	limits models.RunBudget
	res    *reservation
	parent *runBudget
}

// reservation is the part of a parent's budget currently promised to one child.
type reservation struct {
	costLeft   float64
	tokensLeft int64
}

type (
	budgetCtxKey struct{}
	grantCtxKey  struct{}
)

// withRunBudget sets the budget a run's tool calls (and so its subagents) are charged to.
// A nil budget is stored too: a budgetless subagent must not inherit its parent's.
func withRunBudget(ctx context.Context, b *runBudget) context.Context {
	return context.WithValue(ctx, budgetCtxKey{}, b)
}

func runBudgetFrom(ctx context.Context) *runBudget {
	b, _ := ctx.Value(budgetCtxKey{}).(*runBudget)
	return b
}

func withGrant(ctx context.Context, g *grant) context.Context {
	return context.WithValue(ctx, grantCtxKey{}, g)
}

func grantFrom(ctx context.Context) *grant {
	g, _ := ctx.Value(grantCtxKey{}).(*grant)
	return g
}

// runBudget tracks one run's spend against its limits. It is safe for concurrent use:
// parallel subagents charge their parent from their own goroutines.
type runBudget struct {
	mu     sync.Mutex
	lim    models.RunBudget
	cost   float64
	tokens int64
	turns  int

	// activeBase is working time accumulated before this segment (from the journal);
	// segStart is when this segment began. Time paused or down is never counted.
	activeBase time.Duration
	segStart   time.Time
	now        func() time.Time

	price  router.Price
	priced bool // price is real, so cost can be projected

	parent *runBudget
	grant  *grant
	// reservations are promises this run made to children that are still running.
	reservations map[*reservation]struct{}

	warned map[string]int
}

// newRunBudget starts tracking a run that has already spent usage and worked for active.
func newRunBudget(lim models.RunBudget, usage models.TokenUsage, active time.Duration, turns int, now func() time.Time) *runBudget {
	if now == nil {
		now = time.Now
	}
	b := &runBudget{
		lim:          lim,
		cost:         usage.EstimatedCost,
		tokens:       usage.TotalTokens,
		turns:        turns,
		activeBase:   active,
		segStart:     now(),
		now:          now,
		reservations: map[*reservation]struct{}{},
		warned:       map[string]int{},
	}
	// A resumed run must not re-announce thresholds it already crossed.
	for _, d := range b.dims() {
		for _, pct := range b.warnPcts() {
			if d.cap > 0 && d.spent/d.cap*100 >= float64(pct) && pct > b.warned[d.name] {
				b.warned[d.name] = pct
			}
		}
	}
	return b
}

func (b *runBudget) limits() models.RunBudget {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lim
}

func (b *runBudget) setPrice(p router.Price) {
	b.mu.Lock()
	b.price, b.priced = p, p.Known
	b.mu.Unlock()
}

func (b *runBudget) activeLocked() time.Duration {
	return b.activeBase + b.now().Sub(b.segStart)
}

func (b *runBudget) warnPcts() []int {
	if len(b.lim.WarnPct) > 0 {
		return b.lim.WarnPct
	}
	return defaultWarnPct
}

type dim struct {
	name       string
	spent, cap float64
}

func (b *runBudget) dims() []dim {
	return []dim{
		{models.BudgetDimCost, b.cost, b.lim.MaxCostUSD},
		{models.BudgetDimTokens, float64(b.tokens), float64(b.lim.MaxTokens)},
		{models.BudgetDimActive, b.activeLocked().Seconds(), float64(b.lim.MaxActiveSeconds)},
		{models.BudgetDimTurns, float64(b.turns), float64(b.lim.MaxTurns)},
	}
}

// reservedLocked is what this run has promised to children and they have not yet spent.
func (b *runBudget) reservedLocked() (cost float64, tokens int64) {
	for r := range b.reservations {
		cost += r.costLeft
		tokens += r.tokensLeft
	}
	return
}

// charge records one model call's usage here and up the chain of parents, returning any
// warning thresholds this crossing newly reached.
func (b *runBudget) charge(u models.TokenUsage) []BudgetWarning {
	b.mu.Lock()
	b.cost += u.EstimatedCost
	b.tokens += u.TotalTokens
	var warns []BudgetWarning
	for _, d := range b.dims() {
		if d.cap <= 0 {
			continue
		}
		for _, pct := range b.warnPcts() {
			if pct > b.warned[d.name] && d.spent/d.cap*100 >= float64(pct) {
				b.warned[d.name] = pct
				warns = append(warns, BudgetWarning{Dimension: d.name, Pct: pct, Spent: d.spent, Cap: d.cap})
			}
		}
	}
	g := b.grant
	b.mu.Unlock()

	if g != nil {
		g.parent.chargeChild(g.res, u)
	}
	return warns
}

// chargeChild applies a subagent's spend to this run, drawing it down from the child's
// reservation so the same money is not counted as both promised and spent.
func (b *runBudget) chargeChild(res *reservation, u models.TokenUsage) {
	b.mu.Lock()
	res.costLeft -= u.EstimatedCost
	if res.costLeft < 0 {
		res.costLeft = 0
	}
	res.tokensLeft -= u.TotalTokens
	if res.tokensLeft < 0 {
		res.tokensLeft = 0
	}
	b.mu.Unlock()
	b.charge(u)
}

func (b *runBudget) addTurn() {
	b.mu.Lock()
	b.turns++
	b.mu.Unlock()
}

// block describes a gate that did not open.
type block struct {
	pause models.BudgetPause
}

// gate decides whether the next model call may go out. nextTurn is the turn about to start;
// estInput is an estimate of that call's prompt tokens. It checks what has already been
// spent and, where it can price the call, what this one is expected to cost, so a run stops
// before an overshoot rather than after it. It also returns the most output the call may
// produce within what is left (0 meaning no limit), to pass as the request's max_tokens.
func (b *runBudget) gate(nextTurn, estInput int) (*block, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	reservedCost, reservedTokens := b.reservedLocked()

	if b.lim.MaxTurns > 0 && nextTurn > b.lim.MaxTurns {
		return &block{models.BudgetPause{Turn: nextTurn, Dimension: models.BudgetDimTurns, Spent: float64(nextTurn - 1), Cap: float64(b.lim.MaxTurns), Scope: "run"}}, 0
	}
	if b.lim.MaxActiveSeconds > 0 && b.activeLocked() >= time.Duration(b.lim.MaxActiveSeconds)*time.Second {
		return &block{models.BudgetPause{Turn: nextTurn, Dimension: models.BudgetDimActive, Spent: b.activeLocked().Seconds(), Cap: float64(b.lim.MaxActiveSeconds), Scope: "run"}}, 0
	}

	headroom := 0 // 0: unlimited
	capHeadroom := func(n int) {
		if n < 1 {
			n = 1
		}
		if headroom == 0 || n < headroom {
			headroom = n
		}
	}

	if b.lim.MaxTokens > 0 {
		left := b.lim.MaxTokens - b.tokens - reservedTokens - int64(estInput)
		if left < minOutputTokens {
			return &block{models.BudgetPause{Turn: nextTurn, Dimension: models.BudgetDimTokens, Spent: float64(b.tokens + reservedTokens),
				Cap: float64(b.lim.MaxTokens), Projected: float64(estInput + minOutputTokens), Scope: "run"}}, 0
		}
		capHeadroom(int(minInt64(left, maxHeadroomTokens)))
	}

	if b.lim.MaxCostUSD > 0 && b.priced {
		inCost := float64(estInput) / 1000 * b.price.InputPer1K
		leftCost := b.lim.MaxCostUSD - b.cost - reservedCost - inCost
		minOutCost := float64(minOutputTokens) / 1000 * b.price.OutputPer1K
		if leftCost < minOutCost {
			return &block{models.BudgetPause{Turn: nextTurn, Dimension: models.BudgetDimCost, Spent: b.cost + reservedCost,
				Cap: b.lim.MaxCostUSD, Projected: inCost + minOutCost, Scope: "run"}}, 0
		}
		if b.price.OutputPer1K > 0 {
			capHeadroom(int(minFloat(leftCost/b.price.OutputPer1K*1000, maxHeadroomTokens)))
		}
	}
	return nil, headroom
}

// deadline returns how long a single model call may run before the active-time cap would be
// crossed, or 0 for no limit.
func (b *runBudget) deadline() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lim.MaxActiveSeconds <= 0 {
		return 0
	}
	left := time.Duration(b.lim.MaxActiveSeconds)*time.Second - b.activeLocked()
	if left < time.Second {
		left = time.Second
	}
	return left
}

// activeExceeded reports whether the active-time cap has been reached.
func (b *runBudget) activeExceeded() *models.BudgetPause {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lim.MaxActiveSeconds > 0 && b.activeLocked() >= time.Duration(b.lim.MaxActiveSeconds)*time.Second {
		return &models.BudgetPause{Dimension: models.BudgetDimActive, Spent: b.activeLocked().Seconds(), Cap: float64(b.lim.MaxActiveSeconds), Scope: "run"}
	}
	return nil
}

func (b *runBudget) status(turn int) *models.BudgetStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return &models.BudgetStatus{
		Limits:          b.lim,
		SpentCostUSD:    b.cost,
		SpentTokens:     b.tokens,
		SpentActiveSecs: int64(b.activeLocked().Seconds()),
		Turns:           turn,
	}
}

// grantFor reserves part of this run's remaining budget for a subagent and returns the limits
// the child runs under. A child gets at most ChildShare of what is left, never more than its
// own cap, and the reservation is held under the lock so parallel children cannot be promised
// more than the parent has. release must be called when the child returns.
func (b *runBudget) grantFor(childCap models.RunBudget) (*grant, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	share := b.lim.ChildShare
	if share <= 0 || share > 1 {
		share = defaultChildShare
	}
	reservedCost, reservedTokens := b.reservedLocked()
	limits := childCap
	res := &reservation{}

	if b.lim.MaxCostUSD > 0 {
		avail := b.lim.MaxCostUSD - b.cost - reservedCost
		if avail <= 0 {
			return nil, fmt.Errorf("no cost budget left to give a subagent: %w", ErrBudgetExhausted)
		}
		limits.MaxCostUSD = minPositive(limits.MaxCostUSD, avail*share)
		res.costLeft = limits.MaxCostUSD
	}
	if b.lim.MaxTokens > 0 {
		avail := b.lim.MaxTokens - b.tokens - reservedTokens
		if avail <= 0 {
			return nil, fmt.Errorf("no token budget left to give a subagent: %w", ErrBudgetExhausted)
		}
		granted := int64(float64(avail) * share)
		if granted < 1 {
			granted = 1
		}
		if limits.MaxTokens == 0 || granted < limits.MaxTokens {
			limits.MaxTokens = granted
		}
		res.tokensLeft = limits.MaxTokens
	}
	if b.lim.MaxActiveSeconds > 0 {
		left := int64((time.Duration(b.lim.MaxActiveSeconds)*time.Second - b.activeLocked()).Seconds())
		if left <= 0 {
			return nil, fmt.Errorf("no time budget left to give a subagent: %w", ErrBudgetExhausted)
		}
		granted := int64(float64(left) * share)
		if granted < 1 {
			granted = 1
		}
		if limits.MaxActiveSeconds == 0 || granted < limits.MaxActiveSeconds {
			limits.MaxActiveSeconds = granted
		}
	}
	b.reservations[res] = struct{}{}
	return &grant{limits: limits, res: res, parent: b}, nil
}

// grantBatch reserves a share for each of n subagents that will run together. Every share is
// worked out from one reading of what is left, so n children together are promised at most the
// parent's ChildShare of it, however many there are; reserving them one after another would
// hand the first half of everything and the last almost nothing.
func (b *runBudget) grantBatch(n int) ([]*grant, error) {
	if n <= 0 {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	share := b.lim.ChildShare
	if share <= 0 || share > 1 {
		share = defaultChildShare
	}
	each := share / float64(n)
	reservedCost, reservedTokens := b.reservedLocked()

	proto := models.RunBudget{}
	var res reservation
	if b.lim.MaxCostUSD > 0 {
		avail := b.lim.MaxCostUSD - b.cost - reservedCost
		if avail <= 0 {
			return nil, fmt.Errorf("no cost budget left to give the subagents: %w", ErrBudgetExhausted)
		}
		proto.MaxCostUSD = avail * each
		res.costLeft = proto.MaxCostUSD
	}
	if b.lim.MaxTokens > 0 {
		avail := b.lim.MaxTokens - b.tokens - reservedTokens
		if avail <= 0 {
			return nil, fmt.Errorf("no token budget left to give the subagents: %w", ErrBudgetExhausted)
		}
		g := int64(float64(avail) * each)
		if g < 1 {
			g = 1
		}
		proto.MaxTokens = g
		res.tokensLeft = g
	}
	if b.lim.MaxActiveSeconds > 0 {
		left := int64((time.Duration(b.lim.MaxActiveSeconds)*time.Second - b.activeLocked()).Seconds())
		if left <= 0 {
			return nil, fmt.Errorf("no time budget left to give the subagents: %w", ErrBudgetExhausted)
		}
		g := int64(float64(left) * share) // they run at the same time, so each may use the whole share of the time
		if g < 1 {
			g = 1
		}
		proto.MaxActiveSeconds = g
	}

	out := make([]*grant, n)
	for i := range out {
		r := res
		out[i] = &grant{limits: proto, res: &r, parent: b}
		b.reservations[out[i].res] = struct{}{}
	}
	return out, nil
}

func (b *runBudget) release(g *grant) {
	b.mu.Lock()
	delete(b.reservations, g.res)
	b.mu.Unlock()
}

// ── construction ──────────────────────────────────────────────

// baseBudget is the limits a fresh run starts under: the agent's own, tightened by the
// caller's per-request budget (contracts.WithBudget) and by any grant from a delegating
// parent. Nothing here can loosen a limit; only a resume-time extension does.
func baseBudget(ctx context.Context, agent *models.Agent) models.RunBudget {
	var lim models.RunBudget
	if agent.Budget != nil {
		lim = *agent.Budget
	}
	if req := contracts.BudgetFrom(ctx); req != nil {
		lim = lim.Tighten(*req)
	}
	if g := grantFrom(ctx); g != nil {
		lim = lim.Tighten(g.limits)
	}
	return lim
}

// buildBudget gives a run its budget, or nil when nothing sets a limit. base are the limits
// the run started under (from baseBudget, or the journal header on resume); exts are the
// extensions a person has granted since; usage, active and turns are what a resumed run has
// already used. It is called before anything is created, so a refusal (an unpriced model
// under a dollar cap) leaves no session, journal or trace behind.
func (e *Executor) buildBudget(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients, base models.RunBudget, exts []models.BudgetExtension, usage models.TokenUsage, active time.Duration, turns int) (*runBudget, error) {
	lim := base
	for _, ext := range exts {
		lim = lim.Extend(ext)
	}
	if lim.IsZero() {
		return nil, nil
	}
	b := newRunBudget(lim, usage, active, turns, nil)
	if lim.MaxCostUSD > 0 {
		if resolved == nil || resolved.Model == nil {
			return nil, fmt.Errorf("%w: the agent has no resolved model", ErrUnpricedModel)
		}
		price := e.router.PriceFor(ctx, agent.Kitchen, resolved.Model.Provider, resolved.Model.Model)
		if !price.Known {
			return nil, fmt.Errorf("%w (%q): set cost_per_1k_input and cost_per_1k_output on provider %q, or use a token budget",
				ErrUnpricedModel, resolved.Model.Model, resolved.Model.Provider)
		}
		b.setPrice(price)
	}
	if g := grantFrom(ctx); g != nil {
		b.parent, b.grant = g.parent, g
	}
	return b, nil
}

func minPositive(a, b float64) float64 {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// ── helpers the run loop and delegation share ─────────────────

type runIDKey struct{}

func withRunID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, runIDKey{}, id)
}

// parentRunID is the trace ID of the run whose tool call this context belongs to, or "".
func parentRunID(ctx context.Context) string {
	id, _ := ctx.Value(runIDKey{}).(string)
	return id
}

func budgetPtr(b models.RunBudget) *models.RunBudget {
	if b.IsZero() {
		return nil
	}
	return &b
}

// estimateInput roughly counts the prompt a model call will send: the conversation plus the
// tool definitions, at about four characters a token. It is a projection for the gate, not a
// bill: the provider's own count is what is charged afterwards.
func (e *Executor) estimateInput(st *runState, turn int) int {
	n := estimateConversation(e.requestMessages(st, turn))
	if len(st.toolDefs) > 0 {
		if raw, err := json.Marshal(st.toolDefs); err == nil {
			n += len(raw) / 4
		}
	}
	return n
}

// pauseForBudget stops the run at its cap. The checkpoint is already written: the gate runs
// at the top of the loop, after the previous turn was journaled, so that turn's record is
// where a resume picks up. A run that cannot wait for a decision (no journal to checkpoint
// into, or a subagent whose parent is mid-tool-call) is ended instead.
func (e *Executor) pauseForBudget(ctx context.Context, st *runState, opts loopOpts, pause models.BudgetPause) (string, *ExecutionTrace, error) {
	trace := st.trace
	trace.TotalMs = time.Since(st.start).Milliseconds()
	trace.Usage = st.totalUsage
	if pause.Scope == "" {
		pause.Scope = "run"
	}
	trace.BudgetPause = &pause

	isChild := st.budget != nil && st.budget.parent != nil
	durable := false
	if opts.journal != nil && !isChild {
		if err := opts.journal.MarkBudgetPaused(ctx, st.traceID, pause); err != nil {
			log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to journal the budget pause; ending the run instead of pausing it")
		} else {
			durable = true
		}
	}

	if durable {
		e.auditRun(ctx, st.agent.Kitchen, "run.budget_paused", map[string]interface{}{
			"agent": st.agent.Name, "trace_id": st.traceID, "dimension": pause.Dimension,
			"spent": pause.Spent, "cap": pause.Cap, "turn": pause.Turn,
		})
		var status *models.BudgetStatus
		if st.budget != nil { // a run with no budget of its own can still meet a shared one
			status = st.budget.status(len(trace.Turns))
		}
		opts.onEvent.emit(Event{Type: EventBudgetPaused, Turn: pause.Turn, BudgetPause: &pause, Budget: status})
		log.Info().Str("agent", st.agent.Name).Str("trace_id", st.traceID).Str("dimension", pause.Dimension).
			Float64("spent", pause.Spent).Float64("cap", pause.Cap).Msg("Run paused at its budget")
		return "", trace, &PausedError{Kind: PauseBudget, Budget: &pause}
	}

	runErr := fmt.Errorf("%w: %s %.4g of %.4g", ErrBudgetExhausted, pause.Dimension, pause.Spent, pause.Cap)
	if opts.journal != nil {
		if err := opts.journal.MarkDone(ctx, st.traceID, "", runErr); err != nil {
			log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to mark budget-stopped run done")
		}
	}
	opts.onEvent.emit(Event{Type: EventError, Turn: pause.Turn, Err: runErr.Error(), BudgetPause: &pause})
	return "", trace, runErr
}

func addUsage(a, b models.TokenUsage) models.TokenUsage {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.TotalTokens += b.TotalTokens
	a.ThinkingTokens += b.ThinkingTokens
	a.EstimatedCost += b.EstimatedCost
	return a
}
