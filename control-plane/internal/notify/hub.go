package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Hub holds the WebSocket connections listening to notification channels and hands each message to
// every one that listens to the channel it was sent through. It lives in memory: a message is delivered
// to whoever is connected at that moment and kept for no one, so a listener that connects later does
// not see what came before.
type Hub struct {
	mu   sync.RWMutex
	subs map[hubKey]map[*Subscriber]struct{}
}

type hubKey struct{ kitchen, channel string }

// Subscriber is one connection listening to one channel.
type Subscriber struct {
	// C carries the JSON of each message. It is closed when the subscriber is dropped for falling behind.
	C       chan []byte
	hub     *Hub
	key     hubKey
	closeMu sync.Mutex
	closed  bool
}

// subscriberBuffer is how many messages a listener may fall behind by before it is dropped.
const subscriberBuffer = 64

// NewHub creates an empty hub.
func NewHub() *Hub { return &Hub{subs: map[hubKey]map[*Subscriber]struct{}{}} }

// Subscribe starts listening to a channel of a kitchen. Call Close when done.
func (h *Hub) Subscribe(kitchen, channel string) *Subscriber {
	s := &Subscriber{C: make(chan []byte, subscriberBuffer), hub: h, key: hubKey{kitchen, channel}}
	h.mu.Lock()
	if h.subs[s.key] == nil {
		h.subs[s.key] = map[*Subscriber]struct{}{}
	}
	h.subs[s.key][s] = struct{}{}
	h.mu.Unlock()
	return s
}

// Close stops the subscriber listening. It is safe to call more than once.
func (s *Subscriber) Close() {
	s.hub.mu.Lock()
	if set := s.hub.subs[s.key]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(s.hub.subs, s.key)
		}
	}
	s.hub.mu.Unlock()
	s.shut()
}

func (s *Subscriber) shut() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.C)
	}
}

// Publish sends a message to everyone listening to the channel and returns how many it reached. A
// listener whose buffer is full is dropped rather than made to hold up the sender.
func (h *Hub) Publish(kitchen, channel string, message []byte) int {
	h.mu.RLock()
	listeners := make([]*Subscriber, 0, len(h.subs[hubKey{kitchen, channel}]))
	for s := range h.subs[hubKey{kitchen, channel}] {
		listeners = append(listeners, s)
	}
	h.mu.RUnlock()
	delivered := 0
	for _, s := range listeners {
		s.closeMu.Lock()
		if s.closed {
			s.closeMu.Unlock()
			continue
		}
		select {
		case s.C <- message:
			delivered++
			s.closeMu.Unlock()
		default:
			s.closeMu.Unlock()
			s.Close() // too slow: it will see its channel close and disconnect
		}
	}
	return delivered
}

// Listeners reports how many connections are listening to a channel.
func (h *Hub) Listeners(kitchen, channel string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[hubKey{kitchen, channel}])
}

// WebSocketChannelDriver delivers a notification to the connections listening to a channel of kind
// "websocket". It is built in, needs no URL or secret, and counts how many it reached.
type WebSocketChannelDriver struct{ hub *Hub }

// Kind returns ChannelWebSocket.
func (d *WebSocketChannelDriver) Kind() models.ChannelKind { return models.ChannelWebSocket }

// Send publishes the event to the channel's listeners. Nobody listening is not an error.
func (d *WebSocketChannelDriver) Send(ctx context.Context, channel *models.NotificationChannel, event Event) error {
	_, err := d.SendCounting(ctx, channel, event)
	return err
}

// SendCounting is Send, and says how many connections received the message.
func (d *WebSocketChannelDriver) SendCounting(_ context.Context, channel *models.NotificationChannel, event Event) (int, error) {
	body, err := json.Marshal(event)
	if err != nil {
		return 0, fmt.Errorf("marshal notification: %w", err)
	}
	return d.hub.Publish(channel.Kitchen, channel.Name, body), nil
}
