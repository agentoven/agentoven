# ADR-0014: Pluggable Scheduler Dispatcher Abstraction

- **Status:** Accepted
- **Date:** 2026-05-05
- **Relates to:** [ADR-0009](0009-pluggable-test-runner-architecture.md) (test runner backends), [OSS ADR-0011](0011-oss-local-test-runner.md)

## Context

The embedded recipe scheduler dispatches jobs by calling an internal HTTP endpoint in the same process (`HTTPRecipeRunner`). This works for monolithic deployments but breaks down in three scenarios:

1. **Azure Functions / serverless**: Trigger functions via HTTPS with HMAC-signed payloads rather than internal calls.
2. **Airflow**: Users want native DAG-based scheduling; AO should export DAG definitions and let Airflow call back.
3. **Standalone scheduler binary**: When the scheduler runs as a separate Kubernetes pod it still must dispatch to the API server, but over the network.

A hardcoded `http.Client` call is not extensible. We need a dispatcher interface so each backend can be swapped without touching scheduler logic.

## Decision

### Dispatcher Interface

```go
// internal/scheduler/dispatcher.go
type JobPayload struct {
    KitchenID   string
    JobName     string
    JobType     string // "recipe" | "suite"
    TriggeredBy string
    ScheduleID  string
}

type Dispatcher interface {
    Dispatch(ctx context.Context, job JobPayload) error
    HealthCheck(ctx context.Context) error
}
```

### Implementations

| Name | Type string | Description |
|------|-------------|-------------|
| `HTTPDispatcher` | `"http"` | Internal POST to `/api/v1/kitchens/{k}/recipes/{r}/bake`; current default |
| `AzureFunctionDispatcher` | `"azfunc"` | POST to Azure Function URL; HMAC-SHA256 `X-AO-Sig` header; retries with exponential backoff |
| `AirflowDispatcher` | `"airflow"` | Triggers Airflow DAG run via Airflow REST API v1 with Basic auth |

### Factory

```go
func NewDispatcher(kind string, cfg map[string]string) (Dispatcher, error)
```

`SCHEDULER_DISPATCHER` env var (default `"http"`). Config keys per kind:
- `http`: `BASE_URL`
- `azfunc`: `AZFUNC_URL`, `AZFUNC_SECRET`
- `airflow`: `AIRFLOW_URL`, `AIRFLOW_USER`, `AIRFLOW_PASSWORD`

### Backward Compatibility

When `SCHEDULER_DISPATCHER` is unset or `"http"`, existing behaviour is preserved exactly. The `BackgroundScheduler` receives a `Dispatcher` not a concrete `HTTPRecipeRunner`.

## Consequences

- **+** Scheduler logic is fully decoupled from transport.
- **+** Azure Functions users get first-class integration with proper HMAC auth.
- **+** Airflow shops can export DAGs and have Airflow call AO back.
- **–** Three env vars must be documented per deployment target.
- **–** `AirflowDispatcher` triggers a DAG run but does not wait for completion; status tracking is one-way unless the DAG calls a callback.
