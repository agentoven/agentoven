// Package langfuse provides a bridge between AgentOven's trace store
// and LangFuse's trace ingestion format.
//
// This enables two integration patterns:
//
//  1. Export: Convert AgentOven traces to LangFuse format and push them
//     to a LangFuse instance via its /api/public/ingestion endpoint.
//
//  2. Import: Accept LangFuse-format trace events on an AgentOven endpoint
//     and convert them to AgentOven's internal trace format. This lets
//     external LangChain/LangFuse-instrumented apps send traces to
//     AgentOven for unified observability.
//
// The bridge is optional — it's initialized when LANGFUSE_BASE_URL and
// LANGFUSE_PUBLIC_KEY are set.
package langfuse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// ── LangFuse Trace Format ────────────────────────────────────

// LangFuseTrace represents a trace in LangFuse format.
type LangFuseTrace struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Input     interface{}            `json:"input,omitempty"`
	Output    interface{}            `json:"output,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Tags      []string               `json:"tags,omitempty"`
	UserID    string                 `json:"userId,omitempty"`
	SessionID string                 `json:"sessionId,omitempty"`
	Release   string                 `json:"release,omitempty"`
	Version   string                 `json:"version,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// LangFuseGeneration represents a single LLM generation span.
type LangFuseGeneration struct {
	ID               string                 `json:"id"`
	TraceID          string                 `json:"traceId"`
	Name             string                 `json:"name"`
	Model            string                 `json:"model,omitempty"`
	ModelParameters  map[string]interface{} `json:"modelParameters,omitempty"`
	Input            interface{}            `json:"input,omitempty"`
	Output           interface{}            `json:"output,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
	Usage            *LangFuseUsage         `json:"usage,omitempty"`
	Level            string                 `json:"level,omitempty"` // DEBUG, DEFAULT, WARNING, ERROR
	StatusMessage    string                 `json:"statusMessage,omitempty"`
	CompletionStart  *time.Time             `json:"completionStartTime,omitempty"`
	StartTime        time.Time              `json:"startTime"`
	EndTime          *time.Time             `json:"endTime,omitempty"`
}

// LangFuseUsage tracks token usage in LangFuse format.
type LangFuseUsage struct {
	Input  int64   `json:"input,omitempty"`
	Output int64   `json:"output,omitempty"`
	Total  int64   `json:"total,omitempty"`
	Unit   string  `json:"unit,omitempty"` // "TOKENS"
	Cost   float64 `json:"inputCost,omitempty"`
}

// IngestionEvent is one event in a LangFuse batch ingestion request.
type IngestionEvent struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"` // "trace-create", "generation-create", etc.
	Timestamp time.Time   `json:"timestamp"`
	Body      interface{} `json:"body"`
}

