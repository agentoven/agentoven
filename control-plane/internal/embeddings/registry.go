// Package embeddings provides embedding driver registry and OSS drivers.
// OSS ships: OpenAI (text-embedding-3-small/large), Ollama (nomic-embed-text).
// Pro adds: Azure OpenAI Embeddings, Bedrock Titan, Vertex textembedding-gecko.
package embeddings

import (
	"context"
	"fmt"
	"sync"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/rs/zerolog/log"
)

// Registry holds named embedding drivers. Thread-safe.
type Registry struct {
	mu      sync.RWMutex
	drivers map[string]contracts.EmbeddingDriver
}

// NewRegistry creates an empty embedding registry.
func NewRegistry() *Registry {
	return &Registry{
		drivers: make(map[string]contracts.EmbeddingDriver),
	}
}

// Register adds a driver under the given name. Overwrites if exists.
func (r *Registry) Register(name string, driver contracts.EmbeddingDriver) {
	r.mu.Lock()
	r.drivers[name] = driver
	r.mu.Unlock()
	log.Info().Str("name", name).Str("kind", driver.Kind()).Int("dims", driver.Dimensions()).Msg("Embedding driver registered")
}

// Get returns the driver by name, or error if not found.
func (r *Registry) Get(name string) (contracts.EmbeddingDriver, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.drivers[name]
	if !ok {
		return nil, fmt.Errorf("embedding driver not found: %s", name)
	}
	return d, nil
}

// List returns all registered driver names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.drivers))
	for name := range r.drivers {
		names = append(names, name)
	}
	return names
}

// HealthCheckAll pings every registered driver and returns errors keyed by name.
func (r *Registry) HealthCheckAll(ctx context.Context) map[string]error {
	r.mu.RLock()
	snapshot := make(map[string]contracts.EmbeddingDriver, len(r.drivers))
	for k, v := range r.drivers {
		snapshot[k] = v
	}
	r.mu.RUnlock()

	results := make(map[string]error, len(snapshot))
	for name, driver := range snapshot {
		results[name] = driver.HealthCheck(ctx)
	}
	return results
}
