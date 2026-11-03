package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/server"
)

// Every route that can return what users typed or what a model or tool answered is listed here, with the
// request that exercises it. For a caller who may not read content (the guard says no), none of them may
// show any of the markers seeded below, in any field. The test fails for a route that looks like it
// returns trace, span, session or task data and is in neither table, so a new route cannot ship without
// somebody deciding which kind it is (ADR-0043).
var contentRoutes = map[string]struct{ method, path, body string }{
	"GET /api/v1/traces":                                  {"GET", "/api/v1/traces", ""},
	"GET /api/v1/traces (search)":                         {"GET", "/api/v1/traces?q=SECRET-IN&view=", ""},
	"GET /api/v1/traces/{traceId}":                        {"GET", "/api/v1/traces/tr-s", ""},
	"GET /api/v1/traces/{traceId} (spans)":                {"GET", "/api/v1/traces/tr-s?spans=true", ""},
	"PATCH /api/v1/traces/{traceId}":                      {"PATCH", "/api/v1/traces/tr-s", `{"tags":["reviewed"]}`},
	"GET /api/v1/traces/{traceId}/spans":                  {"GET", "/api/v1/traces/tr-s/spans", ""},
	"GET /api/v1/traces/{traceId}/timeline":               {"GET", "/api/v1/traces/tr-s/timeline", ""},
	"GET /api/v1/traces/{traceId}/audit":                  {"GET", "/api/v1/traces/tr-s/audit", ""},
	"GET /api/v1/traces/{traceId}/scores":                 {"GET", "/api/v1/traces/tr-s/scores", ""},
	"POST /api/v1/traces/{traceId}/scores":                {"POST", "/api/v1/traces/tr-s/scores", `{"name":"q","value":1,"comment":"SECRET-NEW-COMMENT"}`},
	"GET /api/v1/spans/{spanId}":                          {"GET", "/api/v1/spans/sp-s", ""},
	"GET /api/v1/agents/{agentName}/sessions":             {"GET", "/api/v1/agents/bot/sessions", ""},
	"GET /api/v1/agents/{agentName}/sessions/{sessionID}": {"GET", "/api/v1/agents/bot/sessions/sess-s", ""},
	"GET /api/v1/tasks":                                   {"GET", "/api/v1/tasks", ""},
	"GET /api/v1/tasks/{taskID}":                          {"GET", "/api/v1/tasks/task-done", ""},
	"GET /api/v1/tasks/{taskID} (paused)":                 {"GET", "/api/v1/tasks/task-paused", ""},
	"GET /api/v1/tasks/{taskID}/events":                   {"GET", "/api/v1/tasks/task-done/events", ""},
	"POST /langfuse/export":                               {"POST", "/langfuse/export", `{}`},
}

// otherTraceRoutes are routes whose path looks like trace, span, session or task data and that return none
// of it: aggregates, names, ids, or only an acknowledgement.
var otherTraceRoutes = map[string]string{
	"GET /api/v1/traces/stats":                                      "counts, tokens, cost and latency percentiles",
	"GET /api/v1/traces/sessions":                                   "per session: counts, agent names, tokens, cost",
	"DELETE /api/v1/traces/{traceId}/scores/{scoreId}":              "204, no body",
	"GET /api/v1/trace-views":                                       "saved filters; a search text typed into a filter is not trace data",
	"POST /api/v1/trace-views":                                      "saved filters",
	"PUT /api/v1/trace-views/{viewId}":                              "saved filters",
	"DELETE /api/v1/trace-views/{viewId}":                           "204, no body",
	"POST /api/v1/agents/{agentName}/sessions":                      "a new session has no messages: the creator is the one who types into it",
	"DELETE /api/v1/agents/{agentName}/sessions/{sessionID}":        "an acknowledgement",
	"POST /api/v1/agents/{agentName}/sessions/{sessionID}/messages": "the reply to the message the caller just sent: an invoke, which needs invoke rights",
	"POST /api/v1/tasks":                                            "202 with the task's id and status",
	"POST /api/v1/tasks/{taskID}/cancel":                            "answered through taskOut",
	"POST /api/v1/tasks/{taskID}/resume":                            "answered through taskOut",
	"GET /api/v1/agents/runs/unfinished":                            "ids, status and counts of runs",
	"POST /langfuse/ingest":                                         "imports traces, returns counts",
	"POST /api/v1/agents/{agentName}/runs/{traceID}/resume":         "the answer of the run the caller resumes: an invoke",
	"POST /api/v1/agents/{agentName}/invoke":                        "the answer to the caller's own request",
	"POST /api/v1/agents/{agentName}/invoke/stream":                 "the answer to the caller's own request",
}

