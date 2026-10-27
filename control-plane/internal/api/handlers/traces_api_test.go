package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func seedTraces(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(id, agent, recipe, status string, ago time.Duration, ms int64, tokens int64, cost float64, in string, tags []string, session string, model string) {
		tr := &models.Trace{ID: id, AgentName: agent, RecipeName: recipe, Kitchen: "default", Status: status, DurationMs: ms,
			TotalTokens: tokens, CostUSD: cost, InputText: in, OutputText: "out " + id, Tags: tags, SessionID: session,
			CreatedAt: now.Add(-ago), Metadata: map[string]interface{}{"model": model, "provider": "anthropic"}}
		if err := s.CreateTrace(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	mk("t1", "support", "", "completed", 1*time.Hour, 800, 1000, 0.01, "Where is my refund?", []string{"billing"}, "s1", "claude-haiku-4-5")
	mk("t2", "support", "", "failed", 2*time.Hour, 200, 50, 0.001, "Cancel my order", nil, "s1", "claude-haiku-4-5")
	mk("t3", "triage", "refunds", "completed", 3*time.Hour, 3000, 9000, 0.5, "Refund batch", []string{"billing", "batch"}, "", "claude-opus-4-5")
	mk("t4", "triage", "old-recipe", "completed", 4*time.Hour, 100, 10, 0.0001, "legacy", nil, "", "claude-haiku-4-5")
}

func getJSON[T any](t *testing.T, h http.HandlerFunc, url string) (T, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withKitchen(req, "default")
	rec := httptest.NewRecorder()
	h(rec, req)
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v: %s", url, err, rec.Body.String())
	}
	return out, rec
}

func ids(ts []models.Trace) string {
	var out []string
	for _, tr := range ts {
		out = append(out, tr.ID)
	}
	return strings.Join(out, ",")
}

func TestTracesCanBeFilteredSortedAndPaged(t *testing.T) {
	h, s := newTestHandlers(t)
	seedTraces(t, s)
	cases := []struct{ query, want string }{
		{"", "t1,t2,t3,t4"}, // newest first
		{"?agent=support", "t1,t2"},
		{"?status=failed", "t2"},
		{"?tag=batch", "t3"},
		{"?tag=billing", "t1,t3"},
		{"?session=s1", "t1,t2"},
		{"?model=claude-opus-4-5", "t3"},
		{"?q=refund", "t1,t3"}, // matches the input, case-insensitively
		{"?q=cancel", "t2"},
		{"?min_cost=0.01", "t1,t3"},
		{"?max_cost=0.01", "t1,t2,t4"},
		{"?min_duration=1000", "t3"},
		{"?range=1h", ""}, // t1 is exactly an hour old: just outside, and the rest are older
		{"?range=24h&sort=cost", "t3,t1,t2,t4"},
		{"?sort=duration&order=asc", "t4,t2,t1,t3"},
		{"?limit=2", "t1,t2"},
		{"?limit=2&offset=2", "t3,t4"},
	}
	for _, c := range cases {
		got, _ := getJSON[[]models.Trace](t, h.ListTraces, "/api/v1/traces"+c.query)
		if ids(got) != c.want {
			t.Errorf("%s: got %q, want %q", c.query, ids(got), c.want)
		}
	}
	_, rec := getJSON[[]models.Trace](t, h.ListTraces, "/api/v1/traces?limit=1")
	if rec.Header().Get("X-Total-Count") != "4" {
		t.Errorf("total count header = %q", rec.Header().Get("X-Total-Count"))
	}
}

func TestTraceStatsSummariseWhatTheFilterMatches(t *testing.T) {
	h, s := newTestHandlers(t)
	seedTraces(t, s)
	st, _ := getJSON[models.TraceStats](t, h.TraceStats, "/api/v1/traces/stats")
	if st.Count != 4 || st.Errors != 1 || st.TotalTokens != 10060 || st.P95Ms != 800 && st.P95Ms != 3000 {
		t.Errorf("stats = %+v", st)
	}
	if st.TotalCost < 0.511 || st.TotalCost > 0.5111 {
		t.Errorf("cost = %v", st.TotalCost)
	}
	st, _ = getJSON[models.TraceStats](t, h.TraceStats, "/api/v1/traces/stats?agent=support")
	if st.Count != 2 || st.Errors != 1 {
		t.Errorf("filtered stats = %+v", st)
	}
}

// The traces of an archived recipe's runs stay in the store (they are auditable) but are not shown
// unless asked for.
func TestTracesOfArchivedRecipesAreHiddenUnlessAskedFor(t *testing.T) {
	h, s := newTestHandlers(t)
	seedTraces(t, s)
	now := time.Now().UTC()
	_ = s.CreateRecipe(context.Background(), &models.Recipe{ID: "r1", Name: "old-recipe", Kitchen: "default", ArchivedAt: &now})
	_ = s.CreateRecipe(context.Background(), &models.Recipe{ID: "r2", Name: "refunds", Kitchen: "default"})

	got, _ := getJSON[[]models.Trace](t, h.ListTraces, "/api/v1/traces")
	if ids(got) != "t1,t2,t3" {
		t.Errorf("hidden: %q", ids(got))
	}
	got, _ = getJSON[[]models.Trace](t, h.ListTraces, "/api/v1/traces?include_archived=true")
	if ids(got) != "t1,t2,t3,t4" {
		t.Errorf("included: %q", ids(got))
	}
	st, _ := getJSON[models.TraceStats](t, h.TraceStats, "/api/v1/traces/stats")
	if st.Count != 3 {
		t.Errorf("stats count the visible ones: %+v", st)
	}
	// And it is still readable directly.
	if _, err := s.GetTrace(context.Background(), "t4"); err != nil {
		t.Error(err)
	}
}

func TestTracesCanBeTaggedAndScored(t *testing.T) {
	h, s := newTestHandlers(t)
	seedTraces(t, s)
	p := map[string]string{"traceId": "t1"}

	rec := call(h.UpdateTrace, "/x", `{"tags":["reviewed"," bug ","bug",""]}`, p)
	if rec.Code != http.StatusOK {
		t.Fatalf("tags: %d %s", rec.Code, rec.Body.String())
	}
	tr, _ := s.GetTrace(context.Background(), "t1")
	if len(tr.Tags) != 2 || tr.Tags[0] != "reviewed" || tr.Tags[1] != "bug" {
		t.Errorf("tags = %v", tr.Tags)
	}
	if rec = call(h.UpdateTrace, "/x", `{"description":"rewrite history"}`, p); rec.Code != http.StatusBadRequest {
		t.Errorf("only tags are editable: %d", rec.Code)
	}

	rec = call(h.AddTraceScore, "/x", `{"name":"helpful","value":1,"label":"good","comment":"exactly right"}`, p)
	if rec.Code != http.StatusCreated {
		t.Fatalf("score: %d %s", rec.Code, rec.Body.String())
	}
	var sc models.TraceScore
	_ = json.Unmarshal(rec.Body.Bytes(), &sc)
	if sc.ID == "" || sc.Source != "user" || sc.TraceID != "t1" || sc.Value != 1 {
		t.Errorf("score = %+v", sc)
	}
	call(h.AddTraceScore, "/x", `{"value":0,"label":"bad"}`, p) // the name defaults to "quality"
	got, _ := getJSON[models.Trace](t, wrapWithParam(h.GetTrace, "traceId", "t1"), "/api/v1/traces/t1")
	if len(got.Scores) != 2 || got.Scores[1].Name != "quality" {
		t.Errorf("a trace carries its scores: %+v", got.Scores)
	}
	if rec = call(h.AddTraceScore, "/x", `{"name":"x"}`, map[string]string{"traceId": "nope"}); rec.Code != http.StatusNotFound {
		t.Errorf("scoring a missing trace: %d", rec.Code)
	}
	rec = call(h.DeleteTraceScore, "/x", ``, map[string]string{"traceId": "t1", "scoreId": sc.ID})
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete score: %d", rec.Code)
	}
	if left, _ := s.ListTraceScores(context.Background(), "t1"); len(left) != 1 {
		t.Errorf("scores left: %v", left)
	}
}

func TestSessionsGroupTheTracesOfAConversation(t *testing.T) {
	h, s := newTestHandlers(t)
	seedTraces(t, s)
	out, _ := getJSON[[]struct {
		SessionID string   `json:"session_id"`
		Traces    int      `json:"traces"`
		Errors    int      `json:"errors"`
		Agents    []string `json:"agents"`
	}](t, h.ListTraceSessions, "/api/v1/traces/sessions")
	if len(out) != 1 || out[0].SessionID != "s1" || out[0].Traces != 2 || out[0].Errors != 1 || out[0].Agents[0] != "support" {
		t.Errorf("sessions = %+v", out)
	}
}

func wrapWithParam(h http.HandlerFunc, key, value string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h(w, withChiParam(r, key, value)) }
}
