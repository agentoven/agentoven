// Package rag provides the RAG (Retrieval-Augmented Generation) pipeline engine.
package rag

import (
	"context"
	"fmt"
	"sync"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/rs/zerolog/log"
)

// Registry holds named RAG service providers. Thread-safe.
// The built-in pipeline registers as "built-in". External systems
// (LlamaIndex, Haystack, etc.) register under their own names.
type Registry struct {
	mu       sync.RWMutex
	services map[string]contracts.RAGService
	order    []string // insertion order — first registered is the default
}

// NewRegistry creates an empty RAG service registry.
func NewRegistry() *Registry {
	return &Registry{
		services: make(map[string]contracts.RAGService),
	}
}

// Register adds a RAG service under the given name. Overwrites if exists.
func (r *Registry) Register(name string, svc contracts.RAGService) {
	r.mu.Lock()
	if _, exists := r.services[name]; !exists {
		r.order = append(r.order, name)
	}
	r.services[name] = svc
	r.mu.Unlock()
	log.Info().Str("name", name).Strs("strategies", ragStrategies(svc)).Msg("RAG service registered")
}

// Get returns the service by name, or error if not found.
func (r *Registry) Get(name string) (contracts.RAGService, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	svc, ok := r.services[name]
	if !ok {
		return nil, fmt.Errorf("RAG service not found: %s", name)
	}
	return svc, nil
}

// Default returns the first registered RAG service, or error if none.
func (r *Registry) Default() (contracts.RAGService, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.order) == 0 {
		return nil, fmt.Errorf("no RAG services registered")
	}
	return r.services[r.order[0]], nil
}

// List returns all registered service names in registration order.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Remove unregisters a RAG service by name. No-op if not found.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.services, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// HealthCheckAll pings every registered service and returns errors keyed by name.
func (r *Registry) HealthCheckAll(ctx context.Context) map[string]error {
	r.mu.RLock()
	snapshot := make(map[string]contracts.RAGService, len(r.services))
	for k, v := range r.services {
		snapshot[k] = v
	}
	r.mu.RUnlock()

	results := make(map[string]error, len(snapshot))
	for name, svc := range snapshot {
		results[name] = svc.HealthCheck(ctx)
	}
	return results
}

// ragStrategies extracts strategy names as strings for logging.
func ragStrategies(svc contracts.RAGService) []string {
	strategies := svc.Strategies()
	out := make([]string, len(strategies))
	for i, s := range strategies {
		out[i] = string(s)
	}
	return out
}
