package executor_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// passRuntime is the least a runtime can be: it lets everything through to the host. What
// it records shows what the executor offers and enforces on its own.
type passRuntime struct {
	enabled bool
	gated   []string
	refuse  error

	mu      sync.Mutex
	spawned []models.SubagentSpec
	parents []models.SubagentParent
}

func (r *passRuntime) Enabled(*models.Agent) bool        { return r.enabled }
func (r *passRuntime) GatedTools(*models.Agent) []string { return r.gated }
func (r *passRuntime) Spawn(ctx context.Context, p models.SubagentParent, spec models.SubagentSpec, h contracts.SubagentHost) (models.SubagentResult, error) {
	r.mu.Lock()
	r.spawned = append(r.spawned, spec)
	r.parents = append(r.parents, p)
	r.mu.Unlock()
	if r.refuse != nil {
		return models.SubagentResult{}, r.refuse
	}
	return h.Run(ctx, spec)
}
func (r *passRuntime) SpawnMany(ctx context.Context, p models.SubagentParent, specs []models.SubagentSpec, h contracts.SubagentHost) ([]models.SubagentResult, error) {
	r.mu.Lock()
	r.spawned = append(r.spawned, specs...)
	r.mu.Unlock()
	if r.refuse != nil {
		return nil, r.refuse
	}
	return h.RunMany(ctx, specs), nil
}

func hasToolNamed(req *models.RouteRequest, name string) bool {
	for _, td := range req.Tools {
		if td.Function.Name == name {
			return true
		}
	}
	return false
}

// spawnDriver drives a parent that spawns once and then answers, and any child (recognised by
// its instruction text) which answers with childAnswer after optionally calling a tool.
func spawnDriver(parentCalls []models.ToolCallResult, childAnswer string) *costDriver {
	var parentN int32
	d := &costDriver{in: 40, out: 20}
	d.fn = func(req *models.RouteRequest) turnReply {
		for _, m := range req.Messages {
			if m.Role == "system" && strings.Contains(m.Content, "Another agent started you") {
				return turnReply{content: childAnswer}
			}
		}
		if atomic.AddInt32(&parentN, 1) == 1 {
			return turnReply{toolCalls: parentCalls}
		}
		return turnReply{content: "parent done"}
	}
	return d
}

