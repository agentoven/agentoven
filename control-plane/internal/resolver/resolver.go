// Package resolver validates and resolves agent ingredients at bake/invoke time.
//
// When an agent is baked, the Resolver verifies that all referenced
// ingredients exist (model provider is configured, MCP tools are registered
// and enabled, prompts exist in the Prompt Store, etc.) and returns a
// fully resolved configuration. This prevents runtime failures.
package resolver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// templateVarRegex matches {{variable}} placeholders in prompt templates.
var templateVarRegex = regexp.MustCompile(`\{\{(\w+)\}\}`)

// Resolver validates and resolves agent ingredients against the store.
type Resolver struct {
	store store.Store
}

// NewResolver creates a new ingredient resolver.
func NewResolver(s store.Store) *Resolver {
	return &Resolver{store: s}
}

// Resolve validates all ingredients for an agent and returns a fully resolved config.
// For each ingredient kind:
//   - model:         looks up the ModelProvider by name, validates the model exists
//   - tool:          looks up the MCPTool, validates it's enabled and has "tool" capability
//   - prompt:        looks up the Prompt in the PromptStore
//   - data:          validates config contains required fields (uri)
//   - observability: looks up the MCPTool and validates it has "tool" capability
func (r *Resolver) Resolve(ctx context.Context, agent *models.Agent) (*models.ResolvedIngredients, error) {
	resolved := &models.ResolvedIngredients{}
	var errors []string

	for _, ing := range agent.Ingredients {
		switch ing.Kind {
		case models.IngredientModel:
			rm, err := r.resolveModel(ctx, ing)
			if err != nil {
				errors = append(errors, fmt.Sprintf("model %q: %s", ing.Name, err))
				continue
			}
			resolved.Model = rm

		case models.IngredientTool:
			rt, err := r.resolveTool(ctx, agent.Kitchen, ing)
			if err != nil {
				if ing.Required {
					errors = append(errors, fmt.Sprintf("tool %q: %s", ing.Name, err))
				} else {
					log.Warn().Str("tool", ing.Name).Err(err).Msg("Optional tool not resolved")
				}
				continue
			}
			resolved.Tools = append(resolved.Tools, *rt)

		case models.IngredientPrompt:
			rp, err := r.resolvePrompt(ctx, agent.Kitchen, ing)
			if err != nil {
				if ing.Required {
					errors = append(errors, fmt.Sprintf("prompt %q: %s", ing.Name, err))
				} else {
					log.Warn().Str("prompt", ing.Name).Err(err).Msg("Optional prompt not resolved")
				}
				continue
			}
			resolved.Prompt = rp

		case models.IngredientData:
			rd, err := r.resolveData(ing)
			if err != nil {
				if ing.Required {
					errors = append(errors, fmt.Sprintf("data %q: %s", ing.Name, err))
				}
				continue
			}
			resolved.Data = append(resolved.Data, *rd)

		case models.IngredientEmbedding:
			re, err := r.resolveEmbedding(ctx, ing)
			if err != nil {
				if ing.Required {
					errors = append(errors, fmt.Sprintf("embedding %q: %s", ing.Name, err))
				} else {
					log.Warn().Str("embedding", ing.Name).Err(err).Msg("Optional embedding not resolved")
				}
				continue
			}
			resolved.Embeddings = append(resolved.Embeddings, *re)

		case models.IngredientVectorStore:
			rv, err := r.resolveVectorStore(ing)
			if err != nil {
				if ing.Required {
					errors = append(errors, fmt.Sprintf("vectorstore %q: %s", ing.Name, err))
				} else {
					log.Warn().Str("vectorstore", ing.Name).Err(err).Msg("Optional vector store not resolved")
				}
				continue
			}
			resolved.VectorStores = append(resolved.VectorStores, *rv)

		case models.IngredientRetriever:
			rr, err := r.resolveRetriever(ing)
			if err != nil {
				if ing.Required {
					errors = append(errors, fmt.Sprintf("retriever %q: %s", ing.Name, err))
				} else {
					log.Warn().Str("retriever", ing.Name).Err(err).Msg("Optional retriever not resolved")
				}
				continue
			}
			resolved.Retrievers = append(resolved.Retrievers, *rr)

		case models.IngredientObservability:
			// Observability ingredients reference MCP tools (e.g., LangFuse MCP)
			_, err := r.resolveTool(ctx, agent.Kitchen, ing)
			if err != nil {
				log.Warn().Str("observability", ing.Name).Err(err).Msg("Observability tool not resolved")
			}
			// Observability doesn't block bake — it's always optional

		default:
			errors = append(errors, fmt.Sprintf("unknown ingredient kind %q for %q", ing.Kind, ing.Name))
		}
	}

	// For managed agents, a model ingredient is required
	if agent.Mode == models.AgentModeManaged && resolved.Model == nil {
		errors = append(errors, "managed agents require at least one model ingredient")
	}

	// Validate retriever cross-references
	for _, ret := range resolved.Retrievers {
		if ret.EmbeddingRef != "" {
			found := false
			for _, emb := range resolved.Embeddings {
				if emb.Provider+"/"+emb.Model == ret.EmbeddingRef || emb.Provider == ret.EmbeddingRef {
					found = true
					break
				}
			}
			if !found {
				errors = append(errors, fmt.Sprintf("retriever references embedding %q which is not in resolved ingredients", ret.EmbeddingRef))
			}
		}
		if ret.VectorStoreRef != "" {
			found := false
			for _, vs := range resolved.VectorStores {
				if string(vs.Backend) == ret.VectorStoreRef || vs.Index == ret.VectorStoreRef {
					found = true
					break
				}
			}
			if !found {
				errors = append(errors, fmt.Sprintf("retriever references vectorstore %q which is not in resolved ingredients", ret.VectorStoreRef))
			}
		}
	}

	if len(errors) > 0 {
		return resolved, fmt.Errorf("ingredient resolution failed:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return resolved, nil
}

// resolveModel looks up the model provider and validates the requested model exists.
func (r *Resolver) resolveModel(ctx context.Context, ing models.Ingredient) (*models.ResolvedModel, error) {
	providerName, _ := ing.Config["provider"].(string)
	modelName, _ := ing.Config["model"].(string)

	if providerName == "" {
		return nil, fmt.Errorf("missing 'provider' in config")
	}
	if modelName == "" {
		return nil, fmt.Errorf("missing 'model' in config")
	}

	provider, err := r.store.GetProvider(ctx, providerName)
	if err != nil {
		return nil, fmt.Errorf("provider %q not found", providerName)
	}

	// Validate the model is in the provider's model list
	found := false
	for _, m := range provider.Models {
		if m == modelName {
			found = true
			break
		}
	}
	if !found {
		// Allow wildcard — if provider has no models listed, accept any
		if len(provider.Models) > 0 {
			return nil, fmt.Errorf("model %q not in provider %q's model list %v", modelName, providerName, provider.Models)
		}
	}

	// Extract API key from provider config
	apiKey, _ := provider.Config["api_key"].(string)

	return &models.ResolvedModel{
		Provider: provider.Name,
		Kind:     provider.Kind,
		Model:    modelName,
		Endpoint: provider.Endpoint,
		APIKey:   apiKey,
		Config:   ing.Config,
	}, nil
}

// resolveTool looks up the MCP tool and validates it's enabled.
func (r *Resolver) resolveTool(ctx context.Context, kitchen string, ing models.Ingredient) (*models.ResolvedTool, error) {
	toolName := ing.Name
	// Allow override via config
	if cfgName, ok := ing.Config["tool_name"].(string); ok {
		toolName = cfgName
	}

	tool, err := r.store.GetTool(ctx, kitchen, toolName)
	if err != nil {
		return nil, fmt.Errorf("MCP tool %q not found in kitchen %q", toolName, kitchen)
	}

	if !tool.Enabled {
		return nil, fmt.Errorf("MCP tool %q is disabled", toolName)
	}

	// Verify it has "tool" capability (not just "notify")
	hasTool := false
	for _, c := range tool.Capabilities {
		if c == "tool" {
			hasTool = true
			break
		}
	}
	if !hasTool {
		return nil, fmt.Errorf("MCP tool %q does not have 'tool' capability", toolName)
	}

	// Compute schema hash for version pinning
	var schemaHash string
	if tool.Schema != nil {
		if raw, err := json.Marshal(tool.Schema); err == nil {
			h := sha256.Sum256(raw)
			schemaHash = fmt.Sprintf("%x", h[:8]) // first 8 bytes = 16 hex chars
		}
	}

	return &models.ResolvedTool{
		Name:       tool.Name,
		Endpoint:   tool.Endpoint,
		Transport:  tool.Transport,
		Schema:     tool.Schema,
		Version:    tool.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		SchemaHash: schemaHash,
		BakedAt:    time.Now().UTC(),
	}, nil
}

// resolvePrompt resolves a prompt ingredient. It supports two modes:
//
//  1. Inline — config contains "text" or "template" with the prompt body directly.
//     This is the path used by the dashboard's agent creation form.
//  2. Store reference — the ingredient name (or config["prompt_name"]) references
//     a prompt in the Prompt Store, optionally pinned to a version.
func (r *Resolver) resolvePrompt(ctx context.Context, kitchen string, ing models.Ingredient) (*models.ResolvedPrompt, error) {
	// ── Path 1: Inline prompt text ──────────────────────────────
	// Dashboard sends {name:"system-prompt", kind:"prompt", config:{text:"..."}}
	// Accept both "text" and "template" keys for convenience.
	if inlineText, ok := ing.Config["text"].(string); ok && inlineText != "" {
		return &models.ResolvedPrompt{
			Name:     ing.Name,
			Version:  0, // inline prompts are unversioned
			Template: inlineText,
		}, nil
	}
	if inlineTemplate, ok := ing.Config["template"].(string); ok && inlineTemplate != "" {
		return &models.ResolvedPrompt{
			Name:     ing.Name,
			Version:  0,
			Template: inlineTemplate,
		}, nil
	}

	// ── Path 2: Prompt Store lookup ─────────────────────────────
	promptName := ing.Name
	if cfgName, ok := ing.Config["prompt_name"].(string); ok {
		promptName = cfgName
	}

	var prompt *models.Prompt
	var err error
	if version, ok := ing.Config["version"].(float64); ok && version > 0 {
		prompt, err = r.store.GetPromptVersion(ctx, kitchen, promptName, int(version))
	} else {
		prompt, err = r.store.GetPrompt(ctx, kitchen, promptName)
	}
	if err != nil {
		return nil, fmt.Errorf("prompt %q not found in kitchen %q (hint: use config.text for inline prompts)", promptName, kitchen)
	}

	return &models.ResolvedPrompt{
		Name:     prompt.Name,
		Version:  prompt.Version,
		Template: prompt.Template,
	}, nil
}

// resolveData validates data ingredient configuration.
func (r *Resolver) resolveData(ing models.Ingredient) (*models.ResolvedData, error) {
	uri, _ := ing.Config["uri"].(string)
	if uri == "" {
		return nil, fmt.Errorf("missing 'uri' in data ingredient config")
	}

	return &models.ResolvedData{
		Name:   ing.Name,
		URI:    uri,
		Config: ing.Config,
	}, nil
}

// resolveEmbedding validates and resolves an embedding ingredient.
// Config fields: provider (required), model (optional — defaults to provider's default).
func (r *Resolver) resolveEmbedding(ctx context.Context, ing models.Ingredient) (*models.ResolvedEmbedding, error) {
	providerName, _ := ing.Config["provider"].(string)
	modelName, _ := ing.Config["model"].(string)

	if providerName == "" {
		return nil, fmt.Errorf("missing 'provider' in embedding config")
	}

	// Look up the model provider to validate it exists and get credentials.
	provider, err := r.store.GetProvider(ctx, providerName)
	if err != nil {
		return nil, fmt.Errorf("provider %q not found — register it first", providerName)
	}

	apiKey, _ := provider.Config["api_key"].(string)
	dims := 0
	if d, ok := ing.Config["dimensions"].(float64); ok {
		dims = int(d)
	}
	batchSize := 0
	if b, ok := ing.Config["batch_size"].(float64); ok {
		batchSize = int(b)
	}
	distanceMetric, _ := ing.Config["distance_metric"].(string)
	if distanceMetric == "" {
		distanceMetric = "cosine"
	}

	// Set defaults based on provider kind
	if modelName == "" {
		switch provider.Kind {
		case "openai", "azure-openai":
			modelName = "text-embedding-3-small"
			if dims == 0 {
				dims = 1536
			}
		case "ollama":
			modelName = "nomic-embed-text"
			if dims == 0 {
				dims = 768
			}
		default:
			return nil, fmt.Errorf("no default embedding model for provider kind %q — specify 'model' in config", provider.Kind)
		}
	}

	return &models.ResolvedEmbedding{
		Provider:       provider.Kind,
		Model:          modelName,
		Dimensions:     dims,
		BatchSize:      batchSize,
		DistanceMetric: distanceMetric,
		Endpoint:       provider.Endpoint,
		APIKey:         apiKey,
		Config:         ing.Config,
	}, nil
}

// resolveVectorStore validates and resolves a vector store ingredient.
// Config fields: backend (required), index (required), namespace (optional), dimensions (optional).
func (r *Resolver) resolveVectorStore(ing models.Ingredient) (*models.ResolvedVectorStore, error) {
	backendStr, _ := ing.Config["backend"].(string)
	if backendStr == "" {
		backendStr = "embedded" // default to in-memory
	}

	index, _ := ing.Config["index"].(string)
	if index == "" {
		return nil, fmt.Errorf("missing 'index' in vectorstore config")
	}

	namespace, _ := ing.Config["namespace"].(string)
	dims := 0
	if d, ok := ing.Config["dimensions"].(float64); ok {
		dims = int(d)
	}

	backend := models.VectorStoreBackend(backendStr)

	return &models.ResolvedVectorStore{
		Backend:    backend,
		Index:      index,
		Namespace:  namespace,
		Dimensions: dims,
		Config:     ing.Config,
	}, nil
}

// resolveRetriever validates and resolves a retriever ingredient.
// Config fields: embedding_ref, vectorstore_ref, top_k, score_threshold, rerank_strategy.
func (r *Resolver) resolveRetriever(ing models.Ingredient) (*models.ResolvedRetriever, error) {
	embRef, _ := ing.Config["embedding_ref"].(string)
	vsRef, _ := ing.Config["vectorstore_ref"].(string)

	topK := 5 // default
	if k, ok := ing.Config["top_k"].(float64); ok && k > 0 {
		topK = int(k)
	}

	scoreThreshold := 0.0
	if s, ok := ing.Config["score_threshold"].(float64); ok {
		scoreThreshold = s
	}

	rerankStrategy, _ := ing.Config["rerank_strategy"].(string)
	if rerankStrategy == "" {
		rerankStrategy = "none"
	}

	hybridSearch := false
	if h, ok := ing.Config["hybrid_search"].(bool); ok {
		hybridSearch = h
	}

	return &models.ResolvedRetriever{
		EmbeddingRef:   embRef,
		VectorStoreRef: vsRef,
		TopK:           topK,
		ScoreThreshold: scoreThreshold,
		RerankStrategy: rerankStrategy,
		HybridSearch:   hybridSearch,
	}, nil
}

// RenderPrompt renders a prompt template by substituting {{variable}} placeholders
// with values from the provided variables map.
func RenderPrompt(template string, variables map[string]string) string {
	result := template
	for key, val := range variables {
		result = strings.ReplaceAll(result, "{{"+key+"}}", val)
	}
	return result
}

// ExtractVariables extracts {{variable}} placeholder names from a prompt template.
func ExtractVariables(template string) []string {
	matches := templateVarRegex.FindAllStringSubmatch(template, -1)
	seen := make(map[string]bool)
	var vars []string
	for _, match := range matches {
		if len(match) > 1 && !seen[match[1]] {
			seen[match[1]] = true
			vars = append(vars, match[1])
		}
	}
	return vars
}
