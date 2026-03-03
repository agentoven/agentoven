# ADR-0009: Pluggable Test Runner Architecture

- **Date:** 2026-02-23
- **Status:** Accepted
- **Deciders:** siddartha

## Context

AgentOven needs agent evaluation (test suites) to close the primary gap
against LangSmith's "Datasets & Experiments" feature. The initial
implementation (Release 7.5) runs test suites **synchronously in the HTTP
handler goroutine** — the executor calls the agent's test endpoint
sequentially for each case and blocks the HTTP request until all cases
complete. The scheduler fires unbounded goroutines for due suites.

### Problems with the Current Approach

1. **API server pressure** — test execution (LLM calls for every case) runs
   inside the Go API process. A 50-case suite against a slow model blocks an
   HTTP handler for minutes, consuming goroutines and memory.
2. **No concurrency control** — the scheduler fires `go executor.RunSuite()`
   per due suite with no bounded pool. 20 suites due at 09:00 → 20 unbounded
   goroutines × N HTTP self-calls each.
3. **Synchronous handler** — `POST /test-suites/{id}/run` blocks until all
   cases complete. Clients must hold the connection open (minutes), or risk
   timeouts and retries that re-trigger the entire suite.
4. **No external backend** — enterprises need to run tests on dedicated
   infrastructure: Celery workers, Temporal workflows, Kubernetes Jobs, or
   CI/CD pipelines. The current design hard-wires the executor into the API
   process.
5. **Kitchen-scoped config** — different kitchens may need different runner
   backends (team A uses local, team B uses Temporal). The current executor is
   global and has no per-kitchen configuration.
6. **No RBAC / ABAC** — test suite operations have no permission checks. Any
   authenticated user can create, run, delete, or schedule any test suite in
   their kitchen. Enterprises need role-based and attribute-based access control.

### Design Constraints

- **Two-repo model** — the `TestRunnerBackend` **interface** lives in OSS
  (`pkg/contracts/`). All **implementations** (local, Celery, Temporal, K8s)
  live in Pro. OSS ships a no-op community backend.
- **Kitchen vocabulary** — configuration lives in `KitchenSettings`, not in
  a new top-level object.
- **Backward compatible** — the local backend (bounded goroutine pool) is the
  default and works without any external infrastructure.

## Decision

### 1. TestRunnerBackend Interface (OSS contracts)

```go
// pkg/contracts/contracts.go

type TestRunnerBackend interface {
    // SubmitRun dispatches a test suite run to the backend for async execution.
    // Returns a run ID immediately. Execution happens asynchronously.
    SubmitRun(ctx context.Context, suite *models.TestSuite, trigger, createdBy string) (runID string, err error)

    // CancelRun requests cancellation of a running test suite execution.
    CancelRun(ctx context.Context, runID string) error

    // GetRunStatus returns the current status of a submitted test run.
    GetRunStatus(ctx context.Context, runID string) (*models.TestRun, error)

    // Kind returns the backend identifier (e.g. "local", "celery", "temporal", "k8s").
    Kind() string

    // HealthCheck verifies the backend is reachable and operational.
    HealthCheck(ctx context.Context) error
}
```

OSS ships `CommunityTestRunnerBackend` — a no-op that returns
`"test suites are a Pro feature"` errors.

### 2. Async Execution Model

```
Client                API Handler              Backend               Worker
  │                      │                        │                     │
  ├─POST /run───────────►│                        │                     │
  │                      ├─SubmitRun()───────────►│                     │
  │                      │◄─runID────────────────┤│                     │
  │◄─202 Accepted────────┤                        │                     │
  │  {run_id, pending}   │                        ├──dispatch──────────►│
  │                      │                        │                     │
  │                      │                        │  (execute cases)    │
  │                      │                        │◄──callback/result───┤
  │                      │                        │                     │
  ├─GET /runs/{id}──────►│                        │                     │
  │◄─{status, results}──┤│                        │                     │
```

- `POST /test-suites/{id}/run` returns **202 Accepted** with `{"run_id": "...", "status": "pending"}`
- Client polls `GET /test-suites/{id}/runs/{runID}` for progress
- External backends call `POST /api/v1/test-runs/{runID}/callback` with results

