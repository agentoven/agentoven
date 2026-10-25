package handlers_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func callAs(h http.HandlerFunc, method, path, body string, params map[string]string, kitchen string, identity *contracts.Identity, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := pkgmw.SetKitchen(req.Context(), kitchen)
	if identity != nil {
		ctx = pkgmw.SetIdentity(ctx, identity)
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func postTask(h *handlers_t, body string) *httptest.ResponseRecorder {
	return callAs(h.CreateTask, http.MethodPost, "/api/v1/tasks", body, nil, "default", nil, nil)
}

type handlers_t = handlers.Handlers

func taskBody(agent, message string) string {
	b, _ := json.Marshal(map[string]interface{}{"agent": agent, "message": message})
	return string(b)
}

func getTask(h *handlers_t, id string) map[string]interface{} {
	rec := callAs(h.GetTask, http.MethodGet, "/api/v1/tasks/"+id, "", map[string]string{"taskID": id}, "default", nil, nil)
	var m map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	m["_code"] = float64(rec.Code)
	return m
}

func waitTask(t *testing.T, h *handlers_t, id, want string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var m map[string]interface{}
	for time.Now().Before(deadline) {
		m = getTask(h, id)
		if m["status"] == want {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s never reached %q, last: %v", id, want, m)
	return nil
}

func createdID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	id, _ := decode(t, rec.Body.String())["id"].(string)
	if id == "" {
		t.Fatalf("no task id in %s", rec.Body.String())
	}
	return id
}

func TestCreatingATaskReturns202AndItRunsToCompletion(t *testing.T) {
	h := budgetHandlersWith(t, 2, models.RunBudget{}, nil, "the finished report", nil)
	rec := postTask(h, taskBody("worker", "write the report"))
	id := createdID(t, rec)

	body := decode(t, rec.Body.String())
	if rec.Header().Get("Location") != "/api/v1/tasks/"+id {
		t.Fatalf("Location = %q", rec.Header().Get("Location"))
	}
	// A task is never unbounded: the operator's defaults fill what the agent left unset.
	b, _ := body["budget"].(map[string]interface{})
	if b["max_tokens"] != float64(1000000) || b["max_turns"] != float64(200) || b["max_active_seconds"] != float64(7200) {
		t.Fatalf("the task should carry the default caps, got %v", b)
	}
	if _, hasDollar := b["max_cost_usd"]; hasDollar {
		t.Fatalf("no dollar cap can be added for a model with no known price: %v", b)
	}

	done := waitTask(t, h, id, "completed")
	if done["result"] != "the finished report" || done["agent"] != "worker" {
		t.Fatalf("%v", done)
	}
}

func TestATaskCapsItselfAtARequestedBudgetAndResumesAfterAnExtension(t *testing.T) {
	h := budgetHandlersWith(t, 6, models.RunBudget{}, nil, "done", nil)
	b, _ := json.Marshal(map[string]interface{}{"agent": "worker", "message": "go", "budget": map[string]interface{}{"max_tokens": 350}})
	id := createdID(t, postTask(h, string(b)))
	paused := waitTask(t, h, id, "paused")
	if paused["pause_kind"] != "budget" {
		t.Fatalf("%v", paused)
	}

	resume := func(body string) *httptest.ResponseRecorder {
		return callAs(h.ResumeTask, http.MethodPost, "/resume", body, map[string]string{"taskID": id}, "default", nil, nil)
	}
	if rec := resume(""); rec.Code != http.StatusBadRequest {
		t.Fatalf("a paused task needs a decision, got %d", rec.Code)
	}
	if rec := resume(`{"approved":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("approving a budget pause needs an extension, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := resume(`{"approved":true,"extend":{"add_tokens":100000}}`); rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := resume(`{"approved":true,"extend":{"add_tokens":100000}}`); rec.Code != http.StatusConflict && rec.Code != http.StatusAccepted && rec.Code != http.StatusBadRequest {
		t.Fatalf("a second resume of the same pause should be refused, got %d", rec.Code)
	}
	waitTask(t, h, id, "completed")
}

func TestATaskIsRefusedForAnAgentThatRunsItsOwnLoop(t *testing.T) {
	h := budgetHandlersWith(t, 1, models.RunBudget{}, nil, "", nil)
	native := &models.Agent{Name: "lc", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		Runtime: models.RuntimeLangChain, ResolvedConfig: &models.ResolvedIngredients{}}
	if err := h.Store.CreateAgent(context.Background(), native); err != nil {
		t.Fatal(err)
	}
	rec := postTask(h, taskBody("lc", "go"))
	if rec.Code != http.StatusUnprocessableEntity || decode(t, rec.Body.String())["error"] != "unsupported_agent" {
		t.Fatalf("expected 422 unsupported_agent, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestATaskWhoseInputIsBlockedIsNeverStarted(t *testing.T) {
	h := budgetHandlersWith(t, 1, models.RunBudget{}, nil, "", nativeGuardrails())
	rec := postTask(h, taskBody("worker", "please "+blockedWord))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	list := callAs(h.ListTasks, http.MethodGet, "/api/v1/tasks", "", nil, "default", nil, nil)
	if decode(t, list.Body.String())["count"] != float64(0) {
		t.Fatalf("a blocked task must not exist: %s", list.Body.String())
	}
}

func TestATasksLeakyResultAndEventsNeverReachAViewer(t *testing.T) {
	h := budgetHandlersWith(t, 1, models.RunBudget{}, nil, "their SSN is "+leakedSSN, nativeGuardrails())
	id := createdID(t, postTask(h, taskBody("worker", "what is the id?")))
	done := waitTask(t, h, id, "completed")

	if done["result_blocked"] != true || done["result"] != nil {
		t.Fatalf("the result must be withheld: %v", done)
	}
	raw, _ := json.Marshal(done)
	if strings.Contains(string(raw), leakedSSN) {
		t.Fatalf("the SSN is in the task view: %s", raw)
	}

	rec := callAs(h.TaskEvents, http.MethodGet, "/events", "", map[string]string{"taskID": id}, "default", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("events: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), leakedSSN) {
		t.Fatalf("the SSN reached the event stream:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "withheld by output guardrails") {
		t.Fatalf("the stream should say the text was withheld:\n%s", rec.Body.String())
	}
}

func TestTheEventStreamHasSequenceIdsAndResumesFromLastEventID(t *testing.T) {
	h := budgetHandlersWith(t, 2, models.RunBudget{}, nil, "all done", nil)
	id := createdID(t, postTask(h, taskBody("worker", "go")))
	waitTask(t, h, id, "completed")

	stream := func(lastID string) []string {
		hdr := map[string]string{}
		if lastID != "" {
			hdr["Last-Event-ID"] = lastID
		}
		rec := callAs(h.TaskEvents, http.MethodGet, "/events", "", map[string]string{"taskID": id}, "default", nil, hdr)
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("content type %q", ct)
		}
		var ids []string
		sc := bufio.NewScanner(rec.Body)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "id: "); ok {
				ids = append(ids, v)
			}
		}
		return ids
	}
	all := stream("")
	if len(all) < 6 {
		t.Fatalf("expected a full event history, got %v", all)
	}
	rest := stream(all[2])
	if len(rest) != len(all)-3 || rest[0] != all[3] {
		t.Fatalf("resuming after %s should continue at %s: got %v of %v", all[2], all[3], rest, all)
	}
}

func TestTasksBelongToTheirKitchen(t *testing.T) {
	h := budgetHandlersWith(t, 1, models.RunBudget{}, nil, "", nil)
	id := createdID(t, postTask(h, taskBody("worker", "go")))
	waitTask(t, h, id, "completed")

	for name, fn := range map[string]http.HandlerFunc{"get": h.GetTask, "cancel": h.CancelTask, "resume": h.ResumeTask, "events": h.TaskEvents} {
		rec := callAs(fn, http.MethodPost, "/x", "", map[string]string{"taskID": id}, "other-kitchen", nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s from another kitchen: expected 404, got %d", name, rec.Code)
		}
	}
	list := callAs(h.ListTasks, http.MethodGet, "/", "", nil, "other-kitchen", nil, nil)
	if decode(t, list.Body.String())["count"] != float64(0) {
		t.Fatalf("list leaked: %s", list.Body.String())
	}
}

func TestAScopedKeySeesOnlyTheTasksOfItsAgents(t *testing.T) {
	h := budgetHandlersWith(t, 1, models.RunBudget{}, nil, "", nil)
	other := &models.Agent{Name: "other", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		ResolvedConfig: &models.ResolvedIngredients{Model: &models.ResolvedModel{Model: "m", Provider: "p"}}}
	if err := h.Store.CreateAgent(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.CreateScopedKey(context.Background(), &models.ScopedAPIKey{ID: "k1", Kitchen: "default", AgentNames: []string{"worker"}}); err != nil {
		t.Fatal(err)
	}
	scoped := &contracts.Identity{Subject: "svc", Provider: "scoped-key", Claims: map[string]string{"key_id": "k1"}}

	mine := createdID(t, postTask(h, taskBody("worker", "go")))
	theirs := createdID(t, postTask(h, taskBody("other", "go")))
	waitTask(t, h, mine, "completed")
	waitTask(t, h, theirs, "completed")

	if rec := callAs(h.GetTask, http.MethodGet, "/", "", map[string]string{"taskID": theirs}, "default", scoped, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("a scoped key must not see another agent's task, got %d", rec.Code)
	}
	if rec := callAs(h.GetTask, http.MethodGet, "/", "", map[string]string{"taskID": mine}, "default", scoped, nil); rec.Code != http.StatusOK {
		t.Fatalf("its own agent's task is visible, got %d", rec.Code)
	}
	if rec := callAs(h.CreateTask, http.MethodPost, "/", taskBody("other", "go"), nil, "default", scoped, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a scoped key may not start a task on another agent, got %d", rec.Code)
	}
	list := callAs(h.ListTasks, http.MethodGet, "/", "", nil, "default", scoped, nil)
	if decode(t, list.Body.String())["count"] != float64(1) {
		t.Fatalf("list should show only its own agent's task: %s", list.Body.String())
	}
}

func TestCancellingATaskOverHTTP(t *testing.T) {
	gate := make(chan struct{})
	drv := &toolLoopDriver{loops: 100, gate: gate}
	h := taskHandlers(t, drv, models.RunBudget{}, nil, nil, executor.TaskManagerOptions{})
	id := createdID(t, postTask(h, taskBody("worker", "go")))
	waitTask(t, h, id, "running")

	rec := callAs(h.CancelTask, http.MethodPost, "/cancel", "", map[string]string{"taskID": id}, "default", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	close(gate) // let the in-flight model call return; the run must stop afterwards
	waitTask(t, h, id, "cancelled")
}

func TestATooBusyServerAnswers429WithRetryAfter(t *testing.T) {
	gate := make(chan struct{})
	drv := &toolLoopDriver{loops: 1, gate: gate}
	h := taskHandlers(t, drv, models.RunBudget{}, nil, nil, executor.TaskManagerOptions{PerKitchen: 1, QueueMax: 1})
	t.Cleanup(func() { // registered after the journal's temp dir, so it runs before it is removed
		close(gate)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			busy := false
			for _, s := range h.Tasks.List(context.Background(), "default") {
				if !s.Status.Terminal() {
					busy = true
				}
			}
			if !busy {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	first := createdID(t, postTask(h, taskBody("worker", "one")))
	waitTask(t, h, first, "running")
	createdID(t, postTask(h, taskBody("worker", "two"))) // queued
	rec := postTask(h, taskBody("worker", "three"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("expected 429 with Retry-After, got %d (%q): %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}

func TestADollarCapOnAnUnpricedModelIsRefusedWhenTheTaskIsCreated(t *testing.T) {
	h := budgetHandlersWith(t, 1, models.RunBudget{MaxCostUSD: 2}, nil, "", nil)
	rec := postTask(h, taskBody("worker", "go"))
	if rec.Code != http.StatusUnprocessableEntity || decode(t, rec.Body.String())["error"] != "unpriced_model" {
		t.Fatalf("expected 422 unpriced_model, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAStalledTaskIsPausedAndCanBeSentOnWithGuidanceOverHTTP(t *testing.T) {
	drv := &toolLoopDriver{loops: 5, same: true, final: "resolved with guidance"}
	h := taskHandlers(t, drv, models.RunBudget{}, nil, nil, executor.TaskManagerOptions{})
	id := createdID(t, postTask(h, taskBody("worker", "go")))
	paused := waitTask(t, h, id, "paused")
	if paused["pause_kind"] != "stall" {
		t.Fatalf("%v", paused)
	}
	stall, _ := paused["stall"].(map[string]interface{})
	if stall["tool"] != "lookup" || stall["count"] != float64(5) {
		t.Fatalf("the stall should say what was repeated: %v", stall)
	}

	// An A2A caller sees the task needs a person.
	a2a := rpcCall(h, "default", "tasks/get", map[string]interface{}{"id": id})
	if _, state := taskState(t, a2a); state != "input-required" || !strings.Contains(toJSON(a2a), "called lookup 5 times") {
		t.Fatalf("A2A should report input-required with the reason: %v", a2a)
	}

	// Sending it on without a decision is refused; with one it carries on.
	if rec := callAs(h.ResumeTask, http.MethodPost, "/resume", "", map[string]string{"taskID": id}, "default", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("a paused task needs a decision, got %d", rec.Code)
	}
	rec := callAs(h.ResumeTask, http.MethodPost, "/resume", `{"approved":true,"note":"try a different query"}`, map[string]string{"taskID": id}, "default", nil, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	waitTask(t, h, id, "completed")
}

func TestAStalledTaskResumedThroughTheAgentRouteStallsAgainWith409(t *testing.T) {
	// A run that is a task keeps its defences when it is later resumed through the agent route.
	drv := &toolLoopDriver{loops: 50, same: true}
	h := taskHandlers(t, drv, models.RunBudget{}, nil, nil, executor.TaskManagerOptions{})
	id := createdID(t, postTask(h, taskBody("worker", "go")))
	waitTask(t, h, id, "paused")
	rec := callAs(h.ResumeRun, http.MethodPost, "/resume", `{"approved":true}`, map[string]string{"agentName": "worker", "traceID": id}, "default", nil, nil)
	// Approving without guidance resumes the run; the driver keeps repeating, so it stalls again.
	if rec.Code != http.StatusConflict || decode(t, rec.Body.String())["error"] != "run_stalled" {
		t.Fatalf("expected 409 run_stalled, got %d: %s", rec.Code, rec.Body.String())
	}
}