var looksLikeTraceData = regexp.MustCompile(`/(traces|spans|sessions|tasks|langfuse)\b`)

func newContentServer(t *testing.T) (*server.Server, http.Handler) {
	t.Helper()
	t.Setenv("AGENTOVEN_API_KEYS", "test-key")
	t.Setenv("AGENTOVEN_REQUIRE_AUTH", "true")
	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	t.Setenv("AGENTOVEN_JOURNAL_DIR", t.TempDir())
	srv, err := server.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sessStore = srv.SessionStore
	return srv, srv.Handler
}

// seedContent stores a trace, span, score, session and two tasks whose every content field carries a
// marker that starts with SECRET-.
func seedContent(t *testing.T, srv *server.Server) {
	t.Helper()
	ctx := context.Background()
	s := srv.Store
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.CreateAgent(ctx, &models.Agent{Name: "bot", Kitchen: "default", Status: models.AgentStatusReady, Mode: models.AgentModeManaged}))
	must(s.CreateTrace(ctx, &models.Trace{
		ID: "tr-s", AgentName: "bot", Kitchen: "default", Status: "failed", DurationMs: 800, TotalTokens: 30, CostUSD: 0.01, CreatedAt: now,
		SessionID: "sess-s", UserID: "u1",
		InputText: "SECRET-IN prompt", OutputText: "SECRET-OUT answer",
		ThinkingBlocks: []models.ThinkingBlock{{Content: "SECRET-THINKING", Model: "m", Provider: "p", Timestamp: now}},
		Metadata: map[string]interface{}{
			"model": "gpt-x", "provider": "oai", "mode": "managed",
			"error": "SECRET-ERROR provider echoed the prompt", "input": "SECRET-META-IN", "output": "SECRET-META-OUT",
			"thinking_blocks": []interface{}{"SECRET-META-THINK"}, "contexts": []interface{}{"SECRET-CONTEXT"}, "ground_truth": "SECRET-TRUTH",
			"turns": 2, "future_key": "SECRET-UNKNOWN-KEY",
		},
		RuntimeMetadata: map[string]interface{}{"input": "SECRET-RUNTIME", "runtime": "agentoven"},
	}))
	must(s.CreateSpan(ctx, &models.Span{
		ID: "sp-s", TraceID: "tr-s", Name: "llm", Kind: models.SpanKindLLM, Status: "failed", StartTime: now, EndTime: now.Add(time.Second), DurationMs: 1000,
		Input: json.RawMessage(`{"messages":[{"role":"user","content":"SECRET-SPAN-IN"}]}`), Output: json.RawMessage(`"SECRET-SPAN-OUT"`),
		Error:    "SECRET-SPAN-ERROR",
		Metadata: map[string]interface{}{"tool_call_id": "c1", "error": "SECRET-SPAN-META-ERROR", "args": "SECRET-SPAN-ARGS"},
		Events:   []models.SpanEvent{{Name: "first token", Timestamp: now, Attributes: map[string]interface{}{"text": "SECRET-EVENT"}}},
		Model:    "gpt-x", Provider: "oai",
	}))
	must(s.CreateTraceScore(ctx, &models.TraceScore{ID: "sc-s", TraceID: "tr-s", Name: "q", Value: 0, Label: "bad", Comment: "SECRET-COMMENT", Source: "user", CreatedAt: now}))
	must(sessStore.CreateSession(ctx, &models.Session{ID: "sess-s", AgentName: "bot", Kitchen: "default", Status: models.SessionActive, CreatedAt: now, UpdatedAt: now,
		Messages: []models.ChatMessage{{Role: "user", Content: "SECRET-SESSION-USER"}, {Role: "assistant", Content: "SECRET-SESSION-ASSISTANT"}},
		Metadata: map[string]interface{}{"note": "SECRET-SESSION-META"}}))

	j := srv.Handlers.Executor.Journal()
	if j == nil {
		t.Fatal("the server has no journal, so no tasks")
	}
	agent := &models.Agent{Name: "bot", Kitchen: "default"}
	must(j.Start(ctx, executor.JournalHeader{TraceID: "task-done", Kitchen: "default", Agent: agent, StartedAt: now,
		InitialMessages: []models.ChatMessage{{Role: "user", Content: "SECRET-TASK-PROMPT"}}}))
	must(j.MarkDone(ctx, "task-done", "SECRET-TASK-RESULT", nil))
	must(j.Start(ctx, executor.JournalHeader{TraceID: "task-paused", Kitchen: "default", Agent: agent, StartedAt: now}))
	must(j.MarkPaused(ctx, "task-paused", executor.PendingApproval{Turn: 1, GatedNames: []string{"wire"},
		ToolCalls: []executor.ToolCall{{ID: "c1", Name: "wire", Arguments: map[string]interface{}{"to": "SECRET-TOOL-ARG"}}}},
		[]models.ChatMessage{{Role: "user", Content: "SECRET-PAUSED-PROMPT"}}))
}

