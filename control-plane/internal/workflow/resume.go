package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Restart-safe runs (ADR-0043).
//
// A run used to live in the memory of the process that started it: a restart left it "running" or
// "paused" with nobody executing it, and a human gate or wait step in it never ended. Now:
//
//   - every step is checkpointed on the stored run as it starts and as it ends (runState.persist);
//   - the process that executes a run holds a lease on it, renewed while the process lives
//     (RunResumeStore); when the process dies the lease runs out and another process, or the same one
//     restarted, takes the run over (ResumeRuns) and carries on from the checkpoint;
//   - the end of a gate's SLA and of a wait is a timer kept by the store on its own clock
//     (ArmRunTimer), so a restart finds what is left of it instead of starting it again;
//   - a gate decision is written only if the record is still pending (ApprovalResolver), so two
//     decisions, or a decision and an expiry, cannot both win.
//
// A step that was running when the process died is not run again unless it is safe to: see resumePolicy.

const (
	// EnvRunResume turns the feature off when set to false. On by default.
	EnvRunResume = "AGENTOVEN_RUN_RESUME"
	// EnvRunLeaseSeconds is how long a lease on a run lasts without being renewed.
	EnvRunLeaseSeconds = "AGENTOVEN_RUN_LEASE_SECONDS"

	defaultLeaseTTL = 20 * time.Second
	// resumeBatch bounds how many runs one pass takes over, so a backlog cannot start a flood of goroutines.
	resumeBatch = 25
)

// resumeState is what an engine with resume enabled holds.
type resumeState struct {
	store store.RunResumeStore
	owner string
	ttl   time.Duration
}

// runState is the checkpoint of one run executed by this process.
type runState struct {
	mu  sync.Mutex
	run *models.RecipeRun
	// root is the run that holds the lease: the run itself, or the top-level run a sub-recipe belongs to.
	root     string
	finished []models.StepResult
	inflight map[string]models.StepResult
	// prior holds the steps that were waiting or running when a restart interrupted them, for the
	// executors that re-enter them (human gate, wait, sub-recipe).
	prior map[string]models.StepResult
}

type rootRunKey struct{}

func (e *Engine) newRunState(ctx context.Context, run *models.RecipeRun) *runState {
	root := run.ID
	if v, ok := ctx.Value(rootRunKey{}).(string); ok {
		root = v
	}
	rs := &runState{run: run, root: root, inflight: map[string]models.StepResult{}, prior: map[string]models.StepResult{}}
	e.runStates.Store(run.ID, rs)
	return rs
}

func (e *Engine) stateOf(runID string) *runState {
	v, ok := e.runStates.Load(runID)
	if !ok {
		return nil
	}
	return v.(*runState)
}

