package middleware

import (
	"net/http"
	"time"

	inttelemetry "github.com/agentoven/agentoven/control-plane/internal/telemetry"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("agentoven-control-plane")

// Telemetry returns OpenTelemetry tracing middleware.
func Telemetry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		kitchen := GetKitchen(r.Context())

		// Extract propagated context from incoming headers
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		// Start a new span for this request
		spanName := r.Method + " " + r.URL.Path
		ctx, span := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("url.path", r.URL.Path),
				attribute.String("url.scheme", scheme(r)),
				attribute.String("agentoven.kitchen", kitchen),
			),
		)
		defer span.End()

		rw := newResponseWriter(w)

		next.ServeHTTP(rw, r.WithContext(ctx))

		// Record response status
		span.SetAttributes(
			attribute.Int("http.response.status_code", rw.statusCode),
			attribute.Int("http.response_content_length", rw.bytes),
		)

		routePattern := r.URL.Path
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			if p := rctx.RoutePattern(); p != "" {
				routePattern = p
			}
		}
		inttelemetry.RecordHTTPRequest(ctx, r.Method, routePattern, kitchen, rw.statusCode, time.Since(start))
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
