package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/notify"
	"github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

// A server with the channel routes, as the control plane mounts them.
func channelServer(t *testing.T, channels ...models.NotificationChannel) (*httptest.Server, *notify.Service) {
	t.Helper()
	h, s := newTestHandlers(t)
	for i := range channels {
		if err := s.CreateChannel(context.Background(), &channels[i]); err != nil {
			t.Fatal(err)
		}
	}
	h.Notifier = notify.NewService(s)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler { // the kitchen the tenant middleware would set
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			k := req.Header.Get("X-Kitchen")
			if k == "" {
				k = "default"
			}
			next.ServeHTTP(w, req.WithContext(middleware.SetKitchen(req.Context(), k)))
		})
	})
	r.Route("/api/v1/channels/{channelName}", func(r chi.Router) {
		r.Get("/ws", h.ChannelListen)
		r.Post("/ws-ticket", h.ChannelTicket)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, h.Notifier
}

func wsCh(name, kitchen string, kind models.ChannelKind, active bool) models.NotificationChannel {
	return models.NotificationChannel{ID: kitchen + name, Name: name, Kitchen: kitchen, Kind: kind, Active: active}
}

func dial(t *testing.T, srv *httptest.Server, channel string, subprotocols ...string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/channels/"+channel+"/ws", &websocket.DialOptions{Subprotocols: subprotocols})
}

func read(t *testing.T, c *websocket.Conn) map[string]interface{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	return m
}

func TestAListenerGetsReadyThenEveryNotificationSentThroughTheChannel(t *testing.T) {
	srv, svc := channelServer(t, wsCh("ops", "default", models.ChannelWebSocket, true))
	c, _, err := dial(t, srv, "ops", "agentoven.v1")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if c.Subprotocol() != "agentoven.v1" {
		t.Errorf("subprotocol %q", c.Subprotocol())
	}
	if ready := read(t, c); ready["type"] != "ready" || ready["channel"] != "ops" {
		t.Fatalf("%v", ready)
	}
	for i, msg := range []string{"first", "second"} {
		res := svc.SendTo(context.Background(), "default", []string{"ops"}, nil,
			notify.NewEvent(notify.EventNotification, "default", "run-1", "refunds", "tell", map[string]interface{}{"message": msg}))
		if len(res) != 1 || !res[0].Success || res[0].Recipients != 1 {
			t.Fatalf("send %d: %+v", i, res)
		}
		got := read(t, c)
		if got["type"] != "notification" || got["payload"].(map[string]interface{})["message"] != msg {
			t.Errorf("%d: %v", i, got)
		}
	}
	// Leaving frees the slot.
	c.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(2 * time.Second)
	for svc.Hub().Listeners("default", "ops") != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if svc.Hub().Listeners("default", "ops") != 0 {
		t.Error("the listener was not forgotten after it left")
	}
}

func TestOnlyAnActiveWebsocketChannelThatExistsCanBeListenedTo(t *testing.T) {
	srv, _ := channelServer(t,
		wsCh("hook", "default", models.ChannelWebhook, true),
		wsCh("off", "default", models.ChannelWebSocket, false),
		wsCh("elsewhere", "acme", models.ChannelWebSocket, true),
	)
	for name, want := range map[string]int{"hook": 400, "off": 409, "missing": 404, "elsewhere": 404} {
		_, resp, err := dial(t, srv, name, "agentoven.v1")
		if err == nil || resp == nil || resp.StatusCode != want {
			t.Errorf("%s: want %d, got %v (%v)", name, want, resp, err)
		}
	}
}

func TestATicketLetsABrowserListenWithoutHeaders(t *testing.T) {
	srv, svc := channelServer(t, wsCh("ops", "default", models.ChannelWebSocket, true), wsCh("billing", "default", models.ChannelWebSocket, true))
	resp, err := http.Post(srv.URL+"/api/v1/channels/ops/ws-ticket", "application/json", nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", resp, err)
	}
	var tk struct {
		Token       string `json:"token"`
		ExpiresIn   int    `json:"expires_in"`
		Subprotocol string `json:"subprotocol"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tk)
	if tk.Token == "" || tk.ExpiresIn != 60 || tk.Subprotocol != notify.TicketSubprotocolPrefix+tk.Token {
		t.Fatalf("%+v", tk)
	}

	c, _, err := dial(t, srv, "ops", "agentoven.v1", tk.Subprotocol)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if read(t, c)["type"] != "ready" {
		t.Fatal("not ready")
	}
	svc.SendTo(context.Background(), "default", []string{"ops"}, nil, notify.NewEvent(notify.EventNotification, "default", "r", "x", "s", map[string]interface{}{"message": "hi"}))
	if read(t, c)["type"] != "notification" {
		t.Error("no message")
	}

	// A ticket is for one channel.
	if _, resp, err := dial(t, srv, "billing", "agentoven.v1", tk.Subprotocol); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Errorf("a ticket for ops must not open billing: %v %v", resp, err)
	}
	// Nor can it be made up or changed.
	if _, resp, err := dial(t, srv, "ops", "agentoven.v1", notify.TicketSubprotocolPrefix+"forged.ticket"); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Errorf("a forged ticket must be refused: %v %v", resp, err)
	}
}

func TestATicketCarriesItsKitchen(t *testing.T) {
	srv, svc := channelServer(t, wsCh("ops", "acme", models.ChannelWebSocket, true))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/channels/ops/ws-ticket", nil)
	req.Header.Set("X-Kitchen", "acme")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", resp, err)
	}
	var tk struct{ Subprotocol string }
	_ = json.NewDecoder(resp.Body).Decode(&tk)
	// Connecting without X-Kitchen (a browser) still lands in the ticket's kitchen.
	c, _, err := dial(t, srv, "ops", "agentoven.v1", tk.Subprotocol)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if ready := read(t, c); ready["kitchen"] != "acme" {
		t.Errorf("%v", ready)
	}
	if svc.Hub().Listeners("acme", "ops") != 1 || svc.Hub().Listeners("default", "ops") != 0 {
		t.Error("listening in the wrong kitchen")
	}
}
