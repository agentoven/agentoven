package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A task paused at its budget that nobody answered is ended as a refusal: the task reads cancelled with
// the reason, a late approval finds nothing to resume, and a request that names the wrong gate, or
// another kitchen, changes nothing.
func TestExpiringAPausedTasksApprovalEndsItAndNeverApprovesIt(t *testing.T) {
	h := budgetHandlersWith(t, 6, models.RunBudget{}, nil, "done", nil)
	b, _ := json.Marshal(map[string]interface{}{"agent": "worker", "message": "go", "budget": map[string]interface{}{"max_tokens": 350}})
	id := createdID(t, postTask(h, string(b)))
	waitTask(t, h, id, "paused")
	ctx := context.Background()

	if ok, err := h.ExpireApproval(ctx, "default", contracts.GateToolApproval, id, "x"); ok || err != nil {
		t.Fatalf("a budget pause expired as a tool approval: %v %v", ok, err)
	}
	if ok, _ := h.ExpireApproval(ctx, "elsewhere", contracts.GateBudgetExtension, id, "x"); ok {
		t.Fatal("another kitchen's run was expired")
	}
	if _, err := h.ExpireApproval(ctx, "default", contracts.GateSkillApproval, id, "x"); err == nil {
		t.Fatal("a gate this seam does not own was accepted")
	}
	if got := getTask(h, id); got["status"] != "paused" {
		t.Fatalf("refused expiries changed the task: %v", got)
	}

	ok, err := h.ExpireApproval(ctx, "default", contracts.GateBudgetExtension, id, "no answer in 24h")
	if !ok || err != nil {
		t.Fatalf("expire = %v %v", ok, err)
	}
	got := getTask(h, id)
	if got["status"] != "cancelled" {
		t.Fatalf("task = %v", got)
	}
	if rec := callAs(h.ResumeTask, http.MethodPost, "/resume", `{"approved":true,"extend":{"add_tokens":100000}}`, map[string]string{"taskID": id}, "default", nil, nil); rec.Code == http.StatusAccepted {
		t.Fatalf("a late approval resumed an expired task: %s", rec.Body.String())
	}
	if ok, _ := h.ExpireApproval(ctx, "default", contracts.GateBudgetExtension, id, "again"); ok {
		t.Fatal("expired twice")
	}
}
