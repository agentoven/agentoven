package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The route a hosted process asks for tool calls on is open to the auth layer because its only credential is a run token the
// handler checks (ADR-0044). Exactly that path is open: nothing under it, nothing like it, and no other route a token could be
// presented to.
func TestOnlyTheToolCallRouteIsOpenForRunTokens(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/v1/internal/tool-calls":   true,
		"/api/v1/internal/tool-calls/":  false,
		"/api/v1/internal/tool-calls/x": false,
		"/api/v1/internal/tool-callsx":  false,
		"/api/v1/internal":              false,
		"/api/v1/internal/other":        false,
		"/internal/tool-calls":          false,
		"/api/v1/agents/a/invoke":       false,
		"/api/v1/tools":                 false,
	} {
		if got := isAuthPublic(httptest.NewRequest(http.MethodPost, path, nil)); got != want {
			t.Errorf("%s: public = %v, want %v", path, got, want)
		}
	}
}
