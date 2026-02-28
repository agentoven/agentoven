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

	"github.com/agentoven/agentoven/control-plane/internal/notify"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Engine executes recipe workflows.
type Engine struct {
	store    store.Store
	client   *http.Client
	notifier *notify.Service

	// baseURL is the control plane's own HTTP origin (e.g. "http://localhost:8080").
	// The engine routes all A2A and RAG calls through the control plane gateway
	// so that auth, RBAC, and observability are applied uniformly (ADR-0007).
	baseURL string

	// Running executions: runID → cancel func
	runsMu sync.RWMutex
	runs   map[string]context.CancelFunc

	// Human gate approvals: runID:stepName → channel
	gatesMu sync.RWMutex
	gates   map[string]chan bool
}

// NewEngine creates a new workflow execution engine.
// baseURL is the control plane's HTTP origin (e.g. "http://localhost:8080").
// All agent and RAG calls are routed through the gateway via this URL.
func NewEngine(s store.Store, notifier *notify.Service, baseURL string) *Engine {
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	return &Engine{
		store:   s,
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		notifier: notifier,
		runs:     make(map[string]context.CancelFunc),
		gates:    make(map[string]chan bool),
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
// Supports both the legacy in-memory channel approach and the durable
// store-backed approach. It checks the store first, then falls back to
// the in-memory channel for backward compatibility.
func (e *Engine) ApproveGate(runID, stepName string, approved bool) bool {
	gateKey := runID + ":" + stepName

	// Try store-backed approval first
	record, err := e.store.GetApproval(context.Background(), gateKey)
	if err == nil && record.Status == "pending" {
		now := time.Now().UTC()
		if approved {
			record.Status = "approved"
		} else {
			record.Status = "rejected"
		}
		record.ResolvedAt = &now
		if updateErr := e.store.UpdateApproval(context.Background(), record); updateErr != nil {
			log.Error().Err(updateErr).Str("gate_key", gateKey).Msg("Failed to update approval record")
		}
		// Also signal the channel if the goroutine is waiting
		e.gatesMu.RLock()
		ch, ok := e.gates[gateKey]
		e.gatesMu.RUnlock()
		if ok {
			ch <- approved
		}
		return true
	}

	// Fallback: legacy in-memory channel
	e.gatesMu.RLock()
	ch, ok := e.gates[gateKey]
	e.gatesMu.RUnlock()
	if !ok {
		return false
	}
	ch <- approved
	return true
}

// ApproveGateWithMetadata approves or rejects a gate with approver identity and comments.
func (e *Engine) ApproveGateWithMetadata(runID, stepName string, approved bool, approverID, approverEmail, channel, comments string) bool {
	gateKey := runID + ":" + stepName

	record, err := e.store.GetApproval(context.Background(), gateKey)
	if err != nil || record.Status != "pending" {
		return false
	}

	now := time.Now().UTC()
	if approved {
		record.Status = "approved"
	} else {
		record.Status = "rejected"
	}
	record.ApproverID = approverID
	record.ApproverEmail = approverEmail
	record.ApproverChannel = channel
	record.Comments = comments
	record.ResolvedAt = &now

	if updateErr := e.store.UpdateApproval(context.Background(), record); updateErr != nil {
		log.Error().Err(updateErr).Str("gate_key", gateKey).Msg("Failed to update approval record")
		return false
	}

	// Signal the waiting goroutine
	e.gatesMu.RLock()
	ch, ok := e.gates[gateKey]
	e.gatesMu.RUnlock()
	if ok {
		ch <- approved
	}
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

			// Dispatch step_completed notifications
			if len(step.NotifyTools) > 0 && e.notifier != nil {
				evt := notify.Event{
					Type:       string(notify.EventStepCompleted),
					RunID:      run.ID,
					RecipeName: run.RecipeID,
					StepName:   step.Name,
					Kitchen:    run.Kitchen,
					Timestamp:  time.Now().UTC(),
				}
				result.NotifyResults = e.notifier.DispatchAll(ctx, run.Kitchen, step.NotifyTools, evt)
			}

			// Evaluate branches for routing
			if len(step.Branches) > 0 {
				branch := e.evaluateBranches(step, result)
				if branch != "" {
					result.BranchTaken = branch
				}
			}

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

	// Dispatch step_failed notifications
	if len(step.NotifyTools) > 0 && e.notifier != nil {
		evt := notify.Event{
			Type:       string(notify.EventStepFailed),
			RunID:      run.ID,
			RecipeName: run.RecipeID,
			StepName:   step.Name,
			Kitchen:    run.Kitchen,
			Payload:    map[string]interface{}{"error": lastErr.Error()},
			Timestamp:  time.Now().UTC(),
		}
		result.NotifyResults = e.notifier.DispatchAll(ctx, run.Kitchen, step.NotifyTools, evt)
	}

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

	case models.StepRAG:
		return e.executeRAGStep(ctx, run, step, result, completed, mu)

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

	// ADR-0007: Always route through the control plane gateway using the
	// stable URL. The gateway handles backend resolution (managed process
	// vs external endpoint), auth, RBAC, and observability.
	endpoint := fmt.Sprintf("%s/agents/%s/a2a", e.baseURL, agentRef)

	stepStart := time.Now()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create A2A request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(httpReq)
	if err != nil {
		e.createStepTrace(ctx, kitchen, agentRef, run.RecipeID, run.ID, step.Name, "error", stepStart, 0, 0, "", err.Error())
		return fmt.Errorf("A2A request failed: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		e.createStepTrace(ctx, kitchen, agentRef, run.RecipeID, run.ID, step.Name, "error", stepStart, 0, 0, "", err.Error())
		return fmt.Errorf("decode A2A response: %w", err)
	}

	// Check for RPC error
	if rpcErr, ok := rpcResp["error"].(map[string]interface{}); ok {
		errMsg := fmt.Sprintf("%v", rpcErr["message"])
		e.createStepTrace(ctx, kitchen, agentRef, run.RecipeID, run.ID, step.Name, "error", stepStart, 0, 0, "", errMsg)
		return fmt.Errorf("A2A error: %v", rpcErr["message"])
	}

	// Extract output text and usage data from the A2A response
	outputText, tokens, costUSD := extractA2AMetrics(rpcResp)

	// Populate cost/token data on the step result
	result.Tokens = tokens
	result.CostUSD = costUSD

	// Create a trace record for this step
	e.createStepTrace(ctx, kitchen, agentRef, run.RecipeID, run.ID, step.Name, "completed", stepStart, tokens, costUSD, outputText, "")

	result.Output = rpcResp
	return nil
}

// createStepTrace persists a Trace record for a single agent/RAG step execution.
func (e *Engine) createStepTrace(ctx context.Context, kitchen, agentName, recipeName, runID, stepName, status string, start time.Time, tokens int64, costUSD float64, outputText, errMsg string) {
	durationMs := time.Since(start).Milliseconds()
	trace := &models.Trace{
		ID:          uuid.New().String(),
		AgentName:   agentName,
		RecipeName:  recipeName,
		Kitchen:     kitchen,
		Status:      status,
		DurationMs:  durationMs,
		TotalTokens: tokens,
		CostUSD:     costUSD,
		OutputText:  outputText,
		Metadata: map[string]interface{}{
			"run_id":    runID,
			"step_name": stepName,
		},
		CreatedAt: time.Now().UTC(),
	}
	if errMsg != "" {
		trace.Metadata["error"] = errMsg
	}
	if err := e.store.CreateTrace(ctx, trace); err != nil {
		log.Error().Err(err).Str("agent", agentName).Str("step", stepName).Msg("Failed to persist trace")
	}
}

// extractA2AMetrics extracts output text, token count, and cost from an A2A JSON-RPC response.
// The response shape is: { "result": { "status": {...}, "artifacts": [{ "parts": [{"type":"text","text":"..."}] }], "usage": {"prompt_tokens":N,"completion_tokens":N,"total_tokens":N,"cost_usd":F} } }
func extractA2AMetrics(rpcResp map[string]interface{}) (outputText string, tokens int64, costUSD float64) {
	rpcResult, ok := rpcResp["result"].(map[string]interface{})
	if !ok {
		return "", 0, 0
	}

	// Extract text from artifacts
	if artifacts, ok := rpcResult["artifacts"].([]interface{}); ok {
		for _, art := range artifacts {
			artMap, ok := art.(map[string]interface{})
			if !ok {
				continue
			}
			if parts, ok := artMap["parts"].([]interface{}); ok {
				for _, part := range parts {
					partMap, ok := part.(map[string]interface{})
					if !ok {
						continue
					}
					if partMap["type"] == "text" {
						if txt, ok := partMap["text"].(string); ok {
							if outputText != "" {
								outputText += "\n"
							}
							outputText += txt
						}
					}
				}
			}
		}
	}

	// Extract usage/token data — check both top-level "usage" and nested in result
	usage, _ := rpcResult["usage"].(map[string]interface{})
	if usage == nil {
		// Some A2A implementations put usage in metadata
		if meta, ok := rpcResult["metadata"].(map[string]interface{}); ok {
			usage, _ = meta["usage"].(map[string]interface{})
		}
	}
	if usage != nil {
		if t, ok := usage["total_tokens"].(float64); ok {
			tokens = int64(t)
		}
		if c, ok := usage["cost_usd"].(float64); ok {
			costUSD = c
		}
	}

	return outputText, tokens, costUSD
}

// executeRAGStep calls the control plane's RAG query endpoint.
// The step config should contain "strategy", "top_k", "namespace", and
// optionally "question_from" (the name of a previous step whose output
// contains the query text). If "question_from" is not set, the recipe
// input is serialised as the question.
func (e *Engine) executeRAGStep(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	// Determine the question text
	question := ""
	if step.Config != nil {
		if qf, ok := step.Config["question_from"].(string); ok && qf != "" {
			// Pull question from a previous step's output
			mu.Lock()
			if sr, ok := completed[qf]; ok && sr.Output != nil {
				if txt, ok := sr.Output["text"].(string); ok {
					question = txt
				} else {
					// Serialise the whole output as the question
					qBytes, _ := json.Marshal(sr.Output)
					question = string(qBytes)
				}
			}
			mu.Unlock()
		}
	}
	if question == "" && run.Input != nil {
		qBytes, _ := json.Marshal(run.Input)
		question = string(qBytes)
	}
	if question == "" {
		question = "No input provided"
	}

	// Build RAG query request
	ragReq := map[string]interface{}{
		"question": question,
		"strategy": "naive",
		"top_k":    3,
	}
	if step.Config != nil {
		if s, ok := step.Config["strategy"].(string); ok && s != "" {
			ragReq["strategy"] = s
		}
		if tk, ok := step.Config["top_k"]; ok {
			ragReq["top_k"] = tk
		}
		if ns, ok := step.Config["namespace"].(string); ok && ns != "" {
			ragReq["namespace"] = ns
		}
	}

	body, _ := json.Marshal(ragReq)
	endpoint := fmt.Sprintf("%s/api/v1/rag/query", e.baseURL)

	stepStart := time.Now()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create RAG request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Kitchen", run.Kitchen)

	resp, err := e.client.Do(httpReq)
	if err != nil {
		e.createStepTrace(ctx, run.Kitchen, "rag-pipeline", run.RecipeID, run.ID, step.Name, "error", stepStart, 0, 0, "", err.Error())
		return fmt.Errorf("RAG request failed: %w", err)
	}
	defer resp.Body.Close()

	var ragResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&ragResp); err != nil {
		e.createStepTrace(ctx, run.Kitchen, "rag-pipeline", run.RecipeID, run.ID, step.Name, "error", stepStart, 0, 0, "", err.Error())
		return fmt.Errorf("decode RAG response: %w", err)
	}

	if resp.StatusCode >= 400 {
		errMsg, _ := ragResp["error"].(string)
		e.createStepTrace(ctx, run.Kitchen, "rag-pipeline", run.RecipeID, run.ID, step.Name, "error", stepStart, 0, 0, "", errMsg)
		return fmt.Errorf("RAG query failed (status %d): %s", resp.StatusCode, errMsg)
	}

	// Extract answer text from RAG response
	answerText, _ := ragResp["answer"].(string)
	e.createStepTrace(ctx, run.Kitchen, "rag-pipeline", run.RecipeID, run.ID, step.Name, "completed", stepStart, 0, 0, answerText, "")

	result.Output = ragResp
	return nil
}

// executeHumanGate waits for human approval.
// Creates a durable ApprovalRecord in the store, then waits via both
// in-memory channel (for immediate API-driven approvals) and periodic
// store polling (for external approvals via Slack/Teams callbacks).
// Supports SLA timeout via MaxGateWaitMinutes in step config.
func (e *Engine) executeHumanGate(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult) error {
	gateKey := run.ID + ":" + step.Name

	// Parse SLA timeout from step config (default: 0 = no timeout, wait forever)
	maxWaitMinutes := 0
	if step.Config != nil {
		if v, ok := step.Config["max_wait_minutes"]; ok {
			switch val := v.(type) {
			case float64:
				maxWaitMinutes = int(val)
			case int:
				maxWaitMinutes = val
			}
		}
	}

	// Create durable approval record
	approval := &models.ApprovalRecord{
		ID:             uuid.New().String(),
		GateKey:        gateKey,
		RunID:          run.ID,
		StepName:       step.Name,
		Kitchen:        run.Kitchen,
		Status:         "pending",
		RequestedAt:    time.Now().UTC(),
		MaxWaitMinutes: maxWaitMinutes,
	}
	if err := e.store.CreateApproval(ctx, approval); err != nil {
		log.Warn().Err(err).Str("gate_key", gateKey).Msg("Failed to persist approval record, falling back to in-memory only")
	}

	// Register in-memory channel for fast signaling
	ch := make(chan bool, 1)
	e.gatesMu.Lock()
	e.gates[gateKey] = ch
	e.gatesMu.Unlock()

	defer func() {
		e.gatesMu.Lock()
		delete(e.gates, gateKey)
		e.gatesMu.Unlock()
	}()

	result.GateStatus = "waiting"

	log.Info().
		Str("run_id", run.ID).
		Str("step", step.Name).
		Int("max_wait_minutes", maxWaitMinutes).
		Msg("⏸️  Human gate — waiting for approval")

	// Dispatch gate_waiting notifications
	if len(step.NotifyTools) > 0 && e.notifier != nil {
		evt := notify.Event{
			Type:       string(notify.EventGateWaiting),
			RunID:      run.ID,
			RecipeName: run.RecipeID,
			StepName:   step.Name,
			Kitchen:    run.Kitchen,
			Timestamp:  time.Now().UTC(),
		}
		result.NotifyResults = e.notifier.DispatchAll(ctx, run.Kitchen, step.NotifyTools, evt)
	}

	// Update run status to paused
	run.Status = models.RecipeRunPaused
	e.store.UpdateRecipeRun(ctx, run)

	// Build SLA deadline context
	var gateCtx context.Context
	var gateCancel context.CancelFunc
	if maxWaitMinutes > 0 {
		gateCtx, gateCancel = context.WithTimeout(ctx, time.Duration(maxWaitMinutes)*time.Minute)
	} else {
		gateCtx, gateCancel = context.WithCancel(ctx)
	}
	defer gateCancel()

	// Poll the store every 5 seconds for external approvals (Slack/Teams callbacks etc.)
	pollTicker := time.NewTicker(5 * time.Second)
	defer pollTicker.Stop()

	for {
		select {
		case approved := <-ch:
			// Direct API approval via ApproveGate / ApproveGateWithMetadata
			return e.resolveGate(ctx, run, step, result, gateKey, approved)

		case <-pollTicker.C:
			// Check store for external approval
			record, err := e.store.GetApproval(gateCtx, gateKey)
			if err == nil && record.Status != "pending" {
				return e.resolveGate(ctx, run, step, result, gateKey, record.Status == "approved")
			}

		case <-gateCtx.Done():
			// SLA timeout or parent cancellation
			if maxWaitMinutes > 0 {
				// SLA breach — mark as timed_out
				now := time.Now().UTC()
				if record, err := e.store.GetApproval(ctx, gateKey); err == nil && record.Status == "pending" {
					record.Status = "timed_out"
					record.ResolvedAt = &now
					record.Comments = fmt.Sprintf("SLA breach: gate not resolved within %d minutes", maxWaitMinutes)
					e.store.UpdateApproval(ctx, record)
				}
				result.GateStatus = "timed_out"
				return fmt.Errorf("human gate '%s' exceeded SLA of %d minutes", step.Name, maxWaitMinutes)
			}
			return fmt.Errorf("human gate '%s' was canceled", step.Name)
		}
	}
}

// resolveGate handles the approval/rejection outcome of a human gate.
func (e *Engine) resolveGate(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, gateKey string, approved bool) error {
	if !approved {
		result.GateStatus = "rejected"
		result.Output = map[string]interface{}{"approved": false}
		return fmt.Errorf("human gate '%s' was rejected", step.Name)
	}
	result.GateStatus = "approved"
	result.Output = map[string]interface{}{"approved": true}

	// Attach approver info to result if available
	if record, err := e.store.GetApproval(ctx, gateKey); err == nil {
		result.Output = map[string]interface{}{
			"approved":       true,
			"approver_id":    record.ApproverID,
			"approver_email": record.ApproverEmail,
			"channel":        record.ApproverChannel,
			"comments":       record.Comments,
			"resolved_at":    record.ResolvedAt,
		}
	}

	// Resume run
	run.Status = models.RecipeRunRunning
	e.store.UpdateRecipeRun(ctx, run)
	return nil
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

	// Aggregate token and cost data across all steps
	var totalTokens int64
	var totalCostUSD float64
	for _, sr := range stepResults {
		totalTokens += sr.Tokens
		totalCostUSD += sr.CostUSD
	}
	run.TotalTokens = totalTokens
	run.TotalCostUSD = totalCostUSD

	if err := e.store.UpdateRecipeRun(context.Background(), run); err != nil {
		log.Error().Err(err).Str("run_id", run.ID).Msg("Failed to update completed run")
	}

	log.Info().
		Str("run_id", run.ID).
		Int64("duration_ms", run.DurationMs).
		Int("steps", len(stepResults)).
		Int64("total_tokens", totalTokens).
		Float64("total_cost_usd", totalCostUSD).
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

// evaluateBranches checks step branches against the step's output.
// Uses simple JSON-path-style matching: each branch condition is
// "output_key == value" or "output_key != value".
// Returns the next step name from the first matching branch,
// or the step's DefaultNext if no branch matches.
func (e *Engine) evaluateBranches(step *models.Step, result *models.StepResult) string {
	if result.Output == nil {
		if step.DefaultNext != "" {
			return step.DefaultNext
		}
		return ""
	}

	for _, branch := range step.Branches {
		if matchCondition(branch.Condition, result.Output) {
			log.Debug().
				Str("step", step.Name).
				Str("condition", branch.Condition).
				Str("next", branch.NextStep).
				Msg("Branch matched")
			return branch.NextStep
		}
	}

	if step.DefaultNext != "" {
		return step.DefaultNext
	}
	return ""
}

// matchCondition evaluates a simple condition against output data.
// Supports:
//   - "key == value"    (string equality)
//   - "key != value"    (string inequality)
//   - "key"             (key exists and is truthy)
//   - "status == completed"
//
// For more complex conditions, we can integrate expr-lang/expr later.
func matchCondition(condition string, output map[string]interface{}) bool {
	// Try "key == value"
	for _, op := range []string{"==", "!="} {
		parts := splitCondition(condition, op)
		if len(parts) == 2 {
			key := trimSpace(parts[0])
			expected := trimSpace(parts[1])

			actual, ok := output[key]
			if !ok {
				return op == "!="
			}

			actualStr := fmt.Sprintf("%v", actual)
			if op == "==" {
				return actualStr == expected
			}
			return actualStr != expected
		}
	}

	// Simple truthy check — key exists and is not false/nil/empty
	key := trimSpace(condition)
	val, ok := output[key]
	if !ok {
		return false
	}
	switch v := val.(type) {
	case bool:
		return v
	case string:
		return v != ""
	case nil:
		return false
	default:
		return true
	}
}

func splitCondition(s, sep string) []string {
	idx := -1
	for i := 0; i <= len(s)-len(sep); i++ {
		if s[i:i+len(sep)] == sep {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	return []string{s[:idx], s[idx+len(sep):]}
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
