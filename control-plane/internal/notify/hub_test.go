package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestAMessageReachesEveryoneListeningToThatChannelOnly(t *testing.T) {
	h := NewHub()
	a1, a2 := h.Subscribe("default", "ops"), h.Subscribe("default", "ops")
	other, otherKitchen := h.Subscribe("default", "billing"), h.Subscribe("acme", "ops")
	defer a1.Close()
	defer a2.Close()
	defer other.Close()
	defer otherKitchen.Close()

	if n := h.Publish("default", "ops", []byte("hello")); n != 2 {
		t.Fatalf("reached %d, want 2", n)
	}
	for _, s := range []*Subscriber{a1, a2} {
		select {
		case m := <-s.C:
			if string(m) != "hello" {
				t.Errorf("got %q", m)
			}
		case <-time.After(time.Second):
			t.Fatal("a listener did not get the message")
		}
	}
	for name, s := range map[string]*Subscriber{"other channel": other, "other kitchen": otherKitchen} {
		select {
		case m := <-s.C:
			t.Errorf("%s received %q", name, m)
		default:
		}
	}
}

func TestPublishingToNobodyIsNotAnError(t *testing.T) {
	if n := NewHub().Publish("default", "empty", []byte("x")); n != 0 {
		t.Errorf("reached %d", n)
	}
}

func TestAListenerThatLeavesIsForgotten(t *testing.T) {
	h := NewHub()
	s := h.Subscribe("default", "ops")
	if h.Listeners("default", "ops") != 1 {
		t.Fatal("not counted")
	}
	s.Close()
	s.Close() // more than once is fine
	if h.Listeners("default", "ops") != 0 || h.Publish("default", "ops", []byte("x")) != 0 {
		t.Error("a closed listener still counts")
	}
}

func TestAListenerThatFallsBehindIsDroppedAndTheSenderIsNotHeldUp(t *testing.T) {
	h := NewHub()
	slow := h.Subscribe("default", "ops")
	fast := h.Subscribe("default", "ops")
	defer fast.Close()
	for i := 0; i < subscriberBuffer+5; i++ {
		h.Publish("default", "ops", []byte("m"))
		select {
		case <-fast.C: // the fast one keeps up
		default:
		}
	}
	if h.Listeners("default", "ops") != 1 {
		t.Errorf("the slow listener should have been dropped, %d left", h.Listeners("default", "ops"))
	}
	n := 0
	for range slow.C { // closed by the drop, so this ends
		n++
	}
	if n != subscriberBuffer {
		t.Errorf("the slow listener kept the %d it could hold, got %d", subscriberBuffer, n)
	}
}

func TestTicketsAreForOneChannelOfOneKitchenAndExpire(t *testing.T) {
	secret := []byte("server-secret")
	now := time.Now()
	tk := MintTicket(secret, "acme", "ops", time.Minute, now)
	k, c, err := VerifyTicket(secret, tk, now.Add(30*time.Second))
	if err != nil || k != "acme" || c != "ops" {
		t.Fatalf("%q %q %v", k, c, err)
	}
	if _, _, err := VerifyTicket(secret, tk, now.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("an old ticket must not work: %v", err)
	}
	if _, _, err := VerifyTicket([]byte("another-secret"), tk, now); err == nil {
		t.Error("a ticket from another server must not work")
	}
	parts := strings.SplitN(tk, ".", 2)
	if _, _, err := VerifyTicket(secret, MintTicket(secret, "acme", "other", time.Minute, now)[:strings.Index(MintTicket(secret, "acme", "other", time.Minute, now), ".")]+"."+parts[1], now); err == nil {
		t.Error("a changed claim must not verify")
	}
	for _, bad := range []string{"", "nodot", "a.b", "!!.!!"} {
		if _, _, err := VerifyTicket(secret, bad, now); err == nil {
			t.Errorf("%q should not verify", bad)
		}
	}
}

// ── Sending to named targets ─────────────────────────────────

func serviceWithChannels(t *testing.T, channels ...models.NotificationChannel) *Service {
	t.Helper()
	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	for i := range channels {
		if err := s.CreateChannel(context.Background(), &channels[i]); err != nil {
			t.Fatal(err)
		}
	}
	return NewService(s)
}

func TestSendToReachesTheNamedWebsocketChannelAndSaysHowManyListeners(t *testing.T) {
	svc := serviceWithChannels(t, models.NotificationChannel{ID: "1", Name: "ops", Kind: models.ChannelWebSocket, Kitchen: "default", Active: true, Events: []string{"gate_waiting"}})
	l1, l2 := svc.Hub().Subscribe("default", "ops"), svc.Hub().Subscribe("default", "ops")
	defer l1.Close()
	defer l2.Close()

	event := NewEvent(EventNotification, "default", "run-1", "refunds", "tell-team", map[string]interface{}{"message": "A person is needed"})
	res := svc.SendTo(context.Background(), "default", []string{"ops"}, nil, event)
	if len(res) != 1 || !res[0].Success || res[0].Recipients != 2 {
		t.Fatalf("%+v", res)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(<-l1.C, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "notification" || got["run_id"] != "run-1" || got["step_name"] != "tell-team" || got["payload"].(map[string]interface{})["message"] != "A person is needed" {
		t.Errorf("message: %v", got)
	}
	// Naming a channel is the request: it receives the event even though it subscribes to others.
}

func TestSendToWithNobodyListeningSucceedsWithZeroRecipients(t *testing.T) {
	svc := serviceWithChannels(t, models.NotificationChannel{ID: "1", Name: "ops", Kind: models.ChannelWebSocket, Kitchen: "default", Active: true})
	res := svc.SendTo(context.Background(), "default", []string{"ops"}, nil, NewEvent(EventNotification, "default", "r", "x", "s", nil))
	if len(res) != 1 || !res[0].Success || res[0].Recipients != 0 {
		t.Errorf("%+v", res)
	}
}

func TestSendToNeverSkipsATargetSilently(t *testing.T) {
	svc := serviceWithChannels(t,
		models.NotificationChannel{ID: "1", Name: "off", Kind: models.ChannelWebSocket, Kitchen: "default", Active: false},
		models.NotificationChannel{ID: "2", Name: "odd", Kind: "carrier-pigeon", Kitchen: "default", Active: true},
	)
	res := svc.SendTo(context.Background(), "default", []string{"missing", "off", "odd"}, []string{"no-such-tool"}, NewEvent(EventNotification, "default", "r", "x", "s", nil))
	if len(res) != 4 {
		t.Fatalf("every target gets a result: %+v", res)
	}
	for _, r := range res {
		if r.Success || r.Error == "" {
			t.Errorf("should have failed with a reason: %+v", r)
		}
	}
}

func TestAChannelOfAnotherKitchenCannotBeReached(t *testing.T) {
	svc := serviceWithChannels(t, models.NotificationChannel{ID: "1", Name: "ops", Kind: models.ChannelWebSocket, Kitchen: "acme", Active: true})
	res := svc.SendTo(context.Background(), "default", []string{"ops"}, nil, NewEvent(EventNotification, "default", "r", "x", "s", nil))
	if res[0].Success {
		t.Error("a kitchen must not reach another kitchen's channel")
	}
}
