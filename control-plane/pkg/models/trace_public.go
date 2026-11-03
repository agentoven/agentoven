package models

import "time"

// What a reader who may see a trace's timings, usage and cost but not its content receives
// (Finance and Viewer in Pro, ADR-0043). The earlier way of doing this blanked a few named fields of
// a Trace and left everything else, so every field added afterwards (the metadata that carries the
// provider's error and the thinking blocks, a span's error, a score's comment) leaked until someone
// remembered to blank it. These are separate types that carry only what is listed here: a field
// added to Trace or Span reaches a reader without content access only when it is added below, on
// purpose. The handlers return these types, never a Trace, to such a reader.

// WithheldError is what a failed span's error text is replaced with. The text may be the provider's
// reply, which often echoes the prompt.
const WithheldError = "error (text withheld)"

// publicMetadataKeys are the trace and span metadata keys that describe a call and say nothing a user
// or a model wrote. Anything else (error, input, output, thinking_blocks, contexts, ground_truth, the
// A2A response, a replayed trace's original metadata) is withheld. A key is added here after reading
// every place that writes it.
var publicMetadataKeys = map[string]bool{
	"agent": true, "agent_name": true, "attempts": true, "batch_size": true, "blocked_by": true,
	"cache_hit": true, "cached_tokens": true, "cost_usd": true, "duration_ms": true, "fallback": true,
	"finish_reason": true, "format": true, "from_env": true, "to_env": true, "instance": true,
	"kitchen": true, "mode": true, "model": true, "monitor_evaluated": true, "price_source": true,
	"provider": true, "release": true, "replay": true, "replay_by": true, "replay_of": true,
	"replay_span": true, "requested_by": true, "requested_model": true, "run_id": true, "runtime": true,
	"session_id": true, "source": true, "step_name": true, "strategy": true, "task_id": true,
	"thinking_block_count": true, "thinking_enabled": true, "thinking_tokens": true,
	"tool_call_id": true, "turn_number": true, "turns": true, "type": true, "version": true,
}

// maxPublicMetadataString bounds a string value kept in public metadata; a longer one is text, not a label.
const maxPublicMetadataString = 128

// publicMetadata keeps the listed keys whose value is a short string, a number or a boolean.
func publicMetadata(m map[string]interface{}) map[string]interface{} {
	var out map[string]interface{}
	for k, v := range m {
		if k == "guardrails" { // how many spans a rule replaced or warned about, not what they said
			if counts := guardrailCounts(v); counts != nil {
				if out == nil {
					out = map[string]interface{}{}
				}
				out[k] = counts
			}
			continue
		}
		if !publicMetadataKeys[k] {
			continue
		}
		switch t := v.(type) {
		case string:
			if len(t) > maxPublicMetadataString {
				continue
			}
		case bool, float64, float32, int, int32, int64, uint, uint32, uint64:
		default:
			continue
		}
		if out == nil {
			out = map[string]interface{}{}
		}
		out[k] = v
	}
	return out
}

// guardrailCounts keeps the numeric "redacted" and "warned" counts of the guardrails entry, and nothing else.
func guardrailCounts(v interface{}) map[string]interface{} {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	var out map[string]interface{}
	for _, k := range []string{"redacted", "warned"} {
		switch n := m[k].(type) {
		case int, int64, float64:
			if out == nil {
				out = map[string]interface{}{}
			}
			out[k] = n
		}
	}
	return out
}

// PublicTrace is a trace without its content.
type PublicTrace struct {
	ID              string                 `json:"id"`
	AgentName       string                 `json:"agent_name"`
	RecipeName      string                 `json:"recipe_name,omitempty"`
	Kitchen         string                 `json:"kitchen"`
	Status          string                 `json:"status"`
	DurationMs      int64                  `json:"duration_ms"`
	TotalTokens     int64                  `json:"total_tokens"`
	CostUSD         float64                `json:"cost_usd"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	UserID          string                 `json:"user_id,omitempty"`
	CreatedAt       time.Time              `json:"created_at"`
	ParentTraceID   string                 `json:"parent_trace_id,omitempty"`
	SessionID       string                 `json:"session_id,omitempty"`
	Tags            []string               `json:"tags,omitempty"`
	Usage           *TokenUsage            `json:"usage,omitempty"`
	RuntimeMetadata map[string]interface{} `json:"runtime_metadata,omitempty"`
	Environment     string                 `json:"environment,omitempty"`
	AgentVersion    string                 `json:"agent_version,omitempty"`
	ContentWithheld bool                   `json:"content_withheld"`
	Spans           []PublicSpan           `json:"spans,omitempty"`
	Scores          []PublicScore          `json:"scores,omitempty"`
}

// PublicSpan is a span without its content.
type PublicSpan struct {
	ID              string                 `json:"id"`
	TraceID         string                 `json:"trace_id"`
	ParentSpanID    string                 `json:"parent_span_id,omitempty"`
	Name            string                 `json:"name"`
	Kind            SpanKind               `json:"kind"`
	Status          string                 `json:"status"`
	StartTime       time.Time              `json:"start_time"`
	EndTime         time.Time              `json:"end_time"`
	DurationMs      int64                  `json:"duration_ms"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	Usage           *TokenUsage            `json:"usage,omitempty"`
	Model           string                 `json:"model,omitempty"`
	Provider        string                 `json:"provider,omitempty"`
	Error           string                 `json:"error,omitempty"`
	Events          []PublicSpanEvent      `json:"events,omitempty"`
	ContentWithheld bool                   `json:"content_withheld"`
}

