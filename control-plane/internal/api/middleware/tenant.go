package middleware

import (
	"context"
	"net/http"
	"strings"

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
		kitchen := ""

		// Priority 1: X-Kitchen header
		if h := r.Header.Get("X-Kitchen"); h != "" {
			kitchen = strings.TrimSpace(h)
		}

		// Priority 2: kitchen query parameter
		if kitchen == "" {
			if q := r.URL.Query().Get("kitchen"); q != "" {
				kitchen = strings.TrimSpace(q)
			}
		}

		// Priority 3: Extract tenant from Authorization header (Bearer token)
		// Phase 1: Read the "sub" or "tenant" claim from a JWT if present.
		// Full JWT validation (signature, expiry) deferred to Phase 2 auth middleware.
		if kitchen == "" {
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				// Future: decode JWT claims and extract tenant/kitchen
				_ = strings.TrimPrefix(auth, "Bearer ")
			}
		}

		// Default kitchen
		if kitchen == "" {
			kitchen = "default"
		}

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
