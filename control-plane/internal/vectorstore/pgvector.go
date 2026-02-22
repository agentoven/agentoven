package vectorstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

// PgvectorStore implements VectorStoreDriver using PostgreSQL with the pgvector extension.
// Users must provide their own PostgreSQL instance with pgvector installed.
// Connection URL is read from AGENTOVEN_PGVECTOR_URL environment variable.
type PgvectorStore struct {
	pool       *pgxpool.Pool
	dimensions int
}

// NewPgvectorStore creates a pgvector-backed vector store.
// It creates the required table and index if they don't exist.
func NewPgvectorStore(ctx context.Context, connURL string, dimensions int) (*PgvectorStore, error) {
	pool, err := pgxpool.New(ctx, connURL)
	if err != nil {
		return nil, fmt.Errorf("pgvector connect: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgvector ping: %w", err)
	}

	s := &PgvectorStore{pool: pool, dimensions: dimensions}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgvector migrate: %w", err)
	}

	log.Info().Str("url", connURL).Int("dims", dimensions).Msg("pgvector store initialized")
	return s, nil
}

func (s *PgvectorStore) migrate(ctx context.Context) error {
	ddl := fmt.Sprintf(`
		CREATE EXTENSION IF NOT EXISTS vector;

		CREATE TABLE IF NOT EXISTS ao_vectors (
			id         TEXT NOT NULL,
			kitchen    TEXT NOT NULL,
			namespace  TEXT NOT NULL DEFAULT '',
			content    TEXT NOT NULL DEFAULT '',
			metadata   JSONB NOT NULL DEFAULT '{}',
			vector     vector(%d) NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (kitchen, id)
		);

		CREATE INDEX IF NOT EXISTS idx_ao_vectors_kitchen ON ao_vectors (kitchen);
		CREATE INDEX IF NOT EXISTS idx_ao_vectors_ns ON ao_vectors (kitchen, namespace);
	`, s.dimensions)

	_, err := s.pool.Exec(ctx, ddl)
	return err
}

func (s *PgvectorStore) Kind() string { return "pgvector" }

func (s *PgvectorStore) Upsert(ctx context.Context, kitchen string, docs []models.VectorDoc) error {
	if len(docs) == 0 {
		return nil
	}

	// Use a batch insert with ON CONFLICT
	var sb strings.Builder
	sb.WriteString(`INSERT INTO ao_vectors (id, kitchen, namespace, content, metadata, vector, created_at)
		VALUES `)

	args := make([]interface{}, 0, len(docs)*7)
	for i, d := range docs {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i*7 + 1
		sb.WriteString(fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d)", base, base+1, base+2, base+3, base+4, base+5, base+6))
		id := d.ID
		if id == "" {
			id = uuid.NewString()
		}
		now := d.CreatedAt
		if now.IsZero() {
			now = time.Now()
		}
		metadata := d.Metadata
		if metadata == nil {
			metadata = map[string]string{}
		}
		args = append(args, id, kitchen, d.Namespace, d.Content, metadata, pgvectorArray(d.Vector), now)
	}

	sb.WriteString(` ON CONFLICT (kitchen, id) DO UPDATE SET
		content = EXCLUDED.content,
		metadata = EXCLUDED.metadata,
		vector = EXCLUDED.vector,
		namespace = EXCLUDED.namespace`)

	_, err := s.pool.Exec(ctx, sb.String(), args...)
	return err
}

func (s *PgvectorStore) Search(ctx context.Context, kitchen string, vector []float64, topK int, filter map[string]string) ([]models.SearchResult, error) {
	// Build query with cosine distance operator
	query := `SELECT id, kitchen, namespace, content, metadata, created_at,
		1 - (vector <=> $1) AS score
		FROM ao_vectors
		WHERE kitchen = $2`

	args := []interface{}{pgvectorArray(vector), kitchen}
	argIdx := 3

	if ns, ok := filter["namespace"]; ok && ns != "" {
		query += fmt.Sprintf(" AND namespace = $%d", argIdx)
		args = append(args, ns)
		argIdx++
	}

	query += fmt.Sprintf(" ORDER BY vector <=> $1 LIMIT $%d", argIdx)
	args = append(args, topK)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("pgvector search: %w", err)
	}
	defer rows.Close()

	var results []models.SearchResult
	for rows.Next() {
		var doc models.VectorDoc
		var score float64
		if err := rows.Scan(&doc.ID, &doc.Kitchen, &doc.Namespace, &doc.Content, &doc.Metadata, &doc.CreatedAt, &score); err != nil {
			return nil, fmt.Errorf("pgvector scan: %w", err)
		}
		results = append(results, models.SearchResult{Doc: doc, Score: score})
	}
	return results, rows.Err()
}

func (s *PgvectorStore) Delete(ctx context.Context, kitchen string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	query := "DELETE FROM ao_vectors WHERE kitchen = $1 AND id = ANY($2)"
	_, err := s.pool.Exec(ctx, query, kitchen, ids)
	return err
}

func (s *PgvectorStore) Count(ctx context.Context, kitchen string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM ao_vectors WHERE kitchen = $1", kitchen).Scan(&count)
	return count, err
}

func (s *PgvectorStore) HealthCheck(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Close releases the connection pool.
func (s *PgvectorStore) Close() {
	s.pool.Close()
}

// pgvectorArray converts a float64 slice to pgvector's text format: [1.0,2.0,3.0]
func pgvectorArray(v []float64) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(fmt.Sprintf("%g", f))
	}
	sb.WriteByte(']')
	return sb.String()
}
