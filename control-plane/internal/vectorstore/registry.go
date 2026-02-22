// Package vectorstore provides vector store driver registry and OSS drivers.
// OSS ships: embedded (in-memory brute-force), pgvector (user-provided PG).
// Pro adds: Pinecone, Qdrant, Cosmos DB, Chroma, Snowflake Cortex, Databricks Vector Search.
package vectorstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/rs/zerolog/log"
)

// Registry holds named vector store drivers. Thread-safe.
type Registry struct {
	mu      sync.RWMutex
	drivers map[string]contracts.VectorStoreDriver
}

// NewRegistry creates an empty vector store registry.
func NewRegistry() *Registry {
	return &Registry{
		drivers: make(map[string]contracts.VectorStoreDriver),
	}
}

// Register adds a driver under the given name. Overwrites if exists.
func (r *Registry) Register(name string, driver contracts.VectorStoreDriver) {
	r.mu.Lock()
	r.drivers[name] = driver
	r.mu.Unlock()
	log.Info().Str("name", name).Str("kind", driver.Kind()).Msg("Vector store driver registered")
}

// Get returns the driver by name, or error if not found.
func (r *Registry) Get(name string) (contracts.VectorStoreDriver, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.drivers[name]
	if !ok {
		return nil, fmt.Errorf("vector store driver not found: %s", name)
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
	snapshot := make(map[string]contracts.VectorStoreDriver, len(r.drivers))
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