// PublicSpanEvent is a span event without its attributes.
type PublicSpanEvent struct {
	Name      string    `json:"name"`
	Timestamp time.Time `json:"timestamp"`
}

// PublicScore is a score without its comment, which is whatever a reviewer typed about the content.
type PublicScore struct {
	ID        string    `json:"id"`
	TraceID   string    `json:"trace_id"`
	SpanID    string    `json:"span_id,omitempty"`
	Name      string    `json:"name"`
	Value     float64   `json:"value"`
	Label     string    `json:"label,omitempty"`
	Source    string    `json:"source"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// ContentWithheld says a comment may exist that this reader may not see.
	ContentWithheld bool `json:"content_withheld"`
}

// Public returns the trace as a reader without content access may see it, spans and scores included.
func (t Trace) Public() PublicTrace {
	p := PublicTrace{
		ID: t.ID, AgentName: t.AgentName, RecipeName: t.RecipeName, Kitchen: t.Kitchen, Status: t.Status,
		DurationMs: t.DurationMs, TotalTokens: t.TotalTokens, CostUSD: t.CostUSD,
		Metadata: publicMetadata(t.Metadata), UserID: t.UserID, CreatedAt: t.CreatedAt,
		ParentTraceID: t.ParentTraceID, SessionID: t.SessionID, Tags: t.Tags, Usage: t.Usage,
		RuntimeMetadata: publicMetadata(t.RuntimeMetadata), Environment: t.Environment,
		AgentVersion: t.AgentVersion, ContentWithheld: true,
	}
	for _, s := range t.Spans {
		p.Spans = append(p.Spans, s.Public())
	}
	for _, sc := range t.Scores {
		p.Scores = append(p.Scores, sc.Public())
	}
	return p
}

// Public returns the span as a reader without content access may see it.
func (s Span) Public() PublicSpan {
	p := PublicSpan{
		ID: s.ID, TraceID: s.TraceID, ParentSpanID: s.ParentSpanID, Name: s.Name, Kind: s.Kind, Status: s.Status,
		StartTime: s.StartTime, EndTime: s.EndTime, DurationMs: s.DurationMs, Metadata: publicMetadata(s.Metadata),
		Usage: s.Usage, Model: s.Model, Provider: s.Provider, ContentWithheld: true,
	}
	if s.Error != "" {
		p.Error = WithheldError
	}
	for _, e := range s.Events {
		p.Events = append(p.Events, PublicSpanEvent{Name: e.Name, Timestamp: e.Timestamp})
	}
	return p
}

// Public returns the score as a reader without content access may see it.
func (sc TraceScore) Public() PublicScore {
	return PublicScore{ID: sc.ID, TraceID: sc.TraceID, SpanID: sc.SpanID, Name: sc.Name, Value: sc.Value,
		Label: sc.Label, Source: sc.Source, CreatedBy: sc.CreatedBy, CreatedAt: sc.CreatedAt, ContentWithheld: true}
}

// PublicTraces, PublicSpans and PublicScores convert a list.
func PublicTraces(ts []Trace) []PublicTrace {
	out := make([]PublicTrace, len(ts))
	for i, t := range ts {
		out[i] = t.Public()
	}
	return out
}

func PublicSpans(ss []Span) []PublicSpan {
	out := make([]PublicSpan, len(ss))
	for i, s := range ss {
		out[i] = s.Public()
	}
	return out
}

func PublicScores(scs []TraceScore) []PublicScore {
	out := make([]PublicScore, len(scs))
	for i, sc := range scs {
		out[i] = sc.Public()
	}
	return out
}

// PublicSession is a conversation without its messages or its free-form metadata.
type PublicSession struct {
	ID              string        `json:"id"`
	AgentName       string        `json:"agent_name"`
	Kitchen         string        `json:"kitchen"`
	UserID          string        `json:"user_id,omitempty"`
	Status          SessionStatus `json:"status"`
	MaxTurns        int           `json:"max_turns,omitempty"`
	TurnCount       int           `json:"turn_count"`
	TotalTokens     int64         `json:"total_tokens"`
	TotalCost       float64       `json:"total_cost_usd"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	ExpiresAt       *time.Time    `json:"expires_at,omitempty"`
	ContentWithheld bool          `json:"content_withheld"`
}

// Public returns the session as a reader without content access may see it.
func (s Session) Public() PublicSession {
	return PublicSession{ID: s.ID, AgentName: s.AgentName, Kitchen: s.Kitchen, UserID: s.UserID, Status: s.Status,
		MaxTurns: s.MaxTurns, TurnCount: s.TurnCount, TotalTokens: s.TotalTokens, TotalCost: s.TotalCost,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, ExpiresAt: s.ExpiresAt, ContentWithheld: true}
}

// PublicSessions converts a list.
func PublicSessions(ss []Session) []PublicSession {
	out := make([]PublicSession, len(ss))
	for i, s := range ss {
		out[i] = s.Public()
	}
	return out
}
