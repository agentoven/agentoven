package router

import (
	"context"
	"strings"
)

// Price is what one model costs per 1,000 tokens, and how much that figure can be trusted.
type Price struct {
	InputPer1K  float64
	OutputPer1K float64
	// Known is true when the figure came from the provider's own config, the model
	// catalog, or the built-in table, or the model is local and free. It is false when the
	// router fell back to its generic placeholder, in which case a dollar cap computed from
	// it would be a guess.
	Known  bool
	Source string // "provider_config", "catalog", "builtin", "local", or "fallback"
}

// PriceFor reports the price of model on the named provider in a kitchen, using the same
// order as the cost the router records (provider config, then the catalog, then the
// built-in table) but, unlike that path, saying when it had to guess.
//
// A run budget in dollars needs this: getModelCost silently answers 0.001 per 1K for any
// model it does not know, so a cap measured against that figure would look enforced while
// being wrong. Callers refuse a dollar cap on an unpriced model instead (tokens stay exact).
func (mr *ModelRouter) PriceFor(ctx context.Context, kitchen, providerName, model string) Price {
	provider, err := mr.store.GetProvider(ctx, kitchen, providerName)
	if err != nil || provider == nil {
		return Price{InputPer1K: 0.001, OutputPer1K: 0.001, Source: "fallback"}
	}

	// A model that runs on the operator's own hardware has no per-token bill.
	if strings.EqualFold(provider.Kind, "ollama") {
		return Price{Known: true, Source: "local"}
	}

	in, inOK := configCost(provider.Config, "cost_per_1k_input")
	out, outOK := configCost(provider.Config, "cost_per_1k_output")
	if inOK && outOK {
		return Price{InputPer1K: in, OutputPer1K: out, Known: true, Source: "provider_config"}
	}

	if mr.catalog != nil {
		if c := mr.catalog.Lookup(provider.Kind, model); c != nil && (c.InputCostPer1K > 0 || c.OutputCostPer1K > 0) {
			return Price{InputPer1K: c.InputCostPer1K, OutputPer1K: c.OutputCostPer1K, Known: true, Source: "catalog"}
		}
	}

	if costs, ok := defaultCosts[model]; ok {
		return Price{InputPer1K: costs["input"], OutputPer1K: costs["output"], Known: true, Source: "builtin"}
	}
	return Price{InputPer1K: 0.001, OutputPer1K: 0.001, Source: "fallback"}
}

func configCost(cfg map[string]interface{}, key string) (float64, bool) {
	switch v := cfg[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}
