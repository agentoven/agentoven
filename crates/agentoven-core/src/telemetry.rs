//! Telemetry — OpenTelemetry integration for AgentOven.
//!
//! Instruments all agent invocations, model calls, and workflow steps
//! with distributed traces.

use opentelemetry::trace::TracerProvider;
use opentelemetry::KeyValue;
use opentelemetry_otlp::WithExportConfig;
use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::util::SubscriberInitExt;
use tracing_subscriber::EnvFilter;

use crate::config::TelemetryConfig;

/// Initialize the AgentOven telemetry pipeline.
///
/// Sets up:
/// - Structured JSON logging
/// - OpenTelemetry tracing with OTLP export
/// - Environment-based log filtering
pub fn init_telemetry(config: &TelemetryConfig) -> anyhow::Result<()> {
    let env_filter = EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| EnvFilter::new("info,agentoven=debug,a2a_ao=debug"));

    let fmt_layer = tracing_subscriber::fmt::layer()
        .json()
        .with_target(true)
        .with_thread_ids(true)
        .with_file(true)
        .with_line_number(true);

    if config.enabled {
        let exporter = opentelemetry_otlp::SpanExporter::builder()
            .with_tonic()
            .with_endpoint(&config.otlp_endpoint)
            .build()?;

        let resource = opentelemetry_sdk::Resource::new(vec![
            KeyValue::new("service.name", "agentoven"),
            KeyValue::new("service.version", "0.1.0"),
        ]);

        let provider = opentelemetry_sdk::trace::TracerProvider::builder()
            .with_simple_exporter(exporter)
            .with_resource(resource)
            .build();

        let tracer = provider.tracer("agentoven");
        let otel_layer = tracing_opentelemetry::layer().with_tracer(tracer);

        tracing_subscriber::registry()
            .with(env_filter)
            .with(fmt_layer)
            .with(otel_layer)
            .init();
    } else {
        tracing_subscriber::registry()
            .with(env_filter)
            .with(fmt_layer)
            .init();
    }

    Ok(())
}
