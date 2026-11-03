package executor_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// memLog is a RecordLog in memory: what a database table is to several replicas, one value of it is
// to several journals in one test.
type memLog struct {
	mu         sync.Mutex
	runs       map[string]*memRun
	sweepReads int // index entries ListSweepable returned: what a janitor pass read
}

type memRun struct {
	lock sync.Mutex
	meta executor.RunMeta
	recs [][]byte
}

func newMemLog() *memLog { return &memLog{runs: map[string]*memRun{}} }

func (l *memLog) get(id string) *memRun {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runs[id]
}

func (l *memLog) Create(_ context.Context, meta executor.RunMeta, first []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, dup := l.runs[meta.TraceID]; dup {
		return fmt.Errorf("run %s already exists", meta.TraceID)
	}
	meta.LastUpdate = time.Now()
	l.runs[meta.TraceID] = &memRun{meta: meta, recs: [][]byte{first}}
	return nil
}

type memTx struct {
	r        *memRun
	staged   [][]byte
	finished *struct {
		outcome string
		at      time.Time
	}
}

func (t *memTx) Count() (int, error) { return len(t.r.recs) + len(t.staged), nil }
func (t *memTx) Records() ([][]byte, error) {
	return append(append([][]byte(nil), t.r.recs...), t.staged...), nil
}
func (t *memTx) Append(recs ...[]byte) error { t.staged = append(t.staged, recs...); return nil }
func (t *memTx) Finish(outcome string, at time.Time) error {
	t.finished = &struct {
		outcome string
		at      time.Time
	}{outcome, at}
	return nil
}

