package contracts

import "github.com/agentoven/agentoven/control-plane/internal/router"

// SpendReserver is an optional extension of SpendGate for a gate that must not let parallel callers
// oversubscribe a budget. A gate that implements it is asked to Reserve the worst case of each call
// before it is made, and the router settles or cancels exactly that reservation afterwards, so the
// budget is held for the calls in flight. Without it, a gate sees only Allow and Record and can only
// check what has already been spent.
//
//	Reserve(ctx context.Context, q models.SpendQuery) (SpendReservation, error)
//
// An error from Reserve means what an error from Allow means (see SpendGate).
type SpendReserver = router.SpendReserver

// SpendReservation is the part of a budget held for one call in flight: Settle it with what the call
// used, or Cancel it when the call failed.
type SpendReservation = router.SpendReservation
