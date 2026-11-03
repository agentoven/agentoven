package router

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// ResponseCache is the router's view of a response cache; see contracts.ResponseCache for what it means.
type ResponseCache interface {
	Lookup(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, bool)
	Store(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest, resp *models.RouteResponse)
}

type cacheBox struct{ c ResponseCache }

// SetResponseCache installs the cache consulted before a model call is sent and told about every
// answer that came back (see contracts.ResponseCache). Pass nil to remove it. It is safe to call
// while the router is in use.
func (mr *ModelRouter) SetResponseCache(c ResponseCache) {
	if c == nil {
		mr.responseCache.Store(nil)
		return
	}
	mr.responseCache.Store(&cacheBox{c: c})
}

// cacheLookup returns a stored answer for the request, marked as served from the cache and priced at
// zero: nothing was sent to the provider, so nothing is charged to a budget, a rate limit or the cost
// summary, and a trace can tell the call apart from a real one.
func (mr *ModelRouter) cacheLookup(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, bool) {
	box := mr.responseCache.Load()
	if box == nil {
		return nil, false
	}
	resp, ok := box.c.Lookup(ctx, provider, req)
	if !ok || resp == nil {
		return nil, false
	}
	resp.Cached = true
	resp.Usage = models.TokenUsage{}
	if resp.Strategy == "" {
		resp.Strategy = req.Strategy
	}
	return resp, true
}

// cacheStore offers a fresh answer to the cache.
func (mr *ModelRouter) cacheStore(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest, resp *models.RouteResponse) {
	if box := mr.responseCache.Load(); box != nil && resp != nil && !resp.Cached {
		box.c.Store(ctx, provider, req, resp)
	}
}
