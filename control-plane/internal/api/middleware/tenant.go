package middleware

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const (
	// TenantIDKey is the context key for the tenant (kitchen) ID.
	TenantIDKey contextKey = "tenant_id"
	// KitchenKey is the context key for the kitchen name.
	KitchenKey contextKey = "kitchen"
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

		// Priority 3: Extract from API key / JWT claims (future)
		// TODO: Extract tenant from auth token

		// Default kitchen
		if kitchen == "" {
			kitchen = "default"
		}

		ctx := context.WithValue(r.Context(), KitchenKey, kitchen)
		ctx = context.WithValue(ctx, TenantIDKey, kitchen)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetKitchen retrieves the kitchen name from the request context.
func GetKitchen(ctx context.Context) string {
	if v, ok := ctx.Value(KitchenKey).(string); ok {
		return v
	}
	return "default"
}

// GetTenantID retrieves the tenant ID from the request context.
func GetTenantID(ctx context.Context) string {
	if v, ok := ctx.Value(TenantIDKey).(string); ok {
		return v
	}
	return "default"
}
