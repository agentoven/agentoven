package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A channel's signing secret is taken on create and update and never returned: every answer says only that the
// channel is signed. An update that does not mention it keeps it, an update that sends it empty removes it, and a
// client cannot name the credential that holds it.
func TestAChannelsSigningSecretGoesInAndNeverComesOut(t *testing.T) {
	h, s := newTestHandlers(t)
	ctx := context.Background()
	create := callAgentAPI(h.CreateChannel, http.MethodPost, "/api/v1/channels", nil,
		`{"name":"ops","kind":"webhook","url":"https://hooks.example.test/in","secret":"whsec-SECRET-123","events":["*"]}`)
	if create.Code != http.StatusCreated || strings.Contains(create.Body.String(), "SECRET-123") || !strings.Contains(create.Body.String(), `"has_secret":true`) {
		t.Fatalf("create: %d %s", create.Code, create.Body)
	}
	if got, _ := s.GetChannel(ctx, "default", "ops"); got == nil || got.Secret != "whsec-SECRET-123" {
		t.Fatalf("the community store keeps the secret it signs with: %+v", got)
	}
	name := map[string]string{"channelName": "ops"}
	for what, w := range map[string]*httptest.ResponseRecorder{
		"get":  callAgentAPI(h.GetChannel, http.MethodGet, "/api/v1/channels/ops", name, ""),
		"list": callAgentAPI(h.ListChannels, http.MethodGet, "/api/v1/channels", nil, ""),
	} {
		if strings.Contains(w.Body.String(), "SECRET-123") || !strings.Contains(w.Body.String(), `"has_secret":true`) {
			t.Errorf("%s shows the secret or hides that there is one: %s", what, w.Body)
		}
	}

	// An edit that does not mention the secret keeps it.
	up := callAgentAPI(h.UpdateChannel, http.MethodPut, "/api/v1/channels/ops", name, `{"kind":"webhook","url":"https://hooks.example.test/in2","events":["*"],"active":true}`)
	if up.Code != http.StatusOK || strings.Contains(up.Body.String(), "SECRET-123") || !strings.Contains(up.Body.String(), `"has_secret":true`) {
		t.Fatalf("update: %d %s", up.Code, up.Body)
	}
	if got, _ := s.GetChannel(ctx, "default", "ops"); got.Secret != "whsec-SECRET-123" || got.URL != "https://hooks.example.test/in2" {
		t.Errorf("an edit erased the secret: %+v", got)
	}
	// A new secret replaces it; an empty one removes it.
	callAgentAPI(h.UpdateChannel, http.MethodPut, "/api/v1/channels/ops", name, `{"kind":"webhook","url":"https://hooks.example.test/in2","secret":"whsec-ROTATED-456","active":true}`)
	if got, _ := s.GetChannel(ctx, "default", "ops"); got.Secret != "whsec-ROTATED-456" {
		t.Errorf("rotation: %+v", got)
	}
	rm := callAgentAPI(h.UpdateChannel, http.MethodPut, "/api/v1/channels/ops", name, `{"kind":"webhook","url":"https://hooks.example.test/in2","secret":"","active":true}`)
	var shown models.NotificationChannel
	_ = json.Unmarshal(rm.Body.Bytes(), &shown)
	if got, _ := s.GetChannel(ctx, "default", "ops"); got.Secret != "" || shown.HasSecret {
		t.Errorf("an explicit empty secret removes it: %+v %+v", got, shown)
	}

	// The credential's name is the server's to set.
	for _, c := range []*httptest.ResponseRecorder{
		callAgentAPI(h.CreateChannel, http.MethodPost, "/api/v1/channels", nil, `{"name":"x","kind":"webhook","url":"https://hooks.example.test/in","secret_ref":"prod-db-password"}`),
		callAgentAPI(h.UpdateChannel, http.MethodPut, "/api/v1/channels/ops", name, `{"kind":"webhook","url":"https://hooks.example.test/in","secret_ref":"prod-db-password"}`),
	} {
		if c.Code != http.StatusBadRequest {
			t.Errorf("a client-named secret_ref must be refused: %d %s", c.Code, c.Body)
		}
	}
	if got, _ := s.GetChannel(ctx, "default", "x"); got != nil {
		t.Error("a refused channel was stored")
	}
}
