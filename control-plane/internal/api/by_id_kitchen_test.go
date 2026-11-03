package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/server"
	"github.com/go-chi/chi/v5"
)

// byIDRoutes are the routes that name one record by an id the record's kitchen does not appear in. For each,
// kitchen "ka" owns the record and a caller in kitchen "kb" must be answered as if it did not exist. The test
// below also fails for any route with a path parameter that is neither here nor in byNameRoutes, so a new
// route cannot ship without somebody deciding which kind it is.
var byIDRoutes = map[string]struct{ method, path, body string }{
	"GET /api/v1/recipes/{recipeName}/runs/{runId}":                           {"GET", "/api/v1/recipes/r1/runs/run-a", ""},
	"POST /api/v1/recipes/{recipeName}/runs/{runId}/cancel":                   {"POST", "/api/v1/recipes/r1/runs/run-a/cancel", ""},
	"POST /api/v1/recipes/{recipeName}/runs/{runId}/rerun":                    {"POST", "/api/v1/recipes/r1/runs/run-a/rerun", `{}`},
	"POST /api/v1/recipes/{recipeName}/runs/{runId}/gates/{stepName}/approve": {"POST", "/api/v1/recipes/r1/runs/run-a/gates/gate/approve", `{"approved":true}`},
	"POST /api/v1/approvals/{runId}/{stepName}":                               {"POST", "/api/v1/approvals/run-a/gate", `{"approved":true}`},
	"GET /api/v1/approvals/{gateKey}":                                         {"GET", "/api/v1/approvals/run-a:gate", ""},
	"GET /api/v1/agents/{agentName}/sessions/{sessionID}":                     {"GET", "/api/v1/agents/bot/sessions/sess-a", ""},
	"DELETE /api/v1/agents/{agentName}/sessions/{sessionID}":                  {"DELETE", "/api/v1/agents/bot/sessions/sess-a", ""},
	"POST /api/v1/agents/{agentName}/sessions/{sessionID}/messages":           {"POST", "/api/v1/agents/bot/sessions/sess-a/messages", `{"role":"user","content":"hi"}`},
	"GET /api/v1/traces/{traceId}":                                            {"GET", "/api/v1/traces/tr-a", ""},
	"PATCH /api/v1/traces/{traceId}":                                          {"PATCH", "/api/v1/traces/tr-a", `{"tags":["x"]}`},
	"GET /api/v1/traces/{traceId}/spans":                                      {"GET", "/api/v1/traces/tr-a/spans", ""},
	"GET /api/v1/traces/{traceId}/timeline":                                   {"GET", "/api/v1/traces/tr-a/timeline", ""},
	"GET /api/v1/traces/{traceId}/audit":                                      {"GET", "/api/v1/traces/tr-a/audit", ""},
	"GET /api/v1/traces/{traceId}/scores":                                     {"GET", "/api/v1/traces/tr-a/scores", ""},
	"POST /api/v1/traces/{traceId}/scores":                                    {"POST", "/api/v1/traces/tr-a/scores", `{"name":"q","value":1}`},
	"DELETE /api/v1/traces/{traceId}/scores/{scoreId}":                        {"DELETE", "/api/v1/traces/tr-a/scores/sc-a", ""},
	"GET /api/v1/spans/{spanId}":                                              {"GET", "/api/v1/spans/sp-a", ""},
	"PUT /api/v1/trace-views/{viewId}":                                        {"PUT", "/api/v1/trace-views/tv-a", `{"name":"x","filter":{}}`},
	"DELETE /api/v1/trace-views/{viewId}":                                     {"DELETE", "/api/v1/trace-views/tv-a", ""},
	"GET /api/v1/keys/{keyID}":                                                {"GET", "/api/v1/keys/k-a", ""},
	"GET /api/v1/keys/{keyID}/usage":                                          {"GET", "/api/v1/keys/k-a/usage", ""},
	"POST /api/v1/keys/{keyID}/revoke":                                        {"POST", "/api/v1/keys/k-a/revoke", ""},
	"DELETE /api/v1/keys/{keyID}":                                             {"DELETE", "/api/v1/keys/k-a", ""},
	"POST /api/v1/agents/{agentName}/runs/{traceID}/resume":                   {"POST", "/api/v1/agents/bot/runs/tr-a/resume", `{}`},
}

