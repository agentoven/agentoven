package api_test

import (
	"net/http"
	"regexp"
	"sort"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/go-chi/chi/v5"
)

// A decision route that is not registered is a decision nobody signs. This walks the real route table
// and fails for a route that looks like a decision and is not in handlers.DecisionRoutes, and for a
// listed route that no longer exists.
func TestEveryDecisionRouteIsRegisteredForSigning(t *testing.T) {
	mux := newTestRouter(t, "false").(*chi.Mux)
	looksLikeADecision := regexp.MustCompile(`(?i)(approv|reject|resume|release|decid|/gates)`)

	routed := map[string]bool{}
	var looks []string
	chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			return nil
		}
		routed[method+" "+route] = true
		if looksLikeADecision.MatchString(route) {
			looks = append(looks, method+" "+route)
		}
		return nil
	})

	listed := map[string]bool{}
	for _, d := range handlers.DecisionRoutes {
		key := d.Method + " " + d.Pattern
		listed[key] = true
		if !routed[key] {
			t.Errorf("DecisionRoutes lists %s, which is not routed", key)
		}
		if d.Gate == "" {
			t.Errorf("%s names no gate", key)
		}
	}
	sort.Strings(looks)
	for _, key := range looks {
		if !listed[key] {
			t.Errorf("%s looks like a decision and is not in handlers.DecisionRoutes: register it, call signDecision, give it a describer", key)
		}
	}
}
