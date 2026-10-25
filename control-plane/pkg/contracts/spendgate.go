package contracts

import "github.com/agentoven/agentoven/control-plane/internal/router"

// SpendGate is consulted before every model call and told about every one that completes. It is
// how spend that outlives a single run is limited: a kitchen's monthly budget, a provider's
// quota, a team's allowance. (A single run's own cap is the executor's RunBudget; a gate is about
// the money many runs share.)
//
//	Allow(ctx context.Context, q models.SpendQuery) error
//	Record(ctx context.Context, r models.SpendRecord)
//
// Allow is called before a call is sent: nil lets it through, an error stops it. An error that
// is (or wraps) *models.SpendRefusedError is a budget decision, and a run pauses at it instead of
// failing. Any other error is a failure of the gate itself and also stops the call, because a
// limit that cannot be checked is not a limit. Record gets the usage of a call that completed.
//
// The community edition installs none. An edition that keeps a ledger installs one with
// ModelRouter.SetSpendGate, and it then sees every call the router makes for any caller: agent
// loops, summaries, the OpenAI-compatible endpoint, anything.
type SpendGate = router.SpendGate
