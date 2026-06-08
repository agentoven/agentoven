package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// ── Unit Tests ──────────────────────────────────────────────

func TestEvalExprBool(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		env     map[string]interface{}
		want    bool
		wantErr bool
	}{
		{"eq true", `category == "billing"`, map[string]interface{}{"category": "billing"}, true, false},
		{"eq false", `category == "billing"`, map[string]interface{}{"category": "tech"}, false, false},
		{"gte true", `score >= 8`, map[string]interface{}{"score": 9.0}, true, false},
		{"gte false", `score >= 8`, map[string]interface{}{"score": 5.0}, false, false},
		{"bool true", `approved == true`, map[string]interface{}{"approved": true}, true, false},
		{"bool false", `approved == true`, map[string]interface{}{"approved": false}, false, false},
		{"and both", `score >= 8 && approved == true`, map[string]interface{}{"score": 9.0, "approved": true}, true, false},
		{"and partial", `score >= 8 && approved == true`, map[string]interface{}{"score": 9.0, "approved": false}, false, false},
		{"or match", `category == "a" || category == "b"`, map[string]interface{}{"category": "b"}, true, false},
		{"neq", `status != "error"`, map[string]interface{}{"status": "ok"}, true, false},
		{"empty expr", ``, map[string]interface{}{}, false, false},
		{"gt int", `total > 0`, map[string]interface{}{"total": 3}, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evalExprBool(tt.expr, tt.env)
			if (err != nil) != tt.wantErr {
				t.Errorf("evalExprBool(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("evalExprBool(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestResolveJSONPath(t *testing.T) {
	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": 1},
			map[string]interface{}{"id": 2},
		},
		"nested": map[string]interface{}{
			"deep": map[string]interface{}{"value": "found"},
		},
	}

	// Top-level field
	val, ok := resolveJSONPath(data, "items")
	if !ok || val == nil {
		t.Error("items should exist")
	}

	// Array field
	arr, ok2 := val.([]interface{})
	if !ok2 || len(arr) != 2 {
		t.Errorf("items: got %v", val)
	}

	// Deep nested
	val, ok = resolveJSONPath(data, "nested.deep.value")
	if !ok || val != "found" {
		t.Errorf("deep nested = %v, want found", val)
	}

	// Missing key
	val, ok = resolveJSONPath(data, "missing.path")
	if ok {
		t.Errorf("missing path should return false, got %v", val)
	}
}

func TestSubRecipeDepth(t *testing.T) {
	ctx := context.Background()
	if d := getSubRecipeDepth(ctx); d != 0 {
		t.Errorf("initial depth = %d, want 0", d)
	}
	ctx1 := withSubRecipeDepth(ctx, 3)
	if d := getSubRecipeDepth(ctx1); d != 3 {
		t.Errorf("after set(3) = %d, want 3", d)
	}
	// original ctx unchanged
	if d := getSubRecipeDepth(ctx); d != 0 {
		t.Errorf("original should stay 0, got %d", d)
	}
}

func TestSkipStepTransitive(t *testing.T) {
	e := &Engine{}
	stepMap := map[string]*models.Step{
		"router": {Name: "router", Kind: models.StepRouter},
		"A":      {Name: "A", Kind: models.StepAgent, DependsOn: []string{"router"}},
		"B":      {Name: "B", Kind: models.StepAgent, DependsOn: []string{"router"}},
		"C":      {Name: "C", Kind: models.StepAgent, DependsOn: []string{"router"}},
		"D":      {Name: "D", Kind: models.StepAgent, DependsOn: []string{"B", "C"}},
	}
	deps := map[string][]string{
		"router": {"A", "B", "C"},
		"B":      {"D"},
		"C":      {"D"},
	}
	done := map[string]*models.StepResult{
		"router": {StepName: "router", Status: "completed", BranchTaken: "A"},
	}
	skip := map[string]bool{}
	var results []models.StepResult

	e.skipStepTransitive("B", skip, done, deps, stepMap, &results)
	e.skipStepTransitive("C", skip, done, deps, stepMap, &results)

	if !skip["B"] {
		t.Error("B should be skipped")
	}
	if !skip["C"] {
		t.Error("C should be skipped")
	}
	if !skip["D"] {
		t.Error("D should be skipped (all parents skipped)")
	}
	if skip["A"] {
		t.Error("A should NOT be skipped")
	}
}

func TestSkipMixedDeps(t *testing.T) {
	e := &Engine{}
	stepMap := map[string]*models.Step{
		"A": {Name: "A", Kind: models.StepAgent},
		"B": {Name: "B", Kind: models.StepAgent},
		"D": {Name: "D", Kind: models.StepAgent, DependsOn: []string{"A", "B"}},
	}
	deps := map[string][]string{
		"A": {"D"},
		"B": {"D"},
	}
	done := map[string]*models.StepResult{
		"A": {StepName: "A", Status: "completed"},
	}
	skip := map[string]bool{}
	var results []models.StepResult

	// B is skipped but A completed normally
	e.skipStepTransitive("B", skip, done, deps, stepMap, &results)

	if skip["D"] {
		t.Error("D should NOT be skipped — A completed normally")
	}
	if !skip["B"] {
		t.Error("B should be skipped")
	}
}

// ── Helpers ─────────────────────────────────────────────────

func setupTestEngine(t *testing.T, srv *httptest.Server) *Engine {
	t.Helper()
	mem := store.NewMemoryStore()
	_ = mem.Migrate(context.Background())
	base := "http://localhost:8080"
	if srv != nil {
		base = srv.URL
	}
	return NewEngine(mem, nil, base)
}

func fakeAgentServer(outputs map[string]map[string]interface{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segs := splitPath(r.URL.Path)
		agentName := "unknown"
		// Engine sends to /agents/{name}/a2a — agent name is at index 1
		if len(segs) >= 2 {
			agentName = segs[1]
		}
		out := outputs[agentName]
		if out == nil {
			out = map[string]interface{}{"result": "ok"}
		}
		// The engine stores the entire decoded JSON body as result.Output
		// (result.Output = rpcResp), so return agent output fields at
		// the top level so branch expressions can reference them directly.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}))
}

func splitPath(path string) []string {
	var segs []string
	cur := ""
	for _, ch := range path {
		if ch == '/' {
			if cur != "" {
				segs = append(segs, cur)
			}
			cur = ""
		} else {
			cur += string(ch)
		}
	}
	if cur != "" {
		segs = append(segs, cur)
	}
	return segs
}

func waitForRun(t *testing.T, eng *Engine, runID string, timeoutSec int) *models.RecipeRun {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < timeoutSec*5; i++ {
		time.Sleep(200 * time.Millisecond)
		run, err := eng.store.GetRecipeRun(ctx, runID)
		if err != nil {
			t.Fatalf("GetRecipeRun: %v", err)
		}
		if run.Status == models.RecipeRunCompleted || run.Status == models.RecipeRunFailed {
			return run
		}
	}
	t.Fatal("timeout waiting for run to finish")
	return nil
}

// ── Integration Tests ───────────────────────────────────────

func TestExecuteRecipeForwardsTriggerAPIKeyToAgentCalls(t *testing.T) {
	var (
		mu       sync.Mutex
		seenAuth string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth = r.Header.Get("Authorization")
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"result": map[string]interface{}{
				"status": map[string]interface{}{"state": "completed"},
			},
			"id": "1",
		})
	}))
	defer srv.Close()

	eng := setupTestEngine(t, srv)
	ctx := context.Background()

	eng.store.CreateAgent(ctx, &models.Agent{
		Name:        "auth-agent",
		Kitchen:     "default",
		Status:      models.AgentStatusReady,
		A2AEndpoint: fmt.Sprintf("%s/api/v1/agents/%s/invoke", srv.URL, "auth-agent"),
	})

	recipe := &models.Recipe{
		ID:      "auth-forwarding",
		Name:    "auth-forwarding",
		Kitchen: "default",
		Steps: []models.Step{
			{Name: "call", Kind: models.StepAgent, AgentRef: "auth-agent"},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	triggerCtx := WithExecutionAPIKey(ctx, "trigger-key-123")
	runID, err := eng.ExecuteRecipe(triggerCtx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}

	run := waitForRun(t, eng, runID, 10)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("run status = %s, error = %s", run.Status, run.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if seenAuth != "Bearer trigger-key-123" {
		t.Fatalf("authorization header = %q, want %q", seenAuth, "Bearer trigger-key-123")
	}
}

func TestRoutingSkipsNonTaken(t *testing.T) {
	outputs := map[string]map[string]interface{}{
		"triage":          {"category": "billing"},
		"billing-handler": {"response": "refund issued"},
		"tech-handler":    {"response": "fixed"},
		"general-handler": {"response": "answered"},
		"qa":              {"verdict": "pass"},
	}
	srv := fakeAgentServer(outputs)
	defer srv.Close()

	eng := setupTestEngine(t, srv)
	ctx := context.Background()
	for name := range outputs {
		eng.store.CreateAgent(ctx, &models.Agent{
			Name:        name,
			Kitchen:     "default",
			Status:      models.AgentStatusReady,
			A2AEndpoint: fmt.Sprintf("%s/api/v1/agents/%s/invoke", srv.URL, name),
		})
	}

	recipe := &models.Recipe{
		ID: "routing-test", Name: "routing-test", Kitchen: "default",
		Steps: []models.Step{
			{
				Name: "triage", Kind: models.StepRouter, AgentRef: "triage",
				Branches: []models.Branch{
					{Condition: `category == "billing"`, NextStep: "billing-handler"},
					{Condition: `category == "technical"`, NextStep: "tech-handler"},
				},
				DefaultNext: "general-handler",
			},
			{Name: "billing-handler", Kind: models.StepAgent, AgentRef: "billing-handler", DependsOn: []string{"triage"}},
			{Name: "tech-handler", Kind: models.StepAgent, AgentRef: "tech-handler", DependsOn: []string{"triage"}},
			{Name: "general-handler", Kind: models.StepAgent, AgentRef: "general-handler", DependsOn: []string{"triage"}},
			{Name: "qa", Kind: models.StepAgent, AgentRef: "qa", DependsOn: []string{"billing-handler", "tech-handler", "general-handler"}},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", map[string]interface{}{"ticket": "refund please"}, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}

	run := waitForRun(t, eng, runID, 15)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("run status = %s, error = %s", run.Status, run.Error)
	}

	sm := make(map[string]string)
	for _, sr := range run.StepResults {
		sm[sr.StepName] = sr.Status
	}

	if sm["billing-handler"] != "completed" {
		t.Errorf("billing-handler = %s, want completed", sm["billing-handler"])
	}
	if sm["tech-handler"] != "skipped" {
		t.Errorf("tech-handler = %s, want skipped", sm["tech-handler"])
	}
	if sm["general-handler"] != "skipped" {
		t.Errorf("general-handler = %s, want skipped", sm["general-handler"])
	}
	if sm["qa"] != "completed" {
		t.Errorf("qa = %s, want completed", sm["qa"])
	}
}

func TestChainingPattern(t *testing.T) {
	outputs := map[string]map[string]interface{}{
		"a": {"r": "alpha"},
		"b": {"r": "bravo"},
		"c": {"r": "charlie"},
	}
	srv := fakeAgentServer(outputs)
	defer srv.Close()

	eng := setupTestEngine(t, srv)
	ctx := context.Background()
	for n := range outputs {
		eng.store.CreateAgent(ctx, &models.Agent{
			Name: n, Kitchen: "default", Status: models.AgentStatusReady,
			A2AEndpoint: fmt.Sprintf("%s/api/v1/agents/%s/invoke", srv.URL, n),
		})
	}

	recipe := &models.Recipe{
		ID: "chain", Name: "chain", Kitchen: "default",
		Steps: []models.Step{
			{Name: "a", Kind: models.StepAgent, AgentRef: "a"},
			{Name: "b", Kind: models.StepAgent, AgentRef: "b", DependsOn: []string{"a"}},
			{Name: "c", Kind: models.StepAgent, AgentRef: "c", DependsOn: []string{"b"}},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	run := waitForRun(t, eng, runID, 15)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("status = %s, err = %s", run.Status, run.Error)
	}
	if len(run.StepResults) != 3 {
		t.Errorf("expected 3 results, got %d", len(run.StepResults))
	}
	for _, sr := range run.StepResults {
		if sr.Status != "completed" {
			t.Errorf("step %s = %s, want completed", sr.StepName, sr.Status)
		}
	}
}

func TestParallelPattern(t *testing.T) {
	var mu sync.Mutex
	invoked := make(map[string]time.Time)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segs := splitPath(r.URL.Path)
		name := "unknown"
		// Engine sends to /agents/{name}/a2a — agent name is at index 1
		if len(segs) >= 2 {
			name = segs[1]
		}
		mu.Lock()
		invoked[name] = time.Now()
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "completed",
			"output": map[string]interface{}{"r": name},
		})
	}))
	defer srv.Close()

	eng := setupTestEngine(t, srv)
	ctx := context.Background()
	for _, n := range []string{"start", "a", "b", "c", "end"} {
		eng.store.CreateAgent(ctx, &models.Agent{
			Name: n, Kitchen: "default", Status: models.AgentStatusReady,
			A2AEndpoint: fmt.Sprintf("%s/api/v1/agents/%s/invoke", srv.URL, n),
		})
	}

	recipe := &models.Recipe{
		ID: "par", Name: "par", Kitchen: "default",
		Steps: []models.Step{
			{Name: "start", Kind: models.StepAgent, AgentRef: "start"},
			{Name: "a", Kind: models.StepAgent, AgentRef: "a", DependsOn: []string{"start"}},
			{Name: "b", Kind: models.StepAgent, AgentRef: "b", DependsOn: []string{"start"}},
			{Name: "c", Kind: models.StepAgent, AgentRef: "c", DependsOn: []string{"start"}},
			{Name: "end", Kind: models.StepAgent, AgentRef: "end", DependsOn: []string{"a", "b", "c"}},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	run := waitForRun(t, eng, runID, 15)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("status = %s", run.Status)
	}

	mu.Lock()
	tA, tB, tC := invoked["a"], invoked["b"], invoked["c"]
	mu.Unlock()
	maxDiff := 500 * time.Millisecond
	if tB.Sub(tA).Abs() > maxDiff || tC.Sub(tA).Abs() > maxDiff {
		t.Errorf("parallel steps should start within %v of each other", maxDiff)
	}
}

func TestEmptyRecipe(t *testing.T) {
	eng := setupTestEngine(t, nil)
	recipe := &models.Recipe{
		ID: "empty", Name: "empty", Kitchen: "default",
		Steps: []models.Step{},
	}
	eng.store.CreateRecipe(context.Background(), recipe)

	runID, err := eng.ExecuteRecipe(context.Background(), recipe, "default", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	run := waitForRun(t, eng, runID, 5)
	if run.Status != models.RecipeRunCompleted {
		t.Errorf("empty recipe status = %s, want completed", run.Status)
	}
}

// ── Per-step auth key tests ────────────────────────────────

func TestExecuteRecipeUsesLiteralAuthKey(t *testing.T) {
	var (
		mu       sync.Mutex
		seenAuth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"result":  map[string]interface{}{"status": map[string]interface{}{"state": "completed"}},
			"id":      "1",
		})
	}))
	defer srv.Close()

	eng := setupTestEngine(t, srv)
	ctx := context.Background()
	eng.store.CreateAgent(ctx, &models.Agent{
		Name:        "lit-agent",
		Kitchen:     "default",
		Status:      models.AgentStatusReady,
		A2AEndpoint: fmt.Sprintf("%s/api/v1/agents/%s/invoke", srv.URL, "lit-agent"),
	})
	recipe := &models.Recipe{
		ID:      "literal-auth",
		Name:    "literal-auth",
		Kitchen: "default",
		Steps: []models.Step{
			{Name: "call", Kind: models.StepAgent, AgentRef: "lit-agent", AuthKey: "step-key-abc"},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	// Context has a different key — step.AuthKey must win.
	triggerCtx := WithExecutionAPIKey(ctx, "trigger-key")
	runID, err := eng.ExecuteRecipe(triggerCtx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}
	run := waitForRun(t, eng, runID, 10)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("run status = %s, error = %s", run.Status, run.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if seenAuth != "Bearer step-key-abc" {
		t.Fatalf("authorization header = %q, want %q", seenAuth, "Bearer step-key-abc")
	}
}

func TestExecuteRecipeResolvesAuthKeyRef(t *testing.T) {
	var (
		mu       sync.Mutex
		seenAuth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"result":  map[string]interface{}{"status": map[string]interface{}{"state": "completed"}},
			"id":      "1",
		})
	}))
	defer srv.Close()

	eng := setupTestEngine(t, srv)
	ctx := context.Background()
	eng.store.CreateAgent(ctx, &models.Agent{
		Name:        "ref-agent",
		Kitchen:     "default",
		Status:      models.AgentStatusReady,
		A2AEndpoint: fmt.Sprintf("%s/api/v1/agents/%s/invoke", srv.URL, "ref-agent"),
	})
	// Seed the credential in the store.
	eng.store.CreateKitchenCredential(ctx, &models.KitchenCredential{
		ID:      "cred-1",
		Kitchen: "default",
		Name:    "my-cred",
		Value:   "cred-key-xyz",
	})
	recipe := &models.Recipe{
		ID:      "ref-auth",
		Name:    "ref-auth",
		Kitchen: "default",
		Steps: []models.Step{
			{Name: "call", Kind: models.StepAgent, AgentRef: "ref-agent", AuthKeyRef: "my-cred"},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	triggerCtx := WithExecutionAPIKey(ctx, "trigger-key")
	runID, err := eng.ExecuteRecipe(triggerCtx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}
	run := waitForRun(t, eng, runID, 10)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("run status = %s, error = %s", run.Status, run.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if seenAuth != "Bearer cred-key-xyz" {
		t.Fatalf("authorization header = %q, want %q", seenAuth, "Bearer cred-key-xyz")
	}
}

func TestExecuteRecipeFallsBackToContextKey(t *testing.T) {
	var (
		mu       sync.Mutex
		seenAuth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"result":  map[string]interface{}{"status": map[string]interface{}{"state": "completed"}},
			"id":      "1",
		})
	}))
	defer srv.Close()

	eng := setupTestEngine(t, srv)
	ctx := context.Background()
	eng.store.CreateAgent(ctx, &models.Agent{
		Name:        "fallback-agent",
		Kitchen:     "default",
		Status:      models.AgentStatusReady,
		A2AEndpoint: fmt.Sprintf("%s/api/v1/agents/%s/invoke", srv.URL, "fallback-agent"),
	})
	recipe := &models.Recipe{
		ID:      "fallback-auth",
		Name:    "fallback-auth",
		Kitchen: "default",
		Steps: []models.Step{
			// Neither AuthKey nor AuthKeyRef set → falls back to context key.
			{Name: "call", Kind: models.StepAgent, AgentRef: "fallback-agent"},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	triggerCtx := WithExecutionAPIKey(ctx, "trigger-key")
	runID, err := eng.ExecuteRecipe(triggerCtx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}
	run := waitForRun(t, eng, runID, 10)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("run status = %s, error = %s", run.Status, run.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if seenAuth != "Bearer trigger-key" {
		t.Fatalf("authorization header = %q, want %q", seenAuth, "Bearer trigger-key")
	}
}

// ── Human Gate Tests ─────────────────────────────────────────

// waitForPaused polls until the run reaches paused status or times out.
func waitForPaused(t *testing.T, eng *Engine, runID string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, _ := eng.store.GetRecipeRun(ctx, runID)
		if run != nil && run.Status == models.RecipeRunPaused {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timeout waiting for run to pause at human_gate")
}

// TestHumanGateApprove: recipe pauses at a human_gate step and resumes after approval.
func TestHumanGateApprove(t *testing.T) {
	outputs := map[string]map[string]interface{}{
		"before": {"status": "ready"},
		"after":  {"status": "done"},
	}
	srv := fakeAgentServer(outputs)
	defer srv.Close()

	eng := setupTestEngine(t, srv)
	ctx := context.Background()
	for n := range outputs {
		eng.store.CreateAgent(ctx, &models.Agent{
			Name:        n,
			Kitchen:     "default",
			Status:      models.AgentStatusReady,
			A2AEndpoint: fmt.Sprintf("%s/api/v1/agents/%s/invoke", srv.URL, n),
		})
	}

	recipe := &models.Recipe{
		ID: "gate-approve", Name: "gate-approve", Kitchen: "default",
		Steps: []models.Step{
			{Name: "before", Kind: models.StepAgent, AgentRef: "before"},
			{Name: "gate", Kind: models.StepHumanGate, DependsOn: []string{"before"}},
			{Name: "after", Kind: models.StepAgent, AgentRef: "after", DependsOn: []string{"gate"}},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}

	waitForPaused(t, eng, runID)

	ok, err := eng.ApproveGateWithMetadata(runID, "gate", true, "uid-1", "approver@example.com", "", "dashboard", "LGTM")
	if err != nil {
		t.Fatalf("ApproveGateWithMetadata error: %v", err)
	}
	if !ok {
		t.Fatal("ApproveGateWithMetadata returned false, expected true")
	}

	finished := waitForRun(t, eng, runID, 15)
	if finished.Status != models.RecipeRunCompleted {
		t.Fatalf("run status = %s, error = %s", finished.Status, finished.Error)
	}

	for _, sr := range finished.StepResults {
		if sr.StepName == "gate" {
			if sr.GateStatus != "approved" {
				t.Errorf("gate_status = %s, want approved", sr.GateStatus)
			}
			if sr.Output["approver_email"] != "approver@example.com" {
				t.Errorf("approver_email = %v", sr.Output["approver_email"])
			}
			return
		}
	}
	t.Error("gate step not found in step_results")
}

// TestHumanGateReject: recipe fails when the gate is rejected.
func TestHumanGateReject(t *testing.T) {
	outputs := map[string]map[string]interface{}{
		"before": {"status": "ready"},
	}
	srv := fakeAgentServer(outputs)
	defer srv.Close()

	eng := setupTestEngine(t, srv)
	ctx := context.Background()
	eng.store.CreateAgent(ctx, &models.Agent{
		Name:        "before",
		Kitchen:     "default",
		Status:      models.AgentStatusReady,
		A2AEndpoint: fmt.Sprintf("%s/api/v1/agents/%s/invoke", srv.URL, "before"),
	})

	recipe := &models.Recipe{
		ID: "gate-reject", Name: "gate-reject", Kitchen: "default",
		Steps: []models.Step{
			{Name: "before", Kind: models.StepAgent, AgentRef: "before"},
			{Name: "gate", Kind: models.StepHumanGate, DependsOn: []string{"before"}},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}

	waitForPaused(t, eng, runID)

	ok, err := eng.ApproveGateWithMetadata(runID, "gate", false, "uid-2", "rejector@example.com", "", "email", "not ready")
	if err != nil {
		t.Fatalf("ApproveGateWithMetadata reject error: %v", err)
	}
	if !ok {
		t.Fatal("ApproveGateWithMetadata returned false")
	}

	finished := waitForRun(t, eng, runID, 15)
	if finished.Status != models.RecipeRunFailed {
		t.Fatalf("run should have failed on rejection, got %s", finished.Status)
	}
	for _, sr := range finished.StepResults {
		if sr.StepName == "gate" && sr.GateStatus != "rejected" {
			t.Errorf("gate_status = %s, want rejected", sr.GateStatus)
		}
	}
}

// TestHumanGateSLATimeout: gate times out when max_wait_minutes elapses.
// Uses a very small SLA to keep the test fast.
func TestHumanGateSLATimeout(t *testing.T) {
	eng := setupTestEngine(t, nil)
	ctx := context.Background()

	// 0.001 minutes ≈ 60ms — enough for CI.
	recipe := &models.Recipe{
		ID: "gate-sla", Name: "gate-sla", Kitchen: "default",
		Steps: []models.Step{
			{
				Name:   "gate",
				Kind:   models.StepHumanGate,
				Config: map[string]interface{}{"max_wait_minutes": 0.001},
			},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}

	finished := waitForRun(t, eng, runID, 15)
	if finished.Status != models.RecipeRunFailed {
		t.Fatalf("expected failed on SLA breach, got %s", finished.Status)
	}
}

// ── Approver Constraint Tests ────────────────────────────────

// TestApproverEmailAllowList: only callers in ApproverEmails may approve.
func TestApproverEmailAllowList(t *testing.T) {
	eng := setupTestEngine(t, nil)
	ctx := context.Background()

	recipe := &models.Recipe{
		ID: "gate-email-acl", Name: "gate-email-acl", Kitchen: "default",
		Steps: []models.Step{
			{
				Name:           "gate",
				Kind:           models.StepHumanGate,
				ApproverEmails: []string{"alice@corp.com", "bob@corp.com"},
			},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}
	waitForPaused(t, eng, runID)

	// Unauthorized caller.
	_, err = eng.ApproveGateWithMetadata(runID, "gate", true, "uid-x", "mallory@evil.com", "", "api", "")
	if !errors.Is(err, ErrApproverUnauthorized) {
		t.Errorf("expected ErrApproverUnauthorized, got %v", err)
	}

	// Authorized caller.
	ok, err := eng.ApproveGateWithMetadata(runID, "gate", true, "uid-a", "alice@corp.com", "", "api", "")
	if err != nil {
		t.Fatalf("alice should be allowed: %v", err)
	}
	if !ok {
		t.Fatal("ApproveGateWithMetadata returned false for alice")
	}
	waitForRun(t, eng, runID, 15)
}

// TestApproverDomainConstraint: caller must have an email matching ApproverDomain.
func TestApproverDomainConstraint(t *testing.T) {
	eng := setupTestEngine(t, nil)
	ctx := context.Background()

	recipe := &models.Recipe{
		ID: "gate-domain", Name: "gate-domain", Kitchen: "default",
		Steps: []models.Step{
			{Name: "gate", Kind: models.StepHumanGate, ApproverDomain: "corp.com"},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}
	waitForPaused(t, eng, runID)

	// Wrong domain.
	_, err = eng.ApproveGateWithMetadata(runID, "gate", true, "uid-x", "user@otherdomain.io", "", "api", "")
	if !errors.Is(err, ErrApproverUnauthorized) {
		t.Errorf("expected ErrApproverUnauthorized for wrong domain, got %v", err)
	}

	// Correct domain.
	ok, err := eng.ApproveGateWithMetadata(runID, "gate", true, "uid-y", "user@corp.com", "", "api", "ok")
	if err != nil {
		t.Fatalf("corp.com user should be allowed: %v", err)
	}
	if !ok {
		t.Fatal("ApproveGateWithMetadata returned false for domain-matched user")
	}
	waitForRun(t, eng, runID, 15)
}

// TestApproverSameTenant: RequireSameTenant blocks callers with a different OIDC issuer.
func TestApproverSameTenant(t *testing.T) {
	eng := setupTestEngine(t, nil)
	eng.SetOIDCIssuer("https://login.microsoftonline.com/tenant-abc/v2.0")
	ctx := context.Background()

	recipe := &models.Recipe{
		ID: "gate-tenant", Name: "gate-tenant", Kitchen: "default",
		Steps: []models.Step{
			{Name: "gate", Kind: models.StepHumanGate, RequireSameTenant: true},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}
	waitForPaused(t, eng, runID)

	// Different tenant issuer — rejected.
	_, err = eng.ApproveGateWithMetadata(runID, "gate", true, "uid-x", "user@other.com",
		"https://accounts.google.com", "api", "")
	if !errors.Is(err, ErrApproverUnauthorized) {
		t.Errorf("expected ErrApproverUnauthorized for cross-tenant issuer, got %v", err)
	}

	// Same tenant — allowed.
	ok, err := eng.ApproveGateWithMetadata(runID, "gate", true, "uid-y", "user@tenant.com",
		"https://login.microsoftonline.com/tenant-abc/v2.0", "api", "approved")
	if err != nil {
		t.Fatalf("same-tenant user should be allowed: %v", err)
	}
	if !ok {
		t.Fatal("ApproveGateWithMetadata returned false for same-tenant user")
	}
	waitForRun(t, eng, runID, 15)
}

// TestApproverSameTenantEmailOverride: explicit email allow-list bypasses same-tenant check.
func TestApproverSameTenantEmailOverride(t *testing.T) {
	eng := setupTestEngine(t, nil)
	eng.SetOIDCIssuer("https://login.microsoftonline.com/tenant-abc/v2.0")
	ctx := context.Background()

	recipe := &models.Recipe{
		ID: "gate-tenant-override", Name: "gate-tenant-override", Kitchen: "default",
		Steps: []models.Step{
			{
				Name:              "gate",
				Kind:              models.StepHumanGate,
				RequireSameTenant: true,
				ApproverEmails:    []string{"external@partner.com"},
			},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}
	waitForPaused(t, eng, runID)

	// External partner explicitly listed — allowed despite cross-tenant issuer.
	ok, err := eng.ApproveGateWithMetadata(runID, "gate", true, "uid-ext", "external@partner.com",
		"https://accounts.google.com", "api", "approved by partner")
	if err != nil {
		t.Fatalf("explicitly listed external user should be allowed: %v", err)
	}
	if !ok {
		t.Fatal("ApproveGateWithMetadata returned false for explicitly listed external user")
	}
	waitForRun(t, eng, runID, 15)
}

// ── CancelRun Test ────────────────────────────────────────────

// TestCancelRun: canceling a run mid-flight terminates execution.
func TestCancelRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"result": "slow"})
	}))
	defer srv.Close()

	eng := setupTestEngine(t, srv)
	ctx := context.Background()
	eng.store.CreateAgent(ctx, &models.Agent{
		Name:        "slow",
		Kitchen:     "default",
		Status:      models.AgentStatusReady,
		A2AEndpoint: fmt.Sprintf("%s/api/v1/agents/%s/invoke", srv.URL, "slow"),
	})

	recipe := &models.Recipe{
		ID: "cancel-test", Name: "cancel-test", Kitchen: "default",
		Steps: []models.Step{
			{Name: "slow", Kind: models.StepAgent, AgentRef: "slow"},
		},
	}
	eng.store.CreateRecipe(ctx, recipe)

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil, "")
	if err != nil {
		t.Fatalf("ExecuteRecipe: %v", err)
	}

	// Give the engine a moment to reach the slow step, then cancel.
	time.Sleep(300 * time.Millisecond)
	if !eng.CancelRun(runID) {
		t.Fatal("CancelRun returned false — run not found")
	}

	finished := waitForRun(t, eng, runID, 15)
	if finished.Status != models.RecipeRunFailed && finished.Status != models.RecipeRunCanceled {
		t.Errorf("expected failed/canceled after CancelRun, got %s", finished.Status)
	}
}
