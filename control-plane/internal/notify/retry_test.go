package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

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