func (l *memLog) Update(_ context.Context, id string, fn func(tx executor.RecordTx) error) error {
	r := l.get(id)
	if r == nil {
		return fmt.Errorf("%w: %s", executor.ErrRunNotFound, id)
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	tx := &memTx{r: r}
	if err := fn(tx); err != nil {
		return err
	}
	r.recs = append(r.recs, tx.staged...)
	r.meta.LastUpdate = time.Now()
	if tx.finished != nil {
		r.meta.Done, r.meta.Outcome, r.meta.DoneAt = true, tx.finished.outcome, tx.finished.at
	}
	return nil
}

func (l *memLog) Read(_ context.Context, id string) ([][]byte, error) {
	r := l.get(id)
	if r == nil {
		return nil, fmt.Errorf("%w: %s", executor.ErrRunNotFound, id)
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	return append([][]byte(nil), r.recs...), nil
}

func (l *memLog) all() []executor.RunMeta {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []executor.RunMeta
	for _, r := range l.runs {
		r.lock.Lock()
		out = append(out, r.meta)
		r.lock.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func (l *memLog) ListUnfinished(_ context.Context, kitchen string) ([]executor.RunMeta, error) {
	var out []executor.RunMeta
	for _, m := range l.all() {
		if m.Done || m.ParentTraceID != "" || (kitchen != "" && m.Kitchen != kitchen) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}
func (l *memLog) ListSweepable(_ context.Context, doneBefore, staleBefore time.Time, after string, limit int) ([]executor.RunMeta, error) {
	var out []executor.RunMeta
	for _, m := range l.all() {
		at := m.DoneAt
		if at.IsZero() {
			at = m.LastUpdate
		}
		due := (m.Done && !doneBefore.IsZero() && at.Before(doneBefore)) || (!m.Done && !staleBefore.IsZero() && m.LastUpdate.Before(staleBefore))
		if due && m.TraceID > after {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TraceID < out[j].TraceID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	l.mu.Lock()
	l.sweepReads += len(out)
	l.mu.Unlock()
	return out, nil
}
func (l *memLog) Delete(_ context.Context, id string) error {
	l.mu.Lock()
	delete(l.runs, id)
	l.mu.Unlock()
	return nil
}
func (l *memLog) backdate(id string, d time.Duration) {
	r := l.get(id)
	r.lock.Lock()
	r.meta.LastUpdate = r.meta.LastUpdate.Add(-d)
	if !r.meta.DoneAt.IsZero() {
		r.meta.DoneAt = r.meta.DoneAt.Add(-d)
	}
	r.lock.Unlock()
}

// logExecutor is an executor whose journal is a LogJournal over log.
func logExecutor(t *testing.T, d *costDriver, gw *fakeGateway, log executor.RecordLog) (*executor.Executor, *executor.LogJournal) {
	t.Helper()
	e, _, _ := newBudgetExecutor(t, d, gw, false)
	j := executor.NewLogJournal(log)
	e.SetJournal(j)
	return e, j
}

// ── the journal over a log behaves as the file journal does ──

func TestALogJournalHoldsARunTheWayTheFileJournalDoes(t *testing.T) {
	run := func(e *executor.Executor) (*executor.JournalState, string) {
		a := testAgent()
		a.MaxTurns = 20
		out, trace, err := e.Execute(context.Background(), a, "go", resolvedWithTools(orderTool()), nil, false)
		if err != nil {
			t.Fatal(err)
		}
		js, err := e.RunState(context.Background(), "default", trace.TraceID)
		if err != nil {
			t.Fatal(err)
		}
		return js, out
	}
	fe, _, _ := journalDirExecutor(t, &costDriver{replies: loopOf(4, "all done"), in: 10, out: 5}, orderGateway())
	fileState, fileOut := run(fe)
	le, _ := logExecutor(t, &costDriver{replies: loopOf(4, "all done"), in: 10, out: 5}, orderGateway(), newMemLog())
	logState, logOut := run(le)

	if fileOut != logOut || fileState.CompletedTurns != logState.CompletedTurns || fileState.Outcome != logState.Outcome ||
		len(fileState.Messages) != len(logState.Messages) || fileState.Usage.TotalTokens != logState.Usage.TotalTokens {
		t.Fatalf("file %+v\nlog  %+v", fileState, logState)
	}
	if logState.Status != executor.RunDone || logState.CompletedTurns != 5 || len(logState.Messages) < 9 {
		t.Fatalf("%+v", logState)
	}
}

func TestARunPausedAtItsBudgetIsResumedFromALogJournal(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "finished"), in: 90, out: 10}
	e, _ := logExecutor(t, d, orderGateway(), newMemLog())
	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 350}), "go", resolvedP(orderTool()), nil, false)
	_ = asBudgetPause(t, err)
	out, _, err := e.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 9000}})
	if err != nil || out != "finished" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestOnlyOneReplicaWinsTheClaimOnAPausedRun(t *testing.T) {
	log := newMemLog()
	d := &costDriver{replies: loopOf(10, "finished"), in: 90, out: 10}
	e, j1 := logExecutor(t, d, orderGateway(), log)
	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 350}), "go", resolvedP(orderTool()), nil, false)
	_ = asBudgetPause(t, err)

	j2 := executor.NewLogJournal(log) // another replica over the same log
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j := j1
			if i%2 == 1 {
				j = j2
			}
			ok, err := j.ClaimResume(context.Background(), trace.TraceID, &models.BudgetExtension{ID: "x", AddTokens: 100})
			if err != nil {
				t.Error(err)
			}
			if ok {
				atomic.AddInt32(&wins, 1)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("a pause is ended once, got %d winners", wins)
	}
}

func TestTwoWritersOverOneLogNeverDuplicateTheHistory(t *testing.T) {
	log := newMemLog()
	a, b := executor.NewLogJournal(log), executor.NewLogJournal(log)
	ctx := context.Background()
	user := models.ChatMessage{Role: "user", Content: "go"}
	asst := func(n int) models.ChatMessage {
		return models.ChatMessage{Role: "assistant", Content: fmt.Sprint("step ", n)}
	}
	hdr := executor.JournalHeader{TraceID: "t1", Kitchen: "default", Agent: testAgent(), StartedAt: time.Now(), InitialMessages: []models.ChatMessage{user}}
	if err := a.Start(ctx, hdr); err != nil {
		t.Fatal(err)
	}
	h := []models.ChatMessage{user, asst(1), asst(2)}
	if err := a.CommitTurn(ctx, "t1", executor.Turn{Number: 1}, h); err != nil {
		t.Fatal(err)
	}
	// The other replica takes the run over and commits two more turns.
	h = append(h, asst(3), asst(4))
	if err := b.CommitTurn(ctx, "t1", executor.Turn{Number: 2}, h); err != nil {
		t.Fatal(err)
	}
	// The run comes back to the first replica, whose memory of it is out of date.
	h = append(h, asst(5))
	if err := a.CommitTurn(ctx, "t1", executor.Turn{Number: 3}, h); err != nil {
		t.Fatal(err)
	}
	st, err := a.Load(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) != 6 || st.CompletedTurns != 3 {
		t.Fatalf("history must be exactly what was committed, no repeats: %d messages, %d turns: %+v", len(st.Messages), st.CompletedTurns, st.Messages)
	}
	for i, m := range st.Messages {
		if i >= 1 && m.Content != fmt.Sprint("step ", i) {
			t.Fatalf("message %d is %q", i, m.Content)
		}
	}
}

