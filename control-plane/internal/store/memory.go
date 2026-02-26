// Package store — in-memory Store implementation.
// Used as a fallback when PostgreSQL is not available (local dev, tests).
// Supports file-based snapshot persistence so data survives restarts.
package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// snapshot is the JSON-serializable shape written to disk.
type snapshot struct {
	Agents        map[string]*models.Agent               `json:"agents"`
	Recipes       map[string]*models.Recipe               `json:"recipes"`
	Kitchens      map[string]*models.Kitchen              `json:"kitchens"`
	Traces        map[string]*models.Trace                `json:"traces"`
	Providers     map[string]*models.ModelProvider         `json:"providers"`
	RecipeRuns    map[string]*models.RecipeRun             `json:"recipe_runs"`
	Tools         map[string]*models.MCPTool               `json:"tools"`
	Prompts       map[string][]*models.Prompt              `json:"prompts"`           // key: kitchen:name → version history
	Settings      map[string]*models.KitchenSettings        `json:"settings"`         // key: kitchen_id
	AgentVersions map[string][]*models.Agent               `json:"agent_versions"`   // key: kitchen:name → version history
	AuditEvents   []*models.AuditEvent                     `json:"audit_events"`
	Approvals     map[string]*models.ApprovalRecord         `json:"approvals"`        // key: gate_key
	Channels      map[string]*models.NotificationChannel    `json:"channels"`         // key: kitchen:name
	VectorDocs    map[string]*models.VectorDoc              `json:"vector_docs"`      // key: kitchen:id
	Connectors    map[string]*models.DataConnectorConfig    `json:"connectors"`       // key: kitchen:id
}

// MemoryStore implements Store with in-memory maps.
type MemoryStore struct {
	mu         sync.RWMutex
	agents     map[string]*models.Agent         // key: kitchen:name
	recipes    map[string]*models.Recipe         // key: kitchen:name
	kitchens   map[string]*models.Kitchen        // key: id
	traces     map[string]*models.Trace          // key: id
	providers  map[string]*models.ModelProvider   // key: name
	recipeRuns map[string]*models.RecipeRun       // key: id
	tools      map[string]*models.MCPTool         // key: kitchen:name
	prompts    map[string][]*models.Prompt        // key: kitchen:name → version history (newest last)
	settings   map[string]*models.KitchenSettings  // key: kitchen_id
	auditEvents  []*models.AuditEvent               // append-only log
	approvals    map[string]*models.ApprovalRecord   // key: gate_key
	channels     map[string]*models.NotificationChannel // key: kitchen:name
	vectorDocs   map[string]*models.VectorDoc        // key: kitchen:id
	connectors   map[string]*models.DataConnectorConfig // key: kitchen:id

	// Agent version history — append-only, keyed by kitchen:name
	agentVersions map[string][]*models.Agent // key: kitchen:name → version history

	// Persistence
	snapshotPath string        // empty = no persistence
	saveMu       sync.Mutex    // guards file writes
	saveCh       chan struct{} // debounce channel
	doneCh       chan struct{} // signals background goroutines to stop

	// Trace TTL — traces older than this are evicted automatically.
	// Defaults to 7 days (community plan). Set via AGENTOVEN_TRACE_TTL env var (Go duration string).
	traceTTL time.Duration
}

