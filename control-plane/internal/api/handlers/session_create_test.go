package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// POST /agents/{a}/sessions answered 500 "response withheld: it carried content without an access check"
// every time, and still created the session: the reply was a bare Session (a content type) and so was
// refused by respondJSON. A client retried and left one orphan session per attempt.
func TestCreatingASessionAnswers201AndCreatesExactlyOne(t *testing.T) {
	h, s := newTestHandlers(t)
	h.Sessions = sessions.NewMemorySessionStore()
	if err := s.CreateAgent(context.Background(), &models.Agent{Name: "bot", Kitchen: "default", Status: models.AgentStatusReady}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		code, body := agentCall(t, h.CreateSession, "POST", `{"max_turns":4}`)
		if code != http.StatusCreated {
			t.Fatalf("attempt %d: %d %s", i, code, body)
		}
		var out struct {
			ID       string `json:"id"`
			MaxTurns int    `json:"max_turns"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil || out.ID == "" || out.MaxTurns != 4 {
			t.Fatalf("the reply must be the session: %s", body)
		}
		all, _ := h.Sessions.ListSessions(context.Background(), "default", "bot")
		if len(all) != i {
			t.Fatalf("after %d creates the store holds %d sessions", i, len(all))
		}
	}
}
