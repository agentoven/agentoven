// Package notify dispatches notification events to MCP tools and
// registered notification channels (webhook, Slack, Teams, etc.).
//
// The service supports two dispatch paths:
//  1. MCP tools — JSON-RPC tools/call to tools with the "notify" capability
//  2. Channel drivers — pluggable ChannelDriver implementations that send to
//     webhook URLs, Slack, Teams, Discord, Email, or Zapier
//
// OSS ships with the WebhookChannelDriver. Pro adds Slack, Teams, Discord,
// Email, and Zapier drivers via RegisterDriver.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// ── Event types ─────────────────────────────────────────────

// EventType describes what happened.
type EventType string

const (
	EventGateWaiting   EventType = "gate_waiting"
	EventStepCompleted EventType = "step_completed"
	EventStepFailed    EventType = "step_failed"
	EventRunCompleted  EventType = "run_completed"
	EventRunFailed     EventType = "run_failed"
)

// Event is the internal notification payload. It maps 1:1 to contracts.NotificationEvent.
type Event = contracts.NotificationEvent

// NewEvent creates an Event with the given type and fields.
func NewEvent(eventType EventType, kitchen, runID, recipeName, stepName string, payload map[string]interface{}) Event {
	return Event{
		Type:       string(eventType),
		RunID:      runID,
		RecipeName: recipeName,
		StepName:   stepName,
		Kitchen:    kitchen,
		Payload:    payload,
		Timestamp:  time.Now().UTC(),
	}
}

// ── Service ──────────────────────────────────────────────────

// Service dispatches notification events to MCP tools and registered channels.
type Service struct {
	store   store.Store
	client  *http.Client
	drivers map[models.ChannelKind]contracts.ChannelDriver
	drvMu   sync.RWMutex
}

// NewService creates a new notification service with the built-in webhook driver.
func NewService(s store.Store) *Service {
	svc := &Service{
		store: s,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		drivers: make(map[models.ChannelKind]contracts.ChannelDriver),
	}
	// Register the built-in OSS webhook driver
	svc.RegisterDriver(&WebhookChannelDriver{
		client: svc.client,
	})
	return svc
}

// RegisterDriver adds or replaces a channel driver for the given kind.
// Pro uses this to register Slack, Teams, Discord, Email, Zapier drivers.
func (s *Service) RegisterDriver(driver contracts.ChannelDriver) {
	s.drvMu.Lock()
	defer s.drvMu.Unlock()
	s.drivers[driver.Kind()] = driver
	log.Info().Str("kind", string(driver.Kind())).Msg("Registered notification channel driver")
}

// GetDriver returns the driver for a given channel kind, or nil.
func (s *Service) GetDriver(kind models.ChannelKind) contracts.ChannelDriver {
	s.drvMu.RLock()
	defer s.drvMu.RUnlock()
	return s.drivers[kind]
}

// ── MCP Tool Dispatch ────────────────────────────────────────

// DispatchToTool sends a notification event to the named MCP tool.
// It validates the tool has the "notify" capability and sends the event
// as a JSON-RPC tools/call to the tool's endpoint.
func (s *Service) DispatchToTool(ctx context.Context, kitchen, toolName string, event Event) models.NotifyResult {
	result := models.NotifyResult{
		Tool:      toolName,
		Timestamp: time.Now().UTC(),
	}

	tool, err := s.store.GetTool(ctx, kitchen, toolName)
	if err != nil {
		result.Error = fmt.Sprintf("tool not found: %s", toolName)
		log.Warn().Str("tool", toolName).Msg("Notify tool not found")
		return result
	}

	if !hasCapability(tool.Capabilities, "notify") {
		result.Error = fmt.Sprintf("tool %s does not have notify capability", toolName)
		log.Warn().Str("tool", toolName).Msg("Tool missing notify capability")
		return result
	}

	if !tool.Enabled {
		result.Error = fmt.Sprintf("tool %s is disabled", toolName)
		return result
	}

	// Send via MCP tools/call JSON-RPC
	err = s.sendMCPNotification(ctx, tool, event)
	if err != nil {
		result.Error = err.Error()
		log.Warn().Err(err).Str("tool", toolName).Str("event", string(event.Type)).Msg("MCP notification failed")
		return result
	}

	result.Success = true
	log.Info().Str("tool", toolName).Str("event", string(event.Type)).Str("run", event.RunID).Msg("MCP notification dispatched")
	return result
}

// Dispatch is the old entrypoint — delegates to DispatchToTool for backward compat.
func (s *Service) Dispatch(ctx context.Context, kitchen, toolName string, event Event) models.NotifyResult {
	return s.DispatchToTool(ctx, kitchen, toolName, event)
}

// ── Channel Dispatch ─────────────────────────────────────────

// DispatchToChannel sends a notification event through a registered notification channel.
func (s *Service) DispatchToChannel(ctx context.Context, channel *models.NotificationChannel, event Event) models.NotifyResult {
	result := models.NotifyResult{
		Tool:      fmt.Sprintf("channel:%s/%s", channel.Kind, channel.Name),
		Timestamp: time.Now().UTC(),
	}

	if !channel.Active {
		result.Error = fmt.Sprintf("channel %s is inactive", channel.Name)
		return result
	}

	// Check if this channel subscribes to this event type
	if !channelSubscribes(channel, event.Type) {
		result.Error = fmt.Sprintf("channel %s does not subscribe to %s events", channel.Name, event.Type)
		return result
	}

	driver := s.GetDriver(channel.Kind)
	if driver == nil {
		result.Error = fmt.Sprintf("no driver registered for channel kind %s", channel.Kind)
		log.Warn().Str("kind", string(channel.Kind)).Str("channel", channel.Name).Msg("No channel driver")
		return result
	}

	if err := driver.Send(ctx, channel, event); err != nil {
		result.Error = err.Error()
		log.Warn().Err(err).Str("channel", channel.Name).Str("kind", string(channel.Kind)).Str("event", string(event.Type)).Msg("Channel notification failed")
		return result
	}

	result.Success = true
	log.Info().Str("channel", channel.Name).Str("kind", string(channel.Kind)).Str("event", string(event.Type)).Str("run", event.RunID).Msg("Channel notification dispatched")
	return result
}

