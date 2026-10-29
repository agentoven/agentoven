// Package middleware provides shared middleware helpers for the AgentOven control plane.
//
// This package lives in pkg/ (not internal/) so that the enterprise repo
// can use GetKitchen() and SetKitchen() in its own middleware.
package middleware

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const kitchenKey contextKey = "kitchen"

// GetKitchen extracts the kitchen name from the context.
// Returns "default" if no kitchen is set.
func GetKitchen(ctx context.Context) string {
	if v, ok := ctx.Value(kitchenKey).(string); ok && v != "" {
		return v
	}
	return "default"
}

// SetKitchen stores the kitchen name in the context.
func SetKitchen(ctx context.Context, kitchen string) context.Context {
	return context.WithValue(ctx, kitchenKey, kitchen)
}

// KitchenFromRequest is the kitchen a request names: the X-Kitchen header, else the kitchen query
// parameter, else "default". The tenant middleware and any identity enricher must agree on it.
func KitchenFromRequest(r *http.Request) string {
	if h := strings.TrimSpace(r.Header.Get("X-Kitchen")); h != "" {
		return h
	}
	if q := strings.TrimSpace(r.URL.Query().Get("kitchen")); q != "" {
		return q
	}
	return "default"
}

// EnvironmentHeader names the environment a call is made for. Recipe runs forward it to the agents
// they call, and a caller may set it on any invocation.
const EnvironmentHeader = "X-AO-Environment"

// EnvironmentFromRequest is the environment slug a request names, or "" when it names none or the
// value is not a slug (lower-case letters, digits and hyphens, at most 63). The value is the caller's
// word: it labels a trace, and a deployment that keeps environments checks it against them.
func EnvironmentFromRequest(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get(EnvironmentHeader))
	if v == "" || len(v) > 63 {
		return ""
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return ""
		}
	}
	return v
}
