// Package kitchentest is the cross-kitchen test of the community routes, shared by both editions so the
// same walk runs against the in-memory store (OSS) and PostgreSQL (Pro, where kitchens are rows with an id
// and a record may hold either). It is test support: it imports "testing" and is not used by any server.
//
// A record named by an id alone belongs to a kitchen all the same. RunByID creates records in a kitchen,
// calls every by-id route as a member of another kitchen and expects each to be refused as if the record did
// not exist; the owner must still reach its own. RunRecipeFlow does the same for a recipe run that really
// paused at a human gate (get, cancel, rerun, both gate-approve routes).
package kitchentest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/server"
)

// APIKey is the credential Do sends. The caller sets AGENTOVEN_API_KEYS to it, and AGENTOVEN_REQUIRE_AUTH to
// true, before it builds the server.
const APIKey = "test-key"

// Call is one request.
type Call struct{ Method, Path, Body string }

// Do sends a request as the API key in the given kitchen.
func Do(h http.Handler, kitchen, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+APIKey)
	req.Header.Set("X-Kitchen", kitchen)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// IDs are the records Seed creates in the owning kitchen.
type IDs struct {
	Owner, Other                           string // kitchen names
	Recipe, Run, GateKey                   string
	Session, Trace, Span, Score, View, Key string
}

// ByIDRoutes maps each by-id route to the call that names a record of ids.Owner.
func ByIDRoutes(id IDs) map[string]Call {
	return map[string]Call{
		"GET /api/v1/recipes/{recipeName}/runs/{runId}":                           {"GET", "/api/v1/recipes/" + id.Recipe + "/runs/" + id.Run, ""},
		"POST /api/v1/recipes/{recipeName}/runs/{runId}/cancel":                   {"POST", "/api/v1/recipes/" + id.Recipe + "/runs/" + id.Run + "/cancel", ""},
		"POST /api/v1/recipes/{recipeName}/runs/{runId}/rerun":                    {"POST", "/api/v1/recipes/" + id.Recipe + "/runs/" + id.Run + "/rerun", `{}`},
		"POST /api/v1/recipes/{recipeName}/runs/{runId}/gates/{stepName}/approve": {"POST", "/api/v1/recipes/" + id.Recipe + "/runs/" + id.Run + "/gates/gate/approve", `{"approved":true}`},
		"POST /api/v1/approvals/{runId}/{stepName}":                               {"POST", "/api/v1/approvals/" + id.Run + "/gate", `{"approved":true}`},
		"GET /api/v1/approvals/{gateKey}":                                         {"GET", "/api/v1/approvals/" + id.GateKey, ""},
		"GET /api/v1/agents/{agentName}/sessions/{sessionID}":                     {"GET", "/api/v1/agents/bot/sessions/" + id.Session, ""},
		"DELETE /api/v1/agents/{agentName}/sessions/{sessionID}":                  {"DELETE", "/api/v1/agents/bot/sessions/" + id.Session, ""},
		"POST /api/v1/agents/{agentName}/sessions/{sessionID}/messages":           {"POST", "/api/v1/agents/bot/sessions/" + id.Session + "/messages", `{"role":"user","content":"hi"}`},
		"GET /api/v1/traces/{traceId}":                                            {"GET", "/api/v1/traces/" + id.Trace, ""},
		"PATCH /api/v1/traces/{traceId}":                                          {"PATCH", "/api/v1/traces/" + id.Trace, `{"tags":["x"]}`},
		"GET /api/v1/traces/{traceId}/spans":                                      {"GET", "/api/v1/traces/" + id.Trace + "/spans", ""},
		"GET /api/v1/traces/{traceId}/timeline":                                   {"GET", "/api/v1/traces/" + id.Trace + "/timeline", ""},
		"GET /api/v1/traces/{traceId}/audit":                                      {"GET", "/api/v1/traces/" + id.Trace + "/audit", ""},
		"GET /api/v1/traces/{traceId}/scores":                                     {"GET", "/api/v1/traces/" + id.Trace + "/scores", ""},
		"POST /api/v1/traces/{traceId}/scores":                                    {"POST", "/api/v1/traces/" + id.Trace + "/scores", `{"name":"q","value":1}`},
		"DELETE /api/v1/traces/{traceId}/scores/{scoreId}":                        {"DELETE", "/api/v1/traces/" + id.Trace + "/scores/" + id.Score, ""},
		"GET /api/v1/spans/{spanId}":                                              {"GET", "/api/v1/spans/" + id.Span, ""},
		"PUT /api/v1/trace-views/{viewId}":                                        {"PUT", "/api/v1/trace-views/" + id.View, `{"name":"x","filter":{}}`},
		"DELETE /api/v1/trace-views/{viewId}":                                     {"DELETE", "/api/v1/trace-views/" + id.View, ""},
		"GET /api/v1/keys/{keyID}":                                                {"GET", "/api/v1/keys/" + id.Key, ""},
		"GET /api/v1/keys/{keyID}/usage":                                          {"GET", "/api/v1/keys/" + id.Key + "/usage", ""},
		"POST /api/v1/keys/{keyID}/revoke":                                        {"POST", "/api/v1/keys/" + id.Key + "/revoke", ""},
		"DELETE /api/v1/keys/{keyID}":                                             {"DELETE", "/api/v1/keys/" + id.Key, ""},
		"POST /api/v1/agents/{agentName}/runs/{traceID}/resume":                   {"POST", "/api/v1/agents/bot/runs/" + id.Trace + "/resume", `{}`},
	}
}

