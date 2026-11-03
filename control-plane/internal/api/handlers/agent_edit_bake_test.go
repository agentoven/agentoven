package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// bakeHandlers builds handlers that can bake. The agent is external so the background step needs no
// process; it only moves the agent from baking to ready.
func bakeHandlers(t *testing.T) (*handlers.Handlers, store.Store) {
	t.Helper()
	h, s := newTestHandlers(t)
	h.Resolver = resolver.NewResolver(s)
	err := s.CreateAgent(context.Background(), &models.Agent{
		Name: "bot", Kitchen: "default", Status: models.AgentStatusDraft, Mode: models.AgentModeExternal,
		MaxTurns: 5, ModelName: "m1", Description: "be brief",
		Guardrails: []models.Guardrail{{ID: "g1", Kind: models.GuardrailKind("keyword_block"), Stage: models.GuardrailStageInput, Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, s
}

func agentCall(t *testing.T, fn http.HandlerFunc, method, body string, params ...string) (int, string) {
	t.Helper()
	req := withKitchen(httptest.NewRequest(method, "/x", strings.NewReader(body)), "default")
	req = withChiParam(req, "agentName", "bot")
	for i := 0; i+1 < len(params); i += 2 {
		req = withChiParam(req, params[i], params[i+1])
	}
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec.Code, rec.Body.String()
}

func waitStatus(t *testing.T, s store.Store, want models.AgentStatus) *models.Agent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a, err := s.GetAgent(context.Background(), "default", "bot")
		if err == nil && a.Status == want {
			return a
		}
		time.Sleep(50 * time.Millisecond)
	}
	a, _ := s.GetAgent(context.Background(), "default", "bot")
	t.Fatalf("agent never reached %s: %+v", want, a)
	return nil
}

// ADR-0043: an agent that has been baked runs what was baked. A PUT that changes guardrails, turns or
// model waits in the draft and changes nothing live; the next bake applies it and a version records it.
func TestAnEditToABakedAgentChangesNothingUntilItIsBaked(t *testing.T) {
	h, s := bakeHandlers(t)
	if code, body := agentCall(t, h.BakeAgent, "POST", `{}`); code != http.StatusAccepted {
		t.Fatalf("bake: %d %s", code, body)
	}
	waitStatus(t, s, models.AgentStatusReady)

	code, body := agentCall(t, h.UpdateAgent, "PUT", `{"guardrails": [], "max_turns": 9, "model_name": "m2", "description": "be long", "tags": {"owner": "me"}}`)
	if code != 200 {
		t.Fatalf("PUT: %d %s", code, body)
	}
	live, _ := s.GetAgent(context.Background(), "default", "bot")
	if len(live.Guardrails) != 1 || live.MaxTurns != 5 || live.ModelName != "m1" || live.Description != "be brief" {
		t.Fatalf("an edit changed what runs before a bake: %+v", live)
	}
	if live.Draft == nil || live.Draft.MaxTurns != 9 || live.Draft.ModelName != "m2" || live.Draft.Guardrails == nil || len(live.Draft.Guardrails) != 0 {
		t.Fatalf("the edit must wait in the draft: %+v", live.Draft)
	}
	if live.Tags["owner"] != "me" {
		t.Fatal("tags are labels, not behaviour: they apply at once")
	}
	if !strings.Contains(body, `"draft"`) {
		t.Fatalf("the response must show the draft so a client can say there are unbaked changes: %s", body)
	}
	if v, _ := s.GetAgentVersion(context.Background(), "default", "bot", "0.1.0"); len(v.Guardrails) != 1 || v.Description != "be brief" {
		t.Fatal("the stored version must still describe what runs")
	}

	if code, body := agentCall(t, h.RecookAgent, "POST", `{}`); code != http.StatusAccepted {
		t.Fatalf("recook: %d %s", code, body)
	}
	baked := waitStatus(t, s, models.AgentStatusReady)
	if baked.Version != "0.2.0" || baked.Draft != nil || baked.MaxTurns != 9 || baked.ModelName != "m2" || len(baked.Guardrails) != 0 || baked.Description != "be long" {
		t.Fatalf("the bake must apply the draft and record a version: %+v", baked)
	}
	v1, _ := s.GetAgentVersion(context.Background(), "default", "bot", "0.1.0")
	v2, _ := s.GetAgentVersion(context.Background(), "default", "bot", "0.2.0")
	if len(v1.Guardrails) != 1 || len(v2.Guardrails) != 0 {
		t.Fatalf("each version must show the guardrails it ran with: %d %d", len(v1.Guardrails), len(v2.Guardrails))
	}
}

// An agent that was never baked runs nothing, so there is nothing to wait for.
func TestAnEditToAnUnbakedAgentApplies(t *testing.T) {
	h, s := bakeHandlers(t)
	if code, body := agentCall(t, h.UpdateAgent, "PUT", `{"max_turns": 9}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	a, _ := s.GetAgent(context.Background(), "default", "bot")
	if a.MaxTurns != 9 || a.Draft != nil {
		t.Fatalf("%+v", a)
	}
}

// Review finding 5: naming an existing version in a bake or re-cook ran the new configuration under the old label.
func TestABakeCannotNameAVersionThatHoldsSomethingElse(t *testing.T) {
	h, s := bakeHandlers(t)
	agentCall(t, h.BakeAgent, "POST", `{}`)
	waitStatus(t, s, models.AgentStatusReady)
	agentCall(t, h.UpdateAgent, "PUT", `{"description": "changed"}`)

	for _, bad := range []string{"../../etc", "9.9.9\\nx", "<script>", " ", strings.Repeat("1", 5000)} {
		for name, fn := range map[string]http.HandlerFunc{"bake": h.BakeAgent, "recook": h.RecookAgent} {
			if code, body := agentCall(t, fn, "POST", `{"version": "`+bad+`"}`); code != http.StatusBadRequest {
				t.Errorf("%s with version %.20q: %d %s, want 400", name, bad, code, body)
			}
		}
	}
	if code, body := agentCall(t, h.RecookAgent, "POST", `{"version": "0.1.0"}`); code != http.StatusConflict {
		t.Fatalf("the review's reproduction (re-cook naming 0.1.0 with a changed prompt) must be refused: %d %s", code, body)
	}
	a, _ := s.GetAgent(context.Background(), "default", "bot")
	if a.Version != "0.1.0" || a.Status != models.AgentStatusReady || a.Draft == nil {
		t.Fatalf("a refused bake must change nothing: %+v", a)
	}
	if vs, _ := s.ListAgentVersions(context.Background(), "default", "bot"); len(vs) != 1 {
		t.Fatalf("versions: %d", len(vs))
	}
	if code, body := agentCall(t, h.RecookAgent, "POST", `{"version": "1.0.0"}`); code != http.StatusAccepted {
		t.Fatalf("a higher version is allowed: %d %s", code, body)
	}
	waitStatus(t, s, models.AgentStatusReady)
	if a, _ := s.GetAgent(context.Background(), "default", "bot"); a.Version != "1.0.0" || a.Draft != nil {
		t.Fatalf("%+v", a)
	}
}

// Review finding 7: the background step wrote the whole agent it had read before the bake finished,
// overwriting edits made in between. Edits and tags that arrive while it runs must survive, and
// overlapping re-cooks must leave a consistent agent. Run with -race.
func TestTheBackgroundStepDoesNotOverwriteLaterEdits(t *testing.T) {
	h, s := bakeHandlers(t)
	agentCall(t, h.BakeAgent, "POST", `{}`)
	waitStatus(t, s, models.AgentStatusReady)

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ { // overlapping re-cooks of the same agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, body := agentCall(t, h.RecookAgent, "POST", `{}`); code != http.StatusAccepted {
				t.Errorf("recook: %d %s", code, body)
			}
		}()
	}
	wg.Wait()
	// The review's sequence: an edit right after a re-cook was accepted.
	if code, body := agentCall(t, h.UpdateAgent, "PUT", `{"guardrails": [{"id": "late", "kind": "keyword_block", "stage": "input", "enabled": true}], "tags": {"note": "late"}}`); code != 200 {
		t.Fatalf("PUT: %d %s", code, body)
	}
	waitStatus(t, s, models.AgentStatusReady)
	time.Sleep(1500 * time.Millisecond) // let every queued step run to its end
	a, _ := s.GetAgent(context.Background(), "default", "bot")
	if a.Status != models.AgentStatusReady {
		t.Fatalf("status %s", a.Status)
	}
	if a.Tags["note"] != "late" {
		t.Fatalf("a tag set while the re-cook ran was overwritten: %v", a.Tags)
	}
	if a.Draft == nil || len(a.Draft.Guardrails) != 1 || a.Draft.Guardrails[0].ID != "late" {
		t.Fatalf("the edit made while the re-cook ran was lost: %+v", a.Draft)
	}
	if a.Version != "0.1.0" {
		t.Fatalf("an unchanged re-cook must not move the version: %s", a.Version)
	}
}

// A bake whose agent was cooled or retired before its background step ran must not mark it ready.
func TestAStaleBakeOutcomeIsDropped(t *testing.T) {
	h, s := bakeHandlers(t)
	agentCall(t, h.BakeAgent, "POST", `{}`)
	cooled := waitStatus(t, s, models.AgentStatusReady)
	agentCall(t, h.RecookAgent, "POST", `{}`)
	cooled.Status = models.AgentStatusCooled // someone cools it while the re-cook step sleeps
	if err := s.UpdateAgent(context.Background(), cooled); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if a, _ := s.GetAgent(context.Background(), "default", "bot"); a.Status != models.AgentStatusCooled {
		t.Fatalf("the re-cook's step marked a cooled agent %s", a.Status)
	}
}