// NewMemoryStore creates a new in-memory store.
// If AGENTOVEN_DATA_DIR is set, data is persisted to a JSON file in that directory.
// Otherwise defaults to ~/.agentoven/data.json.
func NewMemoryStore() *MemoryStore {
	// Parse trace TTL from env (default 7 days — matches community plan)
	traceTTL := 7 * 24 * time.Hour
	if ttlStr := os.Getenv("AGENTOVEN_TRACE_TTL"); ttlStr != "" {
		if parsed, err := time.ParseDuration(ttlStr); err == nil {
			traceTTL = parsed
		} else {
			log.Warn().Str("value", ttlStr).Msg("Invalid AGENTOVEN_TRACE_TTL, using default 7d")
		}
	}

	m := &MemoryStore{
		agents:        make(map[string]*models.Agent),
		recipes:       make(map[string]*models.Recipe),
		kitchens:      make(map[string]*models.Kitchen),
		traces:        make(map[string]*models.Trace),
		providers:     make(map[string]*models.ModelProvider),
		recipeRuns:    make(map[string]*models.RecipeRun),
		tools:         make(map[string]*models.MCPTool),
		prompts:       make(map[string][]*models.Prompt),
		settings:      make(map[string]*models.KitchenSettings),
		agentVersions: make(map[string][]*models.Agent),
		auditEvents:   make([]*models.AuditEvent, 0),
		approvals:     make(map[string]*models.ApprovalRecord),
		channels:      make(map[string]*models.NotificationChannel),
		vectorDocs:    make(map[string]*models.VectorDoc),
		connectors:    make(map[string]*models.DataConnectorConfig),
		saveCh:        make(chan struct{}, 1),
		doneCh:        make(chan struct{}),
		traceTTL:      traceTTL,
	}

	// Determine snapshot path
	dataDir := os.Getenv("AGENTOVEN_DATA_DIR")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			dataDir = filepath.Join(home, ".agentoven")
		}
	}
	if dataDir != "" {
		m.snapshotPath = filepath.Join(dataDir, "data.json")
		// Ensure directory exists
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			log.Warn().Err(err).Str("dir", dataDir).Msg("Cannot create data dir, persistence disabled")
			m.snapshotPath = ""
		}
	}

	// Load existing data from disk
	if m.snapshotPath != "" {
		m.loadSnapshot()
	}

	// Start background save goroutine (debounced)
	if m.snapshotPath != "" {
		go m.saveLoop()
	}

	// Start trace TTL eviction goroutine (runs every 10 minutes)
	go m.traceEvictionLoop()

	log.Info().
		Str("trace_ttl", traceTTL.String()).
		Str("snapshot", m.snapshotPath).
		Msg("Memory store configured")

	return m
}

// requestSave signals the background goroutine to persist data.
// Non-blocking: coalesces multiple rapid writes into one disk flush.
func (m *MemoryStore) requestSave() {
	if m.snapshotPath == "" {
		return
	}
	select {
	case m.saveCh <- struct{}{}:
	default:
		// Already pending
	}
}

// saveLoop runs in a goroutine, debouncing save requests (max 1 write per 500ms).
func (m *MemoryStore) saveLoop() {
	for {
		select {
		case <-m.doneCh:
			return
		case <-m.saveCh:
			time.Sleep(500 * time.Millisecond) // debounce
			m.saveSnapshot()
		}
	}
}

// traceEvictionLoop periodically removes traces older than traceTTL.
func (m *MemoryStore) traceEvictionLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.doneCh:
			return
		case <-ticker.C:
			m.evictExpiredTraces()
		}
	}
}

// evictExpiredTraces removes traces older than the configured TTL.
func (m *MemoryStore) evictExpiredTraces() {
	cutoff := time.Now().Add(-m.traceTTL)

	m.mu.Lock()
	var evicted int
	for id, t := range m.traces {
		if t.CreatedAt.Before(cutoff) {
			delete(m.traces, id)
			evicted++
		}
	}
	m.mu.Unlock()

	if evicted > 0 {
		log.Info().Int("evicted", evicted).Str("ttl", m.traceTTL.String()).Msg("Evicted expired traces")
		m.requestSave()
	}
}

// saveSnapshot persists all data to disk as JSON.
func (m *MemoryStore) saveSnapshot() {
	m.mu.RLock()
	snap := snapshot{
		Agents:        m.agents,
		Recipes:       m.recipes,
		Kitchens:      m.kitchens,
		Traces:        m.traces,
		Providers:     m.providers,
		RecipeRuns:    m.recipeRuns,
		Tools:         m.tools,
		Prompts:       m.prompts,
		Settings:      m.settings,
		AgentVersions: m.agentVersions,
		AuditEvents:   m.auditEvents,
		Approvals:     m.approvals,
		Channels:      m.channels,
		VectorDocs:    m.vectorDocs,
		Connectors:    m.connectors,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	m.mu.RUnlock()

	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal snapshot")
		return
	}

	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	// Write to temp file then rename for atomicity
	tmp := m.snapshotPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Error().Err(err).Str("path", tmp).Msg("Failed to write snapshot tmp")
		return
	}
	if err := os.Rename(tmp, m.snapshotPath); err != nil {
		log.Error().Err(err).Str("path", m.snapshotPath).Msg("Failed to rename snapshot")
		return
	}

	log.Debug().Str("path", m.snapshotPath).Msg("Snapshot saved")
}

