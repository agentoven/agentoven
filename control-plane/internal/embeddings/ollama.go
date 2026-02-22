package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaDriver implements EmbeddingDriver for Ollama's local embedding API.
// Supports nomic-embed-text (768d), mxbai-embed-large (1024d), all-minilm (384d).
type OllamaDriver struct {
	endpoint   string // e.g. http://localhost:11434
	model      string
	dimensions int
	batchSize  int
	client     *http.Client
}

// OllamaOption configures the Ollama driver.
type OllamaOption func(*OllamaDriver)

// WithOllamaBatchSize sets the max texts per Embed call.
func WithOllamaBatchSize(size int) OllamaOption {
	return func(d *OllamaDriver) { d.batchSize = size }
}

// NewOllamaDriver creates an Ollama embedding driver.
func NewOllamaDriver(endpoint, model string, opts ...OllamaOption) *OllamaDriver {
	dims := 768
	switch model {
	case "nomic-embed-text":
		dims = 768
	case "mxbai-embed-large":
		dims = 1024
	case "all-minilm", "all-minilm:l6-v2":
		dims = 384
	}

	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}

	d := &OllamaDriver{
		endpoint:   endpoint,
		model:      model,
		dimensions: dims,
		batchSize:  512,
		client:     &http.Client{Timeout: 120 * time.Second},
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

func (d *OllamaDriver) Kind() string       { return "ollama" }
func (d *OllamaDriver) Dimensions() int    { return d.dimensions }
func (d *OllamaDriver) MaxBatchSize() int  { return d.batchSize }

type ollamaEmbedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"` // string or []string
}

type ollamaEmbedResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

// Embed generates vector embeddings. Ollama supports batch via /api/embed.
func (d *OllamaDriver) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > d.batchSize {
		return nil, fmt.Errorf("batch size %d exceeds max %d", len(texts), d.batchSize)
	}

	body, err := json.Marshal(ollamaEmbedRequest{Model: d.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := d.endpoint + "/api/embed"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result ollamaEmbedResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(result.Embeddings) != len(texts) {
		return nil, fmt.Errorf("expected %d embeddings, got %d", len(texts), len(result.Embeddings))
	}
	return result.Embeddings, nil
}

// HealthCheck verifies Ollama is reachable and the model is available.
func (d *OllamaDriver) HealthCheck(ctx context.Context) error {
	_, err := d.Embed(ctx, []string{"health check"})
	return err
}
