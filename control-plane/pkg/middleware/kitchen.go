// Package middleware provides shared middleware helpers for the AgentOven control plane.
//
// This package lives in pkg/ (not internal/) so that the enterprise repo
// can use GetKitchen() and SetKitchen() in its own middleware.
package middleware

import "context"

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
