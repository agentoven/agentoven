package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Asynchronous A2A. A tasks/send that sets configuration.blocking=false returns a task at
// once and the agent works in the background (the same task manager the REST API uses);
// tasks/get reads it and tasks/cancel stops it. Without that setting tasks/send behaves as it
// always has, because the recipe engine and other callers rely on getting the answer back in
// the same response.

// a2aState maps a task's status to an A2A task state. A paused task, and an interrupted one,
// both wait for a person to act, which is what input-required says; the person acts through
// the REST API (an extension of a budget is not something an A2A caller can grant itself).
func a2aState(s executor.TaskStatus) string {
	switch s {
	case executor.TaskQueued:
		return "submitted"
	case executor.TaskRunning:
		return "working"
	case executor.TaskPaused, executor.TaskInterrupted:
		return "input-required"
	case executor.TaskCompleted, executor.TaskMaxTurns:
		return "completed"
	case executor.TaskCancelled:
		return "canceled"
	default:
		return "failed"
	}
}

// a2aTask is the A2A representation of a task. Its artifact, the agent's answer, has been
// through the output guardrails or is absent.
func (h *Handlers) a2aTask(ctx context.Context, snap executor.TaskSnapshot) map[string]interface{} {
	v := h.viewOf(ctx, snap)
	status := map[string]interface{}{"state": a2aState(snap.Status)}
	var note string
	switch {
	case v.ResultBlocked:
		status["state"] = "failed"
		note = "The output was blocked by guardrails"
	case snap.Status == executor.TaskPaused && snap.PauseKind == string(executor.PauseStall) && snap.Stall != nil:
		note = fmt.Sprintf("Paused: it called %s %d times with the same arguments. A person can send it on with guidance or end it: POST /api/v1/tasks/%s/resume", snap.Stall.Tool, snap.Stall.Count, snap.ID)
	case snap.Status == executor.TaskPaused && snap.PauseKind == string(executor.PauseBudget):
		note = fmt.Sprintf("Paused at the run budget; a person can extend it with POST /api/v1/tasks/%s/resume", snap.ID)
	case snap.Status == executor.TaskPaused:
		note = fmt.Sprintf("Waiting for a person to approve a tool call; POST /api/v1/tasks/%s/resume", snap.ID)
	case snap.Status == executor.TaskInterrupted:
		note = fmt.Sprintf("Interrupted before it finished; POST /api/v1/tasks/%s/resume continues it", snap.ID)
	case snap.Error != "" && a2aState(snap.Status) == "failed":
		note = snap.Error
	}
	if note != "" {
		status["message"] = map[string]interface{}{"role": "agent", "parts": []map[string]string{{"type": "text", "text": note}}}
	}
	out := map[string]interface{}{
		"id":     snap.ID,
		"status": status,
		"metadata": map[string]interface{}{
			"agent_name":        snap.Agent,
			"kitchen":           snap.Kitchen,
			"agentoven_status":  snap.Status,
			"turns":             snap.Turns,
			"max_turns_reached": snap.Status == executor.TaskMaxTurns,
		},
		"usage": map[string]interface{}{"total_tokens": snap.Usage.TotalTokens, "cost_usd": snap.Usage.EstimatedCost},
	}
	if v.Result != "" {
		out["artifacts"] = []map[string]interface{}{{"parts": []map[string]string{{"type": "text", "text": v.Result}}}}
	}
	return out
}

func a2aResult(w http.ResponseWriter, id interface{}, result interface{}) {
	w.Header().Set("Content-Type", "application/a2a+json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "result": result, "id": id})
}

// a2aSendAsync starts the agent as a background task and answers with the task.
func (h *Handlers) a2aSendAsync(w http.ResponseWriter, r *http.Request, rpcID interface{}, agent *models.Agent, resolved *models.ResolvedIngredients,
	gr []models.Guardrail, text string, parts []models.ContentPart, callerKitchen string) {

	switch {
	case h.Tasks == nil:
		a2aRPCError(w, rpcID, -32004, "Asynchronous tasks are not available: the execution journal is not enabled", nil)
		return
	case agent.Kitchen != callerKitchen:
		a2aRPCError(w, rpcID, -32004, "Asynchronous tasks are not supported across kitchens", nil)
		return
	case !agent.IsManaged() || agent.IsFrameworkNative() || (agent.Process != nil && agent.Process.Status == models.ProcessRunning):
		a2aRPCError(w, rpcID, -32004, "Asynchronous tasks run on AgentOven's own agent loop; this agent runs its own, so call it without configuration.blocking=false", nil)
		return
	}
	snap, _, err := h.startTask(r.Context(), agent, resolved, gr, models.ChatMessage{Role: "user", Content: text, ContentParts: parts}, "", nil,
		map[string]string{"source": "a2a"})
	switch {
	case err == nil:
		a2aResult(w, rpcID, h.a2aTask(r.Context(), snap))
	case isQueueFull(err):
		a2aRPCError(w, rpcID, -32004, "Too many tasks are running; try again shortly", map[string]interface{}{"retry_after_seconds": 30})
	default:
		a2aRPCError(w, rpcID, -32003, "The task could not be started", err.Error())
	}
}

func isQueueFull(err error) bool { return errors.Is(err, executor.ErrTaskQueueFull) }

// a2aTaskFromManager answers tasks/get for a task the task manager knows, reporting whether
// it did. A task in another kitchen, or one a scoped key may not reach, is "not found".
func (h *Handlers) a2aTaskFromManager(w http.ResponseWriter, r *http.Request, id string, rpcID interface{}) bool {
	if h.Tasks == nil {
		return false
	}
	kitchen := middleware.GetKitchen(r.Context())
	snap, err := h.Tasks.Get(r.Context(), kitchen, id)
	if err != nil {
		return false
	}
	if !h.canAccessAgent(r.Context(), kitchen, snap.Agent) {
		a2aRPCError(w, rpcID, -32001, "Task not found", id)
		return true
	}
	a2aResult(w, rpcID, h.a2aTask(r.Context(), snap))
	return true
}

// handleA2ATaskCancel stops a background task.
func (h *Handlers) handleA2ATaskCancel(w http.ResponseWriter, r *http.Request, params json.RawMessage, rpcID interface{}) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &req); err != nil || req.ID == "" {
		a2aRPCError(w, rpcID, -32602, "Invalid params", "task id is required")
		return
	}
	if h.Tasks == nil {
		a2aRPCError(w, rpcID, -32001, "Task not found", req.ID)
		return
	}
	kitchen := middleware.GetKitchen(r.Context())
	snap, err := h.Tasks.Get(r.Context(), kitchen, req.ID)
	if err != nil || !h.canAccessAgent(r.Context(), kitchen, snap.Agent) {
		a2aRPCError(w, rpcID, -32001, "Task not found", req.ID)
		return
	}
	if snap.Status.Terminal() && snap.Status != executor.TaskCancelled {
		a2aRPCError(w, rpcID, -32002, "Task cannot be canceled", string(snap.Status))
		return
	}
	out, err := h.Tasks.Cancel(r.Context(), kitchen, req.ID)
	if err != nil {
		a2aRPCError(w, rpcID, -32001, "Task not found", req.ID)
		return
	}
	a2aResult(w, rpcID, h.a2aTask(r.Context(), out))
}
