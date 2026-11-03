package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/netguard"
)

// A webhook channel's URL is chosen by whoever may create channels and is called from inside the deployment, so
// it follows the same egress rule as a tool endpoint (pkg/netguard): loopback, private, link-local and cloud
// metadata addresses are refused unless the operator or the kitchen allowlisted the host.
func TestAWebhookChannelAtAPrivateAddressIsRefusedUnlessAllowlisted(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()            // an httptest server listens on 127.0.0.1
	d := &WebhookChannelDriver{} // no client of its own: the guarded one, as in production
	ch := &models.NotificationChannel{Name: "w", Kind: models.ChannelWebhook, Kitchen: "default", URL: srv.URL}
	ev := NewEvent(EventGateWaiting, "default", "run-1", "recipe", "gate", nil)

	start := time.Now()
	err := d.Send(context.Background(), ch, ev)
	if !errors.Is(err, netguard.ErrRefused) {
		t.Fatalf("a loopback URL must be refused by the egress guard, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("a refused destination must not be connected to")
	}
	if time.Since(start) > time.Second {
		t.Errorf("a refusal is a policy decision, not a network failure: it must not be retried with a pause (took %v)", time.Since(start))
	}

	t.Setenv(netguard.EnvAllow, "127.0.0.1")
	if err := d.Send(context.Background(), ch, ev); err != nil || hits.Load() != 1 {
		t.Fatalf("an allowlisted host is reached: err=%v hits=%d", err, hits.Load())
	}
}

func TestAWebhookChannelNeverReachesTheMetadataServiceEvenIfAllowlisted(t *testing.T) {
	t.Setenv(netguard.EnvAllow, "169.254.169.254")
	d := &WebhookChannelDriver{}
	ch := &models.NotificationChannel{Name: "w", Kind: models.ChannelWebhook, Kitchen: "default", URL: "http://169.254.169.254/latest/meta-data/"}
	if err := d.Send(context.Background(), ch, NewEvent(EventGateWaiting, "default", "r", "x", "y", nil)); !errors.Is(err, netguard.ErrRefused) {
		t.Fatalf("got %v", err)
	}
}

// A redirect to another host is not followed: an allowlisted receiver cannot bounce the control plane to an internal one.
func TestAWebhookChannelDoesNotFollowARedirect(t *testing.T) {
	var internal atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { internal.Add(1) }))
	defer target.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer front.Close()
	t.Setenv(netguard.EnvAllow, "127.0.0.1")
	d := &WebhookChannelDriver{}
	ch := &models.NotificationChannel{Name: "w", Kind: models.ChannelWebhook, Kitchen: "default", URL: front.URL}
	_ = d.Send(context.Background(), ch, NewEvent(EventGateWaiting, "default", "r", "x", "y", nil))
	if internal.Load() != 0 {
		t.Fatal("the redirect target was called")
	}
}
