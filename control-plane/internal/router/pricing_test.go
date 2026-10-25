package router_test

import (
	"context"
	"os"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func priceRouter(t *testing.T, providers ...models.ModelProvider) *router.ModelRouter {
	t.Helper()
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	for i := range providers {
		providers[i].Kitchen = "default"
		if err := s.CreateProvider(context.Background(), &providers[i]); err != nil {
			t.Fatal(err)
		}
	}
	return router.NewModelRouter(s)
}

func TestPriceForReportsWhetherThePriceIsReal(t *testing.T) {
	mr := priceRouter(t,
		models.ModelProvider{Name: "oa", Kind: "openai", Models: []string{"gpt-4o"}},
		models.ModelProvider{Name: "local", Kind: "ollama", Models: []string{"llama3"}},
		models.ModelProvider{Name: "mine", Kind: "openai", Models: []string{"custom"},
			Config: map[string]interface{}{"cost_per_1k_input": 0.5, "cost_per_1k_output": 1.5}},
		models.ModelProvider{Name: "mystery", Kind: "openai", Models: []string{"never-heard-of-it"}},
	)
	ctx := context.Background()

	cases := []struct {
		name, provider, model string
		known                 bool
		source                string
		in, out               float64
	}{
		{"builtin table", "oa", "gpt-4o", true, "builtin", 0.0025, 0.01},
		{"local model is known and free", "local", "llama3", true, "local", 0, 0},
		{"operator-set price wins", "mine", "custom", true, "provider_config", 0.5, 1.5},
		{"an unknown model is not priced", "mystery", "never-heard-of-it", false, "fallback", 0.001, 0.001},
		{"an unknown provider is not priced", "nope", "gpt-4o", false, "fallback", 0.001, 0.001},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := mr.PriceFor(ctx, "default", c.provider, c.model)
			if p.Known != c.known || p.Source != c.source || p.InputPer1K != c.in || p.OutputPer1K != c.out {
				t.Fatalf("got %+v, want known=%v source=%s in=%v out=%v", p, c.known, c.source, c.in, c.out)
			}
		})
	}
}