// ByNamePrefixes are the route prefixes whose parameter is a name looked up together with the request's kitchen.
var ByNamePrefixes = []string{
	"/api/v1/agents/{agentName}", "/api/v1/recipes/{recipeName}", "/api/v1/models/providers/{providerName}",
	"/api/v1/tools/{toolName}", "/api/v1/skills/{name}", "/api/v1/prompts/{promptName}", "/api/v1/channels/{channelName}",
}

// OtherParamRoutes are the routes with a path parameter that are not covered by ByIDRoutes, with why.
var OtherParamRoutes = map[string]string{
	"GET /api/v1/blobs/{ref}":                                                 "keyed by (kitchen, ref): handlers.TestOnlyTheOwningKitchenCanReadOrDeleteABlob",
	"DELETE /api/v1/blobs/{ref}":                                              "keyed by (kitchen, ref): handlers.TestOnlyTheOwningKitchenCanReadOrDeleteABlob",
	"GET /api/v1/tasks/{taskID}":                                              "Tasks.Get takes the kitchen: handlers.TestTasksBelongToTheirKitchen",
	"GET /api/v1/tasks/{taskID}/events":                                       "Tasks.Get takes the kitchen: handlers.TestTasksBelongToTheirKitchen",
	"POST /api/v1/tasks/{taskID}/cancel":                                      "Tasks.Cancel takes the kitchen: handlers.TestTasksBelongToTheirKitchen",
	"POST /api/v1/tasks/{taskID}/resume":                                      "Tasks.Get takes the kitchen: handlers.TestTasksBelongToTheirKitchen",
	"POST /api/v1/recipes/{recipeName}/runs/{runId}/steps/{stepName}/release": "a signed decision whose describer checks the gate's kitchen (signing.go)",
	"DELETE /api/v1/admin/cross-kitchen-grants/{grantId}":                     "platform administrator only (callerAdminsPlatformFor)",
	"GET /api/v1/kitchens/{kitchenId}":                                        "the kitchens API is Pro's (kitchens.ownKitchen); the community route is shadowed",
	"DELETE /api/v1/kitchens/{kitchenId}":                                     "the kitchens API is Pro's (kitchens.ownKitchen); the community route is shadowed",
	"DELETE /api/v1/rag/providers/{name}":                                     "the RAG provider registry is not kitchen data: a platform integration list",
	"GET /api/v1/agents/{agentName}/versions/{version}":                       "looked up by (kitchen, agent, version)",
	"GET /api/v1/prompts/{promptName}/versions/{version}":                     "looked up by (kitchen, prompt, version)",
	"GET /api/v1/models/catalog/{modelID}":                                    "the public model catalog, not kitchen data",
	"POST /api/v1/embeddings/{driver}/embed":                                  "names a driver, not a record",
}

// Classified reports whether a route with a path parameter has been decided: by id (tested here), by name, or
// listed with a reason. Routes are "METHOD pattern" with no trailing slash.
func Classified(method, route string) bool {
	key := method + " " + route
	if _, ok := ByIDRoutes(IDs{})[key]; ok {
		return true
	}
	if _, ok := OtherParamRoutes[key]; ok {
		return true
	}
	for _, p := range ByNamePrefixes {
		if strings.HasPrefix(route, p) && !strings.Contains(strings.TrimPrefix(route, p), "{") {
			return true
		}
	}
	return false
}

