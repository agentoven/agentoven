package middleware

import (
	"net/http/httptest"
	"testing"
)

// The sign-in page asks which sign-in options exist before it has a session. Exactly /auth/methods is open
// for that; the identity routes next to it, the info route (licence and limits) and look-alike paths are not.
func TestOnlyTheSignInMethodsRouteIsOpenForTheSignInPage(t *testing.T) {
	for path, want := range map[string]bool{
		"/auth/methods":        true,
		"/auth/methods/":       false,
		"/auth/methods/x":      false,
		"/auth/methodsx":       false,
		"/auth/me":             false,
		"/api/v1/info":         false,
		"/api/v1/auth/methods": false,
	} {
		if got := isAuthPublic(httptest.NewRequest("GET", path, nil)); got != want {
			t.Errorf("%s: public=%v, want %v", path, got, want)
		}
	}
}
