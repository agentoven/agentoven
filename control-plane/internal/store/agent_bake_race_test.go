package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func seedBaked(t *testing.T, prompt string) (context.Context, interface {
	GetAgent(context.Context, string, string) (*models.Agent, error)
	UpdateAgent(context.Context, *models.Agent) error
	ListAgentVersions(context.Context, string, string) ([]models.Agent, error)
	UpdateAgentLifecycle(context.Context, string, string, models.AgentLifecycleGuard, models.AgentLifecycle) (bool, error)
}) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateAgent(ctx, &models.Agent{Name: "bot", Kitchen: "k"}); err != nil {
		t.Fatal(err)
	}
	bakeWith(t, s, prompt)
	return ctx, s
}

func versionList(t *testing.T, s interface {
	ListAgentVersions(context.Context, string, string) ([]models.Agent, error)
}) []string {
	t.Helper()
	all, err := s.ListAgentVersions(context.Background(), "k", "bot")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range all {
		out = append(out, a.Version)
	}
	return out
}

// Concurrent identical bakes, each read before any of them wrote, append exactly one new version
// (the review found 0.1.0, 0.2.0, 0.2.0). Run with -race.
func TestConcurrentIdenticalBakesAppendOneVersion(t *testing.T) {
	ctx, s := seedBaked(t, "one")
	const n = 12
	copies := make([]*models.Agent, n)
	for i := range copies { // every request reads the agent at 0.1.0 before any writes
		a, _ := s.GetAgent(ctx, "k", "bot")
		a.ResolvedConfig = &models.ResolvedIngredients{Prompt: &models.ResolvedPrompt{Name: "p", Template: "two"}}
		a.VersionBump = models.VersionBumpBake
		copies[i] = a
	}
	var wg sync.WaitGroup
	for _, a := range copies {
		wg.Add(1)
		go func(a *models.Agent) {
			defer wg.Done()
			if err := s.UpdateAgent(ctx, a); err != nil {
				t.Error(err)
			}
		}(a)
	}
	wg.Wait()
	if got := versionList(t, s); len(got) != 2 || got[0] != "0.1.0" || got[1] != "0.2.0" {
		t.Fatalf("versions = %v, want [0.1.0 0.2.0]", got)
	}
	if cur, _ := s.GetAgent(ctx, "k", "bot"); cur.Version != "0.2.0" {
		t.Fatalf("agent at %s", cur.Version)
	}
}

// An update that carries a stale or invented version and asks for nothing must not move the agent: the
// stored version stays (a stale copy written back used to rewrite it and start a snapshot).
func TestAPlainUpdateNeverChangesTheStoredVersion(t *testing.T) {
	ctx, s := seedBaked(t, "one")
	a, _ := s.GetAgent(ctx, "k", "bot")
	a.Version = "7.7.7"
	a.Tags = map[string]string{"x": "y"}
	if err := s.UpdateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.GetAgent(ctx, "k", "bot"); cur.Version != "0.1.0" {
		t.Fatalf("a plain update changed the version to %s", cur.Version)
	}
	if got := versionList(t, s); len(got) != 1 {
		t.Fatalf("a plain update wrote a snapshot: %v", got)
	}
}

func TestNamingAVersionOnABake(t *testing.T) {
	ctx, s := seedBaked(t, "one")
	named := func(version, prompt string) error {
		a, _ := s.GetAgent(ctx, "k", "bot")
		a.ResolvedConfig = &models.ResolvedIngredients{Prompt: &models.ResolvedPrompt{Name: "p", Template: prompt}}
		a.Version, a.VersionBump = version, models.VersionBumpNamed
		return s.UpdateAgent(ctx, a)
	}
	var ve *models.VersionError

	// The review's reproduction: the old version named for a changed configuration.
	if err := named("0.1.0", "two"); !errors.As(err, &ve) || !ve.Conflict {
		t.Fatalf("naming the existing version for a changed bake must be refused, got %v", err)
	}
	for _, bad := range []string{"9.9.9\nx", "../../etc", "<script>", " "} {
		if err := named(bad, "two"); !errors.As(err, &ve) || ve.Conflict {
			t.Fatalf("%q must be refused as malformed, got %v", bad, err)
		}
	}
	if got := versionList(t, s); len(got) != 1 {
		t.Fatalf("a refused name wrote a version: %v", got)
	}
	// The same configuration under its own version is a no-op; a higher version is a new snapshot.
	if err := named("0.1.0", "one"); err != nil {
		t.Fatalf("re-baking the same configuration under its version is fine: %v", err)
	}
	if err := named("1.0.0", "two"); err != nil {
		t.Fatal(err)
	}
	if got := versionList(t, s); len(got) != 2 || got[1] != "1.0.0" {
		t.Fatalf("versions = %v", got)
	}
	if v, _ := s.(interface {
		GetAgentVersion(context.Context, string, string, string) (*models.Agent, error)
	}).GetAgentVersion(ctx, "k", "bot", "0.1.0"); v == nil || v.ResolvedConfig.Prompt.Template != "one" {
		t.Fatal("0.1.0 must still hold what it was baked with")
	}
	// Going back is refused too.
	if err := named("0.5.0", "three"); !errors.As(err, &ve) || !ve.Conflict {
		t.Fatalf("a version below the latest must be refused, got %v", err)
	}
}

// The lifecycle update changes status and tags only, and only while the guard holds.
func TestLifecycleUpdateTouchesOnlyItsFieldsAndRespectsTheGuard(t *testing.T) {
	ctx, s := seedBaked(t, "one")
	a, _ := s.GetAgent(ctx, "k", "bot")
	a.Status = models.AgentStatusBaking
	a.Tags = map[string]string{"owner": "me", "error": "old"}
	if err := s.UpdateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	// An edit lands after the bake began.
	e, _ := s.GetAgent(ctx, "k", "bot")
	e.Guardrails = []models.Guardrail{{ID: "late-edit"}}
	if err := s.UpdateAgent(ctx, e); err != nil {
		t.Fatal(err)
	}

	guard := models.AgentLifecycleGuard{Status: models.AgentStatusBaking, Version: "0.1.0"}
	ok, err := s.UpdateAgentLifecycle(ctx, "k", "bot", guard, models.AgentLifecycle{
		Status: models.AgentStatusReady, DeleteTags: []string{"error"}, BackendEndpoint: "http://x"})
	if err != nil || !ok {
		t.Fatalf("applied=%v err=%v", ok, err)
	}
	got, _ := s.GetAgent(ctx, "k", "bot")
	if got.Status != models.AgentStatusReady || got.Tags["owner"] != "me" || got.Tags["error"] != "" || got.BackendEndpoint != "http://x" {
		t.Fatalf("lifecycle result wrong: %+v", got)
	}
	if len(got.Guardrails) != 1 || got.Guardrails[0].ID != "late-edit" {
		t.Fatalf("the lifecycle update overwrote an edit: %+v", got.Guardrails)
	}
	// Stale: the agent is no longer baking, so nothing is applied.
	ok, err = s.UpdateAgentLifecycle(ctx, "k", "bot", guard, models.AgentLifecycle{Status: models.AgentStatusBurnt})
	if err != nil || ok {
		t.Fatalf("a stale outcome must be dropped: applied=%v err=%v", ok, err)
	}
	if got, _ := s.GetAgent(ctx, "k", "bot"); got.Status != models.AgentStatusReady {
		t.Fatalf("stale outcome applied: %s", got.Status)
	}
	if _, err := s.UpdateAgentLifecycle(ctx, "k", "nobody", guard, models.AgentLifecycle{}); err == nil {
		t.Fatal("an unknown agent is an error")
	}
}