// priorOf returns the result a step had when a restart interrupted it.
func (e *Engine) priorOf(runID, step string) (models.StepResult, bool) {
	rs := e.stateOf(runID)
	if rs == nil {
		return models.StepResult{}, false
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	p, ok := rs.prior[step]
	return p, ok
}

// persistLocked writes the run with its finished steps and the steps in flight. A run with a gate
// waiting is paused, one without is running; a run that has ended is never touched. A process whose
// lease was taken over writes nothing: the new owner's checkpoints must not be overwritten.
func (rs *runState) persistLocked(e *Engine) {
	run := rs.run
	if e.leaseLost(run.ID) {
		return
	}
	steps := append([]models.StepResult(nil), rs.finished...)
	waiting := false
	live := make([]models.StepResult, 0, len(rs.inflight))
	for _, r := range rs.inflight {
		if r.GateStatus == "waiting" {
			waiting = true
		}
		live = append(live, r)
	}
	sort.Slice(live, func(i, j int) bool {
		if !live[i].StartedAt.Equal(live[j].StartedAt) {
			return live[i].StartedAt.Before(live[j].StartedAt)
		}
		return live[i].StepName < live[j].StepName
	})
	run.StepResults = append(steps, live...)
	switch run.Status {
	case models.RecipeRunRunning, models.RecipeRunPaused:
		run.Status = models.RecipeRunRunning
		if waiting {
			run.Status = models.RecipeRunPaused
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.store.UpdateRecipeRun(ctx, run); err != nil {
		log.Warn().Err(err).Str("run_id", run.ID).Msg("Could not checkpoint the run")
	}
}

// begin records that a step has started.
func (e *Engine) begin(run *models.RecipeRun, step *models.Step) {
	rs := e.stateOf(run.ID)
	if rs == nil {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	started := time.Now().UTC()
	if p, ok := rs.prior[step.Name]; ok && !p.StartedAt.IsZero() {
		started = p.StartedAt
	}
	rs.inflight[step.Name] = models.StepResult{StepName: step.Name, StepKind: string(step.Kind), AgentRef: step.AgentRef, Status: "running", StartedAt: started}
	rs.persistLocked(e)
}

// annotate changes what is recorded about a step in flight (a gate that is waiting, a wait's wake time,
// the run a sub-recipe step started) and checkpoints it.
func (e *Engine) annotate(run *models.RecipeRun, step string, f func(r *models.StepResult)) {
	rs := e.stateOf(run.ID)
	if rs == nil {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	r, ok := rs.inflight[step]
	if !ok {
		return
	}
	f(&r)
	rs.inflight[step] = r
	rs.persistLocked(e)
}

// end records that a step has ended; results is every finished step so far (it only grows).
func (e *Engine) end(run *models.RecipeRun, step string, results []models.StepResult) {
	rs := e.stateOf(run.ID)
	if rs == nil {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.inflight, step)
	if len(results) >= len(rs.finished) {
		rs.finished = results
	}
	rs.persistLocked(e)
}

// ── leases ──────────────────────────────────────────────────────────────────

// EnableResume makes runs restart-safe when the store can lease them (store.RunResumeStore): it takes
// over the runs an earlier process left, keeps renewing the leases of the runs this one executes, and
// looks for runs whose process died. It reports false, changing nothing, when the store cannot or
// AGENTOVEN_RUN_RESUME=false. owner names this process; empty means a fresh random one.
func (e *Engine) EnableResume(ctx context.Context, owner string) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvRunResume)), "false") {
		log.Info().Msg("Restart-safe runs are switched off (AGENTOVEN_RUN_RESUME=false)")
		return false
	}
	rstore, ok := e.store.(store.RunResumeStore)
	if !ok {
		log.Info().Msg("This store cannot lease runs: a run that is interrupted by a restart stays where it was")
		return false
	}
	if owner == "" {
		owner = uuid.New().String()
	}
	ttl := defaultLeaseTTL
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvRunLeaseSeconds))); err == nil && n >= 2 {
		ttl = time.Duration(n) * time.Second
	}
	e.resume = &resumeState{store: rstore, owner: owner, ttl: ttl}
	go e.keepLeases(ctx)
	go func() {
		// A resumed agent step calls this control plane's own gateway, so the first pass waits for it.
		if e.readyURL != "" && !awaitServing(ctx, e.readyURL) {
			return
		}
		e.ResumeRuns(ctx)
	}()
	log.Info().Str("owner", owner).Str("lease", ttl.String()).Msg("Restart-safe runs are on")
	return true
}

// SetReadyURL names a URL (the control plane's own /health) that answers once the server is listening;
// the first look for runs to resume waits for it. Without one the first look is immediate.
func (e *Engine) SetReadyURL(u string) { e.readyURL = u }

// awaitServing waits, for at most two minutes, until url answers. It reports false if ctx ended first.
func awaitServing(ctx context.Context, url string) bool {
	deadline := time.Now().Add(2 * time.Minute)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
				if resp.StatusCode < 500 {
					return true
				}
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
	return ctx.Err() == nil
}

// keepLeases renews the leases of the runs this process executes, stops the ones it lost, and looks for
// runs that nobody executes.
func (e *Engine) keepLeases(ctx context.Context) {
	renew := time.NewTicker(e.resume.ttl / 4)
	defer renew.Stop()
	look := time.NewTicker(e.resume.ttl / 2)
	defer look.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew.C:
			e.renewLeases(ctx)
		case <-look.C:
			e.ResumeRuns(ctx)
		}
	}
}

func (e *Engine) renewLeases(ctx context.Context) {
	held, err := e.resume.store.RenewRunLeases(ctx, e.resume.owner, e.resume.ttl)
	if err != nil {
		log.Warn().Err(err).Msg("Could not renew the leases on this process's runs; they stay running and are renewed on the next tick")
		return
	}
	have := make(map[string]bool, len(held))
	for _, id := range held {
		have[id] = true
	}
	e.runsMu.RLock()
	var gone []string
	for id := range e.runs {
		if !have[id] {
			gone = append(gone, id)
		}
	}
	e.runsMu.RUnlock()
	for _, id := range gone {
		// Another process took the run over (this one was too slow to renew). Stop executing it without
		// recording a failure: the run is not over.
		log.Error().Str("run_id", id).Msg("The lease on this run was taken over; this process stops executing it")
		e.lost.Store(id, true)
		e.CancelRun(id)
	}
}

