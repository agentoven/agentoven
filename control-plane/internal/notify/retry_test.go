package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// storeWithCredentials is a memory store with kitchen credentials in the default kitchen.
func storeWithCredentials(t *testing.T, creds map[string]string) store.Store {
	t.Helper()
	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	for name, value := range creds {
		if err := s.CreateKitchenCredential(context.Background(), &models.KitchenCredential{ID: name, Kitchen: "default", Name: name, Value: value}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// A retry must send the message again, not an empty body: the first attempt's body is spent.
func TestAWebhookRetryResendsTheWholeMessage(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			http.Error(w, "try again", http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	d := &WebhookChannelDriver{client: srv.Client()}
	ev := NewEvent(EventGateWaiting, "default", "run-1", "recipe", "gate", nil)
	if err := d.Send(context.Background(), &models.NotificationChannel{Name: "w", URL: srv.URL, Secret: "s3cret"}, ev); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("bodies = %q", bodies)
	}
}

// A webhook whose secret is kept as a credential (SecretRef, Pro) is signed with it, on a copy: the channel the
// store hands out never carries the value. When the credential cannot be read nothing is sent, unsigned or not.
func TestAWebhookWhoseSecretIsACredentialIsSignedAndNeverSentUnsigned(t *testing.T) {
	var got []http.Header
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, bodies = append(got, r.Header.Clone()), append(bodies, string(b))
	}))
	defer srv.Close()
	creds := storeWithCredentials(t, map[string]string{"channel-ops-secret": "whsec-FROM-VAULT"})
	svc := NewService(creds)
	svc.RegisterDriver(&WebhookChannelDriver{client: srv.Client()})
	ch := &models.NotificationChannel{Name: "ops", Kind: models.ChannelWebhook, Kitchen: "default", URL: srv.URL, SecretRef: "channel-ops-secret", Active: true}
	ev := NewEvent(EventGateWaiting, "default", "run-1", "recipe", "gate", nil)

	res := svc.DispatchToChannel(context.Background(), ch, ev)
	if !res.Success || len(got) != 1 {
		t.Fatalf("%+v", res)
	}
	mac := hmac.New(sha256.New, []byte("whsec-FROM-VAULT"))
	mac.Write([]byte(bodies[0]))
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); got[0].Get("X-AgentOven-Signature") != want {
		t.Errorf("signature %q, want %q", got[0].Get("X-AgentOven-Signature"), want)
	}
	if ch.Secret != "" {
		t.Error("the stored channel must not be given the value")
	}

	ch.SecretRef = "channel-gone"
	res = svc.DispatchToChannel(context.Background(), ch, ev)
	if res.Success || len(got) != 1 || !strings.Contains(res.Error, "unsigned") {
		t.Errorf("a secret that cannot be read stops the send: %+v (%d requests)", res, len(got))
	}
}