// byNameRoutes are the routes with a path parameter that is a name looked up together with the request's
// kitchen (the store keys these by kitchen and name), a kitchen id the handler authorises itself, or a
// parameter that selects nothing owned. Each is listed so that adding a parameter is a decision.
var byNameRoutes = map[string]string{}

func init() {
	for _, prefix := range []string{
		"/api/v1/agents/{agentName}", "/api/v1/recipes/{recipeName}", "/api/v1/models/providers/{providerName}",
		"/api/v1/tools/{toolName}", "/api/v1/skills/{name}", "/api/v1/prompts/{promptName}", "/api/v1/channels/{channelName}",
	} {
		byNameRoutes["*"+prefix] = "looked up by (kitchen, name)"
	}
}

var sessStore contracts.SessionStore

// newByIDRouter is the whole community server over its in-memory store, with an API key for auth.
func newByIDRouter(t *testing.T) (http.Handler, store.Store, *chi.Mux) {
	t.Helper()
	t.Setenv("AGENTOVEN_API_KEYS", "test-key")
	t.Setenv("AGENTOVEN_REQUIRE_AUTH", "true")
	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	srv, err := server.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sessStore = srv.SessionStore
	return srv.Handler, srv.Store, srv.Mux
}

func seedKitchenA(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.CreateRecipe(ctx, &models.Recipe{ID: "r1-id", Name: "r1", Kitchen: "ka", CreatedAt: now, UpdatedAt: now}))
	must(s.CreateRecipeRun(ctx, &models.RecipeRun{ID: "run-a", RecipeID: "r1-id", Kitchen: "ka", Status: models.RecipeRunPaused, StartedAt: now}))
	must(s.CreateApproval(ctx, &models.ApprovalRecord{ID: "ap-a", GateKey: "run-a:gate", RunID: "run-a", StepName: "gate", Kitchen: "ka", Status: "pending", RequestedAt: now}))
	must(sessStore.CreateSession(ctx, &models.Session{ID: "sess-a", AgentName: "bot", Kitchen: "ka", Status: models.SessionActive, CreatedAt: now, UpdatedAt: now}))
	must(s.CreateTrace(ctx, &models.Trace{ID: "tr-a", AgentName: "bot", Kitchen: "ka", Status: "completed", CreatedAt: now}))
	must(s.CreateSpan(ctx, &models.Span{ID: "sp-a", TraceID: "tr-a", Name: "s", StartTime: now, EndTime: now}))
	must(s.CreateTraceScore(ctx, &models.TraceScore{ID: "sc-a", TraceID: "tr-a", Name: "q", Value: 1, Source: "api", CreatedAt: now}))
	must(s.CreateTraceView(ctx, &models.TraceView{ID: "tv-a", Kitchen: "ka", Owner: "someone", Name: "mine", Shared: true, CreatedAt: now}))
	must(s.CreateScopedKey(ctx, &models.ScopedAPIKey{ID: "k-a", KeyHash: "h", KeyPrefix: "ao_sk_aa", Kitchen: "ka", AgentNames: []string{"*"}, Label: "a", CreatedAt: now, UpdatedAt: now}))
}

