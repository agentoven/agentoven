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
	Agents     map[string]*models.Agent        `json:"agents"`
	Recipes    map[string]*models.Recipe        `json:"recipes"`
	Kitchens   map[string]*models.Kitchen       `json:"kitchens"`
	Traces     map[string]*models.Trace         `json:"traces"`
	Providers  map[string]*models.ModelProvider  `json:"providers"`
	RecipeRuns map[string]*models.RecipeRun      `json:"recipe_runs"`
	Tools      map[string]*models.MCPTool        `json:"tools"`
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

	// Persistence
	snapshotPath string        // empty = no persistence
	saveMu       sync.Mutex    // guards file writes
	saveCh       chan struct{} // debounce channel
}

// NewMemoryStore creates a new in-memory store.
// If AGENTOVEN_DATA_DIR is set, data is persisted to a JSON file in that directory.
// Otherwise defaults to ~/.agentoven/data.json.
func NewMemoryStore() *MemoryStore {
	m := &MemoryStore{
		agents:     make(map[string]*models.Agent),
		recipes:    make(map[string]*models.Recipe),
		kitchens:   make(map[string]*models.Kitchen),
		traces:     make(map[string]*models.Trace),
		providers:  make(map[string]*models.ModelProvider),
		recipeRuns: make(map[string]*models.RecipeRun),
		tools:      make(map[string]*models.MCPTool),
		saveCh:     make(chan struct{}, 1),
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
	for range m.saveCh {
		time.Sleep(500 * time.Millisecond) // debounce
		m.saveSnapshot()
	}
}

// saveSnapshot persists all data to disk as JSON.
func (m *MemoryStore) saveSnapshot() {
	m.mu.RLock()
	snap := snapshot{
		Agents:     m.agents,
		Recipes:    m.recipes,
		Kitchens:   m.kitchens,
		Traces:     m.traces,
		Providers:  m.providers,
		RecipeRuns: m.recipeRuns,
		Tools:      m.tools,
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

	total := len(m.agents) + len(m.recipes) + len(m.kitchens) + len(m.providers) + len(m.tools)
	log.Info().
		Int("agents", len(m.agents)).
		Int("recipes", len(m.recipes)).
		Int("providers", len(m.providers)).
		Int("tools", len(m.tools)).
		Int("total", total).
		Str("path", m.snapshotPath).
		Msg("Snapshot loaded")
}

func (m *MemoryStore) Ping(_ context.Context) error  { return nil }
func (m *MemoryStore) Close() error                    { return nil }
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
	m.agents[key(agent.Kitchen, agent.Name)] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) UpdateAgent(_ context.Context, agent *models.Agent) error {
	m.mu.Lock()
	copy := *agent
	m.agents[key(agent.Kitchen, agent.Name)] = &copy
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) DeleteAgent(_ context.Context, kitchen, name string) error {
	m.mu.Lock()
	delete(m.agents, key(kitchen, name))
	m.mu.Unlock()
	m.requestSave()
	return nil
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

// Compile-time check that MemoryStore implements Store.
var _ Store = (*MemoryStore)(nil)
