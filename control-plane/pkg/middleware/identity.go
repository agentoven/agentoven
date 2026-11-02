package middleware

import (
	"context"
	"sync"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
)

const identityKey contextKey = "identity"

const identityCaptureKey contextKey = "identity_capture"

// IdentityCapture lets a middleware that wraps the authentication middleware learn who was
// authenticated. Context values only flow inward, so an audit middleware outside the router never
// sees the identity the router's auth middleware stores; SetIdentity also records it here when a
// capture is present. It is empty for an anonymous request.
type IdentityCapture struct {
	mu sync.Mutex
	id *contracts.Identity
}

// Identity returns the identity captured so far, or nil.
func (c *IdentityCapture) Identity() *contracts.Identity {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.id
}

// WithIdentityCapture returns a context that records the identity set below it.
func WithIdentityCapture(ctx context.Context) (context.Context, *IdentityCapture) {
	c := &IdentityCapture{}
	return context.WithValue(ctx, identityCaptureKey, c), c
}

// SetIdentity stores the authenticated Identity in the context.
// Called by the auth middleware after successful authentication.
func SetIdentity(ctx context.Context, identity *contracts.Identity) context.Context {
	if identity == nil {
		return ctx
	}
	if c, ok := ctx.Value(identityCaptureKey).(*IdentityCapture); ok {
		c.mu.Lock()
		c.id = identity
		c.mu.Unlock()
	}
	return context.WithValue(ctx, identityKey, identity)
}

// GetIdentity retrieves the authenticated Identity from the context.
// Returns nil if no identity is set (anonymous/unauthenticated request).
//
// This function is shared between OSS and Pro (lives in pkg/).
// Pro's RBAC middleware uses it to check permissions.
func GetIdentity(ctx context.Context) *contracts.Identity {
	if v, ok := ctx.Value(identityKey).(*contracts.Identity); ok {
		return v
	}
	return nil
}
