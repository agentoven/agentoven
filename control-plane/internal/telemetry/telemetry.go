package telemetry

import (
	"context"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/internal/config"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Init sets up OpenTelemetry tracing with OTLP gRPC exporter.
// Accepts config.TelemetryConfig to match how main.go calls it.
// Returns a shutdown function that should be called on graceful shutdown.
func Init(cfg config.TelemetryConfig) (func(context.Context) error, error) {
	if !cfg.Enabled || cfg.OTLPEndpoint == "" {
		log.Info().Msg("🔕 OpenTelemetry disabled")
		return func(ctx context.Context) error { return nil }, nil
	}

	ctx := context.Background()

	// Create OTLP gRPC exporter
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlptracegrpc.WithInsecure(), // Phase 1: insecure for local dev; production should use TLS via OTEL_EXPORTER_OTLP_CERTIFICATE
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP exporter: %w", err)
	}

	// Create resource with service metadata
	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.version", "0.1.0"),
		),
		resource.WithHost(),
		resource.WithOS(),
		resource.WithProcess(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Use AlwaysSample in dev; production should use TraceIDRatioBased(0.1) or ParentBased
	sampler := sdktrace.AlwaysSample()

	// Create trace provider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	// Register globally
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Info().
		Str("endpoint", cfg.OTLPEndpoint).
		Str("service", cfg.ServiceName).
		Msg("📡 OpenTelemetry tracing initialized")

	return tp.Shutdown, nil
}
