# Release 8 Observability Verification

Date: 2026-05-31

This checklist verifies end-to-end tracking, tracing, and observability behavior after the Release 8 rollout.

## Scope

- OTEL trace + metrics export enabled from control plane
- Prometheus scrapeable metrics emitted by collector
- Provider call latency/token/cost counters emitted from executor path
- Guardrail and compliance stream observability signals emitted
- Tenant and audit API consistency hardening validated

## Canonical Verification Metrics

## 1) Coverage

- `metric_emission_coverage_percent`
  - Definition: instrumented critical flows / total critical flows
  - Target: >= 95%

## 2) Correlation Integrity

- `trace_audit_correlation_ratio`
  - Definition: audit events with correlation metadata / total auditable events
  - Target: >= 99%

## 3) Freshness

- `telemetry_export_lag_seconds_p95`
  - Target: <= 5s

## 4) Reliability

- `metrics_drop_rate`
  - Target: <= 0.1%
- `sink_delivery_success_ratio`
  - Target: >= 99.9%

## 5) Accuracy

- `token_accounting_error_percent`
  - Target: <= 1%
- `cost_accounting_error_percent`
  - Target: <= 1%

## 6) Cardinality Safety

- `high_cardinality_label_violations_total`
  - Target: 0

## 7) Compliance Signal Quality

- `guardrail_blocked_to_audit_written_ratio`
  - Target: 100%
- `thinking_missing_when_required_total`
  - Target: 0 where thinking audit is required

## Runtime Metric Names Introduced in Release 8

- `agentoven.http.requests_total`
- `agentoven.http.requests_failed_total`
- `agentoven.http.request.duration_ms`
- `agentoven.provider.call.duration_ms`
- `agentoven.provider.cost_usd`
- `agentoven.tokens.input`
- `agentoven.tokens.output`
- `agentoven.provider.errors_total`
- `agentoven.audit.events_total`
- `agentoven.guardrail.blocked_total`
- `agentoven.compliance.streams.active`

## Smoke Test Procedure

## A) Start local stack

1. `docker compose up -d` in `agentoven/`
2. Verify collector ports:
   - OTLP gRPC: `4317`
   - Prometheus scrape: `8889`

## B) Generate traffic

1. Invoke at least one managed agent request path.
2. Trigger one guardrail block event.
3. Open one compliance failure SSE stream.

## C) Validate metrics endpoint

1. `curl -s http://localhost:8889/metrics | grep '^agentoven_'`
2. Confirm all metric families appear.

## D) Validate trace + audit consistency

1. Fetch trace detail by id and verify kitchen scoping behavior.
2. Fetch audit list and verify canonical `timestamp` and compatibility `created_at` are present.
3. Verify audit count filtering matches list filtering for `action`, `user_id`, and `resource`.

## E) Failure behavior

1. Set invalid `OTEL_SINK_ENDPOINT` and restart collector.
2. Confirm local Prometheus metrics continue and request flow remains healthy.

## Release Gate

Release 8 observability is green when:

1. Control-plane tests pass.
2. All canonical metric families are emitted.
3. Trace/audit consistency checks pass.
4. No tenant-crossing data exposure is observed on trace/span detail endpoints.
