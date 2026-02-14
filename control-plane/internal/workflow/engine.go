// Package workflow implements the Recipe execution engine.
//
// The engine executes recipe DAGs — multi-agent workflows where each step
// can invoke an agent via A2A, wait for human approval, evaluate outputs,
// branch conditionally, or fan-out/fan-in for parallelism.
//
// Execution flow:
//  1. Topological sort of steps by depends_on edges
//  2. Execute steps in dependency order
//  3. Steps with no dependencies (or all deps met) run concurrently
//  4. Human gates pause execution until approved
//  5. Failed steps are retried per retry policy
//  6. Results are persisted to the recipe_runs table
package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Engine executes recipe workflows.
type Engine struct {
	store  store.Store
	client *http.Client

	// Running executions: runID → cancel func
	runsMu sync.RWMutex
	runs   map[string]context.CancelFunc

	// Human gate approvals: runID:stepName → channel
	gatesMu sync.RWMutex
	gates   map[string]chan bool
}

// NewEngine creates a new workflow execution engine.
func NewEngine(s store.Store) *Engine {
	return &Engine{
		store: s,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		runs:  make(map[string]context.CancelFunc),
		gates: make(map[string]chan bool),
	}
}

// ExecuteRecipe starts an async recipe execution.
// Returns the run ID immediately; execution happens in background.
func (e *Engine) ExecuteRecipe(ctx context.Context, recipe *models.Recipe, kitchen string, input map[string]interface{}) (string, error) {
	runID := uuid.New().String()

	run := &models.RecipeRun{
		ID:        runID,
		RecipeID:  recipe.ID,
		Kitchen:   kitchen,
		Status:    models.RecipeRunRunning,
		Input:     input,
		StartedAt: time.Now().UTC(),
	}

	if err := e.store.CreateRecipeRun(ctx, run); err != nil {
		return "", fmt.Errorf("create recipe run: %w", err)
	}

	// Create cancellable context for this execution
	execCtx, cancel := context.WithCancel(context.Background())
	e.runsMu.Lock()
	e.runs[runID] = cancel
	e.runsMu.Unlock()

	log.Info().
		Str("run_id", runID).
		Str("recipe", recipe.Name).
		Int("steps", len(recipe.Steps)).
		Msg("🍳 Recipe execution started")

	// Execute in background
	go e.executeAsync(execCtx, run, recipe)

	return runID, nil
}

// CancelRun cancels a running recipe execution.
func (e *Engine) CancelRun(runID string) bool {
	e.runsMu.Lock()
	cancel, ok := e.runs[runID]
	if ok {
		cancel()
		delete(e.runs, runID)
	}
	e.runsMu.Unlock()
	return ok
}

// ApproveGate approves a human gate, allowing execution to continue.
func (e *Engine) ApproveGate(runID, stepName string, approved bool) bool {
	key := runID + ":" + stepName
	e.gatesMu.RLock()
	ch, ok := e.gates[key]
	e.gatesMu.RUnlock()
	if !ok {
		return false
	}
	ch <- approved
	return true
}

// ── DAG Execution ───────────────────────────────────────────

func (e *Engine) executeAsync(ctx context.Context, run *models.RecipeRun, recipe *models.Recipe) {
	defer func() {
		e.runsMu.Lock()
		delete(e.runs, run.ID)
		e.runsMu.Unlock()
	}()

	steps := recipe.Steps
	if len(steps) == 0 {
		e.completeRun(run, nil, nil)
		return
	}

	// Build the dependency graph
	stepMap := make(map[string]*models.Step)
	for i := range steps {
		stepMap[steps[i].Name] = &steps[i]
	}

	// Track completed steps and their outputs
	completed := make(map[string]*models.StepResult)
	var completedMu sync.Mutex
	var stepResults []models.StepResult

	// Topological execution: keep running until all steps complete or error
	for {
		select {
		case <-ctx.Done():
			e.failRun(run, stepResults, "execution canceled")
			return
		default:
		}

		// Find steps that are ready to run (all deps satisfied, not yet completed)
		var ready []*models.Step
		completedMu.Lock()
		for _, step := range steps {
			if _, done := completed[step.Name]; done {
				continue
			}
			allDepsMet := true
			for _, dep := range step.DependsOn {
				if _, ok := completed[dep]; !ok {
					allDepsMet = false
					break
				}
			}
			if allDepsMet {
				s := step // copy
				ready = append(ready, &s)
			}
		}
		completedMu.Unlock()

		if len(ready) == 0 {
			// Check if all steps are done
			completedMu.Lock()
			allDone := len(completed) == len(steps)
			completedMu.Unlock()
			if allDone {
				break
			}
			// Deadlock detection — deps can never be satisfied
			e.failRun(run, stepResults, "deadlock: no steps ready but not all complete")
			return
		}

		// Execute ready steps concurrently
		var wg sync.WaitGroup
		errCh := make(chan error, len(ready))

		for _, step := range ready {
			wg.Add(1)
			go func(s *models.Step) {
				defer wg.Done()

				result := e.executeStep(ctx, run, s, completed, &completedMu)

				completedMu.Lock()
				completed[s.Name] = result
				stepResults = append(stepResults, *result)
				completedMu.Unlock()

				if result.Status == "failed" {
					errCh <- fmt.Errorf("step '%s' failed: %s", s.Name, result.Error)
				}
			}(step)
		}

		wg.Wait()
		close(errCh)

		// Check for failures
		for err := range errCh {
			log.Warn().Err(err).Str("run_id", run.ID).Msg("Step failed")
			// For now, continue execution of other branches
			// In the future, we can add fail-fast policy
		}
	}

	// Build output from final steps (steps with no dependents)
	output := make(map[string]interface{})
	for _, sr := range stepResults {
		if sr.Output != nil {
			output[sr.StepName] = sr.Output
		}
	}

	e.completeRun(run, stepResults, output)
}

