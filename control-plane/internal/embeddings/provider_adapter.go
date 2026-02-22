// Package embeddings — provider_adapter.go
//
// ProviderEmbeddingAdapter bridges the router's EmbeddingCapableDriver interface
// with the contracts.EmbeddingDriver interface. This enables auto-discovery of
// embedding capabilities from configured model providers.
//
// Instead of requiring separate embedding configuration (API keys, endpoints),
// the adapter derives everything from an already-configured ModelProvider.
// When a user registers an OpenAI provider with an API key, embeddings become
// available automatically — no extra setup needed.
package embeddings

import (
	"context"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// ProviderEmbeddingAdapter wraps an EmbeddingCapableDriver + ModelProvider + model
// into a contracts.EmbeddingDriver. This is created at startup (or dynamically)
// when the server discovers that a registered provider supports embeddings.
type ProviderEmbeddingAdapter struct {
	driver   router.EmbeddingCapableDriver
	provider *models.ModelProvider
	model    router.EmbeddingModelInfo
}

// NewProviderEmbeddingAdapter creates an adapter that derives embedding capabilities
// from a configured model provider.
func NewProviderEmbeddingAdapter(
	driver router.EmbeddingCapableDriver,
	provider *models.ModelProvider,
	model router.EmbeddingModelInfo,
) *ProviderEmbeddingAdapter {
	return &ProviderEmbeddingAdapter{
		driver:   driver,
		provider: provider,
		model:    model,
	}
}

func (a *ProviderEmbeddingAdapter) Kind() string {
	return fmt.Sprintf("%s/%s", a.provider.Kind, a.model.Model)
}

func (a *ProviderEmbeddingAdapter) Dimensions() int {
	return a.model.Dimensions
}

func (a *ProviderEmbeddingAdapter) MaxBatchSize() int {
	return a.model.MaxBatch
}

func (a *ProviderEmbeddingAdapter) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > a.model.MaxBatch {
		return nil, fmt.Errorf("batch size %d exceeds max %d for %s", len(texts), a.model.MaxBatch, a.model.Model)
	}
	return a.driver.Embed(ctx, a.provider, a.model.Model, texts)
}

func (a *ProviderEmbeddingAdapter) HealthCheck(ctx context.Context) error {
	_, err := a.driver.Embed(ctx, a.provider, a.model.Model, []string{"health check"})
	return err
}

// ProviderName returns the source provider name for display/logging.
func (a *ProviderEmbeddingAdapter) ProviderName() string {
	return a.provider.Name
}

// ModelName returns the embedding model name.
func (a *ProviderEmbeddingAdapter) ModelName() string {
	return a.model.Model
}
