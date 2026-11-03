package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Owner decision A1: an edit to a baked agent is kept as a draft, and GET /draft/diff says what the next bake
// would change, with secrets masked, and nothing for an agent that has no draft.
func TestDraftDiffRoute(t *testing.T) {
	h, s := bakeHandlers(t)
	call := func(kitchen, name string) (int, string) {
		req := withChiParam(withKitchen(httptest.NewRequest("GET", "/x", nil), kitchen), "agentName", name)
		rec := httptest.NewRecorder()
		h.GetAgentDraftDiff(rec, req)
		return rec.Code, rec.Body.String()
	}
	type diffBody struct {
		HasDraft         bool   `json:"has_draft"`
		Baked            bool   `json:"baked"`
		ChangesBehaviour bool   `json:"changes_behaviour"`
		BakedVersion     string `json:"baked_version"`
		Changes          []struct {
			Field, Key, Kind string
			Details          []struct{ Field string }
		}
	}
	read := func(code int, body string) diffBody {
		t.Helper()
		var d diffBody
		if code != 200 || json.Unmarshal([]byte(body), &d) != nil {
			t.Fatalf("%d %s", code, body)
		}
		return d
	}

	// Never baked: edits apply at once, there is nothing to diff.
	if d := read(call("default", "bot")); d.HasDraft || d.Baked || len(d.Changes) != 0 {
		t.Fatalf("%+v", d)
	}
	if code, body := agentCall(t, h.BakeAgent, "POST", `{}`); code != http.StatusAccepted {
		t.Fatalf("bake: %d %s", code, body)
	}
	baked := waitStatus(t, s, models.AgentStatusReady)
	if code, body := call("default", "bot"); code != 200 || !strings.Contains(body, `"changes":[]`) {
		t.Fatalf("a baked agent without a draft has no changes: %d %s", code, body)
	}

	if code, body := agentCall(t, h.UpdateAgent, "PUT", `{"max_turns":9,"description":null,"guardrails":[{"id":"g1","kind":"prompt_injection","stage":"input","enabled":false},{"id":"g9","kind":"llamaguard","stage":"output","enabled":true,"config":{"endpoint":"http://127.0.0.1:9/g","api_key":"sk-super-secret-value"}}]}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	code, body := call("default", "bot")
	d := read(code, body)
	if !d.HasDraft || !d.Baked || d.BakedVersion != baked.Version || !d.ChangesBehaviour {
		t.Fatalf("%+v", d)
	}
	got := map[string]bool{}
	for _, c := range d.Changes {
		got[c.Field+"/"+c.Key+"/"+c.Kind] = true
	}
	for _, want := range []string{"max_turns//changed", "description//removed", "guardrails/g1/changed", "guardrails/g9/added"} {
		if !got[want] {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "sk-super-secret-value") {
		t.Fatalf("the diff leaks a secret: %s", body)
	}
	// The read changed nothing.
	if live, _ := s.GetAgent(context.Background(), "default", "bot"); live.Draft == nil || live.MaxTurns != 5 {
		t.Fatalf("%+v", live)
	}

	// Another kitchen's agent, and one that does not exist, are both a 404 that does not tell them apart.
	seedAgent(t, s, "other", "bot")
	for _, c := range []struct{ kitchen, name string }{{"elsewhere", "bot"}, {"default", "nope"}} {
		if code, _ := call(c.kitchen, c.name); code != http.StatusNotFound {
			t.Errorf("%s/%s: %d, want 404", c.kitchen, c.name, code)
		}
	}
	if d := read(call("other", "bot")); d.HasDraft {
		t.Errorf("the other kitchen's agent has its own state: %+v", d)
	}
}