// leaseLost says whether another process took over the run, or the top-level run it belongs to.
func (e *Engine) leaseLost(runID string) bool {
	if rs := e.stateOf(runID); rs != nil {
		runID = rs.root
	}
	_, lost := e.lost.Load(runID)
	return lost
}

// claim leases a run just created by this process.
func (e *Engine) claim(ctx context.Context, run *models.RecipeRun, recipe *models.Recipe) {
	if e.resume == nil {
		return
	}
	if ok, err := e.resume.store.ClaimRun(ctx, run.ID, e.resume.owner, e.resume.ttl, recipeDigest(recipe)); err != nil || !ok {
		log.Error().Err(err).Str("run_id", run.ID).Msg("Could not lease the run; it will not survive a restart")
	}
}

// ReleaseRuns stops executing the runs this process holds and gives up their leases, so that a process
// started in its place takes them over at once instead of waiting for the leases to run out. Nothing is
// recorded about the runs: they are not over, only no longer executed here. Call it on a graceful shutdown.
func (e *Engine) ReleaseRuns(ctx context.Context) {
	if e.resume == nil {
		return
	}
	e.runsMu.RLock()
	ids := make([]string, 0, len(e.runs))
	for id := range e.runs {
		ids = append(ids, id)
	}
	e.runsMu.RUnlock()
	for _, id := range ids {
		e.lost.Store(id, true) // stop checkpointing: the next owner's checkpoints come first
	}
	if err := e.resume.store.ReleaseRunLeases(ctx, e.resume.owner); err != nil {
		log.Warn().Err(err).Msg("Could not release the leases on this process's runs; they run out by themselves")
	}
	for _, id := range ids {
		e.CancelRun(id)
	}
}

