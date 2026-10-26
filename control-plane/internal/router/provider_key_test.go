package router_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/secretref"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

const geminiSecret = "AIza-SECRET-NEVER-IN-A-URL"

func geminiProvider(endpoint string) *models.ModelProvider {
	return &models.ModelProvider{
		Name: "g", Kitchen: "default", Kind: "gemini", Endpoint: endpoint, Models: []string{"gemini-2.5-flash"},
		Config: map[string]interface{}{"api_key": geminiSecret},
	}
}

// The key must travel in a header: in the URL it ends up in every transport error, which the API returns
// (a provider test, a failed run) and the logs keep.
func TestGeminiKeyIsSentInAHeaderNotTheURL(t *testing.T) {
	var gotURL, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL, gotHeader = r.URL.String(), r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer srv.Close()

	mr := newTestRouter(t)
	p := geminiProvider(srv.URL)
	for name, call := range map[string]func() error{
		"chat": func() error {
			_, err := mr.GetDriver("gemini").Call(context.Background(), p, &models.RouteRequest{Model: "gemini-2.5-flash", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}})
			return err
		},
		"health": func() error { return mr.GetDriver("gemini").HealthCheck(context.Background(), p) },
	} {
		gotURL, gotHeader = "", ""
		_ = call()
		if strings.Contains(gotURL, geminiSecret) || strings.Contains(gotURL, "key=") {
			t.Errorf("%s: the key is in the URL: %s", name, gotURL)
		}
		if gotHeader != geminiSecret {
			t.Errorf("%s: the key should be sent as x-goog-api-key, got %q", name, gotHeader)
		}
	}
}

func TestGeminiErrorsNeverContainTheKey(t *testing.T) {
	mr := newTestRouter(t)
	p := geminiProvider("http://127.0.0.1:1") // nothing listens here
	_, err := mr.GetDriver("gemini").Call(context.Background(), p, &models.RouteRequest{Model: "m", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), geminiSecret) {
		t.Errorf("the error carries the key: %v", err)
	}
	if err := mr.GetDriver("gemini").HealthCheck(context.Background(), p); err == nil || strings.Contains(err.Error(), geminiSecret) {
		t.Errorf("health check error: %v", err)
	}
	res := mr.TestProvider(context.Background(), p)
	if strings.Contains(res.Error, geminiSecret) {
		t.Errorf("the provider test result carries the key: %q", res.Error)
	}
}

func TestProviderKeyIsReadFromTheProviderEachTime(t *testing.T) {
	// A temp data dir, so the store neither reads nor writes the developer's ~/.agentoven.
	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	mr := router.NewModelRouter(s)
	ctx := context.Background()
	p := &models.ModelProvider{ID: "1", Name: "oai", Kitchen: "default", Kind: "openai", Config: map[string]interface{}{"api_key": "sk-old"}}
	if err := s.CreateProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, err := mr.ProviderKey(ctx, "default", "oai"); err != nil || got != "sk-old" {
		t.Fatalf("got %q, %v", got, err)
	}
	p.Config["api_key"] = "sk-rotated"
	if err := s.UpdateProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, _ := mr.ProviderKey(ctx, "default", "oai"); got != "sk-rotated" {
		t.Errorf("a rotated key should be used at once, got %q", got)
	}
	if _, err := mr.ProviderKey(ctx, "default", "gone"); err == nil {
		t.Error("an unknown provider should be an error")
	}
	if _, err := mr.ProviderKey(ctx, "other-kitchen", "oai"); err == nil {
		t.Error("a provider of another kitchen must not be readable")
	}
}

// A provider that keeps its key in an external secret, or only in its rotation pool, has no key of its
// own in config. Every path that reaches a driver must still find the key.
func TestAProviderWithNoStoredKeyWorksOnEveryPath(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{} // "METHOD /path" -> Authorization
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	t.Setenv("PK_TEST_PROVIDER_KEY", "sk-from-the-environment")
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	mr := router.NewModelRouter(s)
	mr.SetSecretResolver(secretref.New())
	ctx := context.Background()

	byRef := &models.ModelProvider{ID: "1", Name: "by-ref", Kitchen: "default", Kind: "openai", Endpoint: srv.URL, Models: []string{"m-ref"}, SecretRef: "env://PK_TEST_PROVIDER_KEY"}
	byPool := &models.ModelProvider{ID: "2", Name: "by-pool", Kitchen: "default", Kind: "openai", Endpoint: srv.URL, Models: []string{"m-pool"},
		APIKeys: []models.APIKeyEntry{{Key: "sk-from-the-pool", Enabled: true}}}
	for _, p := range []*models.ModelProvider{byRef, byPool} {
		if err := s.CreateProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name     string
		provider *models.ModelProvider
		want     string
	}{{"secret_ref", byRef, "Bearer sk-from-the-environment"}, {"key pool", byPool, "Bearer sk-from-the-pool"}} {
		t.Run(c.name, func(t *testing.T) {
			model := c.provider.Models[0]
			check := func(path string, do func() error) {
				mu.Lock()
				delete(seen, path)
				mu.Unlock()
				_ = do() // the fake only answers like a model; what matters is the key it was sent
				mu.Lock()
				got, called := seen[path]
				mu.Unlock()
				if !called {
					t.Errorf("%s: the provider was never called", path)
				} else if got != c.want {
					t.Errorf("%s: sent %q, want %q", path, got, c.want)
				}
			}
			check("POST /chat/completions", func() error {
				_, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: model, Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}})
				return err
			})
			check("POST /chat/completions", func() error {
				return mr.RouteStream(ctx, &models.RouteRequest{Kitchen: "default", Model: model, Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}}, func(*models.StreamChunk) error { return nil })
			})
			check("POST /chat/completions", func() error {
				if res := mr.TestProvider(ctx, c.provider); !res.Healthy {
					return fmt.Errorf("%s", res.Error)
				}
				return nil
			})
			check("GET /models", func() error {
				_, err := mr.DiscoverModelsForProvider(ctx, c.provider)
				return err
			})
		})
	}
	// The test did not leave the key on the provider it was handed.
	if _, set := byRef.Config["api_key"]; set {
		t.Error("testing a provider must not write the key onto it")
	}
	// A long-lived holder reads the provider fresh, with the key applied.
	p, err := mr.FreshProvider(ctx, "default", "by-ref")
	if err != nil || p.Config["api_key"] != "sk-from-the-environment" {
		t.Errorf("FreshProvider: %v %v", p, err)
	}
}
