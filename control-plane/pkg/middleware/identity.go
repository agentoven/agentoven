package middleware

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
)

const identityKey contextKey = "identity"

// SetIdentity stores the authenticated Identity in the context.
// Called by the auth middleware after successful authentication.
func SetIdentity(ctx context.Context, identity *contracts.Identity) context.Context {
	if identity == nil {
		return ctx
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