// executeStep runs a single step with retry support.
func (e *Engine) executeStep(ctx context.Context, run *models.RecipeRun, step *models.Step, completed map[string]*models.StepResult, mu *sync.Mutex) *models.StepResult {
	start := time.Now()

	result := &models.StepResult{
		StepName:  step.Name,
		StepKind:  string(step.Kind),
		AgentRef:  step.AgentRef,
		StartedAt: start,
	}

	maxRetries := step.MaxRetries

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Simple exponential backoff: 1s, 2s, 4s, ...
			delay := time.Duration(1<<(attempt-1)) * time.Second
			log.Info().
				Str("step", step.Name).
				Int("attempt", attempt+1).
				Dur("delay", delay).
				Msg("Retrying step")

			select {
			case <-ctx.Done():
				result.Status = "canceled"
				result.DurationMs = time.Since(start).Milliseconds()
				return result
			case <-time.After(delay):
			}
		}

		err := e.executeStepOnce(ctx, run, step, result, completed, mu)
		if err == nil {
			result.Status = "completed"
			result.DurationMs = time.Since(start).Milliseconds()
			log.Info().
				Str("step", step.Name).
				Str("kind", string(step.Kind)).
				Int64("duration_ms", result.DurationMs).
				Msg("✅ Step completed")
			return result
		}
		lastErr = err
	}

	result.Status = "failed"
	result.Error = lastErr.Error()
	result.DurationMs = time.Since(start).Milliseconds()
	log.Error().
		Str("step", step.Name).
		Err(lastErr).
		Msg("❌ Step failed after retries")
	return result
}

// executeStepOnce executes a step once (without retries).
func (e *Engine) executeStepOnce(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	// Apply timeout if set
	if step.TimeoutSecs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutSecs)*time.Second)
		defer cancel()
	}

	switch step.Kind {
	case models.StepAgent:
		return e.executeAgentStep(ctx, run, step, result, completed, mu)

	case models.StepHumanGate:
		return e.executeHumanGate(ctx, run, step, result)

	case models.StepEvaluator:
		return e.executeAgentStep(ctx, run, step, result, completed, mu) // same as agent

	case models.StepCondition:
		return e.executeCondition(ctx, step, result, completed, mu)

	case models.StepFanOut:
		// Fan-out is handled by the parallel execution of dependent steps
		result.Output = map[string]interface{}{"fan_out": true}
		return nil

	case models.StepFanIn:
		// Fan-in collects outputs from dependencies
		return e.executeFanIn(step, result, completed, mu)

	default:
		return fmt.Errorf("unknown step kind: %s", step.Kind)
	}
}

// executeAgentStep calls an agent via the A2A protocol.
func (e *Engine) executeAgentStep(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	agentRef := step.AgentRef
	if agentRef == "" {
		return fmt.Errorf("agent step '%s' has no agent_ref", step.Name)
	}

	// Get the agent to find its A2A endpoint
	kitchen := run.Kitchen
	agent, err := e.store.GetAgent(ctx, kitchen, agentRef)
	if err != nil {
		return fmt.Errorf("agent lookup failed: %w", err)
	}

	if agent.Status != models.AgentStatusReady {
		return fmt.Errorf("agent '%s' is not ready (status: %s)", agentRef, agent.Status)
	}

	// Build input from previous step outputs
	input := make(map[string]interface{})
	if run.Input != nil {
		input["recipe_input"] = run.Input
	}

	mu.Lock()
	for _, dep := range step.DependsOn {
		if sr, ok := completed[dep]; ok && sr.Output != nil {
			input[dep] = sr.Output
		}
	}
	mu.Unlock()

	// Send A2A task via JSON-RPC
	a2aReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tasks/send",
		"params": map[string]interface{}{
			"id": uuid.New().String(),
			"message": map[string]interface{}{
				"role": "user",
				"parts": []map[string]interface{}{
					{"type": "text", "text": fmt.Sprintf("Execute step: %s\nInput: %v", step.Name, input)},
				},
			},
		},
		"id": uuid.New().String(),
	}

	body, _ := json.Marshal(a2aReq)

	// Determine endpoint
	endpoint := agent.A2AEndpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("http://localhost:8080/agents/%s/a2a", agentRef)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create A2A request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("A2A request failed: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("decode A2A response: %w", err)
	}

	// Check for RPC error
	if rpcErr, ok := rpcResp["error"].(map[string]interface{}); ok {
		return fmt.Errorf("A2A error: %v", rpcErr["message"])
	}

	result.Output = rpcResp
	return nil
}

