package rag

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// Pipeline executes RAG queries with support for 5 strategies:
// naive, sentence-window, parent-document, HyDE, agentic.
//
// Pipeline implements contracts.RAGService — it is the built-in RAG provider.
// External systems (LlamaIndex, Haystack) register as separate RAGService providers.
type Pipeline struct {
	embeddings contracts.EmbeddingDriver
	vectorDB   contracts.VectorStoreDriver
	// modelRouter is used by HyDE strategy to generate hypothetical answers
	// and for answer generation after retrieval.
	// Can be nil if HyDE/answer-gen is not used.
	modelRouter contracts.ModelRouterService
	// ingester handles document chunking, embedding, and upserting.
	ingester *Ingester
}

// Compile-time check that Pipeline implements RAGService.
var _ contracts.RAGService = (*Pipeline)(nil)

// NewPipeline creates a RAG pipeline with integrated ingestion.
func NewPipeline(emb contracts.EmbeddingDriver, vs contracts.VectorStoreDriver, router contracts.ModelRouterService, chunkerCfg ...ChunkerConfig) *Pipeline {
	cfg := DefaultChunkerConfig()
	if len(chunkerCfg) > 0 {
		cfg = chunkerCfg[0]
	}
	return &Pipeline{
		embeddings:  emb,
		vectorDB:    vs,
		modelRouter: router,
		ingester:    NewIngester(emb, vs, cfg),
	}
}

// Name returns the unique identifier for the built-in RAG service.
func (p *Pipeline) Name() string { return "built-in" }

// Ingest processes raw documents into the pipeline's vector store.
func (p *Pipeline) Ingest(ctx context.Context, kitchen string, req models.RAGIngestRequest) (*models.RAGIngestResult, error) {
	if p.ingester == nil {
		return nil, fmt.Errorf("ingester not configured on built-in RAG pipeline")
	}
	return p.ingester.Ingest(ctx, kitchen, req)
}

// Strategies returns the list of retrieval strategies the built-in pipeline supports.
func (p *Pipeline) Strategies() []models.RAGStrategy {
	return []models.RAGStrategy{
		models.RAGNaive,
		models.RAGSentenceWindow,
		models.RAGParentDocument,
		models.RAGHyDE,
		models.RAGAgentic,
	}
}