// Seed creates two kitchens and, in the first, one of each record a by-id route names. Names carry a random
// suffix, so a shared database can be used repeatedly.
func Seed(t *testing.T, srv *server.Server) IDs {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	tag := uuid.New().String()[:8]
	id := IDs{
		Owner: "ka-" + tag, Other: "kb-" + tag, Recipe: "r-" + tag, GateKey: "",
		Run: uuid.New().String(), Session: uuid.New().String(), Trace: uuid.New().String(), Span: uuid.New().String(),
		Score: uuid.New().String(), View: uuid.New().String(), Key: uuid.New().String(),
	}
	id.GateKey = id.Run + ":gate"
	s := srv.Store
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{id.Owner, id.Other} {
		must(s.CreateKitchen(ctx, &models.Kitchen{ID: uuid.New().String(), Name: k, Owner: "test", Plan: models.PlanCommunity, CreatedAt: now}))
	}
	recipeID := uuid.New().String()
	must(s.CreateRecipe(ctx, &models.Recipe{ID: recipeID, Name: id.Recipe, Kitchen: id.Owner, CreatedAt: now, UpdatedAt: now}))
	must(s.CreateRecipeRun(ctx, &models.RecipeRun{ID: id.Run, RecipeID: recipeID, Kitchen: id.Owner, Status: models.RecipeRunPaused, StartedAt: now}))
	must(s.CreateApproval(ctx, &models.ApprovalRecord{ID: uuid.New().String(), GateKey: id.GateKey, RunID: id.Run, StepName: "gate", Kitchen: id.Owner, Status: "pending", RequestedAt: now}))
	must(srv.SessionStore.CreateSession(ctx, &models.Session{ID: id.Session, AgentName: "bot", Kitchen: id.Owner, Status: models.SessionActive, CreatedAt: now, UpdatedAt: now}))
	must(s.CreateTrace(ctx, &models.Trace{ID: id.Trace, AgentName: "bot", Kitchen: id.Owner, Status: "completed", CreatedAt: now}))
	must(s.CreateSpan(ctx, &models.Span{ID: id.Span, TraceID: id.Trace, Name: "s", StartTime: now, EndTime: now}))
	must(s.CreateTraceScore(ctx, &models.TraceScore{ID: id.Score, TraceID: id.Trace, Name: "q", Value: 1, Source: "api", CreatedAt: now}))
	must(s.CreateTraceView(ctx, &models.TraceView{ID: id.View, Kitchen: id.Owner, Owner: "someone", Name: "mine", Shared: true, CreatedAt: now}))
	must(s.CreateScopedKey(ctx, &models.ScopedAPIKey{ID: id.Key, KeyHash: id.Key, KeyPrefix: "ao_sk_aa", Kitchen: id.Owner, AgentNames: []string{"*"}, Label: "a", CreatedAt: now, UpdatedAt: now}))
	return id
}

