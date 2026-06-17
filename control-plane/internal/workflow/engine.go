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
	"strings"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/notify"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/expr-lang/expr"
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

	// oidcIssuer is the expected OIDC issuer URL for this deployment.
	// Set via SetOIDCIssuer at startup (Pro-only). Used to validate same-tenant
	// approvals on human_gate steps that have RequireSameTenant: true.
	oidcIssuer string

	// Running executions: runID → cancel func
	runsMu sync.RWMutex
	runs   map[string]context.CancelFunc

	// Human gate approvals: runID:stepName → channel
	gatesMu sync.RWMutex
	gates   map[string]chan bool

	// Parent trace IDs: runID → parentTraceID (for linking step traces to recipe trace)
	parentTraceIDs sync.Map

	// Recipe names: runID → recipeName (for readable step trace labels)
	runRecipeNames sync.Map
}

// SetOIDCIssuer configures the expected OIDC issuer for same-tenant approver validation.
// Call this at server startup before processing any requests (Pro only).
func (e *Engine) SetOIDCIssuer(issuer string) {
	e.oidcIssuer = strings.TrimSpace(issuer)
}

type executionAPIKeyCtxKey struct{}

// WithExecutionAPIKey stores the trigger API key in context so async workflow
// execution can forward the same auth to internal gateway calls.
func WithExecutionAPIKey(ctx context.Context, apiKey string) context.Context {
	if strings.TrimSpace(apiKey) == "" {
		return ctx
	}
	return context.WithValue(ctx, executionAPIKeyCtxKey{}, apiKey)
}

func executionAPIKeyFromContext(ctx context.Context) string {
	v, _ := ctx.Value(executionAPIKeyCtxKey{}).(string)
	return strings.TrimSpace(v)
}

type triggeredByCtxKey struct{}

// WithTriggeredBy stores the caller's identity subject in context so the engine
// can record it on the RecipeRun for audit trail purposes.
func WithTriggeredBy(ctx context.Context, subject string) context.Context {
	if strings.TrimSpace(subject) == "" {
		return ctx
	}
	return context.WithValue(ctx, triggeredByCtxKey{}, subject)
}