// loadSnapshot reads data from disk on startup.
func (m *MemoryStore) loadSnapshot() {
	data, err := os.ReadFile(m.snapshotPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Info().Str("path", m.snapshotPath).Msg("No snapshot file found, starting fresh")
			return
		}
		log.Warn().Err(err).Str("path", m.snapshotPath).Msg("Failed to read snapshot")
		return
	}

	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		log.Error().Err(err).Str("path", m.snapshotPath).Msg("Failed to parse snapshot, starting fresh")
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if snap.Agents != nil {
		m.agents = snap.Agents
	}
	if snap.Recipes != nil {
		m.recipes = snap.Recipes
	}
	if snap.Kitchens != nil {
		m.kitchens = snap.Kitchens
	}
	if snap.Traces != nil {
		m.traces = snap.Traces
	}
	if snap.Providers != nil {
		m.providers = snap.Providers
	}
	if snap.RecipeRuns != nil {
		m.recipeRuns = snap.RecipeRuns
	}
	if snap.Tools != nil {
		m.tools = snap.Tools
	}
	if snap.Prompts != nil {
		m.prompts = snap.Prompts
	}
	if snap.Settings != nil {
		m.settings = snap.Settings
	}
	if snap.AgentVersions != nil {
		m.agentVersions = snap.AgentVersions
	}
	if snap.AuditEvents != nil {
		m.auditEvents = snap.AuditEvents
	}
	if snap.Approvals != nil {
		m.approvals = snap.Approvals
	}
	if snap.Channels != nil {
		m.channels = snap.Channels
	}
	if snap.VectorDocs != nil {
		m.vectorDocs = snap.VectorDocs
	}
	if snap.Connectors != nil {
		m.connectors = snap.Connectors
	}

	total := len(m.agents) + len(m.recipes) + len(m.kitchens) + len(m.providers) + len(m.tools) + len(m.prompts)
	log.Info().
		Int("agents", len(m.agents)).
		Int("recipes", len(m.recipes)).
		Int("providers", len(m.providers)).
		Int("tools", len(m.tools)).
		Int("total", total).
		Str("path", m.snapshotPath).
		Msg("Snapshot loaded")

	// Migrate legacy integer versions ("1","2") → semver ("0.1.0","0.2.0")
	migrated := 0
	for _, agent := range m.agents {
		if agent.Version != "" && !models.IsSemver(agent.Version) {
			agent.Version = models.MigrateLegacyVersion(agent.Version)
			migrated++
		}
	}
	for _, versions := range m.agentVersions {
		for _, agent := range versions {
			if agent.Version != "" && !models.IsSemver(agent.Version) {
				agent.Version = models.MigrateLegacyVersion(agent.Version)
			}
		}
	}
	if migrated > 0 {
		log.Info().Int("agents", migrated).Msg("Migrated legacy integer versions to semver")
	}
}

func (m *MemoryStore) Ping(_ context.Context) error { return nil }

// Close stops background goroutines and forces a final snapshot write.
// Safe to call multiple times (second call is a no-op).
func (m *MemoryStore) Close() error {
	// Signal all background goroutines to stop
	select {
	case <-m.doneCh:
		// Already closed
		return nil
	default:
		close(m.doneCh)
	}

	// Force a final snapshot write so no in-flight data is lost
	if m.snapshotPath != "" {
		log.Info().Msg("Flushing final snapshot before shutdown...")
		m.saveSnapshot()
	}

	log.Info().Msg("Memory store closed")
	return nil
}

func (m *MemoryStore) Migrate(_ context.Context) error { return nil }

func key(parts ...string) string {
	k := ""
	for i, p := range parts {
		if i > 0 {
			k += ":"
		}
		k += p
	}
	return k
}

// ── Agent Store ─────────────────────────────────────────────

func (m *MemoryStore) ListAgents(_ context.Context, kitchen string) ([]models.Agent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.Agent
	for _, a := range m.agents {
		if a.Kitchen == kitchen || kitchen == "" {
			result = append(result, *a)
		}
	}
	return result, nil
}