// recipeDigest identifies the steps a run started from.
func recipeDigest(recipe *models.Recipe) string {
	b, err := json.Marshal(recipe.Steps)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ── taking runs over ────────────────────────────────────────────────────────

// SetResumeHook names who is told when a run is taken over after a restart (Pro writes an audit
// event). outcome is "resumed" or "failed".
func (e *Engine) SetResumeHook(f func(kitchen, runID, recipe, outcome, detail string)) {
	e.onResume = f
}

// ResumeRuns takes over every run that nobody executes (up to a batch) and carries each on from its
// checkpoint. It returns how many it took. It is safe to call at any time and from several processes.
func (e *Engine) ResumeRuns(ctx context.Context) int {
	if e.resume == nil {
		return 0
	}
	runs, err := e.resume.store.ClaimResumableRuns(ctx, e.resume.owner, e.resume.ttl, resumeBatch)
	if err != nil {
		log.Warn().Err(err).Msg("Could not look for runs to resume")
		return 0
	}
	n := 0
	for _, rr := range runs {
		run := rr.Run
		e.runsMu.RLock()
		_, mine := e.runs[run.ID]
		e.runsMu.RUnlock()
		if mine {
			continue // started by this process a moment ago
		}
		e.resumeOne(ctx, &run, rr.Digest)
		n++
	}
	return n
}

func (e *Engine) resumeOne(ctx context.Context, run *models.RecipeRun, digest string) {
	recipe, err := e.recipeOfRun(ctx, run.Kitchen, run)
	switch {
	case err != nil:
		e.abandon(ctx, run, "", fmt.Sprintf("it was interrupted by a restart and its recipe could not be loaded: %v", err))
		return
	case digest != "" && digest != recipeDigest(recipe):
		e.abandon(ctx, run, recipe.Name, "it was interrupted by a restart and its recipe has been changed since it started, so it cannot carry on")
		return
	}
	execCtx, cancel := context.WithCancel(context.Background())
	e.runsMu.Lock()
	e.runs[run.ID] = cancel
	e.runsMu.Unlock()
	log.Info().Str("run_id", run.ID).Str("recipe", recipe.Name).Int("steps_done", len(run.StepResults)).Msg("Resuming a recipe run after a restart")
	if e.onResume != nil {
		e.onResume(run.Kitchen, run.ID, recipe.Name, "resumed", "")
	}
	go e.executeResumed(execCtx, run, recipe)
}

func (e *Engine) abandon(ctx context.Context, run *models.RecipeRun, recipeName, why string) {
	if recipeName != "" {
		if id := e.findParentTrace(ctx, run, &models.Recipe{Name: recipeName}, true); id != "" {
			e.parentTraceIDs.Store(run.ID, id) // failRun ends it
		}
	}
	e.failRun(run, run.StepResults, why)
	if e.onResume != nil {
		e.onResume(run.Kitchen, run.ID, run.RecipeID, "failed", why)
	}
}

func (e *Engine) executeResumed(ctx context.Context, run *models.RecipeRun, recipe *models.Recipe) {
	defer func() {
		e.runsMu.Lock()
		delete(e.runs, run.ID)
		e.runsMu.Unlock()
		e.lost.Delete(run.ID)
	}()
	prior := run.StepResults
	if prior == nil {
		prior = []models.StepResult{} // non-nil: this is a resumed run, not a new one
	}
	e.runRecipe(ctx, run, recipe, prior)
}

// ── what a restart does to a step that was running ──────────────────────────

// resumePolicy says what happens to a step that was running when the process died.
//
//   - A gate or wait carries on: its record and timer are in the store.
//   - A sub-recipe step carries on in the run it had started.
//   - A step that only reads or computes (condition, router, fan_out, fan_in, rag) is run again.
//   - Any other step may have done something in another system, and running it again could do it twice.
//     It fails with a message saying so, unless the recipe says the step is safe to repeat
//     (config on_restart: rerun).
type resumeAction int

const (
	resumeReenter resumeAction = iota
	resumeRerun
	resumeFail
)

func resumePolicy(step *models.Step, p models.StepResult) resumeAction {
	switch step.Kind {
	case models.StepHumanGate, models.StepWait:
		return resumeReenter
	case models.StepSubRecipe:
		if p.SubRunID != "" {
			return resumeReenter
		}
	case models.StepCondition, models.StepRouter, models.StepFanOut, models.StepFanIn, models.StepRAG:
		return resumeRerun
	}
	if v, _ := step.Config["on_restart"].(string); v == "rerun" {
		return resumeRerun
	}
	return resumeFail
}

// interruptedMessage is what a step that cannot be run again says.
func interruptedMessage(step *models.Step) string {
	return fmt.Sprintf("step '%s' was running when the control plane restarted and may already have acted, so it was not run again; run the recipe again, or set on_restart: rerun on a step that is safe to repeat", step.Name)
}

// restore sorts the results a resumed run was left with: the ones that ended go back into the
// scheduler's tables, the ones that were in flight are re-entered, run again or failed by resumePolicy.
func (e *Engine) restore(rs *runState, steps []models.Step, prior []models.StepResult) (ended []models.StepResult, err error) {
	byName := make(map[string]*models.Step, len(steps))
	for i := range steps {
		byName[steps[i].Name] = &steps[i]
	}
	for _, p := range prior {
		step, known := byName[p.StepName]
		if !known {
			return nil, fmt.Errorf("the run has a result for step '%s' that the recipe no longer has", p.StepName)
		}
		switch p.Status {
		case "completed", "skipped", "failed", "canceled":
			ended = append(ended, p)
			continue
		}
		switch resumePolicy(step, p) {
		case resumeReenter:
			p.Resumed = true
			rs.prior[p.StepName] = p
		case resumeRerun:
			// nothing is kept: the scheduler finds the step ready and runs it again
		default:
			now := time.Now().UTC()
			ended = append(ended, models.StepResult{
				StepName: p.StepName, StepKind: p.StepKind, AgentRef: p.AgentRef, Status: "failed",
				StartedAt: p.StartedAt, DurationMs: now.Sub(p.StartedAt).Milliseconds(), Error: interruptedMessage(step), Resumed: true,
			})
		}
	}
	return ended, nil
}

// findParentTrace returns the id of the trace a run opened before a restart interrupted it, so the run
// goes on writing to it and ends it, instead of leaving it "running" for ever. "" when there is none
// (or the run is new).
func (e *Engine) findParentTrace(ctx context.Context, run *models.RecipeRun, recipe *models.Recipe, resumed bool) string {
	if !resumed {
		return ""
	}
	traces, err := e.store.ListTracesFiltered(ctx, run.Kitchen, store.TraceFilter{RecipeName: recipe.Name, Status: "running", Limit: 200})
	if err != nil {
		return ""
	}
	for _, t := range traces {
		if id, _ := t.Metadata["run_id"].(string); id == run.ID {
			return t.ID
		}
	}
	return ""
}
