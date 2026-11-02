package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
)

// maxGuardrailTestBody bounds the body of the test route: it takes rules and text and runs a
// pattern engine on them, so it is limited like any route that does.
const maxGuardrailTestBody = 512 << 10

// TestGuardrails evaluates the rules in the request against the text and reports what would be
// redacted, blocked or warned about. Nothing is stored and no agent runs. A rule that fails
// validation is refused with the reason, the same check a saved rule gets. Custom rules are
// judged by whatever evaluator the edition has (none in the community edition: they pass).
// POST /api/v1/guardrails/test
func (h *Handlers) TestGuardrails(w http.ResponseWriter, r *http.Request) {
	var req contracts.GuardrailTryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGuardrailTestBody)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	svc, ok := h.Guardrails.(contracts.StageGuardrailService)
	if !ok {
		respondError(w, http.StatusNotImplemented, "this server's guardrail service cannot test text")
		return
	}
	resp, err := contracts.TryGuardrails(r.Context(), svc, req)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, resp)
}