func spawnArgs(extra map[string]interface{}) map[string]interface{} {
	args := map[string]interface{}{"name": "researcher", "system_prompt": "You research.", "input": "find the delivery date"}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func TestWithoutARuntimeThereIsNoTaskTool(t *testing.T) {
	d := &costDriver{replies: []turnReply{{toolCalls: []models.ToolCallResult{toolCall("1", "task", spawnArgs(nil))}}, {content: "ok"}}, in: 10, out: 5}
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	_, trace, err := e.Execute(context.Background(), testAgent(), "go", resolvedWithTools(orderTool()), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if hasToolNamed(d.requests[0], "task") || hasToolNamed(d.requests[0], "tasks") {
		t.Fatal("the community edition does not offer dynamic subagents")
	}
	if r := trace.Turns[0].ToolResults[0]; !r.IsError {
		t.Fatalf("a task call the model made up must not do anything: %+v", r)
	}
}

func TestARuntimeOffersTheTaskToolsAndAChildReturnsOnlyItsAnswer(t *testing.T) {
	d := spawnDriver([]models.ToolCallResult{toolCall("1", "task", spawnArgs(nil))}, "it arrives Tuesday")
	e, dirJournal, dir := journalDirExecutorStore2(t, d, orderGateway())
	rt := &passRuntime{enabled: true}
	e.SetSubagentRuntime(rt)

	a := testAgent()
	a.Guardrails = []models.Guardrail{{ID: "g1", Kind: models.GuardrailContentFilter, Stage: models.GuardrailStageInput, Enabled: true,
		Config: map[string]interface{}{"blocked_words": []interface{}{"never"}}}}
	resp, trace, err := e.Execute(context.Background(), a, "when does my order arrive?", resolvedWithTools(orderTool()), nil, false)
	if err != nil || resp != "parent done" {
		t.Fatalf("%q %v", resp, err)
	}
	if !hasToolNamed(d.requests[0], "task") || !hasToolNamed(d.requests[0], "tasks") {
		t.Fatal("with a runtime installed the model is offered task and tasks")
	}
	if got := trace.Turns[0].ToolResults[0]; got.IsError || got.Content != "it arrives Tuesday" {
		t.Fatalf("the parent gets the child's answer: %+v", got)
	}

	// The child saw a clean context: its instructions and its task, not the parent's conversation.
	var childReq *models.RouteRequest
	for _, req := range d.requests {
		for _, m := range req.Messages {
			if m.Role == "system" && strings.Contains(m.Content, "Another agent started you") {
				childReq = req
			}
		}
	}
	if childReq == nil {
		t.Fatal("the child never ran")
	}
	for _, m := range childReq.Messages {
		if strings.Contains(m.Content, "when does my order arrive?") {
			t.Fatalf("the child saw the parent's conversation: %+v", childReq.Messages)
		}
	}
	// ...and cannot spawn or delegate itself, so the tree stays one level deep.
	for _, name := range []string{"task", "tasks", "agentoven_delegate"} {
		if hasToolNamed(childReq, name) {
			t.Fatalf("a subagent was offered %s", name)
		}
	}

	// The child is its own journaled run, naming its parent, bound by the parent's rules.
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 2 {
		t.Fatalf("expected the parent's journal and the child's, got %v", files)
	}
	var childID string
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		if id != trace.TraceID {
			childID = id
		}
	}
	js, err := dirJournal.Load(context.Background(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if js.Header.ParentTraceID != trace.TraceID {
		t.Fatalf("the child's journal should name its parent: %q", js.Header.ParentTraceID)
	}
	if len(js.Header.Agent.Guardrails) != 1 || js.Header.Agent.Guardrails[0].ID != "g1" {
		t.Fatalf("a subagent runs under its parent's guardrails: %+v", js.Header.Agent.Guardrails)
	}
	if !strings.HasPrefix(js.Header.Agent.Name, "test-agent/researcher-") {
		t.Fatalf("child name %q", js.Header.Agent.Name)
	}
	if un, _ := e.ListUnfinished(context.Background(), "default"); len(un) != 0 {
		t.Fatalf("a subagent is never offered as resumable on its own: %+v", un)
	}
}

func TestASubagentCanOnlyNarrowTheParentsTools(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]interface{}
		wantErr string
		wantNil bool // child offered no tools
		want    []string
	}{
		{"none by default", spawnArgs(nil), "", true, nil},
		{"a subset", spawnArgs(map[string]interface{}{"tools": []interface{}{"lookup_order"}}), "", false, []string{"lookup_order"}},
		{"all of the parent's", spawnArgs(map[string]interface{}{"tools": []interface{}{"*"}}), "", false, []string{"lookup_order"}},
		{"a tool the parent does not have", spawnArgs(map[string]interface{}{"tools": []interface{}{"wire_money"}}), "not one of the parent's tools", false, nil},
		{"a tool needing approval", spawnArgs(map[string]interface{}{"tools": []interface{}{"issue_refund"}}), "needs a person's approval", false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := spawnDriver([]models.ToolCallResult{toolCall("1", "task", c.args)}, "child done")
			e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
			e.SetSubagentRuntime(&passRuntime{enabled: true})
			a := testAgent()
			a.ApprovalTools = []string{"issue_refund"}
			refund := models.ResolvedTool{Name: "issue_refund", Schema: map[string]interface{}{"type": "object"}}
			_, trace, err := e.Execute(context.Background(), a, "go", resolvedWithTools(orderTool(), refund), nil, false)
			if err != nil {
				t.Fatal(err)
			}
			res := trace.Turns[0].ToolResults[0]
			if c.wantErr != "" {
				if !res.IsError || !strings.Contains(res.Content, c.wantErr) {
					t.Fatalf("expected an error containing %q, got %+v", c.wantErr, res)
				}
				return
			}
			if res.IsError {
				t.Fatalf("unexpected error: %+v", res)
			}
			var childReq *models.RouteRequest
			for _, req := range d.requests {
				for _, m := range req.Messages {
					if m.Role == "system" && strings.Contains(m.Content, "Another agent started you") {
						childReq = req
					}
				}
			}
			if childReq == nil {
				t.Fatal("child never ran")
			}
			var got []string
			for _, td := range childReq.Tools {
				got = append(got, td.Function.Name)
			}
			if c.wantNil && len(got) != 0 {
				t.Fatalf("expected no tools, got %v", got)
			}
			if !c.wantNil && strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("child tools = %v, want %v", got, c.want)
			}
		})
	}
}

