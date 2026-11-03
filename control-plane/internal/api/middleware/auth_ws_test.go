package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A browser cannot send headers on a WebSocket, so a listener on a notification channel may offer a ticket as
// a subprotocol and skip the general authentication; the endpoint then checks the ticket. Nothing else gets
// that allowance.
func TestOnlyAChannelListenWithATicketSkipsAuthentication(t *testing.T) {
	req := func(method, path, protocols string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		if protocols != "" {
			r.Header.Set("Sec-WebSocket-Protocol", protocols)
		}
		return r
	}
	ticket := "agentoven.v1, agentoven-token.abc.def"
	for name, c := range map[string]struct {
		r    *http.Request
		want bool
	}{
		"listen with a ticket":            {req("GET", "/api/v1/channels/ops/ws", ticket), true},
		"listen without a ticket":         {req("GET", "/api/v1/channels/ops/ws", "agentoven.v1"), false},
		"listen with no subprotocol":      {req("GET", "/api/v1/channels/ops/ws", ""), false},
		"a ticket on another route":       {req("GET", "/api/v1/channels/ops", ticket), false},
		"a ticket to create a channel":    {req("POST", "/api/v1/channels", ticket), false},
		"a ticket on the ticket route":    {req("POST", "/api/v1/channels/ops/ws-ticket", ticket), false},
		"a ticket on agents":              {req("GET", "/api/v1/agents/ws", ticket), false},
		"a ticket to post to the ws path": {req("POST", "/api/v1/channels/ops/ws", ticket), false},
	} {
		if got := isAuthPublic(c.r); got != c.want {
			t.Errorf("%s: public=%v, want %v", name, got, c.want)
		}
	}
}

// Chat platforms call back with their own signature; only paths under /hooks/chat/ skip the general
// authentication, and a look-alike prefix or an API path does not.
func TestOnlyChatCallbacksSkipAuthenticationBySignature(t *testing.T) {
	for path, want := range map[string]bool{
		"/hooks/chat/approvals/slack/default/ops": true,
		"/hooks/chat/":                            true,
		"/hooks/chatter/x":                        false,
		"/hooks/other/x":                          false,
		"/api/v1/hooks/chat/x":                    false,
		"/api/v1/approval-requests":               false,
	} {
		if got := isAuthPublic(httptest.NewRequest("POST", path, nil)); got != want {
			t.Errorf("%s: public=%v, want %v", path, got, want)
		}
	}
}
