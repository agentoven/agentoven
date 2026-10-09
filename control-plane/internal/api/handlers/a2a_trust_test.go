package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/a2aauth"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
)

const tlsBody = `{"jsonrpc":"2.0","id":7,"method":"tasks/send","params":{"id":"t1","message":{"role":"user","parts":[{"type":"text","text":"hi"}]},"provider_config":{"name":"p","ca_bundle":"PEM","tls_skip_verify":true}}}`

func TestStripProviderOverrideRemovesOnlyTheOverride(t *testing.T) {
	out, stripped := stripProviderOverride([]byte(tlsBody))
	if !stripped {
		t.Fatal("expected the override to be stripped")
	}
	var env struct {
		ID     int                        `json:"id"`
		Method string                     `json:"method"`
		Params map[string]json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatal(err)
	}
	if _, has := env.Params["provider_config"]; has || env.ID != 7 || env.Method != "tasks/send" || len(env.Params["message"]) == 0 || string(env.Params["id"]) != `"t1"` {
		t.Errorf("the rest of the request must be untouched: %s", out)
	}

	for _, body := range []string{`not json`, `{"method":"x"}`, `{"params":{"id":"t"}}`, `{"params":"str"}`, ``} {
		if got, stripped := stripProviderOverride([]byte(body)); stripped || string(got) != body {
			t.Errorf("%q should pass through unchanged, got %q (stripped=%v)", body, got, stripped)
		}
	}
}

func TestProviderOverrideIsAllowedOnlyForThePlatformItself(t *testing.T) {
	req := func(id *contracts.Identity) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/a2a", nil)
		if id != nil {
			r = r.WithContext(pkgmw.SetIdentity(r.Context(), id))
		}
		return r
	}
	if providerOverrideAllowed(req(nil)) {
		t.Error("anonymous")
	}
	for _, role := range []string{"viewer", "baker", "chef", "admin"} {
		if providerOverrideAllowed(req(&contracts.Identity{Subject: "u", Provider: "apikey", Role: role})) {
			t.Errorf("a user with role %s must not choose how the provider is connected to", role)
		}
	}
	if !providerOverrideAllowed(req(&contracts.Identity{Subject: "internal:workflow", Provider: "internal"})) {
		t.Error("the workflow engine passes the provider's own TLS settings and must be believed")
	}
}

// relay runs one call through proxyA2ARequest to a fake pod and returns what the pod received.
func relay(t *testing.T, h *Handlers, id *contracts.Identity, kitchen, agent string) (headers http.Header, body string) {
	return relayTo(t, h, id, kitchen, agent, true)
}

func relayTo(t *testing.T, h *Handlers, id *contracts.Identity, kitchen, agent string, managed bool) (headers http.Header, body string) {
	t.Helper()
	pod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":7,"result":{}}`))
	}))
	defer pod.Close()

	req := httptest.NewRequest(http.MethodPost, "/agents/"+agent+"/a2a", strings.NewReader(tlsBody))
	if id != nil {
		req = req.WithContext(pkgmw.SetIdentity(req.Context(), id))
	}
	h.proxyA2ARequest(httptest.NewRecorder(), req, pod.URL, agent, kitchen, managed)
	return headers, body
}

func TestRelayToAPodPresentsThatPodsTokenAndDropsAUsersTLSOverride(t *testing.T) {
	secret := []byte("shared-secret")
	h := &Handlers{A2ASecret: secret}

	headers, body := relay(t, h, &contracts.Identity{Subject: "u", Provider: "apikey", Role: "admin"}, "default", "alpha")
	if want := a2aauth.PodToken(secret, "default", "alpha"); headers.Get(a2aauth.PodTokenHeader) != want || want == "" {
		t.Errorf("the pod must receive its own token: got %q want %q", headers.Get(a2aauth.PodTokenHeader), want)
	}
	if strings.Contains(body, "provider_config") || strings.Contains(body, "tls_skip_verify") {
		t.Errorf("a user's TLS override reached the pod: %s", body)
	}
	if headers.Get("Authorization") != "" {
		t.Error("the caller's own credential must not be forwarded to the pod")
	}

	// The token is for one agent only.
	headers, _ = relay(t, h, &contracts.Identity{Subject: "u", Provider: "apikey"}, "default", "beta")
	if headers.Get(a2aauth.PodTokenHeader) == a2aauth.PodToken(secret, "default", "alpha") {
		t.Error("beta's pod was given alpha's token")
	}

	// The platform's own call (the workflow engine) keeps its provider settings.
	_, body = relay(t, h, &contracts.Identity{Subject: "internal:workflow", Provider: "internal"}, "default", "alpha")
	if !strings.Contains(body, `"tls_skip_verify":true`) || !strings.Contains(body, `"ca_bundle":"PEM"`) {
		t.Errorf("the engine's provider settings must reach the pod: %s", body)
	}
}

func TestRelayWithoutASecretStillWorksForAPodThatHasNoToken(t *testing.T) {
	headers, body := relay(t, &Handlers{}, &contracts.Identity{Subject: "u", Provider: "apikey"}, "default", "alpha")
	if headers.Get(a2aauth.PodTokenHeader) != "" {
		t.Error("with no secret no token should be sent")
	}
	if body == "" {
		t.Error("the call should still reach the pod")
	}
}

// An external agent's URL belongs to someone else: it must never be handed the platform's pod token.
func TestRelayToAnExternalAgentSendsNoPodToken(t *testing.T) {
	h := &Handlers{A2ASecret: []byte("shared-secret")}
	headers, body := relayTo(t, h, &contracts.Identity{Subject: "u", Provider: "apikey"}, "default", "alpha", false)
	if headers.Get(a2aauth.PodTokenHeader) != "" {
		t.Error("the pod token was sent to an external agent")
	}
	if body == "" {
		t.Error("the call should still be relayed")
	}
}
