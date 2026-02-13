package middleware

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("agentoven-control-plane")

// Telemetry returns OpenTelemetry tracing middleware.
func Telemetry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract propagated context from incoming headers
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		// Start a new span for this request
		spanName := r.Method + " " + r.URL.Path
		ctx, span := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPMethodKey.String(r.Method),
				semconv.HTTPTargetKey.String(r.URL.Path),
				semconv.HTTPSchemeKey.String(scheme(r)),
				attribute.String("agentoven.kitchen", GetKitchen(ctx)),
			),
		)
		defer span.End()

		rw := newResponseWriter(w)

		next.ServeHTTP(rw, r.WithContext(ctx))

		// Record response status
		span.SetAttributes(
			semconv.HTTPStatusCodeKey.Int(rw.statusCode),
			attribute.Int("http.response_content_length", rw.bytes),
		)
	})
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		return fwd
	}
	return "http"
}
