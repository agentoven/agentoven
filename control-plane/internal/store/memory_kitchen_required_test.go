package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// An empty kitchen used to mean "every kitchen" in the in-memory store's list calls, so a caller that lost
// the kitchen on the way listed everyone's records. Each list is now an error without one, and with one it
// returns that kitchen's records and no other's.
func TestNoListTakesAnEmptyKitchenForAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, k := range []string{"alpha", "beta"} {
		mustNoErr(t, s.CreateAgent(ctx, &models.Agent{Name: "a-" + k, Kitchen: k, Framework: "agentoven", Version: "1.0.0"}))
		mustNoErr(t, s.CreateRecipe(ctx, &models.Recipe{Name: "r-" + k, Kitchen: k, Version: "1.0.0"}))
		mustNoErr(t, s.CreateProvider(ctx, &models.ModelProvider{Name: "p-" + k, Kind: "openai", Kitchen: k}))
		mustNoErr(t, s.CreateTool(ctx, &models.MCPTool{Name: "t-" + k, Kitchen: k}))
		mustNoErr(t, s.CreatePrompt(ctx, &models.Prompt{Name: "pr-" + k, Kitchen: k, Template: "x"}))
		mustNoErr(t, s.CreateChannel(ctx, &models.NotificationChannel{Name: "c-" + k, Kind: "webhook", Kitchen: k}))
		mustNoErr(t, s.CreateTrace(ctx, &models.Trace{ID: "tr-" + k, Kitchen: k, AgentName: "a-" + k}))
		mustNoErr(t, s.CreateSkill(ctx, &models.Skill{Name: "s-" + k, Kitchen: k}))
		mustNoErr(t, s.CreateAuditEvent(ctx, &models.AuditEvent{ID: "ev-" + k, Kitchen: k, Action: "x"}))
	}

	empty := map[string]func() (int, error){
		"agents":            func() (int, error) { l, err := s.ListAgents(ctx, ""); return len(l), err },
		"recipes":           func() (int, error) { l, err := s.ListRecipes(ctx, ""); return len(l), err },
		"providers":         func() (int, error) { l, err := s.ListProviders(ctx, ""); return len(l), err },
		"tools":             func() (int, error) { l, err := s.ListTools(ctx, ""); return len(l), err },
		"prompts":           func() (int, error) { l, err := s.ListPrompts(ctx, ""); return len(l), err },
		"channels":          func() (int, error) { l, err := s.ListChannels(ctx, ""); return len(l), err },
		"skills":            func() (int, error) { l, err := s.ListSkills(ctx, ""); return len(l), err },
		"traces":            func() (int, error) { l, err := s.ListTraces(ctx, "", 50); return len(l), err },
		"traces (filtered)": func() (int, error) { l, err := s.ListTracesFiltered(ctx, "", store.TraceFilter{}); return len(l), err },
		"trace stats":       func() (int, error) { _, err := s.TraceStats(ctx, "", store.TraceFilter{}); return 0, err },
		"audit events":      func() (int, error) { l, err := s.ListAuditEvents(ctx, models.AuditFilter{}); return len(l), err },
		"audit count":       func() (int, error) { n, err := s.CountAuditEvents(ctx, models.AuditFilter{}); return int(n), err },
	}
	for name, call := range empty {
		if n, err := call(); !errors.Is(err, store.ErrKitchenRequired) || n != 0 {
			t.Errorf("%s with no kitchen: %d records, err %v; want ErrKitchenRequired", name, n, err)
		}
	}

	// With a kitchen, the kitchen's own and nothing of another's.
	if l, err := s.ListProviders(ctx, "alpha"); err != nil || len(l) != 1 || l[0].Name != "p-alpha" {
		t.Errorf("providers of alpha: %v %v", l, err)
	}
	if l, err := s.ListAuditEvents(ctx, models.AuditFilter{Kitchen: "beta"}); err != nil || len(l) != 1 || l[0].ID != "ev-beta" {
		t.Errorf("audit events of beta: %v %v", l, err)
	}
	if l, err := s.ListAgents(ctx, "alpha"); err != nil || len(l) != 1 || l[0].Name != "a-alpha" {
		t.Errorf("agents of alpha: %v %v", l, err)
	}
	if l, err := s.ListTracesFiltered(ctx, "alpha", store.TraceFilter{}); err != nil || len(l) != 1 {
		t.Errorf("traces of alpha: %v %v", l, err)
	}
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
