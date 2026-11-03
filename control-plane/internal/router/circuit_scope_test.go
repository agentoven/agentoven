package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestClassifyStatus(t *testing.T) {
	boom := fmt.Errorf("boom")
	for status, want := range map[int]string{
		400: "request", 402: "request", 404: "request", 413: "request", 422: "request",
		401: "plain", 403: "plain", 408: "plain",
		429: "retryable", 500: "retryable", 503: "retryable",
		200: "plain",
	} {
		got := "plain"
		switch err := classifyStatus(status, boom); {
		case errors.As(err, new(*RequestError)):
			got = "request"
		case errors.As(err, new(*RetryableError)):
			got = "retryable"
		}
		if got != want {
			t.Errorf("status %d is %s, want %s", status, got, want)
		}
	}
	// The drivers' parsers use it: a malformed prompt and a wrong model name are the request's fault.
	for _, err := range []error{
		parseOpenAIError("openai", 400, []byte(`{"error":{"message":"bad prompt"}}`), "m"),
		parseOpenAIError("openai", 404, []byte(`{"error":{"message":"x"}}`), "m"),
		parseAnthropicError("anthropic", 400, []byte(`{"error":{"type":"invalid_request_error","message":"bad"}}`), "m"),
		parseOpenAIError("openrouter", 402, []byte(`{"error":{"message":"insufficient credits"}}`), ""),
	} {
		if providerFault(err) {
			t.Errorf("%v must not count against the provider", err)
		}
	}
	for _, err := range []error{
		parseOpenAIError("openai", 503, []byte(`{}`), "m"),
		parseOpenAIError("openai", 401, []byte(`{"error":{"message":"bad key"}}`), "m"),
		fmt.Errorf("dial tcp: connection refused"),
	} {
		if !providerFault(err) {
			t.Errorf("%v is the provider's failure and must count", err)
		}
	}
	if providerFault(context.Canceled) || providerFault(fmt.Errorf("x: %w", context.Canceled)) {
		t.Error("a caller that went away is not a provider failure")
	}
}

// kitchenDriver fails with failWith for the kitchens listed, and succeeds for the others.
type kitchenDriver struct {
	kind    string
	failFor map[string]error
	calls   map[string]*int32
}

func (d *kitchenDriver) Kind() string { return d.kind }
func (d *kitchenDriver) Call(_ context.Context, p *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	if c := d.calls[req.Kitchen]; c != nil {
		atomic.AddInt32(c, 1)
	}
	if err := d.failFor[req.Kitchen]; err != nil {
		return nil, err
	}
	return &models.RouteResponse{Provider: p.Name, Model: req.Model, Content: "ok"}, nil
}
func (d *kitchenDriver) HealthCheck(context.Context, *models.ModelProvider) error { return nil }

func twoKitchens(t *testing.T, failFor map[string]error) (*ModelRouter, *kitchenDriver) {
	t.Helper()
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	d := &kitchenDriver{kind: "scope-" + t.Name(), failFor: failFor, calls: map[string]*int32{"kitchen-a": new(int32), "kitchen-b": new(int32)}}
	mr := NewModelRouter(s)
	mr.RegisterDriver(d)
	for _, k := range []string{"kitchen-a", "kitchen-b"} {
		if err := s.CreateProvider(context.Background(), &models.ModelProvider{Name: "openai", Kitchen: k, Kind: d.Kind(),
			Models: []string{"m"}, IsDefault: true, Config: map[string]interface{}{"api_key": "k"}}); err != nil {
			t.Fatal(err)
		}
	}
	return mr, d
}

// Review finding 8: five failures in one kitchen opened the circuit of the provider name for every kitchen.
func TestOneKitchensFailuresDoNotOpenAnotherKitchensCircuit(t *testing.T) {
	mr, d := twoKitchens(t, map[string]error{"kitchen-a": fmt.Errorf("status 401: revoked key")})
	ctx := context.Background()
	for i := 0; i < circuitFailureThreshold+3; i++ {
		if _, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "kitchen-a", Model: "m"}); err == nil {
			t.Fatal("kitchen-a's key is revoked")
		}
	}
	// kitchen-a's own circuit is open: the provider is no longer called for it.
	before := atomic.LoadInt32(d.calls["kitchen-a"])
	_, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "kitchen-a", Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "circuit open") || atomic.LoadInt32(d.calls["kitchen-a"]) != before {
		t.Fatalf("kitchen-a's circuit should be open and skip the call: %v", err)
	}
	// kitchen-b has a provider of the same name and is untouched.
	if resp, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "kitchen-b", Model: "m"}); err != nil || resp.Content != "ok" {
		t.Fatalf("another kitchen must not get 'circuit open' for 30 seconds: %v", err)
	}
}

// Review finding 8: a user-caused 400 or a 402 counted as a provider failure.
func TestRequestCausedErrorsNeverOpenTheCircuit(t *testing.T) {
	for name, fail := range map[string]error{
		"400": parseOpenAIError("openai", 400, []byte(`{"error":{"message":"messages must not be empty"}}`), "m"),
		"402": parseOpenAIError("openai", 402, []byte(`{"error":{"message":"payment required"}}`), "m"),
		"404": parseOpenAIError("openai", 404, []byte(`{"error":{"message":"x"}}`), "m"),
	} {
		mr, d := twoKitchens(t, map[string]error{"kitchen-a": fail})
		ctx := context.Background()
		for i := 0; i < circuitFailureThreshold*3; i++ {
			if _, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "kitchen-a", Model: "m"}); err == nil || strings.Contains(err.Error(), "circuit open") {
				t.Fatalf("%s: call %d: %v (a request-caused error is returned as it is, never as 'circuit open')", name, i, err)
			}
		}
		delete(d.failFor, "kitchen-a") // the caller fixes the request
		if _, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "kitchen-a", Model: "m"}); err != nil {
			t.Fatalf("%s: the provider was healthy all along: %v", name, err)
		}
	}
}
