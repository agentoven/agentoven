package router_test

import (
	"context"
	"os"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// mockDriver is a test ProviderDriver.
type mockDriver struct {
	kind string
}

func (d *mockDriver) Kind() string { return d.kind }
func (d *mockDriver) Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	return &models.RouteResponse{
		Provider: provider.Name,
		Model:    req.Model,
		Content:  "mock response from " + d.kind,
	}, nil
}
func (d *mockDriver) HealthCheck(ctx context.Context, provider *models.ModelProvider) error {
	return nil
}

func newTestRouter(t *testing.T) *router.ModelRouter {
	t.Helper()
	// Use temp dir to avoid loading persistent data from ~/.agentoven/
	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	mr := router.NewModelRouter(s)
	return mr
}

func TestBuiltinDriversRegistered(t *testing.T) {
	mr := newTestRouter(t)

	drivers := mr.ListDrivers()
	expected := []string{"openai", "azure-openai", "anthropic", "ollama"}

	for _, exp := range expected {
		found := false
		for _, d := range drivers {
			if d == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected built-in driver %q not found in %v", exp, drivers)
		}
	}
}

func TestRegisterAndGetDriver(t *testing.T) {
	mr := newTestRouter(t)

	mock := &mockDriver{kind: "test-provider"}
	mr.RegisterDriver(mock)

	got := mr.GetDriver("test-provider")
	if got == nil {
		t.Fatal("GetDriver() returned nil for registered driver")
	}
	if got.Kind() != "test-provider" {
		t.Errorf("GetDriver().Kind() = %q, want %q", got.Kind(), "test-provider")
	}
}

func TestGetDriver_NotFound(t *testing.T) {
	mr := newTestRouter(t)

	got := mr.GetDriver("nonexistent")
	if got != nil {
		t.Errorf("GetDriver() for nonexistent should return nil, got %v", got)
	}
}

func TestRegisterDriver_Overrides(t *testing.T) {
	mr := newTestRouter(t)

	// Override the built-in openai driver
	custom := &mockDriver{kind: "openai"}
	mr.RegisterDriver(custom)

	got := mr.GetDriver("openai")
	if got == nil {
		t.Fatal("GetDriver() returned nil after override")
	}

	// Verify it's our mock by calling it
	resp, err := got.Call(context.Background(), &models.ModelProvider{Name: "test"}, &models.RouteRequest{Model: "gpt-4"})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if resp.Content != "mock response from openai" {
		t.Errorf("Call().Content = %q, want %q", resp.Content, "mock response from openai")
	}
}

func TestHealthCheck(t *testing.T) {
	mr := newTestRouter(t)

	mock := &mockDriver{kind: "healthy"}
	mr.RegisterDriver(mock)

	// HealthCheck() iterates over registered providers in the store,
	// not drivers directly. With no providers registered, result should be empty.
	result := mr.HealthCheck(context.Background())
	if len(result) != 0 {
		t.Errorf("HealthCheck() with no providers: got %d results, want 0", len(result))
	}
}

func TestHealthCheck_UnknownDriver(t *testing.T) {
	// HealthCheck iterates store providers. Without providers, nothing to check.
	// This test verifies the method doesn't panic with no providers.
	mr := newTestRouter(t)
	result := mr.HealthCheck(context.Background())
	if result == nil {
		t.Error("HealthCheck() should return non-nil map")
	}
}
