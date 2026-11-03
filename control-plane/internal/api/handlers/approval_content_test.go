package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A reader without the right to read content still sees that an approval exists, which step it is and
// who decided it, but not the step's inputs or the approver's comment (owner decision A3, 2026-11-03).
func TestAReaderWithoutContentAccessSeesNoApprovalInputs(t *testing.T) {
	h, s := newTestHandlers(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.CreateApproval(ctx, &models.ApprovalRecord{
		ID: "ap1", GateKey: "run1:pay", RunID: "run1", StepName: "pay", Kitchen: "default", Status: "approved",
		ApproverEmail: "boss@example.com", Comments: "iban DE89 is right",
		Metadata:    map[string]interface{}{"inputs": "pay 4111 to DE89"},
		RequestedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	allowed := true
	h.SetTraceContentGuard(func(*http.Request) bool { return allowed })

	get := func(handler http.HandlerFunc, url string, param ...string) string {
		req := withKitchen(httptest.NewRequest(http.MethodGet, url, nil), "default")
		for i := 0; i+1 < len(param); i += 2 {
			req = withChiParam(req, param[i], param[i+1])
		}
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", url, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	calls := map[string]func() string{
		"list": func() string { return get(h.ListApprovals, "/approvals") },
		"get":  func() string { return get(h.GetApproval, "/approvals/run1:pay", "gateKey", "run1:pay") },
	}

	for name, call := range calls {
		if body := call(); !strings.Contains(body, "4111") || !strings.Contains(body, "DE89 is right") {
			t.Errorf("%s: a reader with content access must see the inputs and the comment: %s", name, body)
		}
	}
	allowed = false
	for name, call := range calls {
		body := call()
		for _, leak := range []string{"4111", "DE89"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s leaks %q to a reader without content access: %s", name, leak, body)
			}
		}
		for _, keep := range []string{`"gate_key":"run1:pay"`, `"step_name":"pay"`, `"status":"approved"`, "boss@example.com", `"content_withheld":true`} {
			if !strings.Contains(body, keep) {
				t.Errorf("%s must still say %s: %s", name, keep, body)
			}
		}
	}
}

// An environment promotion waiting for approval keeps its labels for a reader without content access: what
// is being promoted where is not content.
func TestAPromotionKeepsItsLabelsForAReaderWithoutContentAccess(t *testing.T) {
	h, s := newTestHandlers(t)
	if err := s.CreateApproval(context.Background(), &models.ApprovalRecord{
		ID: "ap2", GateKey: "promo:1", Kitchen: "default", Status: "waiting", RequestedAt: time.Now().UTC(),
		Metadata: map[string]interface{}{"agent_name": "billing", "from_env": "qa", "to_env": "prod", "version": "v7", "comment": "customer 4111 asked"},
	}); err != nil {
		t.Fatal(err)
	}
	h.SetTraceContentGuard(func(*http.Request) bool { return false })
	rec := httptest.NewRecorder()
	h.ListApprovals(rec, withKitchen(httptest.NewRequest(http.MethodGet, "/approvals", nil), "default"))
	body := rec.Body.String()
	for _, keep := range []string{`"agent_name":"billing"`, `"from_env":"qa"`, `"to_env":"prod"`} {
		if !strings.Contains(body, keep) {
			t.Errorf("a promotion must keep %s: %s", keep, body)
		}
	}
	if strings.Contains(body, "4111") {
		t.Errorf("a free-text comment must not reach a reader without content access: %s", body)
	}
}
