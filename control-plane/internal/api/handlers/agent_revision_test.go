package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func waitNotBaking(t *testing.T, s store.Store, name string) *models.Agent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a, err := s.GetAgent(context.Background(), "default", name)
		if err == nil && a.Status != models.AgentStatusBaking {
			return a
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the bake never finished")
	return nil
}

// An edit and a bake of the same agent at the same time used to lose one of them: both read the agent,
// changed their copy and wrote the whole row, so the later write restored what the earlier one changed
// (review 5, finding 8: 11 of 25 edits lost). Whatever the interleaving, the edit must be either
// applied or waiting in the draft, and the bake must have happened.
func TestAPutRacingABakeNeverLosesTheEditNorTheBake(t *testing.T) {
	h, s := bakeHandlers(t)
	ctx := context.Background()
	const trials = 12
	for i := 0; i < trials; i++ {
		name := fmt.Sprintf("race%d", i)
		if err := s.CreateAgent(ctx, &models.Agent{Name: name, Kitchen: "default", Status: models.AgentStatusDraft,
			Mode: models.AgentModeExternal, MaxTurns: 5, ModelName: "m1", Description: "original"}); err != nil {
			t.Fatal(err)
		}
		// Bake once, so the second bake runs against an agent whose edits go to the draft.
		if i%2 == 1 {
			if code, body := agentCall(t, h.BakeAgent, "POST", `{}`, "agentName", name); code != http.StatusAccepted {
				t.Fatalf("first bake: %d %s", code, body)
			}
			waitNotBaking(t, s, name)
		}
		want := fmt.Sprintf("edit-%d", i)
		var wg sync.WaitGroup
		var putCode, bakeCode int
		var putBody, bakeBody string
		wg.Add(2)
		go func() {
			defer wg.Done()
			putCode, putBody = agentCall(t, h.UpdateAgent, "PUT", `{"description":"`+want+`"}`, "agentName", name)
		}()
		go func() {
			defer wg.Done()
			bakeCode, bakeBody = agentCall(t, h.BakeAgent, "POST", `{}`, "agentName", name)
		}()
		wg.Wait()
		if putCode != http.StatusOK || bakeCode != http.StatusAccepted {
			t.Fatalf("trial %d: put %d %s, bake %d %s", i, putCode, putBody, bakeCode, bakeBody)
		}
		got := waitNotBaking(t, s, name)
		draft := ""
		if got.Draft != nil {
			draft = got.Draft.Description
		}
		if got.Description != want && draft != want {
			t.Fatalf("trial %d: the edit is lost: description %q, draft %q", i, got.Description, draft)
		}
		if got.ResolvedConfig == nil {
			t.Fatalf("trial %d: the bake is lost: no resolved configuration", i)
		}
		if got.Status != models.AgentStatusReady {
			t.Fatalf("trial %d: status %s, want ready", i, got.Status)
		}
	}
}

func TestAnEditNamingAStaleRevisionIsRefusedWithTheCurrentVersion(t *testing.T) {
	h, s := bakeHandlers(t)
	cur, _ := s.GetAgent(context.Background(), "default", "bot")
	if cur.Revision < 1 {
		t.Fatalf("a stored agent carries a revision from 1: %d", cur.Revision)
	}
	// Somebody else edits first.
	if code, body := agentCall(t, h.UpdateAgent, "PUT", `{"description":"theirs"}`); code != 200 {
		t.Fatalf("PUT: %d %s", code, body)
	}
	code, body := agentCall(t, h.UpdateAgent, "PUT", fmt.Sprintf(`{"description":"mine","revision":%d}`, cur.Revision))
	if code != http.StatusConflict {
		t.Fatalf("an edit based on a revision that moved on must be 409, got %d %s", code, body)
	}
	var out struct {
		Error           string `json:"error"`
		CurrentRevision int64  `json:"current_revision"`
		CurrentVersion  string `json:"current_version"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.CurrentRevision <= cur.Revision || out.CurrentVersion != "0.1.0" {
		t.Fatalf("the refusal must say what the agent is now: %s", body)
	}
	if got, _ := s.GetAgent(context.Background(), "default", "bot"); got.Description != "theirs" {
		t.Fatalf("a refused edit changed the agent: %q", got.Description)
	}
	// The same edit naming the current revision (or sent as If-Match) is accepted.
	if code, body := agentCall(t, h.UpdateAgent, "PUT", fmt.Sprintf(`{"description":"mine","revision":%d}`, out.CurrentRevision)); code != 200 {
		t.Fatalf("an edit naming the current revision: %d %s", code, body)
	}
	if got, _ := s.GetAgent(context.Background(), "default", "bot"); got.Description != "mine" {
		t.Fatalf("the accepted edit: %+v", got)
	}
}

// A write made from a copy read before another write is refused by the store, whichever store it is,
// and a refused write changes nothing.
func TestTheStoreRefusesAWriteFromAStaleCopy(t *testing.T) {
	_, s := newTestHandlers(t)
	ctx := context.Background()
	if err := s.CreateAgent(ctx, &models.Agent{Name: "a", Kitchen: "default", Status: models.AgentStatusDraft, Description: "d0"}); err != nil {
		t.Fatal(err)
	}
	first, _ := s.GetAgent(ctx, "default", "a")
	second, _ := s.GetAgent(ctx, "default", "a")
	first.Description = "d1"
	if err := s.UpdateAgent(ctx, first); err != nil {
		t.Fatal(err)
	}
	if first.Revision != second.Revision+1 {
		t.Fatalf("a write moves the revision on, and leaves it on the caller's copy: %d then %d", second.Revision, first.Revision)
	}
	second.Description = "d2"
	err := s.UpdateAgent(ctx, second)
	var stale *store.ErrStale
	if !asErr(err, &stale) || stale.Revision != first.Revision {
		t.Fatalf("a stale copy must be refused with the current revision: %v", err)
	}
	if got, _ := s.GetAgent(ctx, "default", "a"); got.Description != "d1" {
		t.Fatalf("the refused write changed the agent: %q", got.Description)
	}
	// A lifecycle change moves the revision too: a copy read before it must not put the status back.
	if _, err := s.UpdateAgentLifecycle(ctx, "default", "a", models.AgentLifecycleGuard{}, models.AgentLifecycle{Status: models.AgentStatusCooled}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAgent(ctx, first); !asErr(err, &stale) {
		t.Fatalf("a copy read before a lifecycle change is stale: %v", err)
	}
	// A value that was never read from the store (revision 0) is written unconditionally.
	fresh := &models.Agent{Name: "a", Kitchen: "default", Status: models.AgentStatusReady, Description: "d3"}
	if err := s.UpdateAgent(ctx, fresh); err != nil {
		t.Fatal(err)
	}
}

func asErr(err error, target **store.ErrStale) bool {
	s, ok := err.(*store.ErrStale)
	if ok {
		*target = s
	}
	return ok
}
