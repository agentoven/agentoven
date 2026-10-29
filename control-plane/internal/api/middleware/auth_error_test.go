package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/auth"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
)

type refusing struct{}

func (refusing) Name() string  { return "refusing" }
func (refusing) Enabled() bool { return true }
func (refusing) Authenticate(context.Context, *http.Request) (*contracts.Identity, error) {
	return nil, &contracts.AuthError{Status: http.StatusForbidden, Code: "kitchen_access_denied", Message: "not a member of alpha"}
}

func TestATypedAuthErrorKeepsItsStatusAndCode(t *testing.T) {
	chain := auth.NewProviderChain()
	chain.RegisterProvider(refusing{})
	h := NewAuthMiddleware(chain).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the request must not reach the handler")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/agents", nil))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "kitchen_access_denied") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}
