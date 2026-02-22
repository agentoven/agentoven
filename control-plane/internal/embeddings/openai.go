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

// OpenAIDriver implements EmbeddingDriver for OpenAI's embedding API.
// Supports text-embedding-3-small (1536d), text-embedding-3-large (3072d),
// and text-embedding-ada-002 (1536d).
type OpenAIDriver struct {
	apiKey     string
	model      string
	endpoint   string // defaults to https://api.openai.com/v1/embeddings
	dimensions int
	batchSize  int
	client     *http.Client
}

// OpenAIOption configures the OpenAI driver.
type OpenAIOption func(*OpenAIDriver)

// WithOpenAIEndpoint sets a custom API endpoint (e.g. for proxies).
func WithOpenAIEndpoint(endpoint string) OpenAIOption {
	return func(d *OpenAIDriver) { d.endpoint = endpoint }
}

// WithOpenAIBatchSize sets the max texts per Embed call.
func WithOpenAIBatchSize(size int) OpenAIOption {
	return func(d *OpenAIDriver) { d.batchSize = size }
}

// NewOpenAIDriver creates an OpenAI embedding driver.
func NewOpenAIDriver(apiKey, model string, opts ...OpenAIOption) *OpenAIDriver {
	dims := 1536
	switch model {
	case "text-embedding-3-large":
		dims = 3072
	case "text-embedding-3-small", "text-embedding-ada-002":
		dims = 1536
	}

	d := &OpenAIDriver{
		apiKey:     apiKey,
		model:      model,
		endpoint:   "https://api.openai.com/v1/embeddings",
		dimensions: dims,
		batchSize:  2048,
		client:     &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

func (d *OpenAIDriver) Kind() string       { return "openai" }
func (d *OpenAIDriver) Dimensions() int    { return d.dimensions }
func (d *OpenAIDriver) MaxBatchSize() int  { return d.batchSize }

type openAIEmbedRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type openAIEmbedResponse struct {
	Data  []openAIEmbedData `json:"data"`
	Error *openAIError      `json:"error,omitempty"`
}

type openAIEmbedData struct {
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Embed generates vector embeddings for a batch of texts.
func (d *OpenAIDriver) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > d.batchSize {
		return nil, fmt.Errorf("batch size %d exceeds max %d", len(texts), d.batchSize)
	}

	body, err := json.Marshal(openAIEmbedRequest{Input: texts, Model: d.model})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.apiKey)

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
		return nil, fmt.Errorf("openai embeddings API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result openAIEmbedResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("openai error: %s (%s)", result.Error.Message, result.Error.Type)
	}

	// Reorder by index
	vectors := make([][]float64, len(texts))
	for _, d := range result.Data {
		if d.Index < len(vectors) {
			vectors[d.Index] = d.Embedding
		}
	}
	return vectors, nil
}

// HealthCheck verifies the API key by embedding a test string.
func (d *OpenAIDriver) HealthCheck(ctx context.Context) error {
	_, err := d.Embed(ctx, []string{"health check"})
	return err
}
