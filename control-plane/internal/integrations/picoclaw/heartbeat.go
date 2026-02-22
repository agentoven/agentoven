package picoclaw

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// ── Heartbeat Monitor ────────────────────────────────────────

// HeartbeatMonitor runs a background goroutine that periodically polls
// PicoClaw instances and updates their status. This is inspired by
// PicoClaw's HEARTBEAT.md pattern (every 30min) but more configurable.
type HeartbeatMonitor struct {
	store       store.Store
	adapter     *Adapter
	interval    time.Duration
	stopCh      chan struct{}
	mu          sync.Mutex
	running     bool

	// Callbacks for integration with notifications, audit, etc.
	OnStatusChange func(agentName string, oldStatus, newStatus models.PicoClawStatus)
}

// NewHeartbeatMonitor creates a heartbeat monitor with the given interval.
func NewHeartbeatMonitor(s store.Store, adapter *Adapter, interval time.Duration) *HeartbeatMonitor {
	if interval <= 0 {
		interval = 60 * time.Second // default: check every 60s
	}
	return &HeartbeatMonitor{
		store:    s,
		adapter:  adapter,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the heartbeat polling loop.
func (m *HeartbeatMonitor) Start(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()

	log.Info().Dur("interval", m.interval).Msg("PicoClaw heartbeat monitor started")

	go m.loop(ctx)
}

// Stop gracefully shuts down the heartbeat monitor.
func (m *HeartbeatMonitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	m.running = false
	close(m.stopCh)
	log.Info().Msg("PicoClaw heartbeat monitor stopped")
}

// loop runs the periodic heartbeat check.
func (m *HeartbeatMonitor) loop(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	// Run once immediately
	m.checkAll(ctx)

	for {
		select {
		case <-ticker.C:
			m.checkAll(ctx)
		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// checkAll iterates all kitchens and checks all PicoClaw instances.
func (m *HeartbeatMonitor) checkAll(ctx context.Context) {
	// For now, check the "default" kitchen.
	// In a full implementation, we'd iterate all kitchens.
	kitchens := []string{"default"}

	for _, kitchen := range kitchens {
		agents, err := m.store.ListAgents(ctx, kitchen)
		if err != nil {
			log.Warn().Err(err).Str("kitchen", kitchen).Msg("heartbeat: failed to list agents")
			continue
		}

		for _, agent := range agents {
			if agent.Framework != "picoclaw" {
				continue
			}

			result := m.adapter.checkHealth(ctx, &agent)
			m.processResult(ctx, kitchen, &agent, result)
		}
	}
}

// processResult updates agent status based on heartbeat result and fires callbacks.
func (m *HeartbeatMonitor) processResult(ctx context.Context, kitchen string, agent *models.Agent, result models.HeartbeatResult) {
	oldStatus := inferCurrentStatus(agent)
	newStatus := result.Status

	if oldStatus != newStatus {
		log.Info().
			Str("agent", agent.Name).
			Str("old", string(oldStatus)).
			Str("new", string(newStatus)).
			Msg("PicoClaw status changed")

		// Update agent status based on heartbeat
		switch newStatus {
		case models.PicoClawStatusOnline:
			agent.Status = models.AgentStatusReady
		case models.PicoClawStatusDegraded:
			agent.Status = models.AgentStatusReady // still usable
		case models.PicoClawStatusOffline:
			agent.Status = models.AgentStatusCooled
		default:
			agent.Status = models.AgentStatusCooled
		}

		agent.UpdatedAt = time.Now().UTC()
		if err := m.store.UpdateAgent(ctx, agent); err != nil {
			log.Warn().Err(err).Str("agent", agent.Name).Msg("heartbeat: failed to update agent status")
		}

		// Fire callback if registered
		if m.OnStatusChange != nil {
			m.OnStatusChange(agent.Name, oldStatus, newStatus)
		}
	}

	// Update last-seen tag
	if result.Status == models.PicoClawStatusOnline || result.Status == models.PicoClawStatusDegraded {
		if agent.Tags == nil {
			agent.Tags = make(map[string]string)
		}
		agent.Tags["last_seen"] = time.Now().UTC().Format(time.RFC3339)
		if result.Model != "" {
			agent.Tags["model"] = result.Model
		}
		if result.MemoryMB > 0 {
			agent.Tags["memory_mb"] = fmt.Sprintf("%.1f", result.MemoryMB)
		}
		_ = m.store.UpdateAgent(ctx, agent)
	}
}

// inferCurrentStatus derives PicoClawStatus from the agent's current AgentStatus.
func inferCurrentStatus(agent *models.Agent) models.PicoClawStatus {
	switch agent.Status {
	case models.AgentStatusReady:
		return models.PicoClawStatusOnline
	case models.AgentStatusCooled:
		return models.PicoClawStatusOffline
	default:
		return models.PicoClawStatusUnknown
	}
}
