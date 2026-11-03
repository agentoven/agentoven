package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// LimitTraffic is middleware for the routes that start agent work: it asks the edition's
// TrafficLimiter whether the request may start, before anything is read or run. With none installed
// (the community edition) it lets everything through.
func (h *Handlers) LimitTraffic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.Traffic == nil {
			next.ServeHTTP(w, r)
			return
		}
		if err := h.Traffic.Admit(r, pkgmw.GetKitchen(r.Context()), chi.URLParam(r, "agentName")); err != nil {
			if !respondRateLimited(w, err) {
				// A limiter that cannot answer is not taken to have said yes.
				respondJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
					"error": "traffic_control_unavailable", "message": "the request limit could not be checked: " + err.Error()})
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

// respondRateLimited answers 429 with Retry-After when err is a rate limit, and reports whether it did.
func respondRateLimited(w http.ResponseWriter, err error) bool {
	var rl *models.RateLimitedError
	if !errors.As(err, &rl) {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(rl.RetryAfterSeconds()))
	respondJSON(w, http.StatusTooManyRequests, map[string]interface{}{
		"error":       "rate_limited",
		"message":     rl.Reason,
		"scope":       rl.Scope,
		"limit":       rl.Limit,
		"retry_after": rl.RetryAfterSeconds(),
	})
	return true
}