func TestAStartedRunCannotBeStartedAgain(t *testing.T) {
	j := executor.NewLogJournal(newMemLog())
	hdr := executor.JournalHeader{TraceID: "t1", Kitchen: "default", Agent: testAgent(), StartedAt: time.Now()}
	if err := j.Start(context.Background(), hdr); err != nil {
		t.Fatal(err)
	}
	if err := j.Start(context.Background(), hdr); err == nil {
		t.Fatal("two runs cannot share a trace ID")
	}
	if _, err := j.Load(context.Background(), "nope"); !errors.Is(err, executor.ErrRunNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestLogJournalListsOnlyUnfinishedRunsOfTheKitchenAndNoChildren(t *testing.T) {
	log := newMemLog()
	j := executor.NewLogJournal(log)
	ctx := context.Background()
	start := func(id, kitchen, parent string) {
		a := testAgent()
		a.Kitchen = kitchen
		if err := j.Start(ctx, executor.JournalHeader{TraceID: id, Kitchen: kitchen, Agent: a, ParentTraceID: parent, StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	start("run", "default", "")
	start("done", "default", "")
	start("other", "elsewhere", "")
	start("child", "default", "run")
	if err := j.MarkDone(ctx, "done", "ok", nil); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkBudgetPaused(ctx, "run", models.BudgetPause{Dimension: models.BudgetDimTokens}); err != nil {
		t.Fatal(err)
	}
	got, err := j.ListUnfinished(ctx, "default")
	if err != nil || len(got) != 1 || got[0].TraceID != "run" || got[0].Status != executor.RunPaused || got[0].PauseKind != "budget" {
		t.Fatalf("%+v %v", got, err)
	}
	if all, _ := j.ListUnfinished(ctx, ""); len(all) != 2 {
		t.Fatalf("both kitchens: %+v", all)
	}
}

func TestLogJournalSweepRemovesOldFinishedRunsAndEndsStaleOnesButNotPausedOnes(t *testing.T) {
	log := newMemLog()
	j := executor.NewLogJournal(log)
	ctx := context.Background()
	for _, id := range []string{"old-done", "stale", "paused", "fresh"} {
		if err := j.Start(ctx, executor.JournalHeader{TraceID: id, Kitchen: "default", Agent: testAgent(), StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	_ = j.MarkDone(ctx, "old-done", "x", nil)
	_ = j.MarkBudgetPaused(ctx, "paused", models.BudgetPause{Dimension: models.BudgetDimCost})
	log.backdate("old-done", 10*24*time.Hour)
	log.backdate("stale", 20*24*time.Hour)
	log.backdate("paused", 20*24*time.Hour)

	removed, abandoned := j.Sweep(ctx, time.Now(), 7*24*time.Hour, 14*24*time.Hour)
	if removed != 1 || abandoned != 1 {
		t.Fatalf("removed %d abandoned %d", removed, abandoned)
	}
	if st, _ := j.Load(ctx, "stale"); st == nil || st.Outcome != executor.OutcomeAbandoned {
		t.Fatalf("%+v", st)
	}
	if st, _ := j.Load(ctx, "paused"); st == nil || st.Status != executor.RunPaused {
		t.Fatal("a run waiting for a person is never stale")
	}
	if _, err := j.Load(ctx, "old-done"); err == nil {
		t.Fatal("removed")
	}
}
