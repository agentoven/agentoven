// Package store — in-memory Store implementation.
// Used as a fallback when PostgreSQL is not available (local dev, tests).
package store

import (
	"context"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

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
}

// NewMemoryStore creates a new in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		agents:     make(map[string]*models.Agent),
		recipes:    make(map[string]*models.Recipe),
		kitchens:   make(map[string]*models.Kitchen),
		traces:     make(map[string]*models.Trace),
		providers:  make(map[string]*models.ModelProvider),
		recipeRuns: make(map[string]*models.RecipeRun),
		tools:      make(map[string]*models.MCPTool),
	}
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
	defer m.mu.Unlock()
	copy := *agent
	m.agents[key(agent.Kitchen, agent.Name)] = &copy
	return nil
}

func (m *MemoryStore) UpdateAgent(_ context.Context, agent *models.Agent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := *agent
	m.agents[key(agent.Kitchen, agent.Name)] = &copy
	return nil
}

func (m *MemoryStore) DeleteAgent(_ context.Context, kitchen, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.agents, key(kitchen, name))
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
	defer m.mu.Unlock()
	copy := *recipe
	m.recipes[key(recipe.Kitchen, recipe.Name)] = &copy
	return nil
}

func (m *MemoryStore) UpdateRecipe(_ context.Context, recipe *models.Recipe) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := *recipe
	m.recipes[key(recipe.Kitchen, recipe.Name)] = &copy
	return nil
}

func (m *MemoryStore) DeleteRecipe(_ context.Context, kitchen, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.recipes, key(kitchen, name))
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
	defer m.mu.Unlock()
	copy := *kitchen
	m.kitchens[kitchen.ID] = &copy
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
	defer m.mu.Unlock()
	copy := *trace
	m.traces[trace.ID] = &copy
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
	defer m.mu.Unlock()
	copy := *provider
	m.providers[provider.Name] = &copy
	return nil
}

func (m *MemoryStore) UpdateProvider(_ context.Context, provider *models.ModelProvider) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := *provider
	m.providers[provider.Name] = &copy
	return nil
}

func (m *MemoryStore) DeleteProvider(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.providers, name)
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
	defer m.mu.Unlock()
	copy := *run
	m.recipeRuns[run.ID] = &copy
	return nil
}

func (m *MemoryStore) UpdateRecipeRun(_ context.Context, run *models.RecipeRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := *run
	m.recipeRuns[run.ID] = &copy
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
	defer m.mu.Unlock()
	copy := *tool
	m.tools[key(tool.Kitchen, tool.Name)] = &copy
	return nil
}

func (m *MemoryStore) UpdateTool(_ context.Context, tool *models.MCPTool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := *tool
	m.tools[key(tool.Kitchen, tool.Name)] = &copy
	return nil
}

func (m *MemoryStore) DeleteTool(_ context.Context, kitchen, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tools, key(kitchen, name))
	return nil
}

// Compile-time check that MemoryStore implements Store.
var _ Store = (*MemoryStore)(nil)

// Suppress unused import warnings.
var _ = time.Now
