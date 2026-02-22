package rag

import (
	"context"
	"fmt"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Ingester handles document ingestion: chunk → embed → upsert.
type Ingester struct {
	embeddings contracts.EmbeddingDriver
	vectorDB   contracts.VectorStoreDriver
	chunker    ChunkerConfig
}

// NewIngester creates a document ingester.
func NewIngester(emb contracts.EmbeddingDriver, vs contracts.VectorStoreDriver, chunker ChunkerConfig) *Ingester {
	return &Ingester{
		embeddings: emb,
		vectorDB:   vs,
		chunker:    chunker,
	}
}

// Ingest processes raw documents: splits into chunks, generates embeddings,
// and upserts vector documents into the vector store.
func (ing *Ingester) Ingest(ctx context.Context, kitchen string, req models.RAGIngestRequest) (*models.RAGIngestResult, error) {
	start := time.Now()

	if len(req.Documents) == 0 {
		return &models.RAGIngestResult{DocumentsProcessed: 0}, nil
	}

	config := ing.chunker
	if req.ChunkSize > 0 {
		config.ChunkSize = req.ChunkSize
	}
	if req.ChunkOverlap > 0 {
		config.ChunkOverlap = req.ChunkOverlap
	}

	// Step 1: Chunk all documents
	var allChunks []Chunk
	var chunkSources []int // track which document each chunk came from
	for docIdx, doc := range req.Documents {
		chunks := ChunkText(doc.Content, config)
		for _, c := range chunks {
			// Merge document metadata into chunk metadata
			for k, v := range doc.Metadata {
				c.Metadata[k] = v
			}
			c.Metadata["source"] = doc.ID
			c.Metadata["doc_index"] = fmt.Sprintf("%d", docIdx)
			allChunks = append(allChunks, c)
			chunkSources = append(chunkSources, docIdx)
		}
	}

	log.Info().
		Int("documents", len(req.Documents)).
		Int("chunks", len(allChunks)).
		Str("kitchen", kitchen).
		Msg("Chunking complete")

	// Step 2: Embed in batches
	batchSize := ing.embeddings.MaxBatchSize()
	var allVectors [][]float64

	for i := 0; i < len(allChunks); i += batchSize {
		end := i + batchSize
		if end > len(allChunks) {
			end = len(allChunks)
		}
		batch := allChunks[i:end]
		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.Text
		}

		vectors, err := ing.embeddings.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embed batch %d-%d: %w", i, end, err)
		}
		allVectors = append(allVectors, vectors...)
	}

	log.Info().
		Int("vectors", len(allVectors)).
		Str("kitchen", kitchen).
		Msg("Embedding complete")

	// Step 3: Build VectorDocs and upsert
	now := time.Now()
	docs := make([]models.VectorDoc, len(allChunks))
	for i, chunk := range allChunks {
		docs[i] = models.VectorDoc{
			ID:        uuid.NewString(),
			Kitchen:   kitchen,
			Content:   chunk.Text,
			Metadata:  chunk.Metadata,
			Vector:    allVectors[i],
			Namespace: req.Namespace,
			CreatedAt: now,
		}
	}

	if err := ing.vectorDB.Upsert(ctx, kitchen, docs); err != nil {
		return nil, fmt.Errorf("upsert vectors: %w", err)
	}

	elapsed := time.Since(start)
	log.Info().
		Int("documents", len(req.Documents)).
		Int("chunks_created", len(allChunks)).
		Dur("elapsed", elapsed).
		Str("kitchen", kitchen).
		Msg("Ingestion complete")

	return &models.RAGIngestResult{
		DocumentsProcessed: len(req.Documents),
		ChunksCreated:      len(allChunks),
		VectorsStored:      len(docs),
	}, nil
}
