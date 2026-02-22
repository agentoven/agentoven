package vectorstore

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// DefaultMaxVectors is the default cap for the embedded store (50K).
// Exceeding this triggers a warning nudging users to upgrade.
const DefaultMaxVectors = 50_000

// EmbeddedStore is a lightweight in-memory vector store using brute-force
// cosine similarity search. Suitable for development and small workloads
// (≤50K vectors). For production, use pgvector or a managed vector DB.
type EmbeddedStore struct {
	mu         sync.RWMutex
	docs       map[string]*models.VectorDoc // key: kitchen:id
	maxVectors int
}

// EmbeddedOption configures the embedded store.
type EmbeddedOption func(*EmbeddedStore)

// WithMaxVectors sets the maximum number of vectors (default 50K).
func WithMaxVectors(max int) EmbeddedOption {
	return func(s *EmbeddedStore) { s.maxVectors = max }
}

// NewEmbeddedStore creates an in-memory vector store.
func NewEmbeddedStore(opts ...EmbeddedOption) *EmbeddedStore {
	s := &EmbeddedStore{
		docs:       make(map[string]*models.VectorDoc),
		maxVectors: DefaultMaxVectors,
	}
	for _, opt := range opts {
		opt(s)
	}
	log.Info().Int("max_vectors", s.maxVectors).Msg("Embedded vector store initialized")
	return s
}

func (s *EmbeddedStore) Kind() string { return "embedded" }

func (s *EmbeddedStore) Upsert(_ context.Context, kitchen string, docs []models.VectorDoc) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check capacity
	newCount := 0
	for _, d := range docs {
		k := key(kitchen, d.ID)
		if _, exists := s.docs[k]; !exists {
			newCount++
		}
	}
	total := len(s.docs) + newCount
	if total > s.maxVectors {
		return fmt.Errorf("embedded vector store capacity exceeded: %d > %d (consider upgrading to pgvector or a managed vector DB)", total, s.maxVectors)
	}
	if total > int(float64(s.maxVectors)*0.9) {
		log.Warn().Int("count", total).Int("max", s.maxVectors).Msg("Embedded vector store nearing capacity — consider pgvector or managed vector DB")
	}

	now := time.Now()
	for _, d := range docs {
		cp := d
		cp.Kitchen = kitchen
		if cp.ID == "" {
			cp.ID = uuid.NewString()
		}
		if cp.CreatedAt.IsZero() {
			cp.CreatedAt = now
		}
		s.docs[key(kitchen, cp.ID)] = &cp
	}
	return nil
}

func (s *EmbeddedStore) Search(_ context.Context, kitchen string, vector []float64, topK int, filter map[string]string) ([]models.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type scored struct {
		doc   *models.VectorDoc
		score float64
	}
	var candidates []scored

	ns := filter["namespace"]
	for _, d := range s.docs {
		if d.Kitchen != kitchen {
			continue
		}
		if ns != "" && d.Namespace != ns {
			continue
		}
		if len(d.Vector) != len(vector) {
			continue
		}
		// Apply metadata filters
		match := true
		for fk, fv := range filter {
			if fk == "namespace" {
				continue
			}
			if d.Metadata[fk] != fv {
				match = false
				break
			}
		}
		if !match {
			continue
		}

		score := cosineSimilarity(vector, d.Vector)
		candidates = append(candidates, scored{doc: d, score: score})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	if topK > len(candidates) {
		topK = len(candidates)
	}

	results := make([]models.SearchResult, topK)
	for i := 0; i < topK; i++ {
		cp := *candidates[i].doc
		results[i] = models.SearchResult{Doc: cp, Score: candidates[i].score}
	}
	return results, nil
}

func (s *EmbeddedStore) Delete(_ context.Context, kitchen string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.docs, key(kitchen, id))
	}
	return nil
}

func (s *EmbeddedStore) Count(_ context.Context, kitchen string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, d := range s.docs {
		if d.Kitchen == kitchen {
			count++
		}
	}
	return count, nil
}

func (s *EmbeddedStore) HealthCheck(_ context.Context) error {
	return nil // always healthy — it's in-memory
}

// ── Helpers ─────────────────────────────────────────────────

func key(kitchen, id string) string {
	return kitchen + ":" + id
}

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
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
