package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Background tasks: long-horizon work that runs on its own after the request that started it
// has returned. A task is an agent run (its ID is the run's trace ID), so everything a run has
// (the journal, the budget, the approvals) is a task's too.
//
// The guardrails stand in front of a task exactly where they stand in front of a request:
// the input is checked before the task is accepted, and everything a viewer is shown (the
// event stream and the final result) passes the output rules first. A task never fails open:
// if a rule cannot be evaluated, the text is withheld.

const withheld = "[withheld by output guardrails]"

type taskView struct {
	executor.TaskSnapshot
	ResultBlocked bool `json:"result_blocked,omitempty"`
}

type createTaskRequest struct {
	Agent     string `json:"agent"`
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
	// Budget can only tighten the limits the task would otherwise get.
	Budget   *models.RunBudget `json:"budget,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// canAccessAgent applies a scoped key's agent allowlist. Callers that are not using a scoped
// key are limited only by their kitchen.
func (h *Handlers) canAccessAgent(ctx context.Context, kitchen, agentName string) bool {
	identity := pkgmw.GetIdentity(ctx)
	if identity == nil || identity.Provider != "scoped-key" {
		return true
	}
	key, err := h.Store.GetScopedKey(ctx, kitchen, identity.Claims["key_id"])
	return err == nil && key != nil && key.CanAccessAgent(agentName)
}

// outputFilter returns the function that stands between a task's events and its viewers.
func (h *Handlers) outputFilter(ctx context.Context, agent *models.Agent, gr []models.Guardrail) func(executor.Event) executor.Event {
	if h.Guardrails == nil || !outputStageEnabled(gr) {
		return nil
	}
	check := func(text string) string {
		if pass, _ := h.outputVerdict(ctx, agent, gr, text); pass {
			return text
		}
		return withheld
	}
	return func(ev executor.Event) executor.Event {
		switch ev.Type {
		case executor.EventToken, executor.EventThinkingToken, executor.EventDone:
			ev.Content = check(ev.Content)
		}
		return ev
	}
}

func (h *Handlers) filterFor(ctx context.Context, agent *models.Agent) func(executor.Event) executor.Event {
	gr, err := h.guardrailsFor(ctx, agent, agent.Guardrails)
	if err != nil {
		// Without the policy, show nothing the model said.
		return func(ev executor.Event) executor.Event {
			switch ev.Type {
			case executor.EventToken, executor.EventThinkingToken, executor.EventDone:
				ev.Content = withheld
			}
			return ev
		}
	}
	return h.outputFilter(ctx, agent, gr)
}

func (h *Handlers) viewOf(ctx context.Context, snap executor.TaskSnapshot) taskView {
	v := taskView{TaskSnapshot: snap}
	if snap.Result == "" {
		return v
	}
	agent, err := h.Store.GetAgent(ctx, snap.Kitchen, snap.Agent)
	if err != nil || agent == nil {
		agent = &models.Agent{Name: snap.Agent, Kitchen: snap.Kitchen}
	}
	gr, gerr := h.guardrailsFor(ctx, agent, agent.Guardrails)
	if gerr != nil {
		v.Result, v.ResultBlocked = "", true
		return v
	}
	if pass, _ := h.outputVerdict(ctx, agent, gr, snap.Result); !pass {
		v.Result, v.ResultBlocked = "", true
	}
	return v
}

func (h *Handlers) tasksAvailable(w http.ResponseWriter) bool {
	if h.Tasks == nil {
		respondError(w, http.StatusServiceUnavailable, "background tasks need the execution journal, which is not enabled on this server")
		return false
	}
	return true
}

// taskLookup finds a task for the caller, answering 404 for one that does not exist, is in
// another kitchen, or belongs to an agent a scoped key may not use.
func (h *Handlers) taskLookup(w http.ResponseWriter, r *http.Request) (executor.TaskSnapshot, string, bool) {
	kitchen := middleware.GetKitchen(r.Context())
	id := chi.URLParam(r, "taskID")
	snap, err := h.Tasks.Get(r.Context(), kitchen, id)
	if err != nil || !h.canAccessAgent(r.Context(), kitchen, snap.Agent) {
		respondError(w, http.StatusNotFound, "task not found")
		return executor.TaskSnapshot{}, "", false
	}
	return snap, kitchen, true
}

// CreateTask starts an agent as a background task.
// POST /api/v1/tasks
func (h *Handlers) CreateTask(w http.ResponseWriter, r *http.Request) {
	if !h.tasksAvailable(w) {
		return
	}
	kitchen := middleware.GetKitchen(r.Context())
	var req createTaskRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Agent == "" || strings.TrimSpace(req.Message) == "" {
		respondError(w, http.StatusBadRequest, "a task needs an 'agent' and a non-empty 'message'")
		return
	}
	if len(req.Metadata) > 20 {
		respondError(w, http.StatusBadRequest, "at most 20 metadata entries")
		return
	}
	for k, v := range req.Metadata {
		if len(k) > 64 || len(v) > 512 {
			respondError(w, http.StatusBadRequest, "metadata keys are limited to 64 characters and values to 512")
			return
		}
	}
	if !h.canAccessAgent(r.Context(), kitchen, req.Agent) {
		respondError(w, http.StatusForbidden, fmt.Sprintf("Scoped key does not have access to agent '%s'", req.Agent))
		return
	}

	agent, err := h.Store.GetAgent(r.Context(), kitchen, req.Agent)
	if err != nil || agent == nil {
		if _, ok := err.(*store.ErrNotFound); ok || agent == nil {
			respondError(w, http.StatusNotFound, fmt.Sprintf("agent '%s' not found", req.Agent))
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if !agent.IsManaged() {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("agent '%s' is in '%s' mode; tasks run managed agents", agent.Name, agent.Mode))
		return
	}
	if agent.IsFrameworkNative() || (agent.Process != nil && agent.Process.Status == models.ProcessRunning) {
		// Their model calls are made inside the user's process, where the control plane can
		// neither cap what they spend nor checkpoint them. Say so rather than run them uncapped.
		respondJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
			"error":   "unsupported_agent",
			"message": "background tasks run on AgentOven's own agent loop, where spend caps and checkpoints are enforced; framework-native and process-backed agents run their own loop and can be invoked, but not run as tasks",
		})
		return
	}
	if agent.Status != models.AgentStatusReady {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("agent '%s' is not ready (status: %s), bake it first", agent.Name, agent.Status))
		return
	}

	// Guardrails: established first, then the input checked. Unlike a request someone is
	// waiting on, a task runs unattended, so an evaluator that errors blocks the task.
	gr, ok := h.requireGuardrails(w, r, agent, agent.Guardrails)
	if !ok {
		return
	}
	if h.Guardrails != nil && len(gr) > 0 {
		eval, gErr := h.Guardrails.EvaluateInput(r.Context(), gr, req.Message)
		if gErr != nil {
			h.emitGuardrailAudit(r.Context(), kitchen, agent.Name, "input", nil, gErr)
			respondError(w, http.StatusServiceUnavailable, "the input guardrails could not be evaluated, so the task was not started")
			return
		}
		if !eval.Passed {
			h.emitGuardrailAudit(r.Context(), kitchen, agent.Name, "input", eval, nil)
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Input blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		}
	}

	resolved := agent.ResolvedConfig
	if resolved == nil {
		resolved, err = h.Resolver.Resolve(r.Context(), agent)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Ingredient resolution failed: "+err.Error())
			return
		}
	}

	snap, merged, err := h.startTask(r.Context(), agent, resolved, gr, models.ChatMessage{Role: "user", Content: req.Message}, req.SessionID, req.Budget, req.Metadata)
	switch {
	case errors.Is(err, executor.ErrTaskQueueFull):
		w.Header().Set("Retry-After", "30")
		respondError(w, http.StatusTooManyRequests, err.Error())
		return
	case err != nil:
		if h.respondRunStopped(w, agent.Name, nil, err) {
			return
		}
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	w.Header().Set("Location", "/api/v1/tasks/"+snap.ID)
	respondJSON(w, http.StatusAccepted, map[string]interface{}{
		"id":     snap.ID,
		"status": snap.Status,
		"agent":  snap.Agent,
		"links": map[string]string{
			"self":   "/api/v1/tasks/" + snap.ID,
			"events": "/api/v1/tasks/" + snap.ID + "/events",
			"cancel": "/api/v1/tasks/" + snap.ID + "/cancel",
			"resume": "/api/v1/tasks/" + snap.ID + "/resume",
		},
		"budget": merged,
	})
}

// ListTasks lists the kitchen's live and unfinished tasks.
// GET /api/v1/tasks
func (h *Handlers) ListTasks(w http.ResponseWriter, r *http.Request) {
	if !h.tasksAvailable(w) {
		return
	}
	kitchen := middleware.GetKitchen(r.Context())
	statusFilter := r.URL.Query().Get("status")
	out := []taskView{}
	for _, s := range h.Tasks.List(r.Context(), kitchen) {
		if !h.canAccessAgent(r.Context(), kitchen, s.Agent) {
			continue
		}
		if statusFilter != "" && string(s.Status) != statusFilter {
			continue
		}
		out = append(out, h.viewOf(r.Context(), s))
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"tasks": out, "count": len(out)})
}

// GetTask returns one task.
// GET /api/v1/tasks/{taskID}
func (h *Handlers) GetTask(w http.ResponseWriter, r *http.Request) {
	if !h.tasksAvailable(w) {
		return
	}
	snap, _, ok := h.taskLookup(w, r)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, h.viewOf(r.Context(), snap))
}

// CancelTask stops a task.
// POST /api/v1/tasks/{taskID}/cancel
func (h *Handlers) CancelTask(w http.ResponseWriter, r *http.Request) {
	if !h.tasksAvailable(w) {
		return
	}
	if _, kitchen, ok := h.taskLookup(w, r); ok {
		snap, err := h.Tasks.Cancel(r.Context(), kitchen, chi.URLParam(r, "taskID"))
		if err != nil {
			respondError(w, http.StatusNotFound, "task not found")
			return
		}
		respondJSON(w, http.StatusOK, h.viewOf(r.Context(), snap))
	}
}

// ResumeTask continues a task that is paused (at its budget, or on a tool approval) or was
// interrupted. The body is the same decision ResumeRun takes.
// POST /api/v1/tasks/{taskID}/resume
func (h *Handlers) ResumeTask(w http.ResponseWriter, r *http.Request) {
	if !h.tasksAvailable(w) {
		return
	}
	snap, kitchen, ok := h.taskLookup(w, r)
	if !ok {
		return
	}
	var body struct {
		Approved bool                    `json:"approved"`
		Note     string                  `json:"note,omitempty"`
		Extend   *models.BudgetExtension `json:"extend,omitempty"`
	}
	var decision *executor.ResumeDecision
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		decision = &executor.ResumeDecision{Approved: body.Approved, Note: body.Note, Extend: body.Extend}
		if decision.Extend != nil {
			if !h.authorizeExtension(w, r, decision.Extend) {
				return
			}
			if id := pkgmw.GetIdentity(r.Context()); id != nil {
				decision.Extend.By = id.Subject
			}
		}
	}
	if snap.Status == executor.TaskPaused && decision == nil {
		respondError(w, http.StatusBadRequest, "this task is paused: say whether to continue ('approved') and, at a budget pause, by how much ('extend')")
		return
	}
	if snap.Status == executor.TaskPaused && snap.PauseKind == string(executor.PauseBudget) && decision.Approved &&
		(decision.Extend == nil || decision.Extend.IsZero()) {
		respondError(w, http.StatusBadRequest, "approving a budget pause needs an extension that grants something")
		return
	}

	agent, err := h.Store.GetAgent(r.Context(), kitchen, snap.Agent)
	if err != nil || agent == nil {
		agent = &models.Agent{Name: snap.Agent, Kitchen: kitchen}
	}
	// Established before anything runs, like on every other route.
	if _, ok := h.requireGuardrails(w, r, agent, agent.Guardrails); !ok {
		return
	}

	// A decision on a paused task is a signed gate (Pro ADR-0041).
	var sig *contracts.SignedDecision
	if h.DecisionSigner != nil && decision != nil {
		var ok bool
		if sig, ok = h.signRunDecision(w, r, kitchen, snap.ID, decision); !ok {
			return
		}
	}

	out, err := h.Tasks.Resume(r.Context(), kitchen, snap.Agent, snap.ID, decision, h.filterFor(context.WithoutCancel(r.Context()), agent))
	h.decisionApplied(r, sig, decisionApplyError(err))
	switch {
	case errors.Is(err, executor.ErrTaskNotFound):
		respondError(w, http.StatusNotFound, "task not found")
	case errors.Is(err, executor.ErrTaskNotResumable):
		respondError(w, http.StatusConflict, fmt.Sprintf("task is %s, only a paused or interrupted task can be resumed", out.Status))
	case errors.Is(err, executor.ErrTaskQueueFull):
		w.Header().Set("Retry-After", "30")
		respondError(w, http.StatusTooManyRequests, err.Error())
	case err != nil:
		if h.respondRunStopped(w, snap.Agent, nil, err) {
			return
		}
		respondError(w, http.StatusBadRequest, err.Error())
	default:
		respondJSON(w, http.StatusAccepted, h.viewOf(r.Context(), out))
	}
}

// TaskEvents streams a task's events (server-sent events). Every event has a sequence number
// as its SSE id, so a client that drops can reconnect with Last-Event-ID (or ?after=) and
// carry on from where it was. Events are kept in memory for a while after a task ends; a
// task read back from the journal after a restart has state but no event history.
// GET /api/v1/tasks/{taskID}/events
func (h *Handlers) TaskEvents(w http.ResponseWriter, r *http.Request) {
	if !h.tasksAvailable(w) {
		return
	}
	snap, kitchen, ok := h.taskLookup(w, r)
	if !ok {
		return
	}
	after := int64(0)
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	} else if v := r.URL.Query().Get("after"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	}

	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		respondError(w, http.StatusInternalServerError, "streaming is not supported by this connection")
		return
	}
	// A task can go hours between events; the server's write timeout is for ordinary
	// requests and would cut this stream off, so it is lifted for this response only.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(ev executor.TaskEvent) bool {
		payload, err := json.Marshal(ev)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Event.Type, payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		events, done, wait, err := h.Tasks.Events(kitchen, snap.ID, after)
		if err != nil {
			// Not live in this process: report where the task stands and end.
			cur := h.viewOf(r.Context(), snap)
			send(executor.TaskEvent{Seq: after + 1, Time: time.Now().UTC(), Event: executor.Event{Type: executor.EventTaskStatus, Content: string(cur.Status), Err: cur.Error}})
			return
		}
		for _, ev := range events {
			if !send(ev) {
				return
			}
			after = ev.Seq
		}
		if done {
			return
		}
		select {
		case <-wait:
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// startTaskRecovery resumes tasks that were running when the server last stopped, when the
// operator asked for it (AGENTOVEN_TASK_RECOVERY=auto). By default they are only listed as
// interrupted and a person decides.
func (h *Handlers) startTaskRecovery() {
	if h.Tasks == nil || strings.ToLower(osGetenv("AGENTOVEN_TASK_RECOVERY")) != "auto" {
		return
	}
	h.Tasks.SetRedactFor(func(a *models.Agent) func(executor.Event) executor.Event {
		return h.filterFor(context.Background(), a)
	})
	go func() {
		time.Sleep(3 * time.Second) // let the server finish starting
		log.Info().Msg("Recovering interrupted tasks (AGENTOVEN_TASK_RECOVERY=auto)")
		h.Tasks.Recover(context.Background(), 3, 5*time.Second)
	}()
}

// startTask applies a task's budget and starts it. The budget is the agent's own limits, the
// operator's defaults for whatever it leaves unset (a task is never unbounded) held to the
// ceiling, then the request tightening further. A dollar cap the model's price cannot support
// is refused here, with nothing created.
func (h *Handlers) startTask(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients, gr []models.Guardrail,
	msg models.ChatMessage, sessionID string, reqBudget *models.RunBudget, meta map[string]string) (executor.TaskSnapshot, models.RunBudget, error) {

	priced := false
	if resolved.Model != nil && h.Router != nil {
		priced = h.Router.PriceFor(ctx, agent.Kitchen, resolved.Model.Provider, resolved.Model.Model).Known
	}
	merged := executor.ApplyTaskBudget(agent, priced)
	runAgent := *agent
	runAgent.Budget = &merged
	if reqBudget != nil {
		ctx = contracts.WithBudget(ctx, *reqBudget)
	}
	if err := h.Executor.ValidateBudget(ctx, &runAgent, resolved); err != nil {
		return executor.TaskSnapshot{}, merged, err
	}
	snap, err := h.Tasks.Submit(ctx, executor.TaskSpec{
		ID: uuid.New().String(), Agent: &runAgent, Resolved: resolved, Message: msg,
		SessionID: sessionID, Metadata: meta,
		Redact: h.outputFilter(context.WithoutCancel(ctx), agent, gr),
	})
	return snap, merged, err
}
