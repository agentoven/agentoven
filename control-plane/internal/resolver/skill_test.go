package resolver

import (
	"context"
	"os"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	return s
}

func TestResolveSkillRequiresAnAcceptedSkill(t *testing.T) {
	s := newTestStore(t)
	r := NewResolver(s)
	ctx := context.Background()

	for _, status := range []models.SkillStatus{models.SkillStatusPending, models.SkillStatusRejected, models.SkillStatusNeedsReview} {
		if err := s.CreateSkill(ctx, &models.Skill{Kitchen: "default", Name: "web_search", Status: status, Manifest: &models.SkillManifest{Name: "web_search"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.resolveSkill(ctx, "default", models.Ingredient{Name: "web_search", Kind: models.IngredientSkill}); err == nil {
			t.Fatalf("expected a %q skill to fail to resolve", status)
		}
	}
}

func TestResolveSkillResolvesAnAcceptedSkillsTools(t *testing.T) {
	s := newTestStore(t)
	r := NewResolver(s)
	ctx := context.Background()

	if err := s.CreateTool(ctx, &models.MCPTool{
		Name: "web_search.search", Kitchen: "default", Endpoint: "https://example.com/mcp",
		Transport: "http", Enabled: true, Capabilities: []string{"tool"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSkill(ctx, &models.Skill{
		Kitchen: "default", Name: "web_search", Status: models.SkillStatusAccepted,
		Manifest:        &models.SkillManifest{Name: "web_search", Instructions: "Use search for current events."},
		RegisteredTools: []string{"web_search.search"},
	}); err != nil {
		t.Fatal(err)
	}

	rsk, err := r.resolveSkill(ctx, "default", models.Ingredient{Name: "web_search", Kind: models.IngredientSkill})
	if err != nil {
		t.Fatal(err)
	}
	if rsk.Name != "web_search" || rsk.Instructions != "Use search for current events." {
		t.Fatalf("unexpected resolved skill: %+v", rsk)
	}
	if len(rsk.Tools) != 1 || rsk.Tools[0].Name != "web_search.search" {
		t.Fatalf("expected the skill's registered tool to resolve, got %+v", rsk.Tools)
	}
}

func TestResolveSkillSkipsARegisteredToolThatNoLongerExists(t *testing.T) {
	s := newTestStore(t)
	r := NewResolver(s)
	ctx := context.Background()

	if err := s.CreateSkill(ctx, &models.Skill{
		Kitchen: "default", Name: "web_search", Status: models.SkillStatusAccepted,
		Manifest:        &models.SkillManifest{Name: "web_search"},
		RegisteredTools: []string{"web_search.search"}, // never actually created
	}); err != nil {
		t.Fatal(err)
	}

	rsk, err := r.resolveSkill(ctx, "default", models.Ingredient{Name: "web_search", Kind: models.IngredientSkill})
	if err != nil {
		t.Fatalf("a skill with a dangling tool reference should still resolve (with fewer tools), got error: %v", err)
	}
	if len(rsk.Tools) != 0 {
		t.Fatalf("expected the missing tool to be skipped, got %+v", rsk.Tools)
	}
}

func TestResolveViaIngredientsIncludesSkills(t *testing.T) {
	s := newTestStore(t)
	r := NewResolver(s)
	ctx := context.Background()

	if err := s.CreateProvider(ctx, &models.ModelProvider{Name: "p", Kitchen: "default", Kind: "openai", Models: []string{"gpt-4o"}, IsDefault: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSkill(ctx, &models.Skill{
		Kitchen: "default", Name: "web_search", Status: models.SkillStatusAccepted,
		Manifest: &models.SkillManifest{Name: "web_search", Instructions: "search away"},
	}); err != nil {
		t.Fatal(err)
	}

	agent := &models.Agent{
		Name: "a", Kitchen: "default", Mode: models.AgentModeManaged,
		Ingredients: []models.Ingredient{
			{Name: "p", Kind: models.IngredientModel, Config: map[string]interface{}{"provider": "p", "model": "gpt-4o"}},
			{Name: "web_search", Kind: models.IngredientSkill, Required: true},
		},
	}
	resolved, err := r.Resolve(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Skills) != 1 || resolved.Skills[0].Name != "web_search" {
		t.Fatalf("expected the skill ingredient to resolve via Resolve(), got %+v", resolved.Skills)
	}
}