// IngestionBatch is the request body for LangFuse's /api/public/ingestion endpoint.
type IngestionBatch struct {
	Batch    []IngestionEvent `json:"batch"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ── Bridge ───────────────────────────────────────────────────

// Bridge converts between AgentOven and LangFuse trace formats.
type Bridge struct {
	store     store.Store
	client    *http.Client
	baseURL   string // LangFuse API base URL
	publicKey string // LangFuse public key
	secretKey string // LangFuse secret key
}

// NewBridge creates a LangFuse bridge.
func NewBridge(s store.Store, baseURL, publicKey, secretKey string) *Bridge {
	return &Bridge{
		store:     s,
		client:    &http.Client{Timeout: 30 * time.Second},
		baseURL:   baseURL,
		publicKey: publicKey,
		secretKey: secretKey,
	}
}

// ── Export: AgentOven → LangFuse ──────────────────────────────

// ExportTrace converts an AgentOven trace to LangFuse format and pushes it.
func (b *Bridge) ExportTrace(ctx context.Context, trace *models.Trace) error {
	// Convert to LangFuse trace
	lfTrace := b.convertToLangFuse(trace)

	// Build ingestion batch
	batch := IngestionBatch{
		Batch: []IngestionEvent{
			{
				ID:        trace.ID,
				Type:      "trace-create",
				Timestamp: trace.CreatedAt,
				Body:      lfTrace,
			},
		},
		Metadata: map[string]interface{}{
			"source": "agentoven",
		},
	}

	// If we have thinking blocks, add a generation event
	if len(trace.ThinkingBlocks) > 0 {
		gen := b.convertThinkingToGeneration(trace)
		batch.Batch = append(batch.Batch, IngestionEvent{
			ID:        trace.ID + "-thinking",
			Type:      "generation-create",
			Timestamp: trace.CreatedAt,
			Body:      gen,
		})
	}

	return b.pushToLangFuse(ctx, batch)
}

// ExportTraces exports multiple traces to LangFuse in a single batch.
func (b *Bridge) ExportTraces(ctx context.Context, traces []models.Trace) error {
	batch := IngestionBatch{
		Batch: make([]IngestionEvent, 0, len(traces)*2),
		Metadata: map[string]interface{}{
			"source":     "agentoven",
			"batch_size": len(traces),
		},
	}

	for _, t := range traces {
		lfTrace := b.convertToLangFuse(&t)
		batch.Batch = append(batch.Batch, IngestionEvent{
			ID:        t.ID,
			Type:      "trace-create",
			Timestamp: t.CreatedAt,
			Body:      lfTrace,
		})

		if len(t.ThinkingBlocks) > 0 {
			gen := b.convertThinkingToGeneration(&t)
			batch.Batch = append(batch.Batch, IngestionEvent{
				ID:        t.ID + "-thinking",
				Type:      "generation-create",
				Timestamp: t.CreatedAt,
				Body:      gen,
			})
		}
	}

	return b.pushToLangFuse(ctx, batch)
}

func (b *Bridge) convertToLangFuse(trace *models.Trace) LangFuseTrace {
	tags := []string{"agentoven", trace.Kitchen}
	if trace.Status == "error" {
		tags = append(tags, "error")
	}

	return LangFuseTrace{
		ID:   trace.ID,
		Name: trace.AgentName,
		Input: map[string]interface{}{
			"agent":   trace.AgentName,
			"recipe":  trace.RecipeName,
			"kitchen": trace.Kitchen,
		},
		Output: map[string]interface{}{
			"text":   trace.OutputText,
			"tokens": trace.TotalTokens,
		},
		Metadata: map[string]interface{}{
			"duration_ms":     trace.DurationMs,
			"cost_usd":        trace.CostUSD,
			"thinking_blocks": len(trace.ThinkingBlocks),
			"original_meta":   trace.Metadata,
		},
		Tags:      tags,
		UserID:    trace.UserID,
		SessionID: trace.Kitchen,
		Release:   "agentoven",
		Version:   "1.0",
		Timestamp: trace.CreatedAt,
	}
}

func (b *Bridge) convertThinkingToGeneration(trace *models.Trace) LangFuseGeneration {
	var totalThinkingTokens int64
	thinkingTexts := make([]string, 0, len(trace.ThinkingBlocks))
	for _, tb := range trace.ThinkingBlocks {
		totalThinkingTokens += tb.TokenCount
		thinkingTexts = append(thinkingTexts, tb.Content)
	}

	model := ""
	provider := ""
	if len(trace.ThinkingBlocks) > 0 {
		model = trace.ThinkingBlocks[0].Model
		provider = trace.ThinkingBlocks[0].Provider
	}

	endTime := trace.CreatedAt.Add(time.Duration(trace.DurationMs) * time.Millisecond)

	return LangFuseGeneration{
		ID:      trace.ID + "-thinking",
		TraceID: trace.ID,
		Name:    "thinking",
		Model:   model,
		ModelParameters: map[string]interface{}{
			"provider": provider,
		},
		Input: map[string]interface{}{
			"agent":   trace.AgentName,
			"kitchen": trace.Kitchen,
		},
		Output: thinkingTexts,
		Metadata: map[string]interface{}{
			"thinking_block_count": len(trace.ThinkingBlocks),
		},
		Usage: &LangFuseUsage{
			Total: totalThinkingTokens,
			Unit:  "TOKENS",
		},
		Level:     "DEFAULT",
		StartTime: trace.CreatedAt,
		EndTime:   &endTime,
	}
}

func (b *Bridge) pushToLangFuse(ctx context.Context, batch IngestionBatch) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("marshal langfuse batch: %w", err)
	}

	url := fmt.Sprintf("%s/api/public/ingestion", b.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build langfuse request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(b.publicKey, b.secretKey)

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("langfuse push failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("langfuse returned HTTP %d", resp.StatusCode)
	}

	log.Info().Int("events", len(batch.Batch)).Msg("Traces exported to LangFuse")
	return nil
}

// ── Import: LangFuse → AgentOven ──────────────────────────────

// HandleIngest accepts LangFuse-format trace events and stores them
// as AgentOven traces. This lets external LangChain-instrumented apps
// send traces to AgentOven for unified observability.
//
// POST /langfuse/ingest
func (b *Bridge) HandleIngest(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	if kitchen == "" {
		kitchen = "default"
	}

	var batch IngestionBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	imported := 0
	for _, event := range batch.Batch {
		switch event.Type {
		case "trace-create":
			trace := b.convertFromLangFuse(kitchen, event)
			if trace != nil {
				if err := b.store.CreateTrace(r.Context(), trace); err != nil {
					log.Warn().Err(err).Str("trace_id", trace.ID).Msg("Failed to import LangFuse trace")
					continue
				}
				imported++
			}
		}
		// generation-create, span-create etc. are stored as metadata
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"imported": imported,
		"total":    len(batch.Batch),
	})
}

func (b *Bridge) convertFromLangFuse(kitchen string, event IngestionEvent) *models.Trace {
	bodyJSON, _ := json.Marshal(event.Body)
	var lfTrace LangFuseTrace
	if err := json.Unmarshal(bodyJSON, &lfTrace); err != nil {
		return nil
	}

	return &models.Trace{
		ID:         lfTrace.ID,
		AgentName:  lfTrace.Name,
		Kitchen:    kitchen,
		Status:     "imported",
		UserID:     lfTrace.UserID,
		Metadata: map[string]interface{}{
			"source":     "langfuse",
			"session_id": lfTrace.SessionID,
			"tags":       lfTrace.Tags,
			"release":    lfTrace.Release,
			"input":      lfTrace.Input,
			"output":     lfTrace.Output,
		},
		CreatedAt: lfTrace.Timestamp,
	}
}

// ── Export Handler ────────────────────────────────────────────

// HandleExport exports all traces for a kitchen to LangFuse.
// POST /langfuse/export
func (b *Bridge) HandleExport(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	if kitchen == "" {
		kitchen = "default"
	}

	traces, err := b.store.ListTraces(r.Context(), kitchen, 1000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := b.ExportTraces(r.Context(), traces); err != nil {
		http.Error(w, fmt.Sprintf("export failed: %s", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"exported": len(traces),
		"kitchen":  kitchen,
	})
}