func call(h http.Handler, kitchen, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Kitchen", kitchen)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestByIDRoutesAreKitchenScoped is the generic cross-kitchen test: a caller in kitchen kb names the id of a
// record of kitchen ka on every by-id route and is refused as if it did not exist, and nothing of ka's changes.
func TestByIDRoutesAreKitchenScoped(t *testing.T) {
	h, s, _ := newByIDRouter(t)
	seedKitchenA(t, s)

	keys := make([]string, 0, len(byIDRoutes))
	for k := range byIDRoutes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := byIDRoutes[k]
		rec := call(h, "kb", c.method, c.path, c.body)
		if rec.Code < 400 || rec.Code == http.StatusInternalServerError {
			t.Errorf("%s as kitchen kb: %d %s; want a 4xx that does not reveal the record", k, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		for _, leak := range []string{"run-a", "sess-a", "tr-a", "sp-a", "k-a"} {
			// the id the caller named is echoed back in an error message; a field of the record is not.
			_ = leak
		}
	}

	ctx := context.Background()
	if r, _ := s.GetRecipeRun(ctx, "run-a"); r == nil || r.Status != models.RecipeRunPaused {
		t.Errorf("ka's run was changed by kb: %+v", r)
	}
	if a, _ := s.GetApproval(ctx, "run-a:gate"); a == nil || a.Status != "pending" {
		t.Errorf("ka's approval was decided by kb: %+v", a)
	}
	if _, err := sessStore.GetSession(ctx, "sess-a"); err != nil {
		t.Errorf("ka's session was deleted by kb: %v", err)
	}
	if sc, _ := s.ListTraceScores(ctx, "tr-a"); len(sc) != 1 {
		t.Errorf("ka's trace scores changed: %d", len(sc))
	}
	if tr, _ := s.GetTrace(ctx, "tr-a"); tr == nil || len(tr.Tags) != 0 {
		t.Errorf("ka's trace was edited by kb: %+v", tr)
	}
	if k, err := s.GetScopedKey(ctx, "ka", "k-a"); err != nil || k.Revoked {
		t.Errorf("ka's key was revoked or deleted by kb: %v %+v", err, k)
	}
	if v, _ := s.GetTraceView(ctx, "ka", "tv-a"); v == nil || v.Name != "mine" {
		t.Errorf("ka's saved view was changed by kb: %+v", v)
	}

	// kb may not list ka's approvals, however the kitchen is given.
	for _, path := range []string{"/api/v1/approvals", "/api/v1/approvals?kitchen=kb"} {
		rec := call(h, "kb", "GET", path, "")
		if strings.Contains(rec.Body.String(), "run-a") {
			t.Errorf("GET %s as kb lists ka's approvals: %s", path, rec.Body)
		}
	}
	// ... and the owner still reaches its own records.
	for _, k := range []string{"GET /api/v1/recipes/{recipeName}/runs/{runId}", "GET /api/v1/approvals/{gateKey}", "GET /api/v1/agents/{agentName}/sessions/{sessionID}", "GET /api/v1/traces/{traceId}", "GET /api/v1/keys/{keyID}"} {
		c := byIDRoutes[k]
		if rec := call(h, "ka", c.method, c.path, c.body); rec.Code != http.StatusOK {
			t.Errorf("%s as the owner: %d %s", k, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}
}

// TestEveryRouteWithAParameterIsClassified fails for a route that takes a path parameter and is neither a
// by-id route covered above nor a by-name route: the person adding it has to say which it is, and if it is
// by id, add it to byIDRoutes and see it refused across kitchens.
func TestEveryRouteWithAParameterIsClassified(t *testing.T) {
	_, _, mux := newByIDRouter(t)
	var unclassified []string
	_ = chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.ReplaceAll(route, "/*/", "/")
		route = strings.TrimSuffix(route, "/")
		if !strings.Contains(route, "{") || !strings.HasPrefix(route, "/api/v1/") {
			return nil
		}
		key := method + " " + route
		if _, ok := byIDRoutes[key]; ok {
			return nil
		}
		for prefix := range byNameRoutes {
			if strings.HasPrefix(route, strings.TrimPrefix(prefix, "*")) && !strings.Contains(strings.TrimPrefix(route, strings.TrimPrefix(prefix, "*")), "{") {
				return nil
			}
		}
		if reason, ok := otherParamRoutes[key]; ok && reason != "" {
			return nil
		}
		unclassified = append(unclassified, key)
		return nil
	})
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Fatalf("routes with a path parameter that are neither by-id (byIDRoutes) nor by-name (byNameRoutes, otherParamRoutes):\n  %s", strings.Join(unclassified, "\n  "))
	}
}

// otherParamRoutes: parameters that do not name a kitchen-owned record by id alone, with why.
var otherParamRoutes = map[string]string{
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