// executeHumanGate waits for human approval.
func (e *Engine) executeHumanGate(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult) error {
	key := run.ID + ":" + step.Name

	ch := make(chan bool, 1)
	e.gatesMu.Lock()
	e.gates[key] = ch
	e.gatesMu.Unlock()

	defer func() {
		e.gatesMu.Lock()
		delete(e.gates, key)
		e.gatesMu.Unlock()
	}()

	log.Info().
		Str("run_id", run.ID).
		Str("step", step.Name).
		Msg("⏸️  Human gate — waiting for approval")

	// Update run status to paused
	run.Status = models.RecipeRunPaused
	e.store.UpdateRecipeRun(ctx, run)

	select {
	case approved := <-ch:
		if !approved {
			result.Output = map[string]interface{}{"approved": false}
			return fmt.Errorf("human gate '%s' was rejected", step.Name)
		}
		result.Output = map[string]interface{}{"approved": true}

		// Resume run
		run.Status = models.RecipeRunRunning
		e.store.UpdateRecipeRun(ctx, run)

		return nil

	case <-ctx.Done():
		return fmt.Errorf("human gate '%s' timed out or was canceled", step.Name)
	}
}

// executeCondition evaluates a condition and records which branch to take.
func (e *Engine) executeCondition(ctx context.Context, step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	// Condition config should specify:
	//   "expression": "step_name.output.field == value"
	//   "true_branch": "step_a"
	//   "false_branch": "step_b"
	if step.Config == nil {
		return fmt.Errorf("condition step '%s' has no config", step.Name)
	}

	var config map[string]interface{}
	configBytes, _ := json.Marshal(step.Config)
	json.Unmarshal(configBytes, &config)

	// Simple evaluation: check if a dep output exists
	conditionMet := false
	mu.Lock()
	for _, dep := range step.DependsOn {
		if sr, ok := completed[dep]; ok && sr.Status == "completed" {
			conditionMet = true
			break
		}
	}
	mu.Unlock()

	result.Output = map[string]interface{}{
		"condition_met": conditionMet,
		"branch":        "true",
	}
	if !conditionMet {
		result.Output["branch"] = "false"
	}

	return nil
}

// executeFanIn collects outputs from all dependency steps.
func (e *Engine) executeFanIn(step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	collected := make(map[string]interface{})

	mu.Lock()
	for _, dep := range step.DependsOn {
		if sr, ok := completed[dep]; ok {
			collected[dep] = map[string]interface{}{
				"status": sr.Status,
				"output": sr.Output,
			}
		}
	}
	mu.Unlock()

	result.Output = collected
	return nil
}

// ── Run Lifecycle ───────────────────────────────────────────

func (e *Engine) completeRun(run *models.RecipeRun, stepResults []models.StepResult, output map[string]interface{}) {
	now := time.Now().UTC()
	run.Status = models.RecipeRunCompleted
	run.CompletedAt = &now
	run.DurationMs = now.Sub(run.StartedAt).Milliseconds()
	run.StepResults = stepResults
	run.Output = output

	if err := e.store.UpdateRecipeRun(context.Background(), run); err != nil {
		log.Error().Err(err).Str("run_id", run.ID).Msg("Failed to update completed run")
	}

	log.Info().
		Str("run_id", run.ID).
		Int64("duration_ms", run.DurationMs).
		Int("steps", len(stepResults)).
		Msg("🎉 Recipe execution completed")
}

func (e *Engine) failRun(run *models.RecipeRun, stepResults []models.StepResult, errMsg string) {
	now := time.Now().UTC()
	run.Status = models.RecipeRunFailed
	run.CompletedAt = &now
	run.DurationMs = now.Sub(run.StartedAt).Milliseconds()
	run.StepResults = stepResults
	run.Error = errMsg

	if err := e.store.UpdateRecipeRun(context.Background(), run); err != nil {
		log.Error().Err(err).Str("run_id", run.ID).Msg("Failed to update failed run")
	}

	log.Error().
		Str("run_id", run.ID).
		Str("error", errMsg).
		Msg("💥 Recipe execution failed")
}

// GetPendingGates returns the list of pending human gates for a run.
func (e *Engine) GetPendingGates(runID string) []string {
	e.gatesMu.RLock()
	defer e.gatesMu.RUnlock()

	var pending []string
	prefix := runID + ":"
	for key := range e.gates {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			pending = append(pending, key[len(prefix):])
		}
	}
	return pending
}
