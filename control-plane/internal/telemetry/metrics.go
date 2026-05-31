package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	metricsOnce sync.Once

	httpRequestsTotal       metric.Int64Counter
	httpRequestsFailedTotal metric.Int64Counter
	httpRequestDurationMs   metric.Float64Histogram

	providerCallDurationMs metric.Float64Histogram
	providerCostUSD        metric.Float64Counter
	tokensInputTotal       metric.Int64Counter
	tokensOutputTotal      metric.Int64Counter
	providerErrorsTotal    metric.Int64Counter

	auditEventsTotal      metric.Int64Counter
	guardrailBlockedTotal metric.Int64Counter

	complianceStreamsActive metric.Int64UpDownCounter
)

func initMetrics() {
	meter := otel.Meter("agentoven-control-plane")

	httpRequestsTotal, _ = meter.Int64Counter("agentoven.http.requests_total",
		metric.WithDescription("Total HTTP requests served by the control plane"),
	)
	httpRequestsFailedTotal, _ = meter.Int64Counter("agentoven.http.requests_failed_total",
		metric.WithDescription("Total failed HTTP requests (4xx/5xx)"),
	)
	httpRequestDurationMs, _ = meter.Float64Histogram("agentoven.http.request.duration_ms",
		metric.WithDescription("HTTP request duration in milliseconds"),
	)

	providerCallDurationMs, _ = meter.Float64Histogram("agentoven.provider.call.duration_ms",
		metric.WithDescription("End-to-end latency per provider call in milliseconds"),
	)
	providerCostUSD, _ = meter.Float64Counter("agentoven.provider.cost_usd",
		metric.WithDescription("Accumulated provider cost in USD"),
	)
	tokensInputTotal, _ = meter.Int64Counter("agentoven.tokens.input",
		metric.WithDescription("Input tokens consumed by providers"),
	)
	tokensOutputTotal, _ = meter.Int64Counter("agentoven.tokens.output",
		metric.WithDescription("Output tokens consumed by providers"),
	)
	providerErrorsTotal, _ = meter.Int64Counter("agentoven.provider.errors_total",
		metric.WithDescription("Provider call errors"),
	)

	auditEventsTotal, _ = meter.Int64Counter("agentoven.audit.events_total",
		metric.WithDescription("Audit events recorded by the control plane"),
	)
	guardrailBlockedTotal, _ = meter.Int64Counter("agentoven.guardrail.blocked_total",
		metric.WithDescription("Guardrail-blocked operations"),
	)

	complianceStreamsActive, _ = meter.Int64UpDownCounter("agentoven.compliance.streams.active",
		metric.WithDescription("Active compliance SSE streams"),
	)
}

func ensureMetrics() {
	metricsOnce.Do(initMetrics)
}

func RecordHTTPRequest(ctx context.Context, method, route, kitchen string, statusCode int, duration time.Duration) {
	ensureMetrics()

	statusClass := strconv.Itoa(statusCode/100) + "xx"
	attrs := metric.WithAttributes(
		attribute.String("method", method),
		attribute.String("route", route),
		attribute.String("kitchen", kitchen),
		attribute.String("status_class", statusClass),
	)

	httpRequestsTotal.Add(ctx, 1, attrs)
	httpRequestDurationMs.Record(ctx, duration.Seconds()*1000, attrs)
	if statusCode >= http.StatusBadRequest {
		httpRequestsFailedTotal.Add(ctx, 1, attrs)
	}
}

func RecordProviderCall(ctx context.Context, kitchen, provider, model, status string, duration time.Duration, inputTokens, outputTokens int64, costUSD float64) {
	ensureMetrics()

	attrs := metric.WithAttributes(
		attribute.String("kitchen", kitchen),
		attribute.String("provider", provider),
		attribute.String("model", model),
		attribute.String("status", status),
	)

	providerCallDurationMs.Record(ctx, duration.Seconds()*1000, attrs)
	if inputTokens > 0 {
		tokensInputTotal.Add(ctx, inputTokens, attrs)
	}
	if outputTokens > 0 {
		tokensOutputTotal.Add(ctx, outputTokens, attrs)
	}
	if costUSD > 0 {
		providerCostUSD.Add(ctx, costUSD, attrs)
	}
	if status != "ok" {
		providerErrorsTotal.Add(ctx, 1, attrs)
	}
}

func RecordAuditEvent(ctx context.Context, kitchen, action string, responseStatus int) {
	ensureMetrics()
	auditEventsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kitchen", kitchen),
		attribute.String("action", action),
		attribute.String("status_class", strconv.Itoa(responseStatus/100)+"xx"),
	))
}

func RecordGuardrailBlocked(ctx context.Context, kitchen, stage string) {
	ensureMetrics()
	guardrailBlockedTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kitchen", kitchen),
		attribute.String("stage", stage),
	))
}

func ComplianceStreamConnected(ctx context.Context, kitchen string) {
	ensureMetrics()
	complianceStreamsActive.Add(ctx, 1, metric.WithAttributes(attribute.String("kitchen", kitchen)))
}

func ComplianceStreamDisconnected(ctx context.Context, kitchen string) {
	ensureMetrics()
	complianceStreamsActive.Add(ctx, -1, metric.WithAttributes(attribute.String("kitchen", kitchen)))
}
