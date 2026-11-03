package handlers

import (
	"errors"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// respondRefusal answers the refusals a model call can end in with the status a client can act on, and
// reports whether err was one. A budget that is used up is 402, a rate limit 429 with Retry-After, a
// guardrail 403 and rules that could not be read 503: none of them is the provider's fault, so none is a 502
// or a 500 an SDK would retry. Every route that calls a model answers through it.
func respondRefusal(w http.ResponseWriter, err error) bool {
	var spend *models.SpendRefusedError
	var blocked *models.GuardrailBlockedError
	switch {
	case respondRateLimited(w, err):
		return true
	case errors.As(err, &spend):
		body := map[string]interface{}{
			"error": "budget_exceeded", "message": spend.Reason,
			"scope": spend.Scope, "spent": spend.Spent, "cap": spend.Cap, "unit": spend.Unit, "period": spend.Period,
		}
		if spend.ResetsAt != nil {
			body["resets_at"] = spend.ResetsAt
		}
		respondJSON(w, http.StatusPaymentRequired, body)
		return true
	case errors.As(err, &blocked) && blocked.Stage == "policy":
		respondError(w, http.StatusServiceUnavailable, "the guardrail policy could not be applied, so the request was not made")
		return true
	case errors.As(err, &blocked):
		msg := "Input blocked by guardrails"
		if blocked.Stage == "output" {
			msg = "Output blocked by guardrails"
		}
		respondJSON(w, http.StatusForbidden, map[string]interface{}{"error": msg, "guardrails": blocked.Results})
		return true
	}
	return false
}