func (m *MemoryStore) GetAgent(_ context.Context, kitchen, name string) (*models.Agent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.agents[key(kitchen, name)]
	if !ok {
		return nil, &ErrNotFound{Entity: "agent", Key: name}
	}
	copy := *a
	return &copy, nil
}

func (m *MemoryStore) CreateAgent(_ context.Context, agent *models.Agent) error {
	m.mu.Lock()
	copy := *agent
	if copy.Version == "" {
		copy.Version = models.DefaultAgentVersion
	} else {
		// Migrate legacy integer versions ("1","2") to semver
		copy.Version = models.MigrateLegacyVersion(copy.Version)
	}
	m.agents[key(agent.Kitchen, agent.Name)] = &copy

	// Track version history
	vCopy := copy // separate copy for version history
	vk := key(agent.Kitchen, agent.Name)
	m.agentVersions[vk] = []*models.Agent{&vCopy}
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) UpdateAgent(_ context.Context, agent *models.Agent) error {
	m.mu.Lock()
	copy := *agent

	// Migrate legacy integer versions to semver
	if copy.Version != "" && !models.IsSemver(copy.Version) {
		copy.Version = models.MigrateLegacyVersion(copy.Version)
	}

	// Only bump version and record history when the caller explicitly
	// requests it via VersionBump. Status-only updates (bake background
	// goroutine setting ready/burnt) do NOT create phantom version entries.
	vk := key(agent.Kitchen, agent.Name)
	recordHistory := false
	switch copy.VersionBump {
	case "patch":
		copy.Version = models.BumpPatch(copy.Version)
		recordHistory = true
	case "minor":
		copy.Version = models.BumpMinor(copy.Version)
		recordHistory = true
	case "major":
		copy.Version = models.BumpMajor(copy.Version)
		recordHistory = true
	}
	copy.VersionBump = "" // never persist the signal
	copy.UpdatedAt = time.Now().UTC()

	m.agents[key(agent.Kitchen, agent.Name)] = &copy

	// Append to version history only for meaningful changes
	if recordHistory {
		vCopy := copy
		m.agentVersions[vk] = append(m.agentVersions[vk], &vCopy)
	} else {
		// Update the latest version entry in-place (status/tags changes)
		versions := m.agentVersions[vk]
		if len(versions) > 0 {
			latest := *&copy
			*versions[len(versions)-1] = latest
		}
	}

	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) DeleteAgent(_ context.Context, kitchen, name string) error {
	m.mu.Lock()
	k := key(kitchen, name)
	if _, ok := m.agents[k]; !ok {
		m.mu.Unlock()
		return &ErrNotFound{Entity: "agent", Key: name}
	}
	delete(m.agents, k)
	delete(m.agentVersions, k)
	m.mu.Unlock()
	m.requestSave()
	return nil
}

// ListAgentVersions returns all historical versions of an agent (oldest first).
func (m *MemoryStore) ListAgentVersions(_ context.Context, kitchen, name string) ([]models.Agent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	versions, ok := m.agentVersions[key(kitchen, name)]
	if !ok || len(versions) == 0 {
		return []models.Agent{}, nil
	}
	result := make([]models.Agent, len(versions))
	for i, a := range versions {
		result[i] = *a
	}
	return result, nil
}

// GetAgentVersion returns a specific version of an agent.
func (m *MemoryStore) GetAgentVersion(_ context.Context, kitchen, name, version string) (*models.Agent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	versions, ok := m.agentVersions[key(kitchen, name)]
	if !ok || len(versions) == 0 {
		return nil, &ErrNotFound{Entity: "agent", Key: name}
	}
	for _, a := range versions {
		if a.Version == version {
			copy := *a
			return &copy, nil
		}
	}
	return nil, &ErrNotFound{Entity: "agent version", Key: name + ":" + version}
}

// ── Recipe Store ────────────────────────────────────────────

func (m *MemoryStore) ListRecipes(_ context.Context, kitchen string) ([]models.Recipe, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.Recipe
	for _, r := range m.recipes {
		if r.Kitchen == kitchen || kitchen == "" {
			result = append(result, *r)
		}
	}
	return result, nil
}