### 3. Backend Implementations

The `TestRunnerBackend` interface enables multiple execution backends:

| Backend | How It Works | Use Case |
|---------|-------------|----------|
| **local** (default) | Bounded goroutine pool (max N concurrent per kitchen) | Dev, small teams |

Pro provides additional backend implementations for enterprise workloads.
See the Pro repo for details.

### 4. Kitchen-Level Configuration

`KitchenSettings` gains test runner configuration:

```go
// pkg/models/models.go — inside KitchenSettings struct

// Test runner backend configuration
TestRunnerBackend string            `json:"test_runner_backend,omitempty"` // "local" (OSS), others via Pro
TestRunnerConfig  map[string]string `json:"test_runner_config,omitempty"` // backend-specific config
MaxConcurrentRuns int               `json:"max_concurrent_runs,omitempty"` // concurrency limit per kitchen
```

### 5. Plan Limits

Test suite execution is subject to plan limits defined by `PlanLimits`.
OSS ships the `CommunityLocalTestRunner` (ADR-0011) for basic testing.
Pro provides enhanced limits, scheduling, and external backends. See the
Pro repo for tier-specific quota details.

### 6. Access Control

Test suite operations are subject to the standard RBAC system defined
in AUTH-PLAN.md. Pro provides role-based and attribute-based access control
for test suite operations. See the Pro repo for permission matrices.

### 7. Attribute-Based Access Control (future)

Pro supports attribute-based policies (ABAC) evaluated after RBAC for
fine-grained access control. See the Pro repo for the ABAC policy model.

### 8. Callback Endpoint for External Backends

```
POST /api/v1/test-runs/{runID}/callback
X-Service-Token: <HMAC-signed token>

{
    "status": "completed",
    "results": [...],
    "summary": {...}
}
```

Only service account tokens are accepted on the callback endpoint (not user
API keys) to prevent result tampering. The backend receives a signed service
token when SubmitRun is called.

### 9. Scheduler Refactoring

The scheduler becomes a pluggable component:

- **Embedded** (default) — in-process goroutine, checks every 1 min (current)
- **Standalone** (future) — separate binary/container for horizontal scaling
- All scheduler instances use `TestRunnerBackend.SubmitRun()` instead of
  directly calling the executor

The scheduler scans all kitchens (not just `"default"`) by listing kitchens
from the store when `ListKitchens()` is available, with a fallback to known
kitchen names from active test suites.

## Consequences

### Positive

- **Decoupled execution** — LLM calls happen outside the API process when using
  external backends, eliminating API server pressure.
- **Bounded concurrency** — even the local backend uses a goroutine pool, preventing
  unbounded goroutine spawning.
- **Async API** — 202 Accepted + polling is idiomatic REST for long-running operations.
  No more timeouts on large test suites.
- **Kitchen autonomy** — each kitchen can choose its own execution backend and
  concurrency limits. Platform teams configure Temporal; dev teams use local.
- **Access control** — standard RBAC and ABAC controls are available via the
  Pro repo to govern test suite operations.
- **Two-repo clean** — interface in OSS, enhanced implementations in Pro.

### Negative

- **More moving parts** — external backends (available via Pro) require additional
  infrastructure. Local backend mitigates this for small deployments.
- **Callback security** — the callback endpoint is a new attack surface. Mitigated
  by requiring service account tokens with HMAC verification.

### Neutral

- **Backward compatible** — the local backend reproduces current behavior (minus
  the unbounded goroutines). Existing test suites work without configuration changes.
- **Pro-only feature** — test suite execution remains a Pro feature. OSS ships
  only the models, store interface, and contract interface. The local backend
  is available in OSS for basic testing (see ADR-0011).

## Related

- [ADR-0003: Open-Core Two-Repo Model](0003-open-core-two-repo-model.md)
- [ADR-0008: Three-Layer Product Architecture](0008-three-layer-product-architecture.md)
- [AUTH-PLAN.md](../AUTH-PLAN.md) — pluggable authentication architecture
- [ISSUES.md](../ISSUES.md) — ISS-004 (BakeAgent race condition, similar pattern)
