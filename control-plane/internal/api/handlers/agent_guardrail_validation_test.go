package handlers_test

import (
	"net/http"
	"strings"
	"testing"
)

// Review 5, findings 6 and 14: an agent's own guardrail rules were never validated on save, so a typo
// in a kind, a regex that does not compile or a list over the limit was a rule that did nothing, and
// a llamaguard endpoint was whatever its baker wrote.
func TestAgentGuardrailRulesAreValidatedWhenTheAgentIsSaved(t *testing.T) {
	h, _ := seedBakedAgent(t)
	long := `"` + strings.Repeat(`w","`, 1001) + `w"`
	for name, rule := range map[string]string{
		"typo in the kind":     `{"id":"g","kind":"llama_guard","enabled":true,"config":{"endpoint":"https://guard.example.com"}}`,
		"regex that fails":     `{"id":"g","kind":"regex_filter","enabled":true,"config":{"pattern":"(unclosed"}}`,
		"too many words":       `{"id":"g","kind":"content_filter","enabled":true,"config":{"blocked_words":[` + long + `]}}`,
		"internal endpoint":    `{"id":"g","kind":"llamaguard","enabled":true,"config":{"endpoint":"http://169.254.169.254"}}`,
		"private endpoint":     `{"id":"g","kind":"llamaguard","enabled":true,"config":{"endpoint":"http://10.1.2.3:18493"}}`,
		"endpoint credentials": `{"id":"g","kind":"llamaguard","enabled":true,"config":{"endpoint":"http://u:p@guard.example.com"}}`,
		"unknown pii pattern":  `{"id":"g","kind":"pii_detection","enabled":true,"config":{"patterns":["emial"]}}`,
	} {
		put := callAgentAPI(h.UpdateAgent, http.MethodPut, "/api/v1/agents/keeper", map[string]string{"agentName": "keeper"}, `{"guardrails":[`+rule+`]}`)
		if put.Code != http.StatusBadRequest {
			t.Errorf("PUT, %s: want 400, got %d %s", name, put.Code, put.Body.String())
		}
		post := callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, `{"name":"fresh","guardrails":[`+rule+`]}`)
		if post.Code != http.StatusBadRequest {
			t.Errorf("POST, %s: want 400, got %d %s", name, post.Code, post.Body.String())
		}
	}
	// A disabled rule is not evaluated, so it is not judged; a good rule is accepted.
	ok := `{"id":"g","kind":"content_filter","enabled":true,"config":{"blocked_words":["x"]}}`
	if w := callAgentAPI(h.UpdateAgent, http.MethodPut, "/api/v1/agents/keeper", map[string]string{"agentName": "keeper"}, `{"guardrails":[`+ok+`]}`); w.Code != http.StatusOK {
		t.Fatalf("a valid rule must be accepted: %d %s", w.Code, w.Body.String())
	}
	off := `{"id":"g","kind":"llama_guard","enabled":false}`
	if w := callAgentAPI(h.UpdateAgent, http.MethodPut, "/api/v1/agents/keeper", map[string]string{"agentName": "keeper"}, `{"guardrails":[`+off+`]}`); w.Code != http.StatusOK {
		t.Fatalf("a disabled rule is never evaluated: %d %s", w.Code, w.Body.String())
	}
}

func TestGuardrailKindsListsLlamaguardAndDescribesCustomTruthfully(t *testing.T) {
	h, _ := seedBakedAgent(t)
	body := callAgentAPI(h.ListGuardrailKinds, http.MethodGet, "/api/v1/guardrails/kinds", nil, "").Body.String()
	if !strings.Contains(body, `"kind":"llamaguard"`) {
		t.Fatalf("llamaguard is a kind a rule can have: %s", body)
	}
	if strings.Contains(body, "webhook_url") || strings.Contains(body, "no-op pass-through") {
		t.Fatalf("the custom rule is not described as it is: %s", body)
	}
}