func (m *MemoryStore) GetRecipe(_ context.Context, kitchen, name string) (*models.Recipe, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.recipes[key(kitchen, name)]
	if !ok {
		return nil, &ErrNotFound{Entity: "recipe", Key: name}
	}
	copy := *r
	return &copy, nil
}

func (m *MemoryStore) CreateRecipe(_ context.Context, recipe *models.Recipe) error {
	m.mu.Lock()
	copy := *recipe
	m.recipes[key(recipe.Kitchen, recipe.Name)] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) UpdateRecipe(_ context.Context, recipe *models.Recipe) error {
	m.mu.Lock()
	copy := *recipe
	m.recipes[key(recipe.Kitchen, recipe.Name)] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) DeleteRecipe(_ context.Context, kitchen, name string) error {
	m.mu.Lock()
	delete(m.recipes, key(kitchen, name))
	m.mu.Unlock()
	m.requestSave()
	return nil
}

// ── Kitchen Store ───────────────────────────────────────────

func (m *MemoryStore) ListKitchens(_ context.Context) ([]models.Kitchen, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.Kitchen
	for _, k := range m.kitchens {
		result = append(result, *k)
	}
	return result, nil
}

func (m *MemoryStore) GetKitchen(_ context.Context, id string) (*models.Kitchen, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.kitchens[id]
	if !ok {
		return nil, &ErrNotFound{Entity: "kitchen", Key: id}
	}
	copy := *k
	return &copy, nil
}

