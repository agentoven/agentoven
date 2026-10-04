package router_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// namedDriver always answers with its own provider's name in Content, so a
// test can see exactly which provider actually served a call.
type namedDriver struct {
	kind string
	fail bool
}

func (d *namedDriver) Kind() string { return d.kind }
func (d *namedDriver) Call(_ context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	if d.fail {
		return nil, fmt.Errorf("%s is down", provider.Name)
	}
	return &models.RouteResponse{Provider: provider.Name, Model: req.Model, Content: provider.Name}, nil
}
func (d *namedDriver) HealthCheck(context.Context, *models.ModelProvider) error { return nil }

func twoProviderRouter(t *testing.T, bFails bool) (*router.ModelRouter, context.Context) {
	t.Helper()
	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	mr := router.NewModelRouter(s)
	mr.RegisterDriver(&namedDriver{kind: "kind-a"})
	mr.RegisterDriver(&namedDriver{kind: "kind-b", fail: bFails})

	ctx := context.Background()
	// "a" registered first/default — anything that falls through to normal
	// strategy ordering would naturally prefer it over "b".
	if err := s.CreateProvider(ctx, &models.ModelProvider{Name: "a", Kitchen: "default", Kind: "kind-a", Models: []string{"m"}, IsDefault: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProvider(ctx, &models.ModelProvider{Name: "b", Kitchen: "default", Kind: "kind-b", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	return mr, ctx
}

func TestPinnedProviderRoutesToExactlyTheNamedProvider(t *testing.T) {
	mr, ctx := twoProviderRouter(t, false)
	resp, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", PinnedProvider: "b", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "b" {
		t.Fatalf("expected the pinned provider 'b' to serve the call despite 'a' being default, got %q", resp.Provider)
	}
}

func TestPinnedProviderDoesNotFallBackOnFailure(t *testing.T) {
	mr, ctx := twoProviderRouter(t, true) // "b" fails
	_, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", PinnedProvider: "b", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected pinning to 'b' and 'b' failing to be a hard error, not a silent fallback to 'a'")
	}
}

func TestPinnedProviderErrorsIfNotFound(t *testing.T) {
	mr, ctx := twoProviderRouter(t, false)
	_, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", PinnedProvider: "does-not-exist", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected an error pinning to a provider that doesn't exist in this kitchen")
	}
}