// ── Combined Dispatch ────────────────────────────────────────

// DispatchAll sends a notification to both MCP tools AND all matching registered
// notification channels for the kitchen. MCP tools and channels are dispatched
// concurrently. Results are collected and returned.
func (s *Service) DispatchAll(ctx context.Context, kitchen string, toolNames []string, event Event) []models.NotifyResult {
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results []models.NotifyResult
	)

	// 1. Dispatch to named MCP tools concurrently
	for _, name := range toolNames {
		wg.Add(1)
		go func(toolName string) {
			defer wg.Done()
			r := s.DispatchToTool(ctx, kitchen, toolName, event)
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(name)
	}

	// 2. Dispatch to all active notification channels for this kitchen
	channels, err := s.store.ListChannels(ctx, kitchen)
	if err != nil {
		log.Warn().Err(err).Str("kitchen", kitchen).Msg("Failed to list notification channels")
	} else {
		for i := range channels {
			ch := channels[i] // capture
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := s.DispatchToChannel(ctx, &ch, event)
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}()
		}
	}

	wg.Wait()
	return results
}

// ── MCP Tool Transport ───────────────────────────────────────

// sendMCPNotification sends the event to the tool's endpoint as a JSON-RPC tools/call request.
func (s *Service) sendMCPNotification(ctx context.Context, tool *models.MCPTool, event Event) error {
	eventJSON, _ := json.Marshal(event)
	rpcReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      fmt.Sprintf("notify-%s-%d", event.RunID, time.Now().UnixMilli()),
		"params": map[string]interface{}{
			"name": "notify",
			"arguments": map[string]interface{}{
				"event_type":  string(event.Type),
				"run_id":      event.RunID,
				"recipe_name": event.RecipeName,
				"step_name":   event.StepName,
				"kitchen":     event.Kitchen,
				"payload":     json.RawMessage(eventJSON),
			},
		},
	}

	body, err := json.Marshal(rpcReq)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tool.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	applyAuth(req, tool.AuthConfig)

	return s.sendWithRetries(req)
}

// ── HTTP Helpers ─────────────────────────────────────────────

// sendWithRetries sends an HTTP request with up to 3 attempts and exponential backoff.
func (s *Service) sendWithRetries(req *http.Request) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, req.URL.String())
	}
	return fmt.Errorf("notification failed after 3 attempts: %w", lastErr)
}

// applyAuth adds authentication headers to the request based on tool auth config.
func applyAuth(req *http.Request, authConfig map[string]interface{}) {
	if authConfig == nil {
		return
	}
	authType, _ := authConfig["type"].(string)
	switch authType {
	case "bearer":
		if token, ok := authConfig["token"].(string); ok {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	case "api_key":
		header, _ := authConfig["header"].(string)
		key, _ := authConfig["key"].(string)
		if header != "" && key != "" {
			req.Header.Set(header, key)
		}
	case "basic":
		user, _ := authConfig["username"].(string)
		pass, _ := authConfig["password"].(string)
		req.SetBasicAuth(user, pass)
	}
}

func hasCapability(caps []string, target string) bool {
	for _, c := range caps {
		if c == target {
			return true
		}
	}
	return false
}

func channelSubscribes(ch *models.NotificationChannel, eventType string) bool {
	if len(ch.Events) == 0 {
		return true // empty means "all events"
	}
	for _, e := range ch.Events {
		if e == string(eventType) || e == "*" {
			return true
		}
	}
	return false
}

// ── Webhook Channel Driver (OSS built-in) ────────────────────

// WebhookChannelDriver sends notifications via HTTP POST to a webhook URL
// with optional HMAC-SHA256 signing. This is the default OSS driver.
type WebhookChannelDriver struct {
	client *http.Client
}

// Kind returns ChannelWebhook.
func (d *WebhookChannelDriver) Kind() models.ChannelKind {
	return models.ChannelWebhook
}

// Send posts the event as JSON to the channel's URL with optional HMAC signing.
func (d *WebhookChannelDriver) Send(ctx context.Context, channel *models.NotificationChannel, event Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, channel.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "AgentOven-Webhook/1.0")
	req.Header.Set("X-AgentOven-Event", string(event.Type))
	req.Header.Set("X-AgentOven-Kitchen", event.Kitchen)

	// HMAC-SHA256 signing if secret is configured
	if channel.Secret != "" {
		mac := hmac.New(sha256.New, []byte(channel.Secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-AgentOven-Signature", "sha256="+sig)
	}

	// Apply any kind-specific auth from Config
	if channel.Config != nil {
		applyAuth(req, channel.Config)
	}

	// Send with retries
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
		resp, err := d.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("webhook HTTP %d from %s", resp.StatusCode, channel.URL)
	}
	return fmt.Errorf("webhook failed after 3 attempts: %w", lastErr)
}
