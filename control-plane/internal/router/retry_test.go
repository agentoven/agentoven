package router_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// flakyDriver fails with the given error for the first failUntil calls, then
// succeeds. callCount lets a test assert exactly how many attempts were made.
type flakyDriver struct {
	kind       string
	failUntil  int32
	failWith   error
	callCount  int32
	minCallsOK bool // if true, Call never errors; used for the circuit-breaker test
}

func (d *flakyDriver) Kind() string { return d.kind }
func (d *flakyDriver) Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	n := atomic.AddInt32(&d.callCount, 1)
	if !d.minCallsOK && n <= d.failUntil {
		return nil, d.failWith
	}
	return &models.RouteResponse{Provider: provider.Name, Model: req.Model, Content: "ok"}, nil
}
func (d *flakyDriver) HealthCheck(ctx context.Context, provider *models.ModelProvider) error {
	return nil
}

func routerWithProvider(t *testing.T, name string, driver router.ProviderDriver) (*router.ModelRouter, context.Context) {
	t.Helper()
	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	mr := router.NewModelRouter(s)
	mr.RegisterDriver(driver)

	ctx := context.Background()
	if err := s.CreateProvider(ctx, &models.ModelProvider{
		Name: name, Kitchen: "default", Kind: driver.Kind(), Models: []string{"test-model"},
		IsDefault: true, Config: map[string]interface{}{"api_key": "test"},
	}); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	return mr, ctx
}

func TestRetryableErrorSucceedsAfterTransientFailures(t *testing.T) {
	d := &flakyDriver{kind: "flaky-retry", failUntil: 2, failWith: router.Retryable(503, fmt.Errorf("server overloaded"))}
	mr, ctx := routerWithProvider(t, "p1", d)

	resp, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"})
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("unexpected content %q", resp.Content)
	}
	if got := atomic.LoadInt32(&d.callCount); got != 3 {
		t.Fatalf("expected 2 failures + 1 success = 3 calls, got %d", got)
	}
}

func TestNonRetryableErrorFailsFast(t *testing.T) {
	// 401 is never retryable — a wrong API key does not fix itself on attempt 2.
	d := &flakyDriver{kind: "flaky-401", failUntil: 100, failWith: fmt.Errorf("status 401: unauthorized")}
	mr, ctx := routerWithProvider(t, "p1", d)

	_, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"})
	if err == nil {
		t.Fatal("expected failure")
	}
	if got := atomic.LoadInt32(&d.callCount); got != 1 {
		t.Fatalf("a non-retryable error must not be retried, got %d calls", got)
	}
}

func TestThinkingCallsGetOnlyOneAttempt(t *testing.T) {
	d := &flakyDriver{kind: "flaky-thinking", failUntil: 100, failWith: router.Retryable(503, fmt.Errorf("overloaded"))}
	mr, ctx := routerWithProvider(t, "p1", d)

	_, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model", ThinkingEnabled: true})
	if err == nil {
		t.Fatal("expected failure")
	}
	if got := atomic.LoadInt32(&d.callCount); got != 1 {
		t.Fatalf("a thinking call should not be retried (expensive, often a real timeout), got %d calls", got)
	}
}

func TestCircuitBreakerOpensAfterRepeatedFailuresAndFallsThroughToNextProvider(t *testing.T) {
	bad := &flakyDriver{kind: "always-down", failUntil: 1000, failWith: router.Retryable(503, fmt.Errorf("down"))}
	good := &flakyDriver{kind: "always-up", minCallsOK: true}

	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	mr := router.NewModelRouter(s)
	mr.RegisterDriver(bad)
	mr.RegisterDriver(good)

	ctx := context.Background()
	// "bad" sorts first (fallback strategy, default first then by name) by being marked default.
	if err := s.CreateProvider(ctx, &models.ModelProvider{
		Name: "bad-provider", Kitchen: "default", Kind: "always-down", Models: []string{"test-model"}, IsDefault: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProvider(ctx, &models.ModelProvider{
		Name: "good-provider", Kitchen: "default", Kind: "always-up", Models: []string{"test-model"},
	}); err != nil {
		t.Fatal(err)
	}

	// Five full Route() calls — each retries against "bad-provider" and fails,
	// then falls through to "good-provider" and succeeds. The circuit on
	// bad-provider accumulates one failure per Route() call (not per retry),
	// so this must open it within the 5-call threshold.
	const circuitFailureThreshold = 5 // mirrors retry.go's circuitFailureThreshold
	for i := 0; i < circuitFailureThreshold+1; i++ {
		resp, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"})
		if err != nil {
			t.Fatalf("call %d: expected fallback to good-provider, got %v", i, err)
		}
		if resp.Provider != "good-provider" {
			t.Fatalf("call %d: expected good-provider, got %s", i, resp.Provider)
		}
	}

	callsBeforeOpen := atomic.LoadInt32(&bad.callCount)

	// One more call: the circuit should now be open, so bad-provider is
	// skipped entirely — its call count must not increase.
	resp, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"})
	if err != nil {
		t.Fatalf("expected fallback to still succeed, got %v", err)
	}
	if resp.Provider != "good-provider" {
		t.Fatalf("expected good-provider once the circuit is open, got %s", resp.Provider)
	}
	if got := atomic.LoadInt32(&bad.callCount); got != callsBeforeOpen {
		t.Fatalf("an open circuit must skip the provider entirely: calls went from %d to %d", callsBeforeOpen, got)
	}
}
