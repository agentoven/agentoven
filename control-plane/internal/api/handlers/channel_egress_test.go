package handlers_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/netguard"
)

// A channel's URL is checked when it is saved, with the same rule as a tool endpoint, so a mistake is reported at
// once instead of as a delivery that never arrives. (Delivery checks again at dial time; see internal/notify.)
func TestAChannelCannotBeSavedAtAPrivateAddress(t *testing.T) {
	t.Setenv(netguard.EnvAllow, "") // the package's TestMain allows loopback for the other tests
	t.Setenv(netguard.EnvChannelAllow, "")
	h, s := newTestHandlers(t)
	create := func(body string) (int, string) {
		w := callAgentAPI(h.CreateChannel, http.MethodPost, "/api/v1/channels", nil, body)
		return w.Code, w.Body.String()
	}
	for name, url := range map[string]string{
		"loopback":        "http://127.0.0.1:9/hook",
		"a private range": "http://10.1.2.3/hook",
		"the metadata ip": "http://169.254.169.254/latest/meta-data/",
		"a number":        "http://2130706433/hook",
		"a user in a url": "https://user:pw@hooks.example.test/x",
		"another scheme":  "file:///etc/passwd",
	} {
		if code, body := create(`{"name":"bad","kind":"webhook","url":"` + url + `"}`); code != http.StatusBadRequest || !strings.Contains(body, "url") {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if ch, _ := s.GetChannel(context.Background(), "default", "bad"); ch != nil {
		t.Fatal("a refused channel was kept")
	}

	// The kinds that carry a URL are all covered; a kind without one is not.
	for _, kind := range []string{"slack", "teams", "discord", "zapier"} {
		if code, _ := create(`{"name":"` + kind + `","kind":"` + kind + `","url":"http://127.0.0.1:9/x"}`); code != http.StatusBadRequest {
			t.Errorf("%s at loopback: %d", kind, code)
		}
	}
	if code, body := create(`{"name":"mail","kind":"email","url":"mail","config":{"smtp_host":"smtp.example.test"}}`); code != http.StatusCreated {
		t.Errorf("an e-mail channel has no URL to check: %d %s", code, body)
	}
	if code, body := create(`{"name":"live","kind":"websocket"}`); code != http.StatusCreated {
		t.Errorf("a websocket channel has no URL: %d %s", code, body)
	}
	if code, body := create(`{"name":"public","kind":"webhook","url":"https://hooks.example.test/x"}`); code != http.StatusCreated {
		t.Errorf("a name that does not resolve is not refused at save time (the dial still checks it): %d %s", code, body)
	}

	// An allowlisted host may be private, on the channels' list; the tools' list does not count (owner decision S10).
	t.Setenv(netguard.EnvAllow, "127.0.0.1")
	if code, _ := create(`{"name":"local","kind":"webhook","url":"http://127.0.0.1:9/hook"}`); code != http.StatusBadRequest {
		t.Errorf("the tools' allowlist must not admit a channel: %d", code)
	}
	t.Setenv(netguard.EnvChannelAllow, "127.0.0.1")
	if code, body := create(`{"name":"local","kind":"webhook","url":"http://127.0.0.1:9/hook"}`); code != http.StatusCreated {
		t.Errorf("allowlisted: %d %s", code, body)
	}
}

func TestAChannelCannotBeEditedToAPrivateAddress(t *testing.T) {
	t.Setenv(netguard.EnvAllow, "")
	t.Setenv(netguard.EnvChannelAllow, "")
	h, s := newTestHandlers(t)
	if w := callAgentAPI(h.CreateChannel, http.MethodPost, "/api/v1/channels", nil, `{"name":"ops","kind":"webhook","url":"https://hooks.example.test/x"}`); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w := callAgentAPI(h.UpdateChannel, http.MethodPut, "/api/v1/channels/ops", map[string]string{"channelName": "ops"},
		`{"kind":"webhook","url":"http://169.254.169.254/latest/","events":["*"],"active":true}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if ch, _ := s.GetChannel(context.Background(), "default", "ops"); ch == nil || ch.URL != "https://hooks.example.test/x" {
		t.Errorf("the refused edit changed the channel: %+v", ch)
	}
}