// HealthCheck verifies that the embedding driver and vector store are operational.
func (p *Pipeline) HealthCheck(ctx context.Context) error {
	if p.embeddings == nil {
		return fmt.Errorf("embedding driver not configured")
	}
	if p.vectorDB == nil {
		return fmt.Errorf("vector store not configured")
	}
	if err := p.embeddings.HealthCheck(ctx); err != nil {
		return fmt.Errorf("embedding driver unhealthy: %w", err)
	}
	if err := p.vectorDB.HealthCheck(ctx); err != nil {
		return fmt.Errorf("vector store unhealthy: %w", err)
	}
	return nil
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

	// ── Answer generation ───────────────────────────────────
	// If we have a model router AND retrieved sources, synthesize an answer.
	var answer string
	var tokensUsed int64
	if p.modelRouter != nil && len(results) > 0 {
		answer, tokensUsed = p.generateAnswer(ctx, kitchen, req.Question, results)
	}

	elapsed := time.Since(start)
	latencyMs := elapsed.Milliseconds()

	log.Info().
		Str("strategy", string(strategy)).
		Int("results", len(results)).
		Int64("latency_ms", latencyMs).
		Str("kitchen", kitchen).
		Msg("RAG query complete")

	return &models.RAGQueryResult{
		Answer:          answer,
		Sources:         results,
		Strategy:        strategy,
		ChunksRetrieved: len(results),
		TokensUsed:      tokensUsed,
		LatencyMs:       latencyMs,
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
// around each result by looking up neighboring chunks via VectorStoreDriver.List.
func (p *Pipeline) sentenceWindowQuery(ctx context.Context, kitchen string, req models.RAGQueryRequest) ([]models.SearchResult, error) {
	// First, get naive results
	results, err := p.naiveQuery(ctx, kitchen, req)
	if err != nil {
		return nil, err
	}

	// Expand context: for each result, look up adjacent chunks (same source, neighboring doc_index)
	expanded := make([]models.SearchResult, 0, len(results))
	windowSize := 1 // fetch 1 chunk before and after

	for _, r := range results {
		source := r.Doc.Metadata["source"]
		docIndexStr := r.Doc.Metadata["doc_index"]
		if source == "" || docIndexStr == "" {
			// No source metadata — can't expand, keep original
			expanded = append(expanded, r)
			continue
		}

		docIndex := 0
		fmt.Sscanf(docIndexStr, "%d", &docIndex)

		// Fetch neighboring chunks from the same source document
		var neighborTexts []string
		for delta := -windowSize; delta <= windowSize; delta++ {
			if delta == 0 {
				continue // skip the original chunk
			}
			neighborIdx := docIndex + delta
			if neighborIdx < 0 {
				continue
			}
			filter := map[string]string{
				"source":    source,
				"doc_index": fmt.Sprintf("%d", neighborIdx),
			}
			if r.Doc.Namespace != "" {
				filter["namespace"] = r.Doc.Namespace
			}
			neighbors, listErr := p.vectorDB.List(ctx, kitchen, filter, 1)
			if listErr != nil || len(neighbors) == 0 {
				continue
			}
			neighborTexts = append(neighborTexts, neighbors[0].Content)
		}

		// Reconstruct expanded content: [before] + original + [after]
		if len(neighborTexts) > 0 {
			expandedContent := ""
			// Prepend neighbors that came before
			for i := 0; i < len(neighborTexts) && i < windowSize; i++ {
				expandedContent += neighborTexts[i] + "\n"
			}
			expandedContent += r.Doc.Content
			// Append neighbors that came after
			for i := windowSize; i < len(neighborTexts); i++ {
				expandedContent += "\n" + neighborTexts[i]
			}
			cp := r
			cp.Doc.Content = expandedContent
			expanded = append(expanded, cp)
		} else {
			expanded = append(expanded, r)
		}
	}

	return expanded, nil
}

// parentDocumentQuery: searches child chunks but deduplicates by parent source,
// returning the full parent content when available.
func (p *Pipeline) parentDocumentQuery(ctx context.Context, kitchen string, req models.RAGQueryRequest) ([]models.SearchResult, error) {
	// Search for matching chunks — fetch extra to account for deduplication
	fetchK := req.TopK
	if fetchK <= 0 {
		fetchK = 5
	}
	expandedReq := req
	expandedReq.TopK = fetchK * 3 // over-fetch since we'll deduplicate by parent

	results, err := p.naiveQuery(ctx, kitchen, expandedReq)
	if err != nil {
		return nil, err
	}

	// Deduplicate by source (parent document) and collect all chunks per parent
	type parentEntry struct {
		source    string
		bestScore float64
		chunks    []string
	}
	seen := make(map[string]*parentEntry)
	var order []string

	for _, r := range results {
		source := r.Doc.Metadata["source"]
		if source == "" {
			source = r.Doc.ID // fallback: treat each chunk as its own parent
		}
		if entry, ok := seen[source]; ok {
			entry.chunks = append(entry.chunks, r.Doc.Content)
			if r.Score > entry.bestScore {
				entry.bestScore = r.Score
			}
		} else {
			seen[source] = &parentEntry{
				source:    source,
				bestScore: r.Score,
				chunks:    []string{r.Doc.Content},
			}
			order = append(order, source)
		}
	}

	// Build results: for each unique parent, concatenate child chunks
	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}
	if topK > len(order) {
		topK = len(order)
	}

	parentResults := make([]models.SearchResult, 0, topK)
	for _, src := range order[:topK] {
		entry := seen[src]
		// Try to fetch the full parent from vector store by source metadata
		fullContent := ""
		filter := map[string]string{"source": src, "doc_index": "0"}
		parents, listErr := p.vectorDB.List(ctx, kitchen, filter, 1)
		if listErr == nil && len(parents) > 0 {
			fullContent = parents[0].Content
		}
		if fullContent == "" {
			// Fallback: concatenate matched chunks
			fullContent = strings.Join(entry.chunks, "\n---\n")
		}

		parentResults = append(parentResults, models.SearchResult{
			Doc: models.VectorDoc{
				ID:      src,
				Kitchen: kitchen,
				Content: fullContent,
				Metadata: map[string]string{
					"source":      src,
					"child_count": fmt.Sprintf("%d", len(entry.chunks)),
				},
			},
			Score: entry.bestScore,
		})
	}

	return parentResults, nil
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

// ── Answer Generation ────────────────────────────────────────

// generateAnswer uses the model router to synthesize an answer from retrieved sources.
// Returns the answer text and tokens used. On failure, returns empty answer (graceful).
func (p *Pipeline) generateAnswer(ctx context.Context, kitchen string, question string, sources []models.SearchResult) (string, int64) {
	if p.modelRouter == nil || len(sources) == 0 {
		return "", 0
	}

	// Build context from sources
	var contextBuilder strings.Builder
	for i, s := range sources {
		if i > 0 {
			contextBuilder.WriteString("\n---\n")
		}
		contextBuilder.WriteString(fmt.Sprintf("[Source %d", i+1))
		if src, ok := s.Doc.Metadata["source"]; ok {
			contextBuilder.WriteString(fmt.Sprintf(" (%s)", src))
		}
		contextBuilder.WriteString(fmt.Sprintf(", score: %.3f]\n", s.Score))
		contextBuilder.WriteString(s.Doc.Content)
	}

	prompt := fmt.Sprintf(`Answer the following question using ONLY the provided context. If the context does not contain enough information, say so. Be concise and accurate.

Context:
%s

Question: %s

Answer:`, contextBuilder.String(), question)

	routeReq := &models.RouteRequest{
		Kitchen: kitchen,
		Messages: []models.ChatMessage{
			{Role: "user", Content: prompt},
		},
	}
	resp, err := p.modelRouter.Route(ctx, routeReq)
	if err != nil {
		log.Warn().Err(err).Msg("RAG answer generation failed, returning sources only")
		return "", 0
	}

	var tokensUsed int64
	if resp.Usage.TotalTokens > 0 {
		tokensUsed = int64(resp.Usage.TotalTokens)
	}
	return resp.Content, tokensUsed
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
