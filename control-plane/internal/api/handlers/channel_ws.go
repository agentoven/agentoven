package handlers

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/notify"
	"github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// listenSubprotocol is the WebSocket subprotocol a listener offers (next to a ticket, in a browser).
const listenSubprotocol = "agentoven.v1"

var (
	fallbackTicketSecret     []byte
	fallbackTicketSecretOnce sync.Once
)

// ticketSecret is the key listening tickets are signed with: the control plane's own secret, or one made
// for this process when there is none (tickets then do not outlive a restart, which is a minute anyway).
func (h *Handlers) ticketSecret() []byte {
	if len(h.A2ASecret) > 0 {
		return h.A2ASecret
	}
	fallbackTicketSecretOnce.Do(func() {
		fallbackTicketSecret = make([]byte, 32)
		_, _ = rand.Read(fallbackTicketSecret)
	})
	return fallbackTicketSecret
}

// websocketChannel finds a channel and says whether it can be listened to.
func (h *Handlers) websocketChannel(w http.ResponseWriter, r *http.Request, kitchen, name string) (*models.NotificationChannel, bool) {
	ch, err := h.Store.GetChannel(r.Context(), kitchen, name)
	if err != nil || ch == nil {
		respondError(w, http.StatusNotFound, "Channel '"+name+"' not found")
		return nil, false
	}
	if ch.Kind != models.ChannelWebSocket {
		respondError(w, http.StatusBadRequest, "Channel '"+name+"' is a "+string(ch.Kind)+" channel; only a websocket channel can be listened to")
		return nil, false
	}
	if !ch.Active {
		respondError(w, http.StatusConflict, "Channel '"+name+"' is inactive")
		return nil, false
	}
	return ch, true
}

// ChannelTicket gives a backend that holds an API key a short-lived ticket for one channel, which it
// hands to a browser: a browser cannot set headers on a WebSocket, so it offers the ticket as the
// `agentoven-token.<ticket>` subprotocol instead.
// POST /api/v1/channels/{channelName}/ws-ticket
func (h *Handlers) ChannelTicket(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	name := chi.URLParam(r, "channelName")
	if _, ok := h.websocketChannel(w, r, kitchen, name); !ok {
		return
	}
	token := notify.MintTicket(h.ticketSecret(), kitchen, name, notify.TicketTTL, time.Now())
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"token":       token,
		"expires_in":  int(notify.TicketTTL.Seconds()),
		"subprotocol": notify.TicketSubprotocolPrefix + token,
	})
}

// ChannelListen upgrades the request to a WebSocket and pushes to it every notification sent through the
// channel, one JSON message each, from the moment it connects. Nothing earlier is replayed.
// GET /api/v1/channels/{channelName}/ws
func (h *Handlers) ChannelListen(w http.ResponseWriter, r *http.Request) {
	if h.Notifier == nil {
		respondError(w, http.StatusServiceUnavailable, "Notifications are not available")
		return
	}
	name := chi.URLParam(r, "channelName")
	kitchen := middleware.GetKitchen(r.Context())

	// A ticket stands in for headers a browser cannot send; it fixes the kitchen and the channel.
	for _, offered := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		offered = strings.TrimSpace(offered)
		if !strings.HasPrefix(offered, notify.TicketSubprotocolPrefix) {
			continue
		}
		tk, tc, err := notify.VerifyTicket(h.ticketSecret(), strings.TrimPrefix(offered, notify.TicketSubprotocolPrefix), time.Now())
		if err != nil || tc != name {
			respondError(w, http.StatusUnauthorized, "This ticket cannot be used to listen to '"+name+"'")
			return
		}
		kitchen = tk
		break
	}

	if _, ok := h.websocketChannel(w, r, kitchen, name); !ok {
		return
	}

	opts := &websocket.AcceptOptions{Subprotocols: []string{listenSubprotocol}}
	if origins := strings.TrimSpace(os.Getenv("AGENTOVEN_CORS_ORIGINS")); origins == "*" {
		opts.InsecureSkipVerify = true
	} else if origins != "" {
		for _, o := range strings.Split(origins, ",") {
			o = strings.TrimSpace(o)
			o = strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://")
			if o != "" {
				opts.OriginPatterns = append(opts.OriginPatterns, o)
			}
		}
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return // Accept has answered
	}
	defer conn.CloseNow()

	sub := h.Notifier.Hub().Subscribe(kitchen, name)
	defer sub.Close()
	ctx := conn.CloseRead(r.Context()) // we only send; this notices the client leaving
	ready, _ := json.Marshal(map[string]string{"type": "ready", "channel": name, "kitchen": kitchen})
	if err := conn.Write(ctx, websocket.MessageText, ready); err != nil {
		return
	}
	log.Debug().Str("channel", name).Str("kitchen", kitchen).Msg("Notification listener connected")

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case msg, ok := <-sub.C:
			if !ok {
				_ = conn.Close(websocket.StatusTryAgainLater, "listener fell behind")
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
				return
			}
		case <-ping.C:
			if err := conn.Ping(ctx); err != nil {
				return
			}
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
}
