# ADR-0019: OTel Metrics Pipeline and Multi-Sink Collector

- **Date:** 2026-05-18
- **Status:** Accepted
- **Deciders:** siddartha
- **Relates to:** [ADR-0018](0018-remote-provider-plugin-protocol.md)

## Context

The current telemetry stack emits only **traces** (to Jaeger via OTLP gRPC). Cost and
token usage are tracked as trace *attributes* on spans (`agent.cost_usd`,
`llm.cost_usd`), which means they are:

1. **Not queryable as time-series** — you cannot draw a cost-over-time chart or set a
   Prometheus alert rule from span attributes.
2. **Invisible to budget enforcement** — the router cannot check a running total without
   a separate in-memory accumulator, which resets on restart and is not shared across
   replicas.
3. **Not exportable to external observability platforms** (Datadog, Grafana Cloud,
   CloudWatch) without custom span processors.

Provider plugin authors (ADR-0018) also need a standard way to emit their own metrics
(upstream latency, error rates, quota headroom from the upstream API) into the same
pipeline.

## Decision

### Add OTel Metrics SDK

The `telemetry.go` initialisation gains a `MeterProvider` alongside the existing
`TracerProvider`:

```go
// Init returns both providers; callers use telemetry.Meter("agentoven/router")
func Init(cfg Config) (shutdown func(context.Context) error, err error)
```

The metrics exporter targets the same OTel Collector endpoint (`cfg.OTLPEndpoint`)
using OTLP/gRPC, sharing the connection with the trace exporter. The collector routes
metrics and traces to their respective pipelines independently.

### Canonical Metric Set

All metrics use the `agentoven.` namespace and carry a standard set of labels.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `agentoven.provider.call.duration_ms` | Histogram | provider, model, kitchen, status | End-to-end latency per provider call |
| `agentoven.provider.cost_usd` | Counter | provider, model, kitchen, agent | Accumulated cost in USD |
| `agentoven.tokens.input` | Counter | provider, model, kitchen, agent | Input (prompt) tokens consumed |
| `agentoven.tokens.output` | Counter | provider, model, kitchen, agent | Output (completion) tokens consumed |
| `agentoven.provider.budget_remaining_usd` | Gauge | provider, kitchen | Remaining budget (set by Pro budget module) |
| `agentoven.provider.health` | Gauge | provider, kitchen | `0`=healthy `1`=degraded `2`=unhealthy (from ProviderWatcher) |
| `agentoven.provider.errors_total` | Counter | provider, model, kitchen, error_code | Provider call errors |

These replace the existing trace-attribute-only cost tracking. The in-memory
`CostSummary` struct in `router.go` is kept as a fast read path for the dashboard API
but is now populated by reading metric state rather than accumulating independently.

### Multi-Sink OTel Collector Configuration

`agentoven/infra/otel-collector-config.yaml` is restructured into **named pipelines**
so traces and metrics flow to their own set of exporters, and operators can add
custom sinks by appending to the exporters section:

```yaml
receivers:
  otlp:
    protocols:
      grpc: { endpoint: "0.0.0.0:4317" }
      http: { endpoint: "0.0.0.0:4318" }

processors:
  batch:
    timeout: 1s
    send_batch_size: 1024
  resource:
    attributes:
      - key: service.namespace
        value: agentoven
        action: insert

exporters:
  # ── Traces ──────────────────────────────────────────────
  otlp/jaeger:
    endpoint: jaeger:4317
    tls: { insecure: true }

  # ── Metrics ─────────────────────────────────────────────
  prometheus:
    endpoint: "0.0.0.0:8889"   # Prometheus scrapes this
    namespace: agentoven

  # ── Generic OTLP/HTTP sink (Datadog, Grafana Cloud, etc.) ──
  # Operators enable this by setting OTEL_SINK_ENDPOINT env var.
  # The sink block is present but disabled (no_op) when the env
  # var is unset; the collector resolves it at startup.
  otlphttp/custom:
    endpoint: "${env:OTEL_SINK_ENDPOINT}"
    headers:
      "X-Sink-Token": "${env:OTEL_SINK_TOKEN}"
    sending_queue:
      enabled: true
      num_consumers: 4
    retry_on_failure:
      enabled: true

  debug:
    verbosity: basic

service:
  pipelines:
    traces:
      receivers:  [otlp]
      processors: [resource, batch]
      exporters:  [otlp/jaeger, debug]

    metrics:
      receivers:  [otlp]
      processors: [resource, batch]
      exporters:  [prometheus, debug]

    # Opt-in unified pipeline for external sinks (traces + metrics)
    traces/external:
      receivers:  [otlp]
      processors: [resource, batch]
      exporters:  [otlphttp/custom]

    metrics/external:
      receivers:  [otlp]
      processors: [resource, batch]
      exporters:  [otlphttp/custom]
```

Operators add a custom sink by setting two environment variables on the collector
container: `OTEL_SINK_ENDPOINT` and `OTEL_SINK_TOKEN`. No collector config file
changes are needed for the common case.

### Provider Plugin Metrics Extension

Provider servers (ADR-0018) can emit their own metrics by:
1. Being given the same OTLP endpoint as an environment variable at spawn time (managed
   mode) or via a field in `ConfigureRequest` (supervised-external mode).
2. Using any OTel SDK to emit metrics under the `agentoven.provider.<kind>.*` namespace.

These flow through the same collector pipeline and appear alongside built-in metrics.
The control plane does not need to understand provider-specific metric schemas.

### Budget Gauge Update

The `agentoven.provider.budget_remaining_usd` gauge is updated by the Pro budget
enforcement layer (ADR-0024) after each call and after each janitor reset. OSS
deployments that do not configure budgets see this gauge as `+Inf` (no limit).

## Consequences

- **Easier:** Cost and usage are now queryable as time-series from Prometheus/Grafana.
  Standard OTel pipeline means any OTLP-compatible backend works with two env vars.
  Provider plugin authors can add custom metrics without control-plane changes.
- **Harder:** `telemetry.go` initialisation gains a `MeterProvider` and a second
  OTLP exporter; this increases startup complexity slightly. The `CostSummary` struct
  in `router.go` needs to be kept in sync with the metric counters (or replaced by
  reading from the metric SDK's observable API — preferred long-term).
- **Operational:** Prometheus needs to be told to scrape port `8889` on the collector.
  The Grafana dashboard provisioning in `infra/` should be updated with pre-built
  panels for the canonical metric set.

## Alternatives Considered

1. **Push cost data to Postgres and query from there** — Rejected for the metrics use
   case: not time-series-native, high write amplification, slower alerting latency.
   Postgres is still right for budget *configuration* (ADR-0024).
2. **Custom Prometheus pushgateway per agent** — Rejected: pushgateway has known
   staleness issues; OTel is already in the stack.
3. **Separate metrics service** — Rejected: adds a new operational component. The OTel
   Collector is already deployed; adding a pipeline is zero marginal overhead.
