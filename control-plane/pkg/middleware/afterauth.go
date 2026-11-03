package middleware

import (
	"context"
	"errors"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
)

// AfterAuthFunc runs once per request, after authentication and tenant resolution and before the route
// is served. An edition that needs to act on the request with the verified identity in hand does it
// here: Pro resolves the kitchen a request names (a name or an id) to its one canonical name, and
// writes the "intent" record of a decision in a GxP kitchen. Neither can be done earlier, because
// before this point nothing about the request is believed.
//
// id is nil when the request is anonymous (authentication is not required on this installation). The
// function may return a changed request, whose context then flows on, or an error that refuses the
// request: an *contracts.AuthError names its own status, any other error is a 503.
type AfterAuthFunc func(r *http.Request, id *contracts.Identity) (*http.Request, error)

const afterAuthKey contextKey = "after_auth"

// WithAfterAuth adds a function to run after authentication. Functions run in the order they were
// added, so the outermost middleware's runs first.
func WithAfterAuth(ctx context.Context, fn AfterAuthFunc) context.Context {
	prev, _ := ctx.Value(afterAuthKey).([]AfterAuthFunc)
	next := append(append([]AfterAuthFunc(nil), prev...), fn)
	return context.WithValue(ctx, afterAuthKey, next)
}

// RunAfterAuth runs the functions WithAfterAuth added to the request's context. It returns the request
// to serve, or the first error, which the caller answers.
func RunAfterAuth(r *http.Request, id *contracts.Identity) (*http.Request, error) {
	fns, _ := r.Context().Value(afterAuthKey).([]AfterAuthFunc)
	for _, fn := range fns {
		next, err := fn(r, id)
		if err != nil {
			return nil, err
		}
		if next != nil {
			r = next
		}
	}
	return r, nil
}

// AfterAuthStatus is the HTTP status, code and message an error from an AfterAuthFunc is answered with.
func AfterAuthStatus(err error) (status int, code, message string) {
	var ae *contracts.AuthError
	if errors.As(err, &ae) {
		return ae.Status, ae.Code, ae.Message
	}
	return http.StatusServiceUnavailable, "request_refused", "the request could not be accepted, try again"
}
