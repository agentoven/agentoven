package models

import (
	"context"
	"time"
)

// LLMCall is one model call exactly as it was made and as it was answered. It is what a capture hook
// is handed: the complete request the router received, the complete response, and where the call
// sits in its trace. ADR-0039 (Pro) stores it whole so the call can be replayed later.
type LLMCall struct {
	TraceID string
	// SpanID is the span the call is recorded under. The span may be written after the call (the
	// executor writes a run's spans when the run ends), so the ID is chosen when the call is made.
	SpanID    string
	Kitchen   string
	AgentName string
	Turn      int // the executor's turn number; 0 for a call made through the router directly
	StartedAt time.Time

	// Request is a snapshot of what the router received. The caller must not change it afterwards.
	Request *RouteRequest
	// Response is what answered it: nil when no provider did, with Error saying why.
	Response *RouteResponse
	Error    string
}

// LLMCallCapturer receives every model call that gets a span. It is the one seam through which a
// deployment records complete requests and responses; the community edition installs none and keeps
// today's span, which holds the messages and the answer.
//
// CaptureLLMCall is called on the request path, once per call, after the model has answered. An error
// is logged and the span is marked as not captured; it never fails the call.
type LLMCallCapturer interface {
	CaptureLLMCall(ctx context.Context, call LLMCall) error
}

// TraceStamp is what a trace says about where it ran, carried on the request context so every place
// that writes a trace records it the same way without each one being told.
type TraceStamp struct {
	// Environment is the slug of the environment the call was made for ("staging", "prod").
	Environment string
	// AgentVersion is the baked version of the agent that ran, when known before the trace is written.
	AgentVersion string
	// TraceID is the trace of the run in progress, when it was chosen before the run started, so the
	// audit events the run causes can say which trace they belong to.
	TraceID string
}

type traceStampKey struct{}

// WithTraceStamp returns ctx carrying the stamp, merged over any stamp already there (a field left
// empty keeps what the outer stamp set).
func WithTraceStamp(ctx context.Context, s TraceStamp) context.Context {
	if cur, ok := ctx.Value(traceStampKey{}).(TraceStamp); ok {
		if s.Environment == "" {
			s.Environment = cur.Environment
		}
		if s.AgentVersion == "" {
			s.AgentVersion = cur.AgentVersion
		}
		if s.TraceID == "" {
			s.TraceID = cur.TraceID
		}
	}
	return context.WithValue(ctx, traceStampKey{}, s)
}

// TraceStampFrom returns the stamp on ctx (zero when none).
func TraceStampFrom(ctx context.Context) TraceStamp {
	s, _ := ctx.Value(traceStampKey{}).(TraceStamp)
	return s
}

// Stamp fills the trace's environment and agent version from ctx when the trace does not already
// name them. A store calls it on every trace it creates.
func (t *Trace) Stamp(ctx context.Context) {
	s := TraceStampFrom(ctx)
	if t.Environment == "" {
		t.Environment = s.Environment
	}
	if t.AgentVersion == "" {
		t.AgentVersion = s.AgentVersion
	}
}

// StampTrace gives the event its trace: the one it already names, else the one in its details (the
// executor's run events carry it there), else the run in progress on ctx. When it has one, it is
// written into Details["trace_id"] as well, so a hash chain over the details attests it. A store
// calls it on every audit event it creates, before the event is hashed.
func (e *AuditEvent) StampTrace(ctx context.Context) {
	if e.TraceID == "" {
		if v, ok := e.Details["trace_id"].(string); ok && v != "" {
			e.TraceID = v
		} else {
			e.TraceID = TraceStampFrom(ctx).TraceID
		}
	}
	if e.TraceID == "" {
		return
	}
	if e.Details == nil {
		e.Details = map[string]interface{}{}
	}
	if _, ok := e.Details["trace_id"]; !ok {
		e.Details["trace_id"] = e.TraceID
	}
}