// A caller without content access gets none of the seeded content from any route, and the same routes show
// it to a caller with access (so the test is not passing because the data was never there).
func TestNoRouteShowsContentToAReaderWithoutAccess(t *testing.T) {
	srv, h := newContentServer(t)
	seedContent(t, srv)
	allowed := true
	srv.Handlers.SetTraceContentGuard(func(*http.Request) bool { return allowed })

	keys := make([]string, 0, len(contentRoutes))
	for k := range contentRoutes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	shown := 0
	allowed = true
	for _, k := range keys {
		c := contentRoutes[k]
		rec := call(h, "default", c.method, c.path, c.body)
		if rec.Code >= 500 && k != "POST /langfuse/export" { // no LangFuse is configured: its push fails after the access check
			t.Errorf("%s as a reader with access: %d %s", k, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "SECRET-") {
			shown++
		}
	}
	if shown < 8 {
		t.Fatalf("only %d routes showed the seeded content to a reader with access: the test data is not reaching the routes", shown)
	}

	// Seeds the PATCH and POST changed are irrelevant; re-seeding is not needed for a read.
	allowed = false
	for _, k := range keys {
		c := contentRoutes[k]
		rec := call(h, "default", c.method, c.path, c.body)
		body := rec.Body.String()
		if rec.Code >= 500 {
			t.Errorf("%s without access: %d %s", k, rec.Code, body)
		}
		if strings.Contains(body, "SECRET-") {
			t.Errorf("%s shows content to a reader without access: %s", k, body)
		}
		if rec.Code == http.StatusOK && !strings.Contains(body, "content_withheld") && c.method != "POST" && !strings.Contains(c.path, "/audit") && k != "GET /api/v1/traces/{traceId}/timeline" {
			t.Errorf("%s answers 200 without saying the content is withheld: %s", k, body)
		}
	}
	// What they may see is still there.
	rec := call(h, "default", "GET", "/api/v1/traces/tr-s", "")
	for _, want := range []string{`"cost_usd":0.01`, `"duration_ms":800`, `"agent_name":"bot"`, `"model":"gpt-x"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("a reader without content access should still see %s: %s", want, rec.Body)
		}
	}
	// A search over the bodies would reveal what they hold: it matches nothing narrower than the full list.
	if rec := call(h, "default", "GET", "/api/v1/traces?q=NOSUCHTEXT", ""); !strings.Contains(rec.Body.String(), `"tr-s"`) {
		t.Errorf("a search must be ignored for a reader without content access: %s", rec.Body)
	}
}

// Every route that looks like it returns trace, span, session or task data has been looked at.
func TestEveryTraceDataRouteIsClassified(t *testing.T) {
	_, _, mux := newByIDRouter(t)
	covered := map[string]bool{}
	for k := range contentRoutes {
		covered[strings.Fields(k)[0]+" "+strings.Fields(k)[1]] = true
	}
	var unclassified []string
	_ = chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(strings.ReplaceAll(route, "/*/", "/"), "/")
		if !(strings.HasPrefix(route, "/api/v1/") || strings.HasPrefix(route, "/langfuse/")) || !looksLikeTraceData.MatchString(route) {
			return nil
		}
		key := method + " " + route
		if covered[key] {
			return nil
		}
		if reason, ok := otherTraceRoutes[key]; ok && reason != "" {
			return nil
		}
		unclassified = append(unclassified, key)
		return nil
	})
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Fatalf("routes that look like trace, span, session or task data and are in neither contentRoutes nor otherTraceRoutes.\n"+
			"Return the data through h.traceOut and its siblings (or a Public type) and add the route to contentRoutes with a request that exercises it:\n  %s", strings.Join(unclassified, "\n  "))
	}
	for key := range otherTraceRoutes {
		found := false
		_ = chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			found = found || method+" "+strings.TrimSuffix(strings.ReplaceAll(route, "/*/", "/"), "/") == key
			return nil
		})
		if !found {
			t.Errorf("otherTraceRoutes lists %s, which is not routed", key)
		}
	}
}
