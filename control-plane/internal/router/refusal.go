package router

import (
	"errors"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A gate's refusal (a budget that is used up, a rate limit) is a decision about the caller, not a
// fault of the provider. Two things follow, and both used to be wrong for a budget refusal:
//
//   - it must not count toward opening the provider's circuit: five refused calls in a row would
//     otherwise take a healthy provider out of rotation for thirty seconds, and a budget that was
//     raised a moment later would still be met with "circuit open";
//   - it stops the fallback to the next provider when it is about more than this provider (the
//     kitchen's budget, an agent's or an API key's rate limit): every other provider would be
//     refused for the same reason. A limit on one provider only is exactly the case fallback is for.

// isGateRefusal reports whether err is a spend or rate-limit refusal.
func isGateRefusal(err error) bool {
	return errors.Is(err, models.ErrSpendRefused) || errors.Is(err, models.ErrRateLimited)
}

// refusalCoversAllProviders reports whether err is a refusal that the next provider would repeat.
func refusalCoversAllProviders(err error) bool {
	var sp *models.SpendRefusedError
	if errors.As(err, &sp) {
		return sp.Scope != "provider"
	}
	var rl *models.RateLimitedError
	if errors.As(err, &rl) {
		return rl.Scope != "provider"
	}
	return false
}