// RunByID is the cross-kitchen walk. The server must be built with APIKey as its only credential.
func RunByID(t *testing.T, srv *server.Server) {
	t.Helper()
	id := Seed(t, srv)
	h := srv.Handler
	calls := ByIDRoutes(id)
	keys := make([]string, 0, len(calls))
	for k := range calls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := calls[k]
		rec := Do(h, id.Other, c.Method, c.Path, c.Body)
		if rec.Code < 400 || rec.Code == http.StatusInternalServerError {
			t.Errorf("%s as another kitchen: %d %s; want a 4xx that does not reveal the record", k, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}

	ctx := context.Background()
	if r, err := srv.Store.GetRecipeRun(ctx, id.Run); err != nil || r.Status != models.RecipeRunPaused {
		t.Errorf("the owner's run was changed by another kitchen: %+v %v", r, err)
	}
	if a, err := srv.Store.GetApproval(ctx, id.GateKey); err != nil || a.Status != "pending" {
		t.Errorf("the owner's approval was decided by another kitchen: %+v %v", a, err)
	}
	if _, err := srv.SessionStore.GetSession(ctx, id.Session); err != nil {
		t.Errorf("the owner's session was deleted by another kitchen: %v", err)
	}
	if sc, _ := srv.Store.ListTraceScores(ctx, id.Trace); len(sc) != 1 {
		t.Errorf("the owner's trace scores changed: %d", len(sc))
	}
	if tr, err := srv.Store.GetTrace(ctx, id.Trace); err != nil || len(tr.Tags) != 0 {
		t.Errorf("the owner's trace was edited by another kitchen: %+v %v", tr, err)
	}
	if k, err := srv.Store.GetScopedKey(ctx, id.Owner, id.Key); err != nil || k.Revoked {
		t.Errorf("the owner's key was revoked or deleted by another kitchen: %v %+v", err, k)
	}
	if v, err := srv.Store.GetTraceView(ctx, id.Owner, id.View); err != nil || v.Name != "mine" {
		t.Errorf("the owner's saved view was changed by another kitchen: %+v %v", v, err)
	}

	// The other kitchen may not list the owner's approvals, however the kitchen is given.
	for _, path := range []string{"/api/v1/approvals", "/api/v1/approvals?kitchen=" + id.Other} {
		if rec := Do(h, id.Other, "GET", path, ""); strings.Contains(rec.Body.String(), id.Run) {
			t.Errorf("GET %s as the other kitchen lists the owner's approvals: %s", path, rec.Body)
		}
	}
	// The owner still reaches its own records.
	for _, k := range []string{"GET /api/v1/recipes/{recipeName}/runs/{runId}", "GET /api/v1/approvals/{gateKey}", "GET /api/v1/agents/{agentName}/sessions/{sessionID}", "GET /api/v1/traces/{traceId}", "GET /api/v1/keys/{keyID}"} {
		c := calls[k]
		if rec := Do(h, id.Owner, c.Method, c.Path, c.Body); rec.Code != http.StatusOK {
			t.Errorf("%s as the owner: %d %s", k, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}
	if rec := Do(h, id.Owner, "GET", "/api/v1/approvals", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), id.Run) {
		t.Errorf("the owner lists its approvals: %d %s", rec.Code, rec.Body)
	}
}

// RunRecipeFlow runs a recipe that pauses at a human gate, as the real engine does, and checks the run family
// from the owner (get, rerun, both gate-approve routes, cancel: all work) and from another kitchen (all 404).
func RunRecipeFlow(t *testing.T, srv *server.Server) {
	t.Helper()
	id := Seed(t, srv)
	h := srv.Handler
	owner := func(method, path, body string) (int, map[string]interface{}) {
		rec := Do(h, id.Owner, method, path, body)
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	newRun := func(name string) (recipe, run string) {
		recipe = name + "-" + uuid.New().String()[:6]
		if c, b := owner("POST", "/api/v1/recipes", fmt.Sprintf(`{"name":%q,"description":"one gate","version":"1.0.0","steps":[{"name":"gate","kind":"human_gate"}]}`, recipe)); c != http.StatusCreated && c != http.StatusOK {
			t.Fatalf("create recipe: %d %v", c, b)
		}
		c, b := owner("POST", "/api/v1/recipes/"+recipe+"/bake", `{"input":"x"}`)
		if c >= 300 {
			t.Fatalf("bake: %d %v", c, b)
		}
		run, _ = b["run_id"].(string)
		if run == "" {
			if r, ok := b["run"].(map[string]interface{}); ok {
				run, _ = r["id"].(string)
			}
		}
		if run == "" {
			t.Fatalf("no run id in %v", b)
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if _, r := owner("GET", "/api/v1/recipes/"+recipe+"/runs/"+run, ""); r != nil {
				if rr, ok := r["run"].(map[string]interface{}); ok && rr["status"] == string(models.RecipeRunPaused) {
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("run %s did not pause at its gate", run)
		return
	}

	recipe, run := newRun("flow-a")
	base := "/api/v1/recipes/" + recipe + "/runs/" + run
	other := func(method, path, body string) int { return Do(h, id.Other, method, path, body).Code }
	// Another kitchen: every route of the family is a 404, and nothing happens to the run.
	for _, c := range []Call{
		{"GET", base, ""}, {"POST", base + "/cancel", ""}, {"POST", base + "/rerun", `{}`},
		{"POST", base + "/gates/gate/approve", `{"approved":true}`}, {"POST", "/api/v1/approvals/" + run + "/gate", `{"approved":true}`},
		{"GET", "/api/v1/approvals/" + run + ":gate", ""},
	} {
		if code := other(c.Method, c.Path, c.Body); code != http.StatusNotFound {
			t.Errorf("another kitchen %s %s: %d, want 404", c.Method, c.Path, code)
		}
	}
	if c, r := owner("GET", base, ""); c != http.StatusOK || r["run"].(map[string]interface{})["status"] != string(models.RecipeRunPaused) {
		t.Fatalf("the owner reads its paused run after the other kitchen's attempts: %d %v", c, r)
	}
	if c, b := owner("GET", "/api/v1/approvals/"+run+":gate", ""); c != http.StatusOK {
		t.Errorf("the owner reads its approval: %d %v", c, b)
	}
	// The owner: the plain gate route (and, on a signing server, the signed one) decides it.
	if c, b := owner("POST", base+"/gates/gate/approve", `{"approved":true}`); c != http.StatusOK {
		t.Errorf("the owner approves its gate: %d %v", c, b)
	}
	// Second run: the other approve route, rerun, cancel.
	_, run2 := newRun("flow-b")
	if c, b := owner("POST", "/api/v1/approvals/"+run2+"/gate", `{"approved":false}`); c != http.StatusOK {
		t.Errorf("the owner rejects through /approvals: %d %v", c, b)
	}
	if c, b := owner("POST", "/api/v1/recipes/"+recipe+"/runs/"+run+"/rerun", `{}`); c >= 300 {
		t.Errorf("the owner reruns: %d %v", c, b)
	}
	recipe3, run3 := newRun("flow-c")
	if c, b := owner("POST", "/api/v1/recipes/"+recipe3+"/runs/"+run3+"/cancel", ""); c != http.StatusOK {
		t.Errorf("the owner cancels: %d %v", c, b)
	}
}
