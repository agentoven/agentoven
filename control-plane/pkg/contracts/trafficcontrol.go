package contracts

import (
	"net/http"

	"github.com/agentoven/agentoven/control-plane/internal/router"
)

// ResponseCache answers a repeated model request without calling the provider (Pro ADR-0051). The
// router asks it before it reserves a budget or sends anything, and offers it every answer that came
// back from a provider.
//
//	Lookup(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, bool)
//	Store(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest, resp *models.RouteResponse)
//
// Lookup returns a copy the caller may change. The router marks it Cached and zeroes its usage, so a
// hit costs nothing and counts against no budget or rate limit. Both calls receive the request as it
// goes to the driver (the model resolved, the provider chosen) and run on the call path, so an
// implementation must be quick and must never fail the call: a miss is always an acceptable answer.
// The cache decides for itself what may be stored; the router only streams past it (a streamed call
// is neither looked up nor stored).
//
// The community edition installs none.
type ResponseCache = router.ResponseCache

// TrafficLimiter admits or refuses a request that starts agent work before the work starts: an
// invocation, a streamed invocation, a session message, a task. It is how a rate limit on
// invocations is enforced at the door, ahead of the per-model-call limits the SpendGate sees.
//
//	Admit(r *http.Request, kitchen, agent string) error
//
// Admit is called after authentication, so the request carries the caller's identity. agent is the
// agent the request names, empty when the route does not name one (starting a task). nil lets the
// request through; a *models.RateLimitedError answers 429 with Retry-After; any other error answers
// 503, because a limit that cannot be checked is not assumed to have passed.
//
// The community edition installs none.
type TrafficLimiter interface {
	Admit(r *http.Request, kitchen, agent string) error
}
