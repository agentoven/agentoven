package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/store"
)

type pgLike struct{ code, msg string }

func (e pgLike) Error() string    { return e.msg }
func (e pgLike) SQLState() string { return e.code }

func TestStoreErrorsAreAnsweredWithTheRightStatusAndNoSQL(t *testing.T) {
	for name, c := range map[string]struct {
		err    error
		status int
	}{
		"conflict":         {&store.ErrConflict{Entity: "agent", Key: "a"}, http.StatusConflict},
		"wrapped conflict": {fmt.Errorf("create agent: %w", &store.ErrConflict{Entity: "agent", Key: "a"}), http.StatusConflict},
		"not found":        {&store.ErrNotFound{Entity: "agent", Key: "a"}, http.StatusNotFound},
		"raw unique":       {fmt.Errorf("create agent: %w", pgLike{"23505", `ERROR: duplicate key value violates unique constraint "agents_name_kitchen_key" (SQLSTATE 23505)`}), http.StatusConflict},
		"foreign key":      {fmt.Errorf("create tool: %w", pgLike{"23503", `insert or update on table "x" violates foreign key constraint "y"`}), http.StatusConflict},
		"anything else":    {fmt.Errorf(`pq: relation "agents" does not exist`), http.StatusInternalServerError},
	} {
		rec := httptest.NewRecorder()
		respondCreateError(rec, c.err, "register the agent")
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d", name, rec.Code, c.status)
		}
		body := rec.Body.String()
		if name != "conflict" && name != "wrapped conflict" && name != "not found" {
			for _, leak := range []string{"SQLSTATE", "constraint", "relation", "agents_name"} {
				if strings.Contains(body, leak) {
					t.Errorf("%s: the body leaks %q: %s", name, leak, body)
				}
			}
		}
	}
}
