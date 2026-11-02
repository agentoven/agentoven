package store_test

import (
	"context"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func bakeWith(t *testing.T, s interface {
	GetAgent(context.Context, string, string) (*models.Agent, error)
	UpdateAgent(context.Context, *models.Agent) error
}, prompt string) *models.Agent {
	t.Helper()
	ctx := context.Background()
	a, err := s.GetAgent(ctx, "k", "bot")
	if err != nil {
		t.Fatal(err)
	}
	a.ResolvedConfig = &models.ResolvedIngredients{Prompt: &models.ResolvedPrompt{Name: "p", Template: prompt}}
	a.VersionBump = models.VersionBumpBake
	if err := s.UpdateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAgent(ctx, "k", "bot")
	return got
}

// The version moves only when a bake carries a different configuration, and every version is kept.
func TestAgentVersionMovesOnlyOnAChangedBake(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateAgent(ctx, &models.Agent{Name: "bot", Kitchen: "k"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetAgent(ctx, "k", "bot"); v.Version != "0.1.0" {
		t.Fatalf("a new agent starts at 0.1.0, got %s", v.Version)
	}

	if got := bakeWith(t, s, "be brief"); got.Version != "0.1.0" {
		t.Fatalf("the first bake keeps the registered version, got %s", got.Version)
	}

	// An edit is a draft: no new version, and the stored version is not rewritten.
	a, _ := s.GetAgent(ctx, "k", "bot")
	a.Description = "edited"
	if err := s.UpdateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAgent(ctx, "k", "bot"); got.Version != "0.1.0" {
		t.Fatalf("an edit must not change the version, got %s", got.Version)
	}

	// Re-baking the same configuration changes nothing.
	if got := bakeWith(t, s, "be brief"); got.Version != "0.1.0" {
		t.Fatalf("an unchanged re-bake keeps the version, got %s", got.Version)
	}

	// A changed prompt takes the next minor version and the old one stays readable as it was.
	if got := bakeWith(t, s, "be thorough"); got.Version != "0.2.0" {
		t.Fatalf("a changed bake takes the next minor version, got %s", got.Version)
	}
	old, err := s.GetAgentVersion(ctx, "k", "bot", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if old.ResolvedConfig.Prompt.Template != "be brief" {
		t.Fatalf("0.1.0 must still hold the prompt it was baked with, got %q", old.ResolvedConfig.Prompt.Template)
	}
	all, _ := s.ListAgentVersions(ctx, "k", "bot")
	if len(all) != 2 {
		t.Fatalf("both versions are kept, got %d", len(all))
	}
}

func TestBakedFingerprintIgnoresStateAndResolveTime(t *testing.T) {
	a := &models.Agent{Name: "x", Status: models.AgentStatusReady, ResolvedConfig: &models.ResolvedIngredients{
		Tools: []models.ResolvedTool{{Name: "t"}}}}
	b := *a
	b.Status = models.AgentStatusBurnt
	b.Tags = map[string]string{"owner": "me"}
	if models.BakedFingerprint(a) != models.BakedFingerprint(&b) {
		t.Fatal("status and tags are not behaviour")
	}
	b.ModelName = "other"
	if models.BakedFingerprint(a) == models.BakedFingerprint(&b) {
		t.Fatal("a different model is behaviour")
	}
}