func TestASubagentsSpendIsChargedToTheParentsBudget(t *testing.T) {
	d := spawnDriver([]models.ToolCallResult{toolCall("1", "task", spawnArgs(nil))}, "child done")
	e, j, _ := journalDirExecutorStore2(t, d, orderGateway())
	e.SetSubagentRuntime(&passRuntime{enabled: true})
	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 100000}), "go", resolvedP(orderTool()), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// parent 60 + child 60 + parent 60
	if trace.Budget.SpentTokens != 180 || trace.Usage.TotalTokens != 180 {
		t.Fatalf("budget spent %d, trace usage %d, want 180", trace.Budget.SpentTokens, trace.Usage.TotalTokens)
	}
	if js, _ := j.Load(context.Background(), trace.TraceID); js.Usage.TotalTokens != 180 {
		t.Fatalf("journal usage %d", js.Usage.TotalTokens)
	}
}

func TestAFanOutSharesTheParentsBudgetAndRunsInParallel(t *testing.T) {
	var running, peak int32
	var mu sync.Mutex
	d := &costDriver{in: 40, out: 20}
	var parentN int32
	d.fn = func(req *models.RouteRequest) turnReply {
		for _, m := range req.Messages {
			if m.Role == "system" && strings.Contains(m.Content, "Another agent started you") {
				mu.Lock()
				running++
				if int32(running) > peak {
					peak = int32(running)
				}
				mu.Unlock()
				defer func() { mu.Lock(); running--; mu.Unlock() }()
				return turnReply{content: "part"}
			}
		}
		if atomic.AddInt32(&parentN, 1) == 1 {
			var tasks []interface{}
			for i := 0; i < 4; i++ {
				tasks = append(tasks, spawnArgs(map[string]interface{}{"name": fmt.Sprint("w", i), "input": fmt.Sprint("part ", i)}))
			}
			return turnReply{toolCalls: []models.ToolCallResult{toolCall("1", "tasks", map[string]interface{}{"tasks": tasks})}}
		}
		return turnReply{content: "assembled"}
	}
	d.onCall = nil
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	e.SetSubagentRuntime(&passRuntime{enabled: true})

	resp, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 100000}), "go", resolvedP(orderTool()), nil, false)
	if err != nil || resp != "assembled" {
		t.Fatalf("%q %v", resp, err)
	}
	res := trace.Turns[0].ToolResults[0]
	for i := 0; i < 4; i++ {
		if !strings.Contains(res.Content, fmt.Sprintf(`"name":"test-agent/w%d-`, i)) {
			t.Fatalf("results should list every subagent in order: %s", res.Content)
		}
	}
	// parent 2 calls + 4 children
	if trace.Budget.SpentTokens != 6*60 {
		t.Fatalf("spent %d, want %d", trace.Budget.SpentTokens, 6*60)
	}
	if res.ChildUsage.TotalTokens != 4*60 {
		t.Fatalf("the children's cost is carried on the result: %d", res.ChildUsage.TotalTokens)
	}
}

func TestTooLargeABatchIsRefused(t *testing.T) {
	var tasks []interface{}
	for i := 0; i < 11; i++ {
		tasks = append(tasks, spawnArgs(nil))
	}
	d := spawnDriver([]models.ToolCallResult{toolCall("1", "tasks", map[string]interface{}{"tasks": tasks})}, "x")
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	rt := &passRuntime{enabled: true}
	e.SetSubagentRuntime(rt)
	_, trace, _ := e.Execute(context.Background(), testAgent(), "go", resolvedWithTools(orderTool()), nil, false)
	if r := trace.Turns[0].ToolResults[0]; !r.IsError || !strings.Contains(r.Content, "at most 10") {
		t.Fatalf("%+v", r)
	}
	if len(rt.spawned) != 0 {
		t.Fatal("the runtime must not be asked to spawn an oversized batch")
	}
}