func triggeredByFromContext(ctx context.Context) string {
	v, _ := ctx.Value(triggeredByCtxKey{}).(string)
	return strings.TrimSpace(v)
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
//
// envSlug optionally pins the run to a specific environment (e.g. "staging", "prod").
// When set, each agent step resolves the version-pinned deployment in that environment
// via GetActiveDeployment → GetAgentVersion. Pass "" to resolve the live agent (OSS default).
// If envSlug is empty, recipe.DefaultEnvironment is used as a fallback.
func (e *Engine) ExecuteRecipe(ctx context.Context, recipe *models.Recipe, kitchen string, input map[string]interface{}, envSlug string) (string, error) {
	runID := uuid.New().String()
	authAPIKey := executionAPIKeyFromContext(ctx)

	// Resolve environment: explicit > recipe default > none
	if envSlug == "" {
		envSlug = recipe.DefaultEnvironment
	}

	run := &models.RecipeRun{
		ID:          runID,
		RecipeID:    recipe.ID,
		Kitchen:     kitchen,
		Environment: envSlug,
		Status:      models.RecipeRunRunning,
		Input:       input,
		TriggeredBy: triggeredByFromContext(ctx),
		StartedAt:   time.Now().UTC(),
	}

	if err := e.store.CreateRecipeRun(ctx, run); err != nil {
		return "", fmt.Errorf("create recipe run: %w", err)
	}

	// Create cancellable context for this execution
	execBase := context.Background()
	if authAPIKey != "" {
		execBase = WithExecutionAPIKey(execBase, authAPIKey)
	}
	execCtx, cancel := context.WithCancel(execBase)
	e.runsMu.Lock()
	e.runs[runID] = cancel
	e.runsMu.Unlock()

	log.Info().
		Str("run_id", runID).
		Str("recipe", recipe.Name).
		Str("environment", envSlug).
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

// ErrApproverUnauthorized is returned by ApproveGateWithMetadata when the
// caller does not satisfy the approver constraints defined on the Step.
var ErrApproverUnauthorized = fmt.Errorf("approver is not authorised for this gate")

// ApproveGateWithMetadata approves or rejects a gate with approver identity and comments.
// approverIssuer is the OIDC issuer URL from the caller's identity token (empty if not OIDC).
// Returns (false, ErrApproverUnauthorized) when the caller fails the step's approver
// constraints. Returns (false, nil) when the gate doesn't exist or is no longer pending.
func (e *Engine) ApproveGateWithMetadata(runID, stepName string, approved bool, approverID, approverEmail, approverIssuer, channel, comments string) (bool, error) {
	gateKey := runID + ":" + stepName
	ctx := context.Background()

	record, err := e.store.GetApproval(ctx, gateKey)
	if err != nil || record.Status != "pending" {
		return false, nil
	}

	// Enforce approver constraints declared in the recipe step definition.
	if authErr := e.checkApproverConstraints(ctx, record, approverID, approverEmail, approverIssuer); authErr != nil {
		return false, authErr
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

	if updateErr := e.store.UpdateApproval(ctx, record); updateErr != nil {
		log.Error().Err(updateErr).Str("gate_key", gateKey).Msg("Failed to update approval record")
		return false, nil
	}

	// Signal the waiting goroutine
	e.gatesMu.RLock()
	ch, ok := e.gates[gateKey]
	e.gatesMu.RUnlock()
	if ok {
		ch <- approved
	}
	return true, nil
}

// checkApproverConstraints validates approverID/approverEmail against the
// approver constraints declared on the recipe Step.  Returns nil when no
// constraints are defined (open gate) or when the approver satisfies them.
// approverIssuer is the OIDC issuer from the caller's token (empty for API-key callers).
func (e *Engine) checkApproverConstraints(ctx context.Context, record *models.ApprovalRecord, approverID, approverEmail, approverIssuer string) error {
	// Load the recipe run to find the kitchen and recipe name.
	run, err := e.store.GetRecipeRun(ctx, record.RunID)
	if err != nil {
		// Can't load run — don't block approval, just log.
		log.Warn().Err(err).Str("run_id", record.RunID).Msg("checkApproverConstraints: could not load run, skipping constraint check")
		return nil
	}

	// Load the recipe to find the step definition.
	recipe, err := e.store.GetRecipe(ctx, run.Kitchen, run.RecipeID)
	if err != nil {
		log.Warn().Err(err).Str("recipe", run.RecipeID).Msg("checkApproverConstraints: could not load recipe, skipping constraint check")
		return nil
	}

	// Find the matching step.
	var step *models.Step
	for i := range recipe.Steps {
		if recipe.Steps[i].Name == record.StepName {
			step = &recipe.Steps[i]
			break
		}
	}
	if step == nil {
		// Step not found in recipe (shouldn't happen, but don't block).
		return nil
	}

	// No constraints declared — gate is open to any authenticated caller.
	hasConstraints := len(step.ApproverEmails) > 0 ||
		len(step.ApproverRoles) > 0 ||
		step.ApproverDomain != "" ||
		step.RequireSameTenant

	if !hasConstraints {
		return nil
	}

	email := strings.ToLower(strings.TrimSpace(approverEmail))

	// 1. Explicit email allow-list wins outright (even cross-tenant).
	if len(step.ApproverEmails) > 0 {
		for _, allowed := range step.ApproverEmails {
			if strings.ToLower(strings.TrimSpace(allowed)) == email {
				return nil
			}
		}
		// Caller is not in the explicit allow-list. If no other constraint can
		// grant access, deny immediately (allow-list is exclusive when it is the
		// only declared constraint).
		if step.ApproverDomain == "" && !step.RequireSameTenant {
			return fmt.Errorf("%w: email %q is not in the approver allow-list", ErrApproverUnauthorized, approverEmail)
		}
	}

	// 2. Domain check — approver's email must end with the required domain.
	if step.ApproverDomain != "" {
		domain := strings.ToLower(strings.TrimSpace(step.ApproverDomain))
		suffix := "@" + domain
		if !strings.HasSuffix(email, suffix) {
			return fmt.Errorf("%w: email %q does not belong to domain %q", ErrApproverUnauthorized, approverEmail, step.ApproverDomain)
		}
	}

	// 3. RequireSameTenant — the approver's OIDC issuer must match the
	//    configured server OIDC issuer (set via SetOIDCIssuer at startup).
	//    If the engine has no issuer configured (OSS default), we only check
	//    that the caller is authenticated (has a non-empty identity).
	if step.RequireSameTenant {
		if approverID == "" && email == "" {
			return fmt.Errorf("%w: unauthenticated caller on a same-tenant gate", ErrApproverUnauthorized)
		}
		if e.oidcIssuer != "" && approverIssuer != "" {
			if strings.TrimSpace(approverIssuer) != e.oidcIssuer {
				// Allow if the approver is explicitly listed by email (cross-tenant exception).
				explicitlyAllowed := false
				for _, allowed := range step.ApproverEmails {
					if strings.ToLower(strings.TrimSpace(allowed)) == email {
						explicitlyAllowed = true
						break
					}
				}
				if !explicitlyAllowed {
					return fmt.Errorf("%w: approver issuer %q does not match expected tenant issuer", ErrApproverUnauthorized, approverIssuer)
				}
			}
		}
	}

	// 4. Role constraints are enforced by the Pro RBAC layer; OSS has no roles.
	//    If only role constraints are set and we reach here, allow through
	//    (the Pro handler will have already applied RBAC middleware).
	_ = approverID // used by Pro layer
	return nil
}

// ── DAG Execution ───────────────────────────────────────────

func (e *Engine) executeAsync(ctx context.Context, run *models.RecipeRun, recipe *models.Recipe) {
	defer func() {
		e.runsMu.Lock()
		delete(e.runs, run.ID)
		e.runsMu.Unlock()
	}()

	// Create a parent trace for the entire recipe run
	recipeStart := time.Now().UTC()
	parentTraceID := uuid.New().String()
	parentTrace := &models.Trace{
		ID:         parentTraceID,
		AgentName:  recipe.Name,
		RecipeName: recipe.Name,
		Kitchen:    run.Kitchen,
		Status:     "running",
		Metadata: map[string]interface{}{
			"run_id": run.ID,
			"type":   "recipe",
			"steps":  len(recipe.Steps),
		},
		CreatedAt: recipeStart,
	}
	if err := e.store.CreateTrace(ctx, parentTrace); err != nil {
		log.Error().Err(err).Str("run_id", run.ID).Msg("Failed to create parent recipe trace")
	}

	// Store parentTraceID for linking step traces to the recipe trace
	e.parentTraceIDs.Store(run.ID, parentTraceID)
	e.runRecipeNames.Store(run.ID, recipe.Name)

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

	// Build reverse dependency map: step → steps that depend on it
	dependents := make(map[string][]string) // parent → list of children
	for _, step := range steps {
		for _, dep := range step.DependsOn {
			dependents[dep] = append(dependents[dep], step.Name)
		}
	}

	// Track completed steps and their outputs
	completed := make(map[string]*models.StepResult)
	skipped := make(map[string]bool) // R8: steps skipped by routing
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

		// Find steps that are ready to run (all deps satisfied, not yet completed, not skipped)
		var ready []*models.Step
		var blockedByFailedDeps []string
		completedMu.Lock()
		for _, step := range steps {
			if _, done := completed[step.Name]; done {
				continue
			}
			if skipped[step.Name] {
				continue
			}
			allDepsMet := true
			blockedByFailure := false
			for _, dep := range step.DependsOn {
				sr, ok := completed[dep]
				if !ok {
					allDepsMet = false
					break
				}
				if sr.Status == "failed" || sr.Status == "canceled" {
					allDepsMet = false
					blockedByFailure = true
					break
				}
			}
			if allDepsMet {
				s := step // copy
				ready = append(ready, &s)
			} else if blockedByFailure {
				blockedByFailedDeps = append(blockedByFailedDeps, step.Name)
			}
		}
		completedMu.Unlock()

		if len(ready) == 0 {
			if len(blockedByFailedDeps) > 0 {
				e.failRun(run, stepResults, fmt.Sprintf("blocked by failed dependency: %s", strings.Join(blockedByFailedDeps, ", ")))
				return
			}
			// Check if all steps are done (completed + skipped)
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
				run.StepResults = append([]models.StepResult(nil), stepResults...)

				// R8: Routing skip-set propagation.
				// If this step has branches and BranchTaken is set, skip
				// all dependent steps that are NOT the taken branch.
				if result.BranchTaken != "" && (s.Kind == models.StepRouter || len(s.Branches) > 0) {
					children := dependents[s.Name]
					for _, child := range children {
						if child != result.BranchTaken {
							e.skipStepTransitive(child, skipped, completed, dependents, stepMap, &stepResults)
						}
					}
				}
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

	// Fail the run if any step ended in a failure state (e.g. rejected gate,
	// agent error with no downstream dependent to trigger blockedByFailedDeps).
	for _, sr := range stepResults {
		if sr.Status == "failed" {
			e.failRun(run, stepResults, fmt.Sprintf("step '%s' failed: %s", sr.StepName, sr.Error))
			return
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

// skipStepTransitive marks a step as skipped and propagates the skip to
// downstream steps that depend ONLY on skipped/completed steps.
// Must be called with completedMu held.
func (e *Engine) skipStepTransitive(name string, skipped map[string]bool, completed map[string]*models.StepResult, dependents map[string][]string, stepMap map[string]*models.Step, stepResults *[]models.StepResult) {
	if _, done := completed[name]; done {
		return // already completed, nothing to skip
	}
	if skipped[name] {
		return // already skipped
	}

	skipped[name] = true

	// Create a skipped StepResult so downstream steps see this as "done"
	sr := &models.StepResult{
		StepName:   name,
		Status:     "skipped",
		StartedAt:  time.Now(),
		DurationMs: 0,
	}
	if s, ok := stepMap[name]; ok {
		sr.StepKind = string(s.Kind)
		sr.AgentRef = s.AgentRef
	}
	completed[name] = sr
	*stepResults = append(*stepResults, *sr)

	log.Info().Str("step", name).Msg("⏭️  Step skipped (branch not taken)")

	// Propagate: skip children whose ALL deps are now complete/skipped
	// Only skip children that have no non-skipped, non-completed dependencies
	for _, child := range dependents[name] {
		if _, done := completed[child]; done {
			continue
		}
		childStep, ok := stepMap[child]
		if !ok {
			continue
		}
		// Check if ALL parents of this child are either skipped or completed
		allParentsResolved := true
		allParentsSkipped := true
		for _, dep := range childStep.DependsOn {
			if _, ok := completed[dep]; !ok {
				allParentsResolved = false
				break
			}
			if !skipped[dep] {
				allParentsSkipped = false
			}
		}
		// Only skip if all parents are resolved AND at least one is skipped
		// (if a step has a non-skipped completed parent, it should still run)
		if allParentsResolved && allParentsSkipped {
			e.skipStepTransitive(child, skipped, completed, dependents, stepMap, stepResults)
		}
	}
}

// executeStep runs a single step with retry support and optional looping.
func (e *Engine) executeStep(ctx context.Context, run *models.RecipeRun, step *models.Step, completed map[string]*models.StepResult, mu *sync.Mutex) *models.StepResult {
	start := time.Now()

	result := &models.StepResult{
		StepName:  step.Name,
		StepKind:  string(step.Kind),
		AgentRef:  step.AgentRef,
		StartedAt: start,
	}

	maxRetries := step.MaxRetries

	// R8: Outer loop — repeat while LoopCondition is true (max MaxIterations).
	// Inner loop — retry on error (max MaxRetries).
	maxIter := 1
	if step.LoopCondition != "" && step.MaxIterations > 0 {
		maxIter = step.MaxIterations
	}

	for iteration := 0; iteration < maxIter; iteration++ {
		var lastErr error
		succeeded := false

		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
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
				succeeded = true
				break
			}
			lastErr = err
		}

		if !succeeded {
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

		// Step succeeded — check loop condition
		if step.LoopCondition != "" && step.MaxIterations > 0 {
			result.LoopIterations = iteration + 1
			if result.Output != nil {
				historyCopy := make(map[string]interface{})
				for k, v := range result.Output {
					historyCopy[k] = v
				}
				result.LoopHistory = append(result.LoopHistory, historyCopy)
			}

			shouldContinue, evalErr := evalExprBool(step.LoopCondition, result.Output)
			if evalErr != nil {
				log.Warn().Err(evalErr).Str("step", step.Name).Msg("Loop condition evaluation error, exiting loop")
				break
			}
			if !shouldContinue {
				log.Info().Str("step", step.Name).Int("iterations", iteration+1).Msg("🔄 Loop condition false, exiting loop")
				break
			}
			log.Info().Str("step", step.Name).Int("iteration", iteration+1).Msg("🔄 Loop condition true, continuing iteration")
			continue
		}

		// No loop — we're done
		break
	}

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
		Int("loop_iterations", result.LoopIterations).
		Msg("✅ Step completed")
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

	case models.StepRouter:
		return e.executeRouter(ctx, run, step, result, completed, mu)

	case models.StepMap:
		return e.executeMap(ctx, run, step, result, completed, mu)

	case models.StepSubRecipe:
		return e.executeSubRecipe(ctx, run, step, result, completed, mu)

	default:
		return fmt.Errorf("unknown step kind: %s", step.Kind)
	}
}

// executeAgentStep calls an agent via the A2A protocol.
func (e *Engine) executeAgentStep(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	// Extract parent trace ID for linking step traces to the recipe trace
	var parentTID string
	if v, ok := e.parentTraceIDs.Load(run.ID); ok {
		parentTID, _ = v.(string)
	}
	recipeName := e.recipeNameForRun(run)

	agentRef := step.AgentRef
	if agentRef == "" {
		return fmt.Errorf("agent step '%s' has no agent_ref", step.Name)
	}

	// Get the agent to find its A2A endpoint.
	// When run.Environment is set (Pro: env-targeted run), attempt to resolve the
	// version-pinned agent snapshot from the active deployment in that environment.
	// Falls back to the live agent if no active deployment exists (graceful degradation).
	kitchen := run.Kitchen
	agent, err := e.store.GetAgent(ctx, kitchen, agentRef)
	if err != nil {
		return fmt.Errorf("agent lookup failed: %w", err)
	}

	if run.Environment != "" {
		dep, depErr := e.store.GetActiveDeployment(ctx, kitchen, agentRef, run.Environment)
		if depErr == nil && dep != nil && dep.Version != "" {
			if pinned, verErr := e.store.GetAgentVersion(ctx, kitchen, agentRef, dep.Version); verErr == nil {
				agent = pinned
				log.Debug().
					Str("agent", agentRef).
					Str("environment", run.Environment).
					Str("version", dep.Version).
					Msg("engine: resolved version-pinned agent for environment")
			}
		}
	}

	switch agent.Status {
	case models.AgentStatusReady:
		// ok
	case models.AgentStatusDraft:
		return fmt.Errorf("agent '%s' has never been baked — bake it before using it in a recipe", agentRef)
	case models.AgentStatusBaking:
		return fmt.Errorf("agent '%s' is currently baking — wait for it to reach ready status", agentRef)
	case models.AgentStatusBurnt:
		return fmt.Errorf("agent '%s' is burnt (last bake failed) — check provider config and re-bake", agentRef)
	default:
		return fmt.Errorf("agent '%s' is not ready (status: %s) — bake it before using it in a recipe", agentRef, agent.Status)
	}

	// Build input from previous step outputs.
	// Extract artifact text from each dependency's A2A result so the LLM
	// receives clean natural-language output, not a raw JSON-RPC envelope.
	depOutputs := make(map[string]string) // dep name → extracted text
	mu.Lock()
	for _, dep := range step.DependsOn {
		if sr, ok := completed[dep]; ok && sr.Output != nil {
			depOutputs[dep] = extractTextFromA2AOutput(sr.Output)
		}
	}
	mu.Unlock()

	// Compose the message sent to the agent.
	var msgParts []string
	if run.Input != nil {
		inputBytes, _ := json.Marshal(run.Input)
		msgParts = append(msgParts, "Recipe input: "+string(inputBytes))
	}
	for _, dep := range step.DependsOn {
		if txt, ok := depOutputs[dep]; ok && txt != "" {
			msgParts = append(msgParts, fmt.Sprintf("Output from '%s': %s", dep, txt))
		}
	}
	userMsg := strings.Join(msgParts, "\n\n")
	if userMsg == "" {
		userMsg = "Execute step: " + step.Name
	}

	// Build A2A params, including provider config if the agent has a model provider
	a2aParams := map[string]interface{}{
		"id": uuid.New().String(),
		"message": map[string]interface{}{
			"role": "user",
			"parts": []map[string]interface{}{
				{"type": "text", "text": userMsg},
			},
		},
	}

	// If agent has a ModelProvider, fetch it and include TLS config for the agent to use
	if agent.ModelProvider != "" {
		provider, err := e.store.GetProvider(ctx, agent.ModelProvider)
		if err != nil {
			log.Warn().
				Err(err).
				Str("run_id", run.ID).
				Str("step", step.Name).
				Str("agent", agentRef).
				Str("provider", agent.ModelProvider).
				Msg("Failed to load model provider for A2A TLS forwarding")
		}
		if err == nil && provider != nil {
			providerConfig := map[string]interface{}{
				"name": provider.Name,
				"kind": provider.Kind,
			}
			if provider.CABundle != "" {
				providerConfig["ca_bundle"] = provider.CABundle
			}
			if provider.TLSSkipVerify {
				providerConfig["tls_skip_verify"] = true
			}
			a2aParams["provider_config"] = providerConfig
			log.Info().
				Str("run_id", run.ID).
				Str("step", step.Name).
				Str("agent", agentRef).
				Str("provider", provider.Name).
				Bool("has_ca_bundle", provider.CABundle != "").
				Int("ca_bundle_len", len(provider.CABundle)).
				Bool("tls_skip_verify", provider.TLSSkipVerify).
				Msg("Forwarding provider TLS config in A2A request")
		}
	}

	// Send A2A task via JSON-RPC
	a2aReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tasks/send",
		"params":  a2aParams,
		"id":      uuid.New().String(),
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

	// Resolve the effective auth key for this step using a priority chain:
	//   1. step.AuthKey      — literal Bearer token set directly on the step
	//   2. step.AuthKeyRef   — name of a KitchenCredential looked up at runtime
	//   3. trigger context   — the API key used to call /bake (default behaviour)
	effectiveKey := strings.TrimSpace(step.AuthKey)
	if effectiveKey == "" && strings.TrimSpace(step.AuthKeyRef) != "" {
		if cred, credErr := e.store.GetKitchenCredential(ctx, kitchen, strings.TrimSpace(step.AuthKeyRef)); credErr == nil {
			effectiveKey = cred.Value
		} else {
			log.Warn().Str("ref", step.AuthKeyRef).Str("step", step.Name).Msg("engine: auth_key_ref not found, falling back to trigger key")
		}
	}
	if effectiveKey == "" {
		effectiveKey = executionAPIKeyFromContext(ctx)
	}
	if effectiveKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+effectiveKey)
	}
	if run.Environment != "" {
		// Forward the environment context so the CP gateway and target agent
		// can observe which environment triggered this invocation.
		httpReq.Header.Set("X-AO-Environment", run.Environment)
	}

	resp, err := e.client.Do(httpReq)
	if err != nil {
		e.createStepTrace(ctx, kitchen, agentRef, recipeName, run.ID, step.Name, "error", stepStart, 0, 0, "", err.Error(), parentTID)
		return fmt.Errorf("A2A request failed: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		// Provide actionable context: HTTP status + body snippet instead of raw EOF.
		var hint string
		switch {
		case resp.StatusCode == http.StatusNotFound:
			hint = fmt.Sprintf("agent '%s' A2A endpoint not found (HTTP 404) — the agent pod may not be running; try re-baking", agentRef)
		case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusBadGateway:
			hint = fmt.Sprintf("agent '%s' is unreachable (HTTP %d) — pod may be starting or crash-looping", agentRef, resp.StatusCode)
		case resp.StatusCode >= 500:
			hint = fmt.Sprintf("agent '%s' returned HTTP %d — check agent pod logs", agentRef, resp.StatusCode)
		default:
			hint = fmt.Sprintf("agent '%s' returned an empty or invalid response (HTTP %d)", agentRef, resp.StatusCode)
		}
		e.createStepTrace(ctx, kitchen, agentRef, recipeName, run.ID, step.Name, "error", stepStart, 0, 0, "", hint, parentTID)
		return fmt.Errorf("%s", hint)
	}

	// Check for RPC error
	if rpcErr, ok := rpcResp["error"].(map[string]interface{}); ok {
		errMsg := fmt.Sprintf("%v", rpcErr["message"])
		e.createStepTrace(ctx, kitchen, agentRef, recipeName, run.ID, step.Name, "error", stepStart, 0, 0, "", errMsg, parentTID)
		return fmt.Errorf("A2A error: %v", rpcErr["message"])
	}

	// Check for task-level failure in an otherwise valid JSON-RPC response.
	if rpcResult, ok := rpcResp["result"].(map[string]interface{}); ok {
		if statusMap, ok := rpcResult["status"].(map[string]interface{}); ok {
			state, _ := statusMap["state"].(string)
			if state == "failed" || state == "error" || state == "canceled" || state == "rejected" {
				errMsg := "A2A task returned failed state"
				if msg, ok := statusMap["message"].(map[string]interface{}); ok {
					if parts, ok := msg["parts"].([]interface{}); ok {
						for _, part := range parts {
							if partMap, ok := part.(map[string]interface{}); ok {
								if txt, ok := partMap["text"].(string); ok && strings.TrimSpace(txt) != "" {
									errMsg = txt
									break
								}
							}
						}
					}
				}

				result.Output = rpcResp
				e.createStepTrace(ctx, kitchen, agentRef, recipeName, run.ID, step.Name, "error", stepStart, 0, 0, "", errMsg, parentTID)
				return fmt.Errorf("A2A task failed (%s): %s", state, errMsg)
			}
		}
	}

	// Extract output text and usage data from the A2A response.
	// If the task was accepted asynchronously (state == "submitted" or "working"),
	// poll tasks/get until it reaches a terminal state (completed/failed/canceled).
	rpcResp = e.pollA2ATaskToCompletion(ctx, endpoint, rpcResp, httpReq, effectiveKey)

	outputText, tokens, costUSD := extractA2AMetrics(rpcResp)

	// Populate cost/token data on the step result
	result.Tokens = tokens
	result.CostUSD = costUSD

	// Create a trace record for this step
	stepTraceID := e.createStepTrace(ctx, kitchen, agentRef, recipeName, run.ID, step.Name, "completed", stepStart, tokens, costUSD, outputText, "", parentTID)

	// Persist executor spans from the pod's exec_trace so the waterfall
	// visualisation is populated in the pro Postgres store.
	if stepTraceID != "" {
		e.persistA2ASpans(ctx, rpcResp, stepTraceID)
	}

	result.Output = rpcResp
	return nil
}

// createStepTrace persists a Trace record for a single agent/RAG step execution.
// If parentTraceID is set, it links this step trace to a parent recipe trace.
// Returns the new trace ID so callers can attach spans to it.
func (e *Engine) createStepTrace(ctx context.Context, kitchen, agentName, recipeName, runID, stepName, status string, start time.Time, tokens int64, costUSD float64, outputText, errMsg string, parentTraceID ...string) string {
	durationMs := time.Since(start).Milliseconds()
	traceID := uuid.New().String()
	trace := &models.Trace{
		ID:          traceID,
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
	if len(parentTraceID) > 0 && parentTraceID[0] != "" {
		trace.ParentTraceID = parentTraceID[0]
	}
	if errMsg != "" {
		trace.Metadata["error"] = errMsg
	}
	if err := e.store.CreateTrace(ctx, trace); err != nil {
		log.Error().Err(err).Str("agent", agentName).Str("step", stepName).Msg("Failed to persist trace")
		return ""
	}
	return traceID
}

// persistA2ASpans extracts the exec_trace embedded in an A2A JSON-RPC response
// by the pod's handleA2ATaskSend and persists the span waterfall to the store.
// The exec_trace is nested at result.metadata.exec_trace.
func (e *Engine) persistA2ASpans(ctx context.Context, rpcResp map[string]interface{}, traceID string) {
	result, ok := rpcResp["result"].(map[string]interface{})
	if !ok {
		return
	}
	meta, ok := result["metadata"].(map[string]interface{})
	if !ok {
		return
	}
	rawTrace, ok := meta["exec_trace"]
	if !ok {
		return
	}
	traceBytes, err := json.Marshal(rawTrace)
	if err != nil {
		return
	}
	var execTrace executor.ExecutionTrace
	if err := json.Unmarshal(traceBytes, &execTrace); err != nil || len(execTrace.Turns) == 0 {
		return
	}

	// Override the trace ID so spans link to the step trace in Postgres
	// (the pod's exec_trace carries the pod-local task ID, not the pro trace ID).
	execTrace.TraceID = traceID

	now := time.Now().UTC()
	execStart := now.Add(-time.Duration(execTrace.TotalMs) * time.Millisecond)
	rootSpanID := uuid.New().String()
	agentUsage := execTrace.Usage

	inputJSON, _ := json.Marshal(map[string]interface{}{
		"agent":   execTrace.AgentName,
		"kitchen": execTrace.Kitchen,
	})
	outputJSON, _ := json.Marshal(map[string]interface{}{
		"turns":        len(execTrace.Turns),
		"total_ms":     execTrace.TotalMs,
		"total_tokens": execTrace.Usage.TotalTokens,
	})

	spans := []models.Span{
		{
			ID:         rootSpanID,
			TraceID:    traceID,
			Name:       execTrace.AgentName,
			Kind:       models.SpanKindAgent,
			Status:     "completed",
			StartTime:  execStart,
			EndTime:    now,
			DurationMs: execTrace.TotalMs,
			Input:      inputJSON,
			Output:     outputJSON,
			Usage:      &agentUsage,
		},
	}

	turnOffset := execStart
	for _, turn := range execTrace.Turns {
		turnStart := turnOffset
		turnEnd := turnStart.Add(time.Duration(turn.LatencyMs) * time.Millisecond)
		turnOffset = turnEnd

		llmSpanID := uuid.New().String()
		turnUsage := turn.Usage
		reqJSON, _ := json.Marshal(turn.Request)
		respJSON, _ := json.Marshal(map[string]interface{}{
			"content":    turn.Response,
			"tool_calls": turn.ToolCalls,
		})
		spans = append(spans, models.Span{
			ID:           llmSpanID,
			TraceID:      traceID,
			ParentSpanID: rootSpanID,
			Name:         fmt.Sprintf("llm.turn_%d", turn.Number),
			Kind:         models.SpanKindLLM,
			Status:       "completed",
			StartTime:    turnStart,
			EndTime:      turnEnd,
			DurationMs:   turn.LatencyMs,
			Input:        reqJSON,
			Output:       respJSON,
			Usage:        &turnUsage,
			Metadata:     map[string]interface{}{"turn_number": turn.Number},
		})

		if len(turn.ToolCalls) > 0 {
			toolDuration := turn.LatencyMs / int64(len(turn.ToolCalls)+1)
			toolStart := turnStart.Add(time.Duration(toolDuration) * time.Millisecond)
			for i, tc := range turn.ToolCalls {
				toolEnd := toolStart.Add(time.Duration(toolDuration) * time.Millisecond)
				tcInputJSON, _ := json.Marshal(tc.Arguments)
				var tcOutputJSON json.RawMessage
				toolStatus := "completed"
				toolError := ""
				if i < len(turn.ToolResults) {
					tr := turn.ToolResults[i]
					tcOutputJSON, _ = json.Marshal(map[string]interface{}{"content": tr.Content, "is_error": tr.IsError})
					if tr.IsError {
						toolStatus = "failed"
						toolError = tr.Content
					}
				}
				spans = append(spans, models.Span{
					ID:           uuid.New().String(),
					TraceID:      traceID,
					ParentSpanID: llmSpanID,
					Name:         tc.Name,
					Kind:         models.SpanKindTool,
					Status:       toolStatus,
					StartTime:    toolStart,
					EndTime:      toolEnd,
					DurationMs:   toolDuration,
					Input:        tcInputJSON,
					Output:       tcOutputJSON,
					Error:        toolError,
					Metadata:     map[string]interface{}{"tool_call_id": tc.ID},
				})
				toolStart = toolEnd
			}
		}
	}

	if err := e.store.CreateSpans(ctx, spans); err != nil {
		log.Warn().Err(err).Str("trace_id", traceID).Int("span_count", len(spans)).Msg("engine: failed to persist A2A spans")
	}
}

// extractTextFromA2AOutput extracts the plain text from a step's stored Output map,
// which is an A2A JSON-RPC response envelope. Used to pipe one step's output as
// the next step's input in a clean, LLM-readable form.
func extractTextFromA2AOutput(output map[string]interface{}) string {
	txt, _, _ := extractA2AMetrics(output)
	return txt
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

// pollA2ATaskToCompletion polls a tasks/get A2A endpoint until the task reaches a
// terminal state (completed, failed, canceled, rejected). If the initial response
// already contains a terminal state it is returned immediately.
// Returns the final RPC response (which may have artifacts + usage populated).
func (e *Engine) pollA2ATaskToCompletion(ctx context.Context, endpoint string, initial map[string]interface{}, _ *http.Request, authKey string) map[string]interface{} {
	taskID := extractA2ATaskID(initial)
	if taskID == "" {
		return initial
	}

	// Check if already in a terminal state.
	if state := extractA2AState(initial); state == "completed" || state == "failed" || state == "canceled" || state == "rejected" {
		return initial
	}

	// Poll with exponential backoff capped at 5s, total timeout 90s.
	const maxPollDuration = 90 * time.Second
	deadline := time.Now().Add(maxPollDuration)
	delay := 500 * time.Millisecond

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return initial
		case <-time.After(delay):
		}
		if delay < 5*time.Second {
			delay = delay * 2
		}

		getReq := map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "tasks/get",
			"params":  map[string]interface{}{"id": taskID},
			"id":      uuid.New().String(),
		}
		body, _ := json.Marshal(getReq)
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if authKey != "" {
			req.Header.Set("Authorization", "Bearer "+authKey)
		}

		resp, err := e.client.Do(req)
		if err != nil {
			continue
		}
		var pollResp map[string]interface{}
		if decErr := json.NewDecoder(resp.Body).Decode(&pollResp); decErr != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		state := extractA2AState(pollResp)
		if state == "completed" || state == "failed" || state == "canceled" || state == "rejected" {
			return pollResp
		}
	}

	// Timed out — return best effort (likely still working).
	return initial
}

// extractA2ATaskID extracts the task ID from an A2A JSON-RPC response.
func extractA2ATaskID(rpcResp map[string]interface{}) string {
	result, ok := rpcResp["result"].(map[string]interface{})
	if !ok {
		return ""
	}
	id, _ := result["id"].(string)
	return id
}

// extractA2AState extracts the task state string from an A2A JSON-RPC response.
func extractA2AState(rpcResp map[string]interface{}) string {
	result, ok := rpcResp["result"].(map[string]interface{})
	if !ok {
		return ""
	}
	statusMap, ok := result["status"].(map[string]interface{})
	if !ok {
		return ""
	}
	state, _ := statusMap["state"].(string)
	return state
}

// executeRAGStep calls the control plane's RAG query endpoint.
// The step config should contain "strategy", "top_k", "namespace", and
// optionally "question_from" (the name of a previous step whose output
// contains the query text). If "question_from" is not set, the recipe
// input is serialised as the question.
func (e *Engine) executeRAGStep(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	// Extract parent trace ID for linking step traces to the recipe trace
	var parentTID string
	if v, ok := e.parentTraceIDs.Load(run.ID); ok {
		parentTID, _ = v.(string)
	}
	recipeName := e.recipeNameForRun(run)

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
		e.createStepTrace(ctx, run.Kitchen, "rag-pipeline", recipeName, run.ID, step.Name, "error", stepStart, 0, 0, "", err.Error(), parentTID)
		return fmt.Errorf("RAG request failed: %w", err)
	}
	defer resp.Body.Close()

	var ragResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&ragResp); err != nil {
		e.createStepTrace(ctx, run.Kitchen, "rag-pipeline", recipeName, run.ID, step.Name, "error", stepStart, 0, 0, "", err.Error(), parentTID)
		return fmt.Errorf("decode RAG response: %w", err)
	}

	if resp.StatusCode >= 400 {
		errMsg, _ := ragResp["error"].(string)
		e.createStepTrace(ctx, run.Kitchen, "rag-pipeline", recipeName, run.ID, step.Name, "error", stepStart, 0, 0, "", errMsg, parentTID)
		return fmt.Errorf("RAG query failed (status %d): %s", resp.StatusCode, errMsg)
	}

	// Extract answer text from RAG response
	answerText, _ := ragResp["answer"].(string)
	e.createStepTrace(ctx, run.Kitchen, "rag-pipeline", recipeName, run.ID, step.Name, "completed", stepStart, 0, 0, answerText, "", parentTID)

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

	// Parse SLA timeout from step config (default: 0 = no timeout, wait forever).
	// Stored as float64 to support sub-minute values (useful in tests; e.g. 0.1 = 6 seconds).
	var maxWaitMinutes float64
	if step.Config != nil {
		if v, ok := step.Config["max_wait_minutes"]; ok {
			switch val := v.(type) {
			case float64:
				maxWaitMinutes = val
			case int:
				maxWaitMinutes = float64(val)
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
		MaxWaitMinutes: int(maxWaitMinutes),
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
		Float64("max_wait_minutes", maxWaitMinutes).
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
	run.StepResults = append(append([]models.StepResult(nil), run.StepResults...), *result)
	run.Status = models.RecipeRunPaused
	e.store.UpdateRecipeRun(ctx, run)

	// Build SLA deadline context
	var gateCtx context.Context
	var gateCancel context.CancelFunc
	if maxWaitMinutes > 0 {
		gateCtx, gateCancel = context.WithTimeout(ctx, time.Duration(float64(time.Minute)*maxWaitMinutes))
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
					record.Comments = fmt.Sprintf("SLA breach: gate not resolved within %.2f minutes", maxWaitMinutes)
					e.store.UpdateApproval(ctx, record)
				}
				result.GateStatus = "timed_out"
				return fmt.Errorf("human gate '%s' exceeded SLA of %.2f minutes", step.Name, maxWaitMinutes)
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

// executeCondition evaluates an expression and records which branch to take.
func (e *Engine) executeCondition(ctx context.Context, step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	if step.Config == nil {
		return fmt.Errorf("condition step '%s' has no config", step.Name)
	}

	expression, _ := step.Config["expression"].(string)

	// Build environment from dependency outputs
	env := make(map[string]interface{})
	mu.Lock()
	for _, dep := range step.DependsOn {
		if sr, ok := completed[dep]; ok && sr.Output != nil {
			env[dep] = sr.Output
			// Also flatten top-level keys for convenience
			for k, v := range sr.Output {
				env[k] = v
			}
		}
	}
	mu.Unlock()

	conditionMet := false
	if expression != "" {
		val, err := evalExprBool(expression, env)
		if err != nil {
			log.Warn().Err(err).Str("step", step.Name).Str("expression", expression).Msg("Condition expression evaluation error")
		} else {
			conditionMet = val
		}
	} else {
		// Fallback: check if any dep completed successfully
		mu.Lock()
		for _, dep := range step.DependsOn {
			if sr, ok := completed[dep]; ok && sr.Status == "completed" {
				conditionMet = true
				break
			}
		}
		mu.Unlock()
	}

	branch := "true"
	if !conditionMet {
		branch = "false"
	}

	result.Output = map[string]interface{}{
		"condition_met": conditionMet,
		"branch":        branch,
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

	// Finalize the parent recipe trace
	e.finalizeParentTrace(run, "completed", totalTokens, totalCostUSD, "")

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

	// Aggregate whatever tokens were used before failure
	var totalTokens int64
	var totalCostUSD float64
	for _, sr := range stepResults {
		totalTokens += sr.Tokens
		totalCostUSD += sr.CostUSD
	}

	// Finalize the parent recipe trace with error status
	e.finalizeParentTrace(run, "error", totalTokens, totalCostUSD, errMsg)

	log.Error().
		Str("run_id", run.ID).
		Str("error", errMsg).
		Msg("💥 Recipe execution failed")
}

// finalizeParentTrace updates the parent recipe trace with final status, duration, and totals.
func (e *Engine) finalizeParentTrace(run *models.RecipeRun, status string, totalTokens int64, totalCostUSD float64, errMsg string) {
	e.runRecipeNames.Delete(run.ID)

	v, ok := e.parentTraceIDs.LoadAndDelete(run.ID)
	if !ok {
		return
	}
	parentTID, _ := v.(string)
	if parentTID == "" {
		return
	}

	ctx := context.Background()
	trace, err := e.store.GetTrace(ctx, parentTID)
	if err != nil {
		log.Error().Err(err).Str("trace_id", parentTID).Msg("Failed to get parent recipe trace for finalization")
		return
	}

	trace.Status = status
	trace.DurationMs = run.DurationMs
	trace.TotalTokens = totalTokens
	trace.CostUSD = totalCostUSD
	trace.Usage = &models.TokenUsage{
		TotalTokens: totalTokens,
	}
	if errMsg != "" {
		trace.Metadata["error"] = errMsg
	}

	if err := e.store.UpdateTrace(ctx, trace); err != nil {
		log.Error().Err(err).Str("trace_id", parentTID).Msg("Failed to finalize parent recipe trace")
	}
}

func (e *Engine) recipeNameForRun(run *models.RecipeRun) string {
	if v, ok := e.runRecipeNames.Load(run.ID); ok {
		if name, ok := v.(string); ok && strings.TrimSpace(name) != "" {
			return name
		}
	}
	return run.RecipeID
}

// GetPendingGates returns the list of pending human gates for a run.
func (e *Engine) GetPendingGates(runID string) []string {
	run, err := e.store.GetRecipeRun(context.Background(), runID)
	if err != nil || run == nil {
		return nil
	}

	// Primary source: durable approval records (survives control-plane restarts).
	if approvals, err := e.store.ListApprovals(context.Background(), run.Kitchen, "pending", 1000); err == nil {
		pending := make([]string, 0)
		for _, approval := range approvals {
			if approval.RunID == runID {
				pending = append(pending, approval.StepName)
			}
		}
		if len(pending) > 0 {
			return pending
		}
	}

	// Fallback: in-memory channels for same-process fast-path approvals.
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

// evaluateBranches checks step branches against the step's output using
// expr-lang/expr for expression evaluation. Returns the NextStep from the
// first matching branch, or DefaultNext if no branch matches.
func (e *Engine) evaluateBranches(step *models.Step, result *models.StepResult) string {
	if result.Output == nil {
		if step.DefaultNext != "" {
			return step.DefaultNext
		}
		return ""
	}

	for _, branch := range step.Branches {
		matched, err := evalExprBool(branch.Condition, result.Output)
		if err != nil {
			log.Warn().Err(err).
				Str("step", step.Name).
				Str("condition", branch.Condition).
				Msg("Branch condition evaluation error, skipping branch")
			continue
		}
		if matched {
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

// ── Expression Engine (expr-lang/expr) ─────────────────────

// evalExprBool evaluates an expression against an environment map and returns
// a boolean result. The expression can use any syntax supported by expr-lang/expr:
//   - Property access: category == "billing", output.score >= 4
//   - Boolean operators: a && b, a || b, !a
//   - Comparisons: ==, !=, <, >, <=, >=
//   - String operations: contains(name, "test"), startsWith(name, "pre")
//   - Membership: category in ["billing", "technical"]
func evalExprBool(expression string, env map[string]interface{}) (bool, error) {
	if expression == "" {
		return false, nil
	}

	program, err := expr.Compile(expression, expr.AsBool())
	if err != nil {
		return false, fmt.Errorf("compile expression %q: %w", expression, err)
	}

	output, err := expr.Run(program, env)
	if err != nil {
		return false, fmt.Errorf("evaluate expression %q: %w", expression, err)
	}

	result, ok := output.(bool)
	if !ok {
		return false, fmt.Errorf("expression %q did not return bool, got %T", expression, output)
	}
	return result, nil
}

// resolveJSONPath traverses a dot-separated path (e.g. "results.documents")
// into a nested map and returns the value at that path.
func resolveJSONPath(data map[string]interface{}, path string) (interface{}, bool) {
	parts := strings.Split(path, ".")
	var current interface{} = data

	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// ── Router Step ────────────────────────────────────────────

// executeRouter calls the router agent (if set), evaluates branches against
// the agent's output, and sets BranchTaken. The scheduler in executeAsync
// uses BranchTaken to skip non-selected downstream steps.
func (e *Engine) executeRouter(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	// If the router has an agent, call it to get the routing output
	if step.AgentRef != "" {
		if err := e.executeAgentStep(ctx, run, step, result, completed, mu); err != nil {
			return err
		}
	} else {
		// No agent — use dependency outputs as the routing input
		env := make(map[string]interface{})
		mu.Lock()
		for _, dep := range step.DependsOn {
			if sr, ok := completed[dep]; ok && sr.Output != nil {
				for k, v := range sr.Output {
					env[k] = v
				}
			}
		}
		mu.Unlock()
		result.Output = env
	}

	// Evaluate branches — the scheduler reads BranchTaken to propagate skips
	if len(step.Branches) > 0 {
		branch := e.evaluateBranches(step, result)
		if branch != "" {
			result.BranchTaken = branch
		}
	}

	return nil
}

// ── Map / Iteration Step ──────────────────────────────────

// executeMap extracts an array from an upstream step's output and runs the
// referenced agent once per item, collecting sub-results.
func (e *Engine) executeMap(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	if step.AgentRef == "" {
		return fmt.Errorf("map step '%s' has no agent_ref", step.Name)
	}
	if step.SourcePath == "" {
		return fmt.Errorf("map step '%s' has no source_path", step.Name)
	}
	if len(step.DependsOn) == 0 {
		return fmt.Errorf("map step '%s' has no dependencies to read items from", step.Name)
	}

	// Find the array to iterate over
	var items []interface{}
	mu.Lock()
	for _, dep := range step.DependsOn {
		if sr, ok := completed[dep]; ok && sr.Output != nil {
			val, found := resolveJSONPath(sr.Output, step.SourcePath)
			if found {
				if arr, ok := val.([]interface{}); ok {
					items = arr
					break
				}
			}
		}
	}
	mu.Unlock()

	if len(items) == 0 {
		result.Output = map[string]interface{}{"items_processed": 0}
		result.SubResults = nil
		result.ItemCount = 0
		return nil
	}

	result.ItemCount = len(items)

	// Determine concurrency
	maxConc := len(items)
	if step.MaxConcurrency > 0 && step.MaxConcurrency < maxConc {
		maxConc = step.MaxConcurrency
	}

	// Semaphore for bounded concurrency
	sem := make(chan struct{}, maxConc)
	subResults := make([]models.StepResult, len(items))
	var wg sync.WaitGroup
	var mapErr error
	var mapErrOnce sync.Once

	onError := "continue" // default
	if step.Config != nil {
		if oe, ok := step.Config["on_error"].(string); ok {
			onError = oe
		}
	}

	for i, item := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, itemData interface{}) {
			defer wg.Done()
			defer func() { <-sem }()

			subResult := &models.StepResult{
				StepName:  fmt.Sprintf("%s[%d]", step.Name, idx),
				StepKind:  "agent",
				AgentRef:  step.AgentRef,
				StartedAt: time.Now(),
			}

			// Build a synthetic completed map with just the item
			itemCompleted := make(map[string]*models.StepResult)
			var itemMu sync.Mutex
			itemCompleted["_item"] = &models.StepResult{
				StepName: "_item",
				Status:   "completed",
				Output: map[string]interface{}{
					"item":  itemData,
					"index": idx,
				},
			}
			// Also include original deps
			mu.Lock()
			for k, v := range completed {
				itemCompleted[k] = v
			}
			mu.Unlock()

			// Create a synthetic step for the per-item agent call
			itemStep := &models.Step{
				Name:      fmt.Sprintf("%s[%d]", step.Name, idx),
				Kind:      models.StepAgent,
				AgentRef:  step.AgentRef,
				DependsOn: append([]string{"_item"}, step.DependsOn...),
			}

			err := e.executeAgentStep(ctx, run, itemStep, subResult, itemCompleted, &itemMu)
			if err != nil {
				subResult.Status = "failed"
				subResult.Error = err.Error()
				if onError == "fail_fast" {
					mapErrOnce.Do(func() { mapErr = err })
				}
			} else {
				subResult.Status = "completed"
			}
			subResult.DurationMs = time.Since(subResult.StartedAt).Milliseconds()
			subResults[idx] = *subResult
		}(i, item)
	}
	wg.Wait()

	result.SubResults = subResults

	// Aggregate outputs
	outputs := make([]interface{}, 0, len(subResults))
	var totalTokens int64
	var totalCost float64
	for _, sr := range subResults {
		outputs = append(outputs, sr.Output)
		totalTokens += sr.Tokens
		totalCost += sr.CostUSD
	}
	result.Output = map[string]interface{}{
		"items_processed": len(items),
		"results":         outputs,
	}
	result.Tokens = totalTokens
	result.CostUSD = totalCost

	if mapErr != nil {
		return mapErr
	}
	return nil
}

// ── Sub-Recipe / Hierarchy Step ───────────────────────────

// maxSubRecipeDepth limits nesting to prevent infinite recursion.
const maxSubRecipeDepth = 5

// executeSubRecipe invokes another recipe as a nested workflow.
func (e *Engine) executeSubRecipe(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, completed map[string]*models.StepResult, mu *sync.Mutex) error {
	if step.RecipeRef == "" {
		return fmt.Errorf("sub_recipe step '%s' has no recipe_ref", step.Name)
	}

	// Check recursion depth
	depth := getSubRecipeDepth(ctx)
	if depth >= maxSubRecipeDepth {
		return fmt.Errorf("sub_recipe step '%s' exceeded max nesting depth (%d)", step.Name, maxSubRecipeDepth)
	}

	// Look up the sub-recipe
	subRecipe, err := e.store.GetRecipe(ctx, run.Kitchen, step.RecipeRef)
	if err != nil {
		return fmt.Errorf("sub_recipe '%s' lookup failed: %w", step.RecipeRef, err)
	}

	// Build input from InputMapping
	subInput := make(map[string]interface{})
	if run.Input != nil {
		subInput["parent_input"] = run.Input
	}
	mu.Lock()
	for _, dep := range step.DependsOn {
		if sr, ok := completed[dep]; ok && sr.Output != nil {
			subInput[dep] = sr.Output
		}
	}
	mu.Unlock()

	// Apply input mapping if provided
	if step.InputMapping != nil {
		mapped := make(map[string]interface{})
		for targetKey, sourcePath := range step.InputMapping {
			if val, ok := resolveJSONPath(subInput, sourcePath); ok {
				mapped[targetKey] = val
			}
		}
		subInput = mapped
	}

	// Execute the sub-recipe synchronously (with incremented depth)
	subCtx := withSubRecipeDepth(ctx, depth+1)
	subRunID := uuid.New().String()

	subRun := &models.RecipeRun{
		ID:          subRunID,
		RecipeID:    subRecipe.ID,
		Kitchen:     run.Kitchen,
		Status:      models.RecipeRunRunning,
		Input:       subInput,
		ParentRunID: run.ID,
		StartedAt:   time.Now().UTC(),
	}

	if err := e.store.CreateRecipeRun(ctx, subRun); err != nil {
		return fmt.Errorf("create sub-recipe run: %w", err)
	}

	result.SubRunID = subRunID

	log.Info().
		Str("parent_run", run.ID).
		Str("sub_run", subRunID).
		Str("sub_recipe", step.RecipeRef).
		Int("depth", depth+1).
		Msg("🔀 Starting sub-recipe execution")

	// Run synchronously (blocking) so the parent step waits
	e.executeAsync(subCtx, subRun, subRecipe)

	// Read the completed sub-run
	completedSubRun, err := e.store.GetRecipeRun(ctx, subRunID)
	if err != nil {
		return fmt.Errorf("read sub-recipe run result: %w", err)
	}

	if completedSubRun.Status == models.RecipeRunFailed {
		return fmt.Errorf("sub-recipe '%s' failed: %s", step.RecipeRef, completedSubRun.Error)
	}

	// Apply output mapping if provided
	output := completedSubRun.Output
	if step.OutputMapping != nil && output != nil {
		mapped := make(map[string]interface{})
		for targetKey, sourcePath := range step.OutputMapping {
			if val, ok := resolveJSONPath(output, sourcePath); ok {
				mapped[targetKey] = val
			}
		}
		output = mapped
	}

	result.Output = output
	result.Tokens = completedSubRun.TotalTokens
	result.CostUSD = completedSubRun.TotalCostUSD

	return nil
}

// ── Sub-recipe depth tracking via context ────────────────

type subRecipeDepthKey struct{}

func getSubRecipeDepth(ctx context.Context) int {
	if v, ok := ctx.Value(subRecipeDepthKey{}).(int); ok {
		return v
	}
	return 0
}

func withSubRecipeDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, subRecipeDepthKey{}, depth)
}
