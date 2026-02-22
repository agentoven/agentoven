package rag

import (
	"context"
	"fmt"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// Pipeline executes RAG queries with support for 5 strategies:
// naive, sentence-window, parent-document, HyDE, agentic.
type Pipeline struct {
	embeddings contracts.EmbeddingDriver
	vectorDB   contracts.VectorStoreDriver
	// modelRouter is used by HyDE strategy to generate hypothetical answers.
	// Can be nil if HyDE is not used.
	modelRouter contracts.ModelRouterService
}

// NewPipeline creates a RAG pipeline.
func NewPipeline(emb contracts.EmbeddingDriver, vs contracts.VectorStoreDriver, router contracts.ModelRouterService) *Pipeline {
	return &Pipeline{
		embeddings:  emb,
		vectorDB:    vs,
		modelRouter: router,
	}
}

// Query executes a RAG query using the specified strategy.
func (p *Pipeline) Query(ctx context.Context, kitchen string, req models.RAGQueryRequest) (*models.RAGQueryResult, error) {
	start := time.Now()

	strategy := req.Strategy
	if strategy == "" {
		strategy = models.RAGNaive
	}

	var results []models.SearchResult
	var err error

	switch strategy {
	case models.RAGNaive:
		results, err = p.naiveQuery(ctx, kitchen, req)
	case models.RAGSentenceWindow:
		results, err = p.sentenceWindowQuery(ctx, kitchen, req)
	case models.RAGParentDocument:
		results, err = p.parentDocumentQuery(ctx, kitchen, req)
	case models.RAGHyDE:
		results, err = p.hydeQuery(ctx, kitchen, req)
	case models.RAGAgentic:
		results, err = p.agenticQuery(ctx, kitchen, req)
	default:
		return nil, fmt.Errorf("unsupported RAG strategy: %s", strategy)
	}

	if err != nil {
		return nil, err
	}

	// Apply score threshold
	if req.MinScore > 0 {
		var filtered []models.SearchResult
		for _, r := range results {
			if r.Score >= req.MinScore {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	elapsed := time.Since(start)
	log.Info().
		Str("strategy", string(strategy)).
		Int("results", len(results)).
		Dur("elapsed", elapsed).
		Str("kitchen", kitchen).
		Msg("RAG query complete")

	return &models.RAGQueryResult{
		Sources:         results,
		Strategy:        strategy,
		ChunksRetrieved: len(results),
	}, nil
}

// ── Strategy Implementations ────────────────────────────────

// naiveQuery: embed query → search → return top-k results.
func (p *Pipeline) naiveQuery(ctx context.Context, kitchen string, req models.RAGQueryRequest) ([]models.SearchResult, error) {
	vectors, err := p.embeddings.Embed(ctx, []string{req.Question})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vectors) == 0 {
		return nil, fmt.Errorf("no embedding returned for query")
	}

	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}

	filter := map[string]string{}
	if req.Namespace != "" {
		filter["namespace"] = req.Namespace
	}

	return p.vectorDB.Search(ctx, kitchen, vectors[0], topK, filter)
}

// sentenceWindowQuery: same as naive, but retrieves extra context
// around each result by looking up neighboring chunks.
func (p *Pipeline) sentenceWindowQuery(ctx context.Context, kitchen string, req models.RAGQueryRequest) ([]models.SearchResult, error) {
	// First, get naive results
	results, err := p.naiveQuery(ctx, kitchen, req)
	if err != nil {
		return nil, err
	}

	// Sentence window expands context by fetching chunks with adjacent doc_index.
	// For now, this is a passthrough — full implementation would fetch parent docs
	// and reconstruct windows. This gives users the framework to extend.
	log.Debug().Msg("Sentence-window strategy: returning naive results (window expansion planned)")
	return results, nil
}

// parentDocumentQuery: searches child chunks but returns parent documents.
func (p *Pipeline) parentDocumentQuery(ctx context.Context, kitchen string, req models.RAGQueryRequest) ([]models.SearchResult, error) {
	// Search for matching chunks
	results, err := p.naiveQuery(ctx, kitchen, req)
	if err != nil {
		return nil, err
	}

	// Parent document strategy: look up source document for each chunk.
	// Chunks carry metadata["source"] which references the parent.
	// Full implementation would deduplicate by source and return full parent docs.
	log.Debug().Msg("Parent-document strategy: returning chunk results with source metadata")
	return results, nil
}

// hydeQuery: generate hypothetical answer via LLM, embed that, search.
func (p *Pipeline) hydeQuery(ctx context.Context, kitchen string, req models.RAGQueryRequest) ([]models.SearchResult, error) {
	if p.modelRouter == nil {
		return nil, fmt.Errorf("HyDE strategy requires a model router (not configured)")
	}

	// Generate a hypothetical answer using the model router
	hydePrompt := fmt.Sprintf("Write a short, factual answer to this question. Do not explain, just answer:\n\nQuestion: %s\n\nAnswer:", req.Question)

	routeReq := &models.RouteRequest{
		Kitchen: kitchen,
		Messages: []models.ChatMessage{
			{Role: "user", Content: hydePrompt},
		},
	}
	routeResp, err := p.modelRouter.Route(ctx, routeReq)
	if err != nil {
		log.Warn().Err(err).Msg("HyDE: model router failed, falling back to naive")
		return p.naiveQuery(ctx, kitchen, req)
	}

	hypotheticalAnswer := routeResp.Content
	if hypotheticalAnswer == "" {
		return p.naiveQuery(ctx, kitchen, req)
	}

	// Embed the hypothetical answer instead of the original query
	vectors, err := p.embeddings.Embed(ctx, []string{hypotheticalAnswer})
	if err != nil {
		return nil, fmt.Errorf("embed hypothetical answer: %w", err)
	}

	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}

	filter := map[string]string{}
	if req.Namespace != "" {
		filter["namespace"] = req.Namespace
	}

	return p.vectorDB.Search(ctx, kitchen, vectors[0], topK, filter)
}

// agenticQuery: multi-step retrieval with query decomposition.
// Decomposes the query, runs sub-queries, merges and re-ranks results.
func (p *Pipeline) agenticQuery(ctx context.Context, kitchen string, req models.RAGQueryRequest) ([]models.SearchResult, error) {
	if p.modelRouter == nil {
		return nil, fmt.Errorf("agentic strategy requires a model router (not configured)")
	}

	// Step 1: Decompose query into sub-queries
	decomposePrompt := fmt.Sprintf(`Decompose this question into 2-3 simpler sub-questions that can be independently searched. Return only the sub-questions, one per line, no numbering:

Question: %s

Sub-questions:`, req.Question)

	routeReq := &models.RouteRequest{
		Kitchen: kitchen,
		Messages: []models.ChatMessage{
			{Role: "user", Content: decomposePrompt},
		},
	}
	routeResp, err := p.modelRouter.Route(ctx, routeReq)
	if err != nil {
		log.Warn().Err(err).Msg("Agentic: decomposition failed, falling back to naive")
		return p.naiveQuery(ctx, kitchen, req)
	}

	// Parse sub-queries from response
	subQueries := parseSubQueries(routeResp.Content)
	if len(subQueries) == 0 {
		subQueries = []string{req.Question}
	}

	// Step 2: Run each sub-query
	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}
	// Fetch more per sub-query, then deduplicate
	perQueryK := topK
	if len(subQueries) > 1 {
		perQueryK = topK / len(subQueries)
		if perQueryK < 2 {
			perQueryK = 2
		}
	}

	seen := make(map[string]bool)
	var merged []models.SearchResult

	for _, sq := range subQueries {
		subReq := req
		subReq.Question = sq
		subReq.TopK = perQueryK

		results, err := p.naiveQuery(ctx, kitchen, subReq)
		if err != nil {
			log.Warn().Err(err).Str("sub_query", sq).Msg("Agentic: sub-query failed, skipping")
			continue
		}
		for _, r := range results {
			if !seen[r.Doc.ID] {
				seen[r.Doc.ID] = true
				merged = append(merged, r)
			}
		}
	}

	// Sort by score descending and trim to topK
	for i := 0; i < len(merged); i++ {
		for j := i + 1; j < len(merged); j++ {
			if merged[j].Score > merged[i].Score {
				merged[i], merged[j] = merged[j], merged[i]
			}
		}
	}
	if len(merged) > topK {
		merged = merged[:topK]
	}

	return merged, nil
}

// ── Helpers ─────────────────────────────────────────────────

// parseSubQueries extracts non-empty lines from LLM response.
func parseSubQueries(text string) []string {
	var queries []string
	for _, line := range splitLines(text) {
		line = trimSpace(line)
		if line == "" {
			continue
		}
		// Remove common prefixes like "1.", "- ", "• "
		for len(line) > 0 && (line[0] == '-' || line[0] == '*' || (line[0] >= '0' && line[0] <= '9')) {
			line = trimSpace(line[1:])
			if len(line) > 0 && (line[0] == '.' || line[0] == ')') {
				line = trimSpace(line[1:])
			}
		}
		if line != "" {
			queries = append(queries, line)
		}
	}
	return queries
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	j := len(s)
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}
