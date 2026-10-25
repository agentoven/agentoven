package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func rpcCall(h *handlers_t, kitchen, method string, params map[string]interface{}) map[string]interface{} {
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	rec := callAs(h.A2AEndpoint, http.MethodPost, "/a2a", string(body), nil, kitchen, nil, nil)
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func sendParams(agent, text string, blocking bool) map[string]interface{} {
	return map[string]interface{}{
		"message":       map[string]interface{}{"role": "user", "parts": []map[string]string{{"type": "text", "text": text}}},
		"metadata":      map[string]string{"agent_name": agent},
		"configuration": map[string]interface{}{"blocking": blocking},
	}
}

func taskState(t *testing.T, resp map[string]interface{}) (id, state string) {
	t.Helper()
	res, _ := resp["result"].(map[string]interface{})
	if res == nil {
		t.Fatalf("no result: %v", resp)
	}
	st, _ := res["status"].(map[string]interface{})
	id, _ = res["id"].(string)
	state, _ = st["state"].(string)
	return
}

func pollA2A(t *testing.T, h *handlers_t, id, want string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last map[string]interface{}
	for time.Now().Before(deadline) {
		last = rpcCall(h, "default", "tasks/get", map[string]interface{}{"id": id})
		if _, state := taskState(t, last); state == want {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("A2A task %s never reached %q: %v", id, want, last)
	return nil
}

func TestAnAsyncA2ASendReturnsATaskAtOnceAndGetFindsTheAnswer(t *testing.T) {
	h := budgetHandlersWith(t, 2, models.RunBudget{}, nil, "async answer", nil)
	id, state := taskState(t, rpcCall(h, "default", "tasks/send", sendParams("worker", "go", false)))
	if id == "" || (state != "submitted" && state != "working") {
		t.Fatalf("expected a task in flight, got id=%q state=%q", id, state)
	}
	done := pollA2A(t, h, id, "completed")
	arts, _ := done["result"].(map[string]interface{})["artifacts"].([]interface{})
	if len(arts) != 1 || !strings.Contains(toJSON(arts), "async answer") {
		t.Fatalf("the answer should be an artifact: %v", done)
	}
}

func toJSON(v interface{}) string { b, _ := json.Marshal(v); return string(b) }

func TestABlockingA2ASendIsUnchanged(t *testing.T) {
	h := budgetHandlersWith(t, 1, models.RunBudget{}, nil, "sync answer", nil)
	resp := rpcCall(h, "default", "tasks/send", sendParams("worker", "go", true))
	res, _ := resp["result"].(map[string]interface{})
	if res == nil || !strings.Contains(toJSON(res), "sync answer") {
		t.Fatalf("the blocking path must still answer in the same response: %v", resp)
	}
}

func TestAPausedA2ATaskIsInputRequired(t *testing.T) {
	h := budgetHandlersWith(t, 10, models.RunBudget{MaxTokens: 350}, nil, "done", nil)
	id, _ := taskState(t, rpcCall(h, "default", "tasks/send", sendParams("worker", "go", false)))
	paused := pollA2A(t, h, id, "input-required")
	if !strings.Contains(toJSON(paused), "/api/v1/tasks/"+id+"/resume") {
		t.Fatalf("the status should say how a person continues it: %v", paused)
	}
}

func TestA2ATaskLookupsAreScopedToTheKitchen(t *testing.T) {
	h := budgetHandlersWith(t, 1, models.RunBudget{}, nil, "ok", nil)
	id, _ := taskState(t, rpcCall(h, "default", "tasks/send", sendParams("worker", "go", false)))
	pollA2A(t, h, id, "completed")

	for _, method := range []string{"tasks/get", "tasks/cancel"} {
		resp := rpcCall(h, "other-kitchen", method, map[string]interface{}{"id": id})
		if e, _ := resp["error"].(map[string]interface{}); e == nil || e["code"] != float64(-32001) {
			t.Fatalf("%s from another kitchen should be 'Task not found', got %v", method, resp)
		}
	}

	// The older trace-backed tasks are scoped too: a task id is not a credential.
	if err := h.Store.CreateTrace(context.Background(), &models.Trace{ID: "legacy-1", AgentName: "worker", Kitchen: "kitchen-a", Status: "completed", OutputText: "private"}); err != nil {
		t.Fatal(err)
	}
	other := rpcCall(h, "kitchen-b", "tasks/get", map[string]interface{}{"id": "legacy-1"})
	if e, _ := other["error"].(map[string]interface{}); e == nil || strings.Contains(toJSON(other), "private") {
		t.Fatalf("another kitchen's task was readable: %v", other)
	}
	if own := rpcCall(h, "kitchen-a", "tasks/get", map[string]interface{}{"id": "legacy-1"}); !strings.Contains(toJSON(own), "private") {
		t.Fatalf("the owner should still read it: %v", own)
	}
}

func TestCancellingAnA2ATask(t *testing.T) {
	gate := make(chan struct{})
	h := taskHandlers(t, &toolLoopDriver{loops: 100, gate: gate}, models.RunBudget{}, nil, nil, executor.TaskManagerOptions{})
	id, _ := taskState(t, rpcCall(h, "default", "tasks/send", sendParams("worker", "go", false)))
	pollA2A(t, h, id, "working")

	resp := rpcCall(h, "default", "tasks/cancel", map[string]interface{}{"id": id})
	if _, state := taskState(t, resp); state != "canceled" && state != "working" {
		t.Fatalf("unexpected state after cancel: %v", resp)
	}
	close(gate)
	pollA2A(t, h, id, "canceled")

	// A finished task cannot be canceled.
	h2 := budgetHandlersWith(t, 1, models.RunBudget{}, nil, "ok", nil)
	id2, _ := taskState(t, rpcCall(h2, "default", "tasks/send", sendParams("worker", "go", false)))
	pollA2A(t, h2, id2, "completed")
	resp = rpcCall(h2, "default", "tasks/cancel", map[string]interface{}{"id": id2})
	if e, _ := resp["error"].(map[string]interface{}); e == nil || e["code"] != float64(-32002) {
		t.Fatalf("expected TaskNotCancelable, got %v", resp)
	}
}

func TestAnAsyncA2ATasksLeakyAnswerIsNotAnArtifact(t *testing.T) {
	h := budgetHandlersWith(t, 1, models.RunBudget{}, nil, "their SSN is "+leakedSSN, nativeGuardrails())
	id, _ := taskState(t, rpcCall(h, "default", "tasks/send", sendParams("worker", "what is it?", false)))
	got := pollA2A(t, h, id, "failed")
	if strings.Contains(toJSON(got), leakedSSN) {
		t.Fatalf("the SSN reached the A2A caller: %v", got)
	}
	if !strings.Contains(toJSON(got), "blocked by guardrails") {
		t.Fatalf("the caller should be told why: %v", got)
	}
}

func TestAnAsyncA2ASendForAnAgentThatRunsItsOwnLoopIsRefused(t *testing.T) {
	h := budgetHandlersWith(t, 1, models.RunBudget{}, nil, "", nil)
	native := &models.Agent{Name: "lc", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		Runtime: models.RuntimeLangChain, ResolvedConfig: &models.ResolvedIngredients{Model: &models.ResolvedModel{Model: "m", Provider: "p"}}}
	if err := h.Store.CreateAgent(context.Background(), native); err != nil {
		t.Fatal(err)
	}
	resp := rpcCall(h, "default", "tasks/send", sendParams("lc", "go", false))
	if e, _ := resp["error"].(map[string]interface{}); e == nil || !strings.Contains(toJSON(e), "own") {
		t.Fatalf("expected a refusal, got %v", resp)
	}
}
