package workflow

import (
	"context"
	"encoding/json"
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

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", map[string]interface{}{"ticket": "refund please"})
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

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil)
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

	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", nil)
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

	runID, err := eng.ExecuteRecipe(context.Background(), recipe, "default", nil)
	if err != nil {
		t.Fatal(err)
	}

	run := waitForRun(t, eng, runID, 5)
	if run.Status != models.RecipeRunCompleted {
		t.Errorf("empty recipe status = %s, want completed", run.Status)
	}
}
