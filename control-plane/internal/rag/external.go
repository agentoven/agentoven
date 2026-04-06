// Package rag provides the RAG (Retrieval-Augmented Generation) pipeline engine.
package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// ExternalRAGService proxies RAG queries and ingestion to an external system
// (LlamaIndex, Haystack, custom pipeline, etc.) via HTTP.
//
// The external system must expose:
//   - POST /query  — accepts RAGQueryRequest, returns RAGQueryResult
//   - POST /ingest — accepts RAGIngestRequest, returns RAGIngestResult
//   - GET  /health — returns 200 if healthy
//
// This enables AgentOven to orchestrate external RAG systems alongside
// the built-in pipeline, with the same contracts.RAGService interface.
type ExternalRAGService struct {
	name       string
	endpoint   string
	strategies []models.RAGStrategy
	authHeader string // optional "Bearer xxx" or "ApiKey xxx"
	client     *http.Client
}

// Compile-time check that ExternalRAGService implements RAGService.
var _ contracts.RAGService = (*ExternalRAGService)(nil)

// ExternalRAGConfig configures an external RAG service.
type ExternalRAGConfig struct {
	// Name is the unique identifier (e.g. "llamaindex", "haystack").
	Name string `json:"name"`
	// Endpoint is the base URL of the external RAG service (no trailing slash).
	Endpoint string `json:"endpoint"`
	// Strategies lists the strategies the external service supports.
	// If empty, defaults to ["naive"].
	Strategies []models.RAGStrategy `json:"strategies,omitempty"`
	// AuthHeader is an optional Authorization header value.
	AuthHeader string `json:"auth_header,omitempty"`
	// TimeoutSeconds is the HTTP timeout (default: 30).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// NewExternalRAGService creates a proxy RAG service from configuration.
func NewExternalRAGService(cfg ExternalRAGConfig) (*ExternalRAGService, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("external RAG service name is required")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("external RAG service endpoint is required")
	}

	timeout := 30 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}

	strategies := cfg.Strategies
	if len(strategies) == 0 {
		strategies = []models.RAGStrategy{models.RAGNaive}
	}

	return &ExternalRAGService{
		name:       cfg.Name,
		endpoint:   cfg.Endpoint,
		strategies: strategies,
		authHeader: cfg.AuthHeader,
		client:     &http.Client{Timeout: timeout},
	}, nil
}

func (s *ExternalRAGService) Name() string { return s.name }

func (s *ExternalRAGService) Query(ctx context.Context, kitchen string, req models.RAGQueryRequest) (*models.RAGQueryResult, error) {
	req.Kitchen = kitchen

	var result models.RAGQueryResult
	if err := s.post(ctx, "/query", req, &result); err != nil {
		return nil, fmt.Errorf("external RAG query (%s): %w", s.name, err)
	}
	return &result, nil
}

func (s *ExternalRAGService) Ingest(ctx context.Context, kitchen string, req models.RAGIngestRequest) (*models.RAGIngestResult, error) {
	req.Kitchen = kitchen

	var result models.RAGIngestResult
	if err := s.post(ctx, "/ingest", req, &result); err != nil {
		return nil, fmt.Errorf("external RAG ingest (%s): %w", s.name, err)
	}
	return &result, nil
}

func (s *ExternalRAGService) Strategies() []models.RAGStrategy {
	return s.strategies
}

func (s *ExternalRAGService) HealthCheck(ctx context.Context) error {
	url := s.endpoint + "/health"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create health request: %w", err)
	}
	if s.authHeader != "" {
		httpReq.Header.Set("Authorization", s.authHeader)
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("health check request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("health check returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// post sends a JSON POST request and decodes the response.
func (s *ExternalRAGService) post(ctx context.Context, path string, reqBody, respBody interface{}) error {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := s.endpoint + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.authHeader != "" {
		httpReq.Header.Set("Authorization", s.authHeader)
	}

	log.Debug().Str("url", url).Str("service", s.name).Msg("External RAG request")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB limit
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("external service returned %d: %s", resp.StatusCode, string(body))
	}

	if err := json.Unmarshal(body, respBody); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