func (m *MemoryStore) CreateKitchen(_ context.Context, kitchen *models.Kitchen) error {
	m.mu.Lock()
	copy := *kitchen
	m.kitchens[kitchen.ID] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

// ── Trace Store ─────────────────────────────────────────────

func (m *MemoryStore) ListTraces(_ context.Context, kitchen string, limit int) ([]models.Trace, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.Trace
	for _, t := range m.traces {
		if t.Kitchen == kitchen || kitchen == "" {
			result = append(result, *t)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *MemoryStore) GetTrace(_ context.Context, id string) (*models.Trace, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.traces[id]
	if !ok {
		return nil, &ErrNotFound{Entity: "trace", Key: id}
	}
	copy := *t
	return &copy, nil
}

func (m *MemoryStore) CreateTrace(_ context.Context, trace *models.Trace) error {
	m.mu.Lock()
	copy := *trace
	m.traces[trace.ID] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) DeleteTrace(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.traces[id]; !ok {
		return &ErrNotFound{Entity: "trace", Key: id}
	}
	delete(m.traces, id)
	m.requestSave()
	return nil
}

// ── Model Provider Store ────────────────────────────────────

func (m *MemoryStore) ListProviders(_ context.Context) ([]models.ModelProvider, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.ModelProvider
	for _, p := range m.providers {
		result = append(result, *p)
	}
	return result, nil
}

func (m *MemoryStore) GetProvider(_ context.Context, name string) (*models.ModelProvider, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.providers[name]
	if !ok {
		return nil, &ErrNotFound{Entity: "provider", Key: name}
	}
	copy := *p
	return &copy, nil
}

func (m *MemoryStore) CreateProvider(_ context.Context, provider *models.ModelProvider) error {
	m.mu.Lock()
	copy := *provider
	m.providers[provider.Name] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) UpdateProvider(_ context.Context, provider *models.ModelProvider) error {
	m.mu.Lock()
	copy := *provider
	m.providers[provider.Name] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) DeleteProvider(_ context.Context, name string) error {
	m.mu.Lock()
	delete(m.providers, name)
	m.mu.Unlock()
	m.requestSave()
	return nil
}

// ── Recipe Run Store ────────────────────────────────────────

func (m *MemoryStore) ListRecipeRuns(_ context.Context, recipeID string, limit int) ([]models.RecipeRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.RecipeRun
	for _, r := range m.recipeRuns {
		if r.RecipeID == recipeID {
			result = append(result, *r)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *MemoryStore) GetRecipeRun(_ context.Context, id string) (*models.RecipeRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.recipeRuns[id]
	if !ok {
		return nil, &ErrNotFound{Entity: "recipe_run", Key: id}
	}
	copy := *r
	return &copy, nil
}

func (m *MemoryStore) CreateRecipeRun(_ context.Context, run *models.RecipeRun) error {
	m.mu.Lock()
	copy := *run
	m.recipeRuns[run.ID] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) UpdateRecipeRun(_ context.Context, run *models.RecipeRun) error {
	m.mu.Lock()
	copy := *run
	m.recipeRuns[run.ID] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

// ── MCP Tool Store ──────────────────────────────────────────

func (m *MemoryStore) ListTools(_ context.Context, kitchen string) ([]models.MCPTool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.MCPTool
	for _, t := range m.tools {
		if t.Kitchen == kitchen || kitchen == "" {
			result = append(result, *t)
		}
	}
	return result, nil
}

func (m *MemoryStore) GetTool(_ context.Context, kitchen, name string) (*models.MCPTool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tools[key(kitchen, name)]
	if !ok {
		return nil, &ErrNotFound{Entity: "tool", Key: name}
	}
	copy := *t
	return &copy, nil
}

func (m *MemoryStore) CreateTool(_ context.Context, tool *models.MCPTool) error {
	m.mu.Lock()
	copy := *tool
	m.tools[key(tool.Kitchen, tool.Name)] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) UpdateTool(_ context.Context, tool *models.MCPTool) error {
	m.mu.Lock()
	copy := *tool
	m.tools[key(tool.Kitchen, tool.Name)] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) DeleteTool(_ context.Context, kitchen, name string) error {
	m.mu.Lock()
	delete(m.tools, key(kitchen, name))
	m.mu.Unlock()
	m.requestSave()
	return nil
}

// ── Prompt Store ────────────────────────────────────────────

func (m *MemoryStore) ListPrompts(_ context.Context, kitchen string) ([]models.Prompt, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.Prompt
	for _, versions := range m.prompts {
		if len(versions) == 0 {
			continue
		}
		latest := versions[len(versions)-1]
		if latest.Kitchen == kitchen || kitchen == "" {
			result = append(result, *latest)
		}
	}
	return result, nil
}

func (m *MemoryStore) GetPrompt(_ context.Context, kitchen, name string) (*models.Prompt, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	versions, ok := m.prompts[key(kitchen, name)]
	if !ok || len(versions) == 0 {
		return nil, &ErrNotFound{Entity: "prompt", Key: name}
	}
	copy := *versions[len(versions)-1]
	return &copy, nil
}

func (m *MemoryStore) GetPromptVersion(_ context.Context, kitchen, name string, version int) (*models.Prompt, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	versions, ok := m.prompts[key(kitchen, name)]
	if !ok || len(versions) == 0 {
		return nil, &ErrNotFound{Entity: "prompt", Key: name}
	}
	for _, p := range versions {
		if p.Version == version {
			copy := *p
			return &copy, nil
		}
	}
	return nil, &ErrNotFound{Entity: "prompt version", Key: name}
}

func (m *MemoryStore) ListPromptVersions(_ context.Context, kitchen, name string) ([]models.Prompt, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	versions, ok := m.prompts[key(kitchen, name)]
	if !ok || len(versions) == 0 {
		return nil, &ErrNotFound{Entity: "prompt", Key: name}
	}
	result := make([]models.Prompt, len(versions))
	for i, p := range versions {
		result[i] = *p
	}
	return result, nil
}

func (m *MemoryStore) CreatePrompt(_ context.Context, prompt *models.Prompt) error {
	m.mu.Lock()
	prompt.Version = 1
	copy := *prompt
	k := key(prompt.Kitchen, prompt.Name)
	m.prompts[k] = []*models.Prompt{&copy}
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) UpdatePrompt(_ context.Context, prompt *models.Prompt) error {
	m.mu.Lock()
	k := key(prompt.Kitchen, prompt.Name)
	versions := m.prompts[k]
	newVersion := 1
	if len(versions) > 0 {
		newVersion = versions[len(versions)-1].Version + 1
	}
	prompt.Version = newVersion
	copy := *prompt
	m.prompts[k] = append(versions, &copy)
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) DeletePrompt(_ context.Context, kitchen, name string) error {
	m.mu.Lock()
	delete(m.prompts, key(kitchen, name))
	m.mu.Unlock()
	m.requestSave()
	return nil
}

// ── Kitchen Settings ────────────────────────────────────────

func (m *MemoryStore) GetKitchenSettings(_ context.Context, kitchenID string) (*models.KitchenSettings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.settings[kitchenID]
	if !ok {
		// Return default settings if none exist
		return &models.KitchenSettings{
			KitchenID:    kitchenID,
			AutoValidate: false,
		}, nil
	}
	copy := *s
	return &copy, nil
}

func (m *MemoryStore) UpsertKitchenSettings(_ context.Context, settings *models.KitchenSettings) error {
	m.mu.Lock()
	copy := *settings
	m.settings[settings.KitchenID] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

// ── Audit Store ─────────────────────────────────────────────

func (m *MemoryStore) CreateAuditEvent(_ context.Context, event *models.AuditEvent) error {
	m.mu.Lock()
	copy := *event
	m.auditEvents = append(m.auditEvents, &copy)
	m.mu.Unlock()
	// Audit events are not persisted in OSS memory store (community: no audit)
	return nil
}

func (m *MemoryStore) ListAuditEvents(_ context.Context, filter models.AuditFilter) ([]models.AuditEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.AuditEvent
	for i := len(m.auditEvents) - 1; i >= 0; i-- { // newest first
		e := m.auditEvents[i]
		if filter.Kitchen != "" && e.Kitchen != filter.Kitchen {
			continue
		}
		if filter.UserID != "" && e.UserID != filter.UserID {
			continue
		}
		if filter.Action != "" && e.Action != filter.Action {
			continue
		}
		if filter.Resource != "" && e.Resource != filter.Resource {
			continue
		}
		if filter.Since != nil && e.Timestamp.Before(*filter.Since) {
			continue
		}
		if filter.Until != nil && e.Timestamp.After(*filter.Until) {
			continue
		}
		if filter.Offset > 0 {
			filter.Offset--
			continue
		}
		result = append(result, *e)
		if filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}
	return result, nil
}

func (m *MemoryStore) CountAuditEvents(_ context.Context, filter models.AuditFilter) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var count int64
	for _, e := range m.auditEvents {
		if filter.Kitchen != "" && e.Kitchen != filter.Kitchen {
			continue
		}
		if filter.Since != nil && e.Timestamp.Before(*filter.Since) {
			continue
		}
		if filter.Until != nil && e.Timestamp.After(*filter.Until) {
			continue
		}
		count++
	}
	return count, nil
}

func (m *MemoryStore) DeleteAuditEvent(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.auditEvents {
		if e.ID == id {
			m.auditEvents = append(m.auditEvents[:i], m.auditEvents[i+1:]...)
			return nil
		}
	}
	return &ErrNotFound{Entity: "audit_event", Key: id}
}

// ── Approval Store ──────────────────────────────────────────

func (m *MemoryStore) CreateApproval(_ context.Context, record *models.ApprovalRecord) error {
	m.mu.Lock()
	copy := *record
	m.approvals[record.GateKey] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) GetApproval(_ context.Context, gateKey string) (*models.ApprovalRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.approvals[gateKey]
	if !ok {
		return nil, &ErrNotFound{Entity: "approval", Key: gateKey}
	}
	copy := *r
	return &copy, nil
}

func (m *MemoryStore) UpdateApproval(_ context.Context, record *models.ApprovalRecord) error {
	m.mu.Lock()
	copy := *record
	m.approvals[record.GateKey] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) ListApprovals(_ context.Context, kitchen, status string, limit int) ([]models.ApprovalRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.ApprovalRecord
	for _, r := range m.approvals {
		if kitchen != "" && r.Kitchen != kitchen {
			continue
		}
		if status != "" && r.Status != status {
			continue
		}
		result = append(result, *r)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// ── Notification Channel Store ──────────────────────────────

func (m *MemoryStore) ListChannels(_ context.Context, kitchen string) ([]models.NotificationChannel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.NotificationChannel
	for _, ch := range m.channels {
		if ch.Kitchen == kitchen || kitchen == "" {
			result = append(result, *ch)
		}
	}
	return result, nil
}

func (m *MemoryStore) GetChannel(_ context.Context, kitchen, name string) (*models.NotificationChannel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.channels[key(kitchen, name)]
	if !ok {
		return nil, &ErrNotFound{Entity: "notification_channel", Key: name}
	}
	copy := *ch
	return &copy, nil
}

func (m *MemoryStore) CreateChannel(_ context.Context, channel *models.NotificationChannel) error {
	m.mu.Lock()
	copy := *channel
	m.channels[key(channel.Kitchen, channel.Name)] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) UpdateChannel(_ context.Context, channel *models.NotificationChannel) error {
	m.mu.Lock()
	copy := *channel
	m.channels[key(channel.Kitchen, channel.Name)] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) DeleteChannel(_ context.Context, kitchen, name string) error {
	m.mu.Lock()
	delete(m.channels, key(kitchen, name))
	m.mu.Unlock()
	m.requestSave()
	return nil
}

// ── Vector Doc Store ────────────────────────────────────────

func (m *MemoryStore) UpsertVectorDocs(_ context.Context, kitchen string, docs []models.VectorDoc) error {
	m.mu.Lock()
	for _, d := range docs {
		copy := d
		copy.Kitchen = kitchen
		m.vectorDocs[key(kitchen, d.ID)] = &copy
	}
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) SearchVectorDocs(_ context.Context, kitchen string, vector []float64, topK int, namespace string) ([]models.SearchResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	type scored struct {
		doc   *models.VectorDoc
		score float64
	}
	var candidates []scored
	for _, d := range m.vectorDocs {
		if d.Kitchen != kitchen {
			continue
		}
		if namespace != "" && d.Namespace != namespace {
			continue
		}
		if len(d.Vector) != len(vector) {
			continue
		}
		score := cosineSimilarity(vector, d.Vector)
		candidates = append(candidates, scored{doc: d, score: score})
	}

	// Sort descending by score
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].score > candidates[i].score {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}

	if topK > len(candidates) {
		topK = len(candidates)
	}
	results := make([]models.SearchResult, topK)
	for i := 0; i < topK; i++ {
		copy := *candidates[i].doc
		results[i] = models.SearchResult{Doc: copy, Score: candidates[i].score}
	}
	return results, nil
}

func (m *MemoryStore) DeleteVectorDocs(_ context.Context, kitchen string, ids []string) error {
	m.mu.Lock()
	for _, id := range ids {
		delete(m.vectorDocs, key(kitchen, id))
	}
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) CountVectorDocs(_ context.Context, kitchen, namespace string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var count int64
	for _, d := range m.vectorDocs {
		if d.Kitchen != kitchen {
			continue
		}
		if namespace != "" && d.Namespace != namespace {
			continue
		}
		count++
	}
	return count, nil
}

func (m *MemoryStore) ListVectorNamespaces(_ context.Context, kitchen string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[string]bool)
	for _, d := range m.vectorDocs {
		if d.Kitchen == kitchen {
			seen[d.Namespace] = true
		}
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	return out, nil
}

// cosineSimilarity computes cosine similarity between two equal-length vectors.
func cosineSimilarity(a, b []float64) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (sqrt(normA) * sqrt(normB))
}

// sqrt is a simple Newton's method square root (avoids math import for one func).
func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 20; i++ {
		z = z - (z*z-x)/(2*z)
	}
	return z
}

// ── Data Connector Store ────────────────────────────────────

func (m *MemoryStore) ListConnectors(_ context.Context, kitchen string) ([]models.DataConnectorConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []models.DataConnectorConfig
	for _, c := range m.connectors {
		if c.Kitchen == kitchen {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (m *MemoryStore) GetConnector(_ context.Context, kitchen, id string) (*models.DataConnectorConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.connectors[key(kitchen, id)]
	if !ok {
		return nil, &ErrNotFound{Entity: "connector", Key: id}
	}
	copy := *c
	return &copy, nil
}

func (m *MemoryStore) CreateConnector(_ context.Context, connector *models.DataConnectorConfig) error {
	m.mu.Lock()
	copy := *connector
	m.connectors[key(connector.Kitchen, connector.ID)] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) UpdateConnector(_ context.Context, connector *models.DataConnectorConfig) error {
	m.mu.Lock()
	copy := *connector
	m.connectors[key(connector.Kitchen, connector.ID)] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) DeleteConnector(_ context.Context, kitchen, id string) error {
	m.mu.Lock()
	delete(m.connectors, key(kitchen, id))
	m.mu.Unlock()
	m.requestSave()
	return nil
}

// Compile-time check that MemoryStore implements Store.
var _ Store = (*MemoryStore)(nil)
