package router

import (
	"context"
	"regexp"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Price is what one model costs per 1,000 tokens, and how much that figure can be trusted.
type Price struct {
	InputPer1K  float64
	OutputPer1K float64

	// Rates that differ from the plain ones; zero means "the same as input" or "the same as output".
	CacheReadPer1K  float64 // prompt tokens served from the provider's cache
	CacheWritePer1K float64 // prompt tokens written to the cache
	ReasoningPer1K  float64 // thinking tokens, for the few models that bill them apart from output

	// Above LongContextOver prompt tokens the model bills at the Long rates (0 = no tier).
	LongContextOver int64
	LongInputPer1K  float64
	LongOutputPer1K float64

	// Known is true when the figure is the model's own: an operator override for that model, the
	// catalog, the built-in table, or a local model that is free. It is false when the router fell back
	// to its generic placeholder, in which case a dollar cap computed from it would be a guess.
	Known bool
	// Source is where the figure came from: "model_override", "catalog", "builtin", "provider_default",
	// "local" or "fallback".
	Source string
}

// priceOf finds the price of one model on one provider. The model's own price always beats anything
// that describes the provider as a whole, so a provider that serves a cheap model and an expensive one
// charges each at its own rate rather than at a single average:
//
//  1. model_costs on the provider, an operator's price for that model (negotiated rates, a model the
//     catalog does not know);
//  2. the catalog's price for the model (exact name, then without a snapshot date);
//  3. the built-in table;
//  4. cost_per_1k_input and cost_per_1k_output on the provider, a default for the models that have
//     no price of their own;
//  5. a placeholder, flagged as not known.
//
// A model running on the operator's own hardware (Ollama) is free unless step 1 says otherwise.
func (mr *ModelRouter) priceOf(provider *models.ModelProvider, model string) Price {
	if provider != nil {
		if p, ok := modelOverride(provider.Config, model); ok {
			return p
		}
		if strings.EqualFold(provider.Kind, "ollama") {
			return Price{Known: true, Source: "local"}
		}
	}
	kind := ""
	if provider != nil {
		kind = provider.Kind
	}
	if mr.catalog != nil {
		if c := mr.catalog.Lookup(kind, model); c != nil && (c.InputCostPer1K > 0 || c.OutputCostPer1K > 0) {
			src := "catalog"
			if c.Source == "builtin" {
				src = "builtin"
			}
			return Price{
				InputPer1K: c.InputCostPer1K, OutputPer1K: c.OutputCostPer1K,
				CacheReadPer1K: c.CacheReadCostPer1K, CacheWritePer1K: c.CacheWriteCostPer1K, ReasoningPer1K: c.ReasoningCostPer1K,
				LongContextOver: c.LongContextOver, LongInputPer1K: c.LongInputCostPer1K, LongOutputPer1K: c.LongOutputCostPer1K,
				Known: true, Source: src,
			}
		}
	}
	for _, name := range []string{model, trimSnapshot(model)} {
		if costs, ok := defaultCosts[name]; ok {
			return Price{InputPer1K: costs["input"], OutputPer1K: costs["output"], Known: true, Source: "builtin"}
		}
	}
	if provider != nil {
		in, inOK := configCost(provider.Config, "cost_per_1k_input")
		out, outOK := configCost(provider.Config, "cost_per_1k_output")
		if inOK && outOK {
			return Price{InputPer1K: in, OutputPer1K: out, Known: true, Source: "provider_default"}
		}
	}
	return Price{InputPer1K: 0.001, OutputPer1K: 0.001, Source: "fallback"}
}

// PriceFor reports the price of model on the named provider in a kitchen, the same figure the cost the
// router records is computed from, and says when it had to guess.
//
// A run budget in dollars needs this: a model with no known price is priced at a placeholder, so a cap
// measured against it would look enforced while being wrong. Callers refuse a dollar cap on an unpriced
// model instead (tokens stay exact).
func (mr *ModelRouter) PriceFor(ctx context.Context, kitchen, providerName, model string) Price {
	provider, err := mr.store.GetProvider(ctx, kitchen, providerName)
	if err != nil || provider == nil {
		return Price{InputPer1K: 0.001, OutputPer1K: 0.001, Source: "fallback"}
	}
	return mr.priceOf(provider, model)
}

// Cost is what a call that used u costs at this price. Prompt tokens are split into fresh, cache-read
// and cache-write ones (u.InputTokens counts all three), each at its own rate; output tokens include
// thinking tokens, which cost the output rate unless the model has a separate reasoning rate.
func (p Price) Cost(u models.TokenUsage) float64 {
	in, out := p.InputPer1K, p.OutputPer1K
	cacheRead, cacheWrite, reasoning := p.CacheReadPer1K, p.CacheWritePer1K, p.ReasoningPer1K
	if p.LongContextOver > 0 && u.InputTokens > p.LongContextOver && p.LongInputPer1K > 0 {
		scale := p.LongInputPer1K / in
		in = p.LongInputPer1K
		if cacheRead > 0 {
			cacheRead *= scale
		}
		if cacheWrite > 0 {
			cacheWrite *= scale
		}
		if p.LongOutputPer1K > 0 {
			out = p.LongOutputPer1K
		}
	}
	if cacheRead == 0 {
		cacheRead = in
	}
	if cacheWrite == 0 {
		cacheWrite = in
	}
	cached, wrote := u.CachedTokens, u.CacheCreation
	fresh := u.InputTokens - cached - wrote
	if fresh < 0 { // a provider that reports cache tokens outside its prompt count
		fresh = 0
	}
	cost := float64(fresh)/1000*in + float64(cached)/1000*cacheRead + float64(wrote)/1000*cacheWrite
	if reasoning > 0 && u.ThinkingTokens > 0 && u.ThinkingTokens <= u.OutputTokens {
		cost += float64(u.OutputTokens-u.ThinkingTokens)/1000*out + float64(u.ThinkingTokens)/1000*reasoning
	} else {
		cost += float64(u.OutputTokens) / 1000 * out
	}
	return cost
}

// Settle prices usage a caller measured itself (a hosted agent process reports its own tokens) at the
// named provider's price for the model, filling in the cost, what caching saved and the price source.
func (mr *ModelRouter) Settle(ctx context.Context, kitchen, providerName, model string, u *models.TokenUsage) {
	mr.apply(mr.PriceFor(ctx, kitchen, providerName, model), u)
}

// settle fills in a usage record's cost, what caching saved, and where the price came from.
func (mr *ModelRouter) settle(provider *models.ModelProvider, model string, u *models.TokenUsage) {
	mr.apply(mr.priceOf(provider, model), u)
}

func (mr *ModelRouter) apply(p Price, u *models.TokenUsage) {
	u.EstimatedCost = p.Cost(*u)
	u.PriceSource = p.Source
	if p.CacheReadPer1K > 0 && p.CacheReadPer1K < p.InputPer1K && u.CachedTokens > 0 {
		u.CacheSavingsUSD = float64(u.CachedTokens) / 1000 * (p.InputPer1K - p.CacheReadPer1K)
	}
}

// modelOverride reads provider.Config["model_costs"][model]: the operator's price for that model,
// {"input": per 1K, "output": per 1K, "cache_read": ..., "cache_write": ...}.
func modelOverride(cfg map[string]interface{}, model string) (Price, bool) {
	all, _ := cfg["model_costs"].(map[string]interface{})
	entry, _ := all[model].(map[string]interface{})
	if entry == nil {
		return Price{}, false
	}
	in, inOK := configCost(entry, "input")
	out, outOK := configCost(entry, "output")
	if !inOK || !outOK {
		return Price{}, false
	}
	p := Price{InputPer1K: in, OutputPer1K: out, Known: true, Source: "model_override"}
	p.CacheReadPer1K, _ = configCost(entry, "cache_read")
	p.CacheWritePer1K, _ = configCost(entry, "cache_write")
	return p, true
}

var snapshotSuffix = regexp.MustCompile(`-(\d{4}-\d{2}-\d{2}|\d{8})$`)

// trimSnapshot removes a trailing snapshot date ("gpt-4o-2024-08-06" -> "gpt-4o").
func trimSnapshot(name string) string { return snapshotSuffix.ReplaceAllString(name, "") }

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
