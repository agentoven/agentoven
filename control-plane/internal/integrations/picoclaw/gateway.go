package picoclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ── Chat Gateway Manager ─────────────────────────────────────

// GatewayManager manages chat platform gateways (Telegram, Discord, etc.)
// and routes incoming messages to AgentOven agents via the PicoClaw adapter.
type GatewayManager struct {
	store   store.Store
	adapter *Adapter
	drivers map[models.ChatGatewayKind]contracts.ChatGatewayDriver
	active  map[string]context.CancelFunc // gatewayID → cancel func
	mu      sync.RWMutex
}

// NewGatewayManager creates a new chat gateway manager.
func NewGatewayManager(s store.Store, adapter *Adapter) *GatewayManager {
	return &GatewayManager{
		store:   s,
		adapter: adapter,
		drivers: make(map[models.ChatGatewayKind]contracts.ChatGatewayDriver),
		active:  make(map[string]context.CancelFunc),
	}
}

// RegisterDriver registers a chat gateway driver for a specific platform.
func (m *GatewayManager) RegisterDriver(driver contracts.ChatGatewayDriver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drivers[driver.Kind()] = driver
	log.Info().Str("kind", string(driver.Kind())).Msg("registered chat gateway driver")
}

// HasDriver returns true if a driver is registered for the given kind.
func (m *GatewayManager) HasDriver(kind models.ChatGatewayKind) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.drivers[kind]
	return ok
}

// ── Gateway CRUD ─────────────────────────────────────────────

// CreateGatewayRequest is the payload to create a chat gateway.
type CreateGatewayRequest struct {
	Name      string                 `json:"name"`
	Kind      models.ChatGatewayKind `json:"kind"` // telegram, discord, slack-bot, etc.
	AgentName string                 `json:"agent_name"` // target PicoClaw agent
	Config    map[string]interface{} `json:"config"` // platform-specific: bot_token, channel_id, etc.
	Active    bool                   `json:"active"`
}

// CreateGateway creates and optionally starts a chat gateway.
func (m *GatewayManager) CreateGateway(ctx context.Context, kitchen string, req CreateGatewayRequest) (*models.ChatGateway, error) {
	if req.Name == "" || req.Kind == "" || req.AgentName == "" {
		return nil, fmt.Errorf("name, kind, and agent_name are required")
	}

	gw := &models.ChatGateway{
		ID:        uuid.New().String(),
		Name:      req.Name,
		Kind:      req.Kind,
		Kitchen:   kitchen,
		AgentName: req.AgentName,
		Active:    req.Active,
		Config:    req.Config,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	// If active, start the gateway
	if gw.Active {
		if err := m.startGateway(ctx, gw); err != nil {
			return nil, fmt.Errorf("start gateway: %w", err)
		}
	}

	log.Info().
		Str("gateway", gw.Name).
		Str("kind", string(gw.Kind)).
		Str("agent", gw.AgentName).
		Bool("active", gw.Active).
		Msg("chat gateway created")

	return gw, nil
}

// StopGateway stops a running chat gateway.
func (m *GatewayManager) StopGateway(ctx context.Context, gatewayID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cancel, ok := m.active[gatewayID]
	if !ok {
		return fmt.Errorf("gateway %s is not running", gatewayID)
	}

	cancel()
	delete(m.active, gatewayID)
	log.Info().Str("gateway", gatewayID).Msg("chat gateway stopped")
	return nil
}

// StopAll stops all running gateways. Called during shutdown.
func (m *GatewayManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, cancel := range m.active {
		cancel()
		log.Info().Str("gateway", id).Msg("chat gateway stopped (shutdown)")
	}
	m.active = make(map[string]context.CancelFunc)
}

// ── Internal ─────────────────────────────────────────────────

// startGateway starts a chat gateway and routes messages to the linked agent.
func (m *GatewayManager) startGateway(ctx context.Context, gw *models.ChatGateway) error {
	m.mu.RLock()
	driver, ok := m.drivers[gw.Kind]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("no driver for gateway kind %q (available drivers: %v)", gw.Kind, m.driverKinds())
	}

	gwCtx, cancel := context.WithCancel(ctx)

	// onMessage callback: relay incoming chat messages to the PicoClaw agent
	onMessage := func(msg models.GatewayMessage) {
		if msg.Direction != "inbound" {
			return
		}

		log.Debug().
			Str("gateway", gw.Name).
			Str("user", msg.UserName).
			Str("text", msg.Text).
			Msg("incoming chat message")

		// Relay to PicoClaw agent
		resp := m.adapter.Relay(gwCtx, gw.Kitchen, RelayRequest{
			InstanceName: extractInstanceName(gw.AgentName),
			Message:      msg.Text,
		})

		// Send response back via gateway
		reply := models.GatewayMessage{
			GatewayID: gw.ID,
			Platform:  string(gw.Kind),
			ChannelID: msg.ChannelID,
			UserID:    msg.UserID,
			UserName:  "agentoven",
			Text:      resp.Output,
			Direction: "outbound",
			Timestamp: time.Now().UTC(),
		}

		if resp.Error != "" {
			reply.Text = fmt.Sprintf("⚠️ Error: %s", resp.Error)
		}

		if err := driver.Send(gwCtx, gw, reply); err != nil {
			log.Warn().Err(err).Str("gateway", gw.Name).Msg("failed to send reply")
		}
	}

	// Start the driver in the background
	if err := driver.Start(gwCtx, gw, onMessage); err != nil {
		cancel()
		return fmt.Errorf("start driver: %w", err)
	}

	m.mu.Lock()
	m.active[gw.ID] = cancel
	m.mu.Unlock()

	return nil
}

func (m *GatewayManager) driverKinds() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	kinds := make([]string, 0, len(m.drivers))
	for k := range m.drivers {
		kinds = append(kinds, string(k))
	}
	return kinds
}

// extractInstanceName strips the "picoclaw-" prefix from an agent name.
func extractInstanceName(agentName string) string {
	const prefix = "picoclaw-"
	if len(agentName) > len(prefix) && agentName[:len(prefix)] == prefix {
		return agentName[len(prefix):]
	}
	return agentName
}

// ── HTTP Handlers ────────────────────────────────────────────

// HandleCreateGateway handles POST /picoclaw/gateways
func (m *GatewayManager) HandleCreateGateway(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	if kitchen == "" {
		kitchen = "default"
	}

	var req CreateGatewayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	gw, err := m.CreateGateway(r.Context(), kitchen, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(gw)
}

// HandleStopGateway handles DELETE /picoclaw/gateways/{id}
func (m *GatewayManager) HandleStopGateway(w http.ResponseWriter, r *http.Request) {
	gatewayID := r.URL.Query().Get("id")
	if gatewayID == "" {
		http.Error(w, "id query parameter required", http.StatusBadRequest)
		return
	}

	if err := m.StopGateway(r.Context(), gatewayID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleListDrivers handles GET /picoclaw/gateways/drivers
func (m *GatewayManager) HandleListDrivers(w http.ResponseWriter, r *http.Request) {
	kinds := m.driverKinds()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"drivers": kinds,
		"count":   len(kinds),
	})
}
