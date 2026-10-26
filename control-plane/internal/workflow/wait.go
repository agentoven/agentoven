package workflow

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// maxWait is the longest a wait step may last. A run lives in the control plane's memory, so a wait
// that outlasts a restart is lost with it; thirty days is already far past what that allows, and a
// limit stops a forgotten wait from holding a run open forever.
const maxWait = 30 * 24 * time.Hour

// ParseWaitDuration reads a length of time written as "30s", "5m", "2h" or "3d", or a bare number of
// seconds. Fractions are fine ("1.5h").
func ParseWaitDuration(text string) (time.Duration, error) {
	t := strings.TrimSpace(strings.ToLower(text))
	if t == "" {
		return 0, fmt.Errorf("no length of time given")
	}
	unit := time.Second
	switch t[len(t)-1] {
	case 's':
		t = t[:len(t)-1]
	case 'm':
		unit, t = time.Minute, t[:len(t)-1]
	case 'h':
		unit, t = time.Hour, t[:len(t)-1]
	case 'd':
		unit, t = 24*time.Hour, t[:len(t)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a length of time like 30s, 5m, 2h or 3d", text)
	}
	return time.Duration(n * float64(unit)), nil
}

// waitDeadline is when a wait step ends: `duration` after now, or at `until` (an RFC 3339 time). A time
// already past ends it at once.
func waitDeadline(step *models.Step, now time.Time) (time.Time, error) {
	cfg := step.Config
	if cfg == nil {
		cfg = map[string]interface{}{}
	}
	var deadline time.Time
	switch {
	case cfg["duration"] != nil && fmt.Sprint(cfg["duration"]) != "":
		var d time.Duration
		var err error
		switch v := cfg["duration"].(type) {
		case string:
			d, err = ParseWaitDuration(v)
		case float64:
			d = time.Duration(v * float64(time.Second))
		case int:
			d = time.Duration(v) * time.Second
		default:
			err = fmt.Errorf("duration must be text like \"5m\"")
		}
		if err != nil {
			return time.Time{}, fmt.Errorf("wait step '%s': %w", step.Name, err)
		}
		deadline = now.Add(d)
	case cfg["until"] != nil && fmt.Sprint(cfg["until"]) != "":
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(fmt.Sprint(cfg["until"])))
		if err != nil {
			return time.Time{}, fmt.Errorf("wait step '%s': until %q is not a time like 2026-11-02T09:00:00Z", step.Name, cfg["until"])
		}
		deadline = at
	default:
		return time.Time{}, fmt.Errorf("wait step '%s' needs a duration or an until time", step.Name)
	}
	return deadline, nil
}

// checkLength refuses a wait longer than maxWait.
func checkLength(step *models.Step, deadline, now time.Time) error {
	if deadline.Sub(now) > maxWait {
		return fmt.Errorf("wait step '%s' would wait longer than %d days", step.Name, int(maxWait/(24*time.Hour)))
	}
	return nil
}

// waiters holds, for each wait step that is waiting, the channel that ends it early.
type waiters struct{ m sync.Map }

func waitKey(runID, step string) string { return runID + ":" + step }

// executeWait pauses the run at this step until its time comes, it is released, or the run is canceled.
// The step shows as "waiting", with the time it will end, on the run in the store while it waits.
func (e *Engine) executeWait(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, mu *sync.Mutex) error {
	start := time.Now().UTC()
	deadline, err := waitDeadline(step, start)
	if err == nil {
		err = checkLength(step, deadline, start)
	}
	if err != nil {
		return err
	}

	release := make(chan struct{}, 1)
	key := waitKey(run.ID, step.Name)
	e.waits.m.Store(key, release)
	defer e.waits.m.Delete(key)

	released := false
	if wait := deadline.Sub(start); wait > 0 {
		// Show the wait on the stored run. The scheduler and this step share mu, so the copy is consistent.
		mu.Lock()
		snapshot := *run
		snapshot.StepResults = append(append([]models.StepResult(nil), run.StepResults...), models.StepResult{
			StepName: step.Name, StepKind: string(step.Kind), Status: "waiting", StartedAt: start, WakeAt: &deadline,
		})
		mu.Unlock()
		if err := e.store.UpdateRecipeRun(ctx, &snapshot); err != nil {
			log.Warn().Err(err).Str("run_id", run.ID).Msg("Could not record that a wait step is waiting")
		}
		log.Info().Str("run_id", run.ID).Str("step", step.Name).Time("until", deadline).Msg("⏳ Wait step — waiting")

		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-release:
			released = true
		case <-ctx.Done():
			return fmt.Errorf("wait step '%s' was canceled", step.Name)
		}
	}

	result.Output = map[string]interface{}{
		"waited_seconds": time.Since(start).Seconds(),
		"released_early": released,
		"until":          deadline.Format(time.RFC3339),
	}
	return nil
}

// ReleaseWait ends a wait step early. It reports false when that step is not waiting.
func (e *Engine) ReleaseWait(runID, stepName string) bool {
	v, ok := e.waits.m.Load(waitKey(runID, stepName))
	if !ok {
		return false
	}
	select {
	case v.(chan struct{}) <- struct{}{}:
	default: // already released
	}
	return true
}

// ValidateWaitStep says what is wrong with a wait step's settings, so a recipe can be refused when it is
// saved instead of failing when it runs. A length of time is checked against the limit; a fixed time is
// not, because whether it is too far away is a question for the day the recipe runs.
func ValidateWaitStep(step *models.Step) error {
	now := time.Now().UTC()
	deadline, err := waitDeadline(step, now)
	if err != nil {
		return err
	}
	if step.Config["duration"] != nil {
		return checkLength(step, deadline, now)
	}
	return nil
}