func TestARuntimeThatRefusesATaskLeavesTheParentRunning(t *testing.T) {
	d := spawnDriver([]models.ToolCallResult{toolCall("1", "task", spawnArgs(nil))}, "x")
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	e.SetSubagentRuntime(&passRuntime{enabled: true, refuse: errors.New("fan-out limit reached for this kitchen")})
	resp, trace, err := e.Execute(context.Background(), testAgent(), "go", resolvedWithTools(), nil, false)
	if err != nil || resp != "parent done" {
		t.Fatalf("%q %v", resp, err)
	}
	if r := trace.Turns[0].ToolResults[0]; !r.IsError || !strings.Contains(r.Content, "fan-out limit") {
		t.Fatalf("the model should be told why: %+v", r)
	}
}

func TestARuntimeCanRequireApprovalToSpawn(t *testing.T) {
	d := spawnDriver([]models.ToolCallResult{toolCall("1", "task", spawnArgs(nil))}, "child done")
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	rt := &passRuntime{enabled: true, gated: []string{"task"}}
	e.SetSubagentRuntime(rt)
	_, trace, err := e.Execute(context.Background(), testAgent(), "go", resolvedWithTools(), nil, false)
	if err != executor.ErrPausedForApproval {
		t.Fatalf("expected the spawn to wait for approval, got %v", err)
	}
	if len(rt.spawned) != 0 {
		t.Fatal("nothing may be spawned before a person approves")
	}
	resp, _, err := e.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: true})
	if err != nil || resp != "parent done" {
		t.Fatalf("%q %v", resp, err)
	}
	if len(rt.spawned) != 1 {
		t.Fatalf("approving runs the spawn, spawned %d", len(rt.spawned))
	}
}

func TestADisabledRuntimeOffersNothing(t *testing.T) {
	d := &costDriver{replies: []turnReply{{content: "hi"}}, in: 10, out: 5}
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	e.SetSubagentRuntime(&passRuntime{enabled: false})
	if _, _, err := e.Execute(context.Background(), testAgent(), "go", resolvedWithTools(), nil, false); err != nil {
		t.Fatal(err)
	}
	if hasToolNamed(d.requests[0], "task") {
		t.Fatal("a runtime that is not enabled for the agent offers no tools")
	}
}

func TestSubagentsSpawnedDuringATaskAreNotTasksThemselves(t *testing.T) {
	d := spawnDriver([]models.ToolCallResult{toolCall("1", "task", spawnArgs(map[string]interface{}{"tools": []interface{}{"*"}}))}, "child done")
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	e.SetSubagentRuntime(&passRuntime{enabled: true})
	if _, _, err := e.Execute(taskCtx(), budgetAgent(models.RunBudget{MaxTokens: 1_000_000}), "go", resolvedP(orderTool()), nil, false); err != nil {
		t.Fatal(err)
	}
	for _, req := range d.requests {
		isChild := false
		for _, m := range req.Messages {
			if m.Role == "system" && strings.Contains(m.Content, "Another agent started you") {
				isChild = true
			}
		}
		if isChild && (hasToolNamed(req, "agentoven_notes") || pinnedNotes(req) != "") {
			t.Fatal("a subagent should not get the task's working notes")
		}
	}
}

func TestASpawnApprovedAfterAPauseIsStillChargedToTheParent(t *testing.T) {
	d := spawnDriver([]models.ToolCallResult{toolCall("1", "task", spawnArgs(nil))}, "child done")
	e, j, _ := journalDirExecutorStore2(t, d, orderGateway())
	e.SetSubagentRuntime(&passRuntime{enabled: true, gated: []string{"task"}})
	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 100000}), "go", resolvedP(), nil, false)
	if err != executor.ErrPausedForApproval {
		t.Fatalf("expected the spawn to wait for approval, got %v", err)
	}
	_, final, err := e.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	// Run by the resume rather than the main loop, the child must still be bound by the parent.
	if final.Budget == nil || final.Budget.SpentTokens != 180 {
		t.Fatalf("parent 60 + child 60 + parent 60 = 180 expected on the budget, got %+v", final.Budget)
	}
	if js, _ := j.Load(context.Background(), trace.TraceID); js.Usage.TotalTokens != 180 {
		t.Fatalf("journal usage %d", js.Usage.TotalTokens)
	}
}
