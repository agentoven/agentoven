package middleware

import (
	"context"
	"net/http"

	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
)

type contextKey string

const (
	// TenantIDKey is the context key for the tenant (kitchen) ID.
	TenantIDKey contextKey = "tenant_id"
)

// TenantExtractor extracts tenant information from the request.
// It checks the X-Kitchen header, then the kitchen query parameter,
// and falls back to "default".
func TenantExtractor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// X-Kitchen header, else the kitchen query parameter, else "default".
		kitchen := pkgmw.KitchenFromRequest(r)

		// Use pkg/middleware for the kitchen context key (shared with enterprise repo)
		ctx := pkgmw.SetKitchen(r.Context(), kitchen)
		ctx = context.WithValue(ctx, TenantIDKey, kitchen)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetKitchen retrieves the kitchen name from the request context.
// Delegates to pkg/middleware.GetKitchen for cross-module compatibility.
func GetKitchen(ctx context.Context) string {
	return pkgmw.GetKitchen(ctx)
}

// GetTenantID retrieves the tenant ID from the request context.
func GetTenantID(ctx context.Context) string {
	if v, ok := ctx.Value(TenantIDKey).(string); ok {
		return v
	}
	return "default"
}
