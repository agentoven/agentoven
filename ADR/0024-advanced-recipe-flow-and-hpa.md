# ADR-0024: Advanced Recipe Flow Control — Gate Branching, Loop Controls, Multi-Scope Rate Limiting, and K8s HPA in Agent Form

**Status:** Proposed  
**Date:** 2026-07-07  
**Authors:** AgentOven Engineering

---

## Context

Recipe execution (ADR-0009, ADR-0011) supports agent steps, human gates, fan-out/fan-in, conditional routing, loops, map/reduce, and sub-recipes. Four gaps have been identified from production usage:

### Gap 1 — Human gates cannot branch on approve/reject

A `human_gate` step can currently produce only two outcomes: continue (approved) or fail the entire run (rejected). There is no way to send approved runs down one path and rejected runs down a different path (e.g. "route to remediation agent on reject, escalate on approve"). 

`resolveGate()` in `engine.go` returns an `error` on rejection — this unwinds the step-dispatch loop before `evaluateBranches()` is ever called. The `Step.Branches` field already exists and `resolveGate()` already synthesises `{"approved": false, ...}` as `result.Output`, but the error short-circuit prevents the branch evaluator from seeing it.

### Gap 2 — Loop steps lack inter-iteration delay and error-handling policy

`LoopCondition` + `MaxIterations` is modeled and executed. Missing:
- `LoopDelaySecs` — minimum wall-clock pause between iterations (prevents hot-looping an LLM agent)
- `LoopOnError` policy — when an iteration fails, should the loop stop (`"stop"`, the current hard default), skip and continue (`"continue"`), or retry the failed iteration up to `MaxRetries` (`"retry"`)?

### Gap 3 — No rate limiting at any scope

There is no mechanism to prevent:
- A loop step from hammering a downstream agent at unbounded speed (step scope)
- Multiple concurrent recipe runs from overloading a kitchen's model quota (recipe scope)  
- Individual `/invoke` callers from abusing a single agent's endpoint (agent scope)

ADR-0007 noted "rate limiting" as a future capability of the A2A gateway but it was never implemented.

### Gap 4 — HPA fields exist in the DB but are invisible in the Agent form

`k8s_workloads` table in Pro has `hpa_enabled`, `hpa_min_replicas`, `hpa_max_replicas`, `hpa_cpu_target` columns (added in `migrationK8sHPAFieldsUp`). The `AgentForm` in `Agents.tsx` has three tabs (`basic | guardrails | ingredients`) but exposes none of these fields. Users can only configure HPA by editing raw Helm values or calling the API directly.

---

## Decisions

### 1. Human gate conditional branching

**Decision:** When a `human_gate` step has `Branches` set, rejection is treated as a routing outcome rather than a run failure. The engine evaluates `step.Branches` against the gate's output (`{"approved": false, "comments": ...}`) and activates the matching branch's `NextStep`. If no branch covers the rejected case, the existing failure-propagation behaviour is preserved (backward compatible).

**Model change — `Step` struct (`models.go`):** no new fields required. `Branches` and `DefaultNext` already exist.

**Engine change (`engine.go`):**  
`resolveGate()` is modified from:
```go
if !approved {
    result.GateStatus = "rejected"
    result.Output = map[string]interface{}{"approved": false}
    return fmt.Errorf("human gate '%s' was rejected", step.Name)
}
```
to:
```go
if !approved {
    result.GateStatus = "rejected"
    result.Output = map[string]interface{}{
        "approved": false,
        "approver_email": record.ApproverEmail,
        "comments":       record.Comments,
    }
    // If branches are configured, route rather than fail.
    if len(step.Branches) > 0 {
        return nil // branch evaluation happens in the outer executeStep wrapper
    }
    return fmt.Errorf("human gate '%s' was rejected", step.Name)
}
```

`evaluateBranches()` is already called unconditionally in the outer `executeStep()` wrapper after a step returns `nil` error — so the branch evaluation path is activated for free.

**Recipe definition example:**
```yaml
steps:
  - name: review_gate
    kind: human_gate
    branches:
      - condition: "approved == true"
        next_step: publish_agent
      - condition: "approved == false"
        next_step: notify_requester_rejected
    default_next: notify_requester_rejected
  - name: publish_agent
    kind: agent
    agent_ref: publisher-agent
    depends_on: [review_gate]
  - name: notify_requester_rejected
    kind: agent
    agent_ref: notifier-agent
    depends_on: [review_gate]
```

### 2. Loop control improvements

**Decision:** Add two optional fields to `Step` in `models.go`:

```go
// LoopDelaySecs is the minimum pause between loop iterations (in seconds).
// 0 = no delay (default, backward compatible).
LoopDelaySecs int `json:"loop_delay_secs,omitempty"`

// LoopOnError controls iteration error handling.
// "stop"     — abort the loop on first error (default, backward compatible).
// "continue" — log the error, skip the failed iteration, and check LoopCondition.
// "retry"    — retry the failed iteration up to MaxRetries before stopping.
LoopOnError string `json:"loop_on_error,omitempty"`
```

**Engine change (`engine.go`):**  
In the `LoopCondition != "" && MaxIterations > 0` block inside `executeStep()`, add:

```go
// Inter-iteration delay
if step.LoopDelaySecs > 0 {
    select {
    case <-time.After(time.Duration(step.LoopDelaySecs) * time.Second):
    case <-ctx.Done():
        return nil, fmt.Errorf("loop '%s' canceled during delay", step.Name)
    }
}
```

For `LoopOnError`:
```go
if err != nil {
    switch step.LoopOnError {
    case "continue":
        log.Warn().Err(err).Str("step", step.Name).Int("iter", iter).Msg("Loop iteration failed, continuing")
        continue
    case "retry":
        // inner retry handled by MaxRetries — fall through to existing retry logic
    default: // "stop"
        return nil, err
    }
}
```

### 3. Multi-scope rate limiting

Rate limiting is implemented at three scopes. All three use `golang.org/x/time/rate` (token-bucket) as the primitive.

#### 3a. Step-level rate limiting (max invocations per minute against a downstream agent)

Add `StepRateLimit` field to `Step`:

```go
// StepRateLimit is the maximum number of calls this step may make per minute.
// Only meaningful for agent steps inside a loop (prevents hot-looping an LLM).
// 0 = unlimited (default).
StepRateLimit int `json:"step_rate_limit,omitempty"`
```

**Engine change:** Each `(runID, stepName)` gets a `rate.Limiter` stored in a `sync.Map` on `Engine`. Before each agent invocation in a loop iteration:

```go
if step.StepRateLimit > 0 {
    limiter := e.getOrCreateStepLimiter(run.ID, step.Name, step.StepRateLimit)
    if err := limiter.Wait(ctx); err != nil {
        return fmt.Errorf("step rate limit wait canceled: %w", err)
    }
}
```

Limiters are cleaned up when the run completes.

#### 3b. Recipe-level concurrency limiting (max concurrent runs)

Add `MaxConcurrentRuns` field to `Recipe`:

```go
// MaxConcurrentRuns is the maximum number of simultaneous runs of this recipe.
// 0 = unlimited (default). When the limit is reached, new bake requests
// receive HTTP 429 with Retry-After header.
MaxConcurrentRuns int `json:"max_concurrent_runs,omitempty" db:"max_concurrent_runs"`
```

**Engine change:** `Engine` maintains a `map[recipeName]*semaphore.Weighted` (using `golang.org/x/sync/semaphore`). `BakeRecipe` acquires the semaphore before starting; releases it when the run transitions to a terminal state.

**Handler change (`handlers.go`):** When `semaphore.TryAcquire()` returns false, respond with `HTTP 429 Too Many Requests` and `Retry-After: 30`.

**Store change:** `migrationRecipeRateLimitUp` adds `max_concurrent_runs INT NOT NULL DEFAULT 0` to the `recipes` table (Pro); MemoryStore gets the field on the in-memory `Recipe` struct (OSS).

#### 3c. Agent-level rate limiting at the A2A gateway (calls per minute per agent)

Add `InvokeRateLimit` field to `Agent`:

```go
// InvokeRateLimit is the maximum number of /invoke (and A2A) calls per minute
// that the control-plane gateway will accept for this agent.
// 0 = unlimited (default).
InvokeRateLimit int `json:"invoke_rate_limit,omitempty" db:"invoke_rate_limit"`
```

**Handler change (`handlers.go` / A2A handler):** A kitchen-scoped `rate.Limiter` per agent is loaded from a `sync.Map` keyed by `"kitchen:agentName"`. Each `InvokeAgent` and `A2AEndpoint` handler checks the limiter before dispatching. Exceeded limit → `HTTP 429` with `Retry-After: 60/rate`.

**Store change:** `invoke_rate_limit INT NOT NULL DEFAULT 0` column on `agents` table (Pro); field on OSS `Agent` struct.

Rate limiters are lazy-initialised on first request and evicted from the map when the agent is deleted or cooled.

### 4. K8s HPA in the Agent form

**Decision:** Expose HPA fields in the `AgentForm` basic tab when `execution_mode === 'k8s'`. Show a collapsible "Scaling" section at the bottom of the basic tab — not a 4th tab — since it is 3 inputs that are always invisible for non-k8s agents.

**Model change (`models.go`):** Add HPA fields directly to `Agent`:

```go
// HPAEnabled enables the Horizontal Pod Autoscaler for K8s execution mode.
HPAEnabled bool `json:"hpa_enabled,omitempty" db:"hpa_enabled"`

// HPAMinReplicas is the minimum number of pod replicas (default 1).
HPAMinReplicas int32 `json:"hpa_min_replicas,omitempty" db:"hpa_min_replicas"`

// HPAMaxReplicas is the maximum number of pod replicas (default 3).
// Exposed as a slider in the basic tab when execution_mode = "k8s".
HPAMaxReplicas int32 `json:"hpa_max_replicas,omitempty" db:"hpa_max_replicas"`

// HPACPUTarget is the target average CPU utilisation (0–100) that triggers scale-out.
// Default 70.
HPACPUTarget int32 `json:"hpa_cpu_target,omitempty" db:"hpa_cpu_target"`
```

**Note on duplication:** These fields currently exist only on the `k8s_workloads` table via the `dbWorkload` struct in `postgres.go`. The canonical definition should move to the `Agent` model. The operator reads them from `AgentSpec` (which mirrors `Agent`) and creates the `HorizontalPodAutoscaler` K8s resource. The `k8s_workloads` columns become a copy maintained by the operator for operational state, not the source of truth.

**Store change (Pro):** `migrationAgentHPAFieldsUp` adds `hpa_enabled BOOL NOT NULL DEFAULT false`, `hpa_min_replicas INT`, `hpa_max_replicas INT`, `hpa_cpu_target INT` to the `agents` table. Operator continues to write the same values to `k8s_workloads` for telemetry purposes.

**Store change (OSS):** HPA fields are accepted and stored on the in-memory `Agent` struct. OSS ignores them at execution time (OSS has no operator), but the fields round-trip cleanly so that exported agent definitions can be imported into Pro without data loss.

**UI change (`Agents.tsx`):**

In `AgentForm`, the `form` state gains:
```typescript
hpa_enabled: false,
hpa_min_replicas: 1,
hpa_max_replicas: 3,
hpa_cpu_target: 70,
```

At the bottom of the `tab === 'basic'` panel, conditionally render when `form.execution_mode === 'k8s'`:

```tsx
{form.execution_mode === 'k8s' && (
  <div className="border-t border-[var(--ao-border)] pt-4 mt-2">
    <label className="flex items-center gap-2 mb-3 cursor-pointer">
      <input
        type="checkbox"
        checked={form.hpa_enabled}
        onChange={(e) => setForm({ ...form, hpa_enabled: e.target.checked })}
      />
      <span className="text-sm font-medium">Enable autoscaling (HPA)</span>
    </label>
    {form.hpa_enabled && (
      <div className="space-y-3">
        <FormField label={`Max replicas: ${form.hpa_max_replicas}`}>
          <input
            type="range" min={1} max={20}
            value={form.hpa_max_replicas}
            onChange={(e) => setForm({ ...form, hpa_max_replicas: Number(e.target.value) })}
            className="w-full accent-[var(--ao-brand)]"
          />
        </FormField>
        <FormField label={`Min replicas: ${form.hpa_min_replicas}`}>
          <input
            type="range" min={1} max={form.hpa_max_replicas}
            value={form.hpa_min_replicas}
            onChange={(e) => setForm({ ...form, hpa_min_replicas: Number(e.target.value) })}
            className="w-full accent-[var(--ao-brand)]"
          />
        </FormField>
        <FormField label={`CPU scale-out target: ${form.hpa_cpu_target}%`}>
          <input
            type="range" min={10} max={100} step={5}
            value={form.hpa_cpu_target}
            onChange={(e) => setForm({ ...form, hpa_cpu_target: Number(e.target.value) })}
            className="w-full accent-[var(--ao-brand)]"
          />
        </FormField>
      </div>
    )}
  </div>
)}
```

The same form fields are passed in `AgentEditForm` (`AgentForm` re-used with an agent prop).

---

## Consequences

**Positive:**
- Gate branching enables first-class "approval workflow DAGs" — rejected pipelines can be routed to remediation without manual intervention.
- Loop delay + error policy makes recipe loops safe to deploy against production LLM endpoints without engineering custom throttle wrappers.
- Multi-scope rate limiting closes the abuse surface identified in ADR-0007 (rate limiting was noted but never delivered).
- HPA fields promoted to `Agent` model make autoscaling a first-class citizen with zero CLI/API bypassing.

**Negative / Risks:**
- Step-level rate limiters live in engine memory. If the control plane restarts mid-run, limiter state resets. Acceptable: limiters are per-run, not per-kitchen; a restarted run starts fresh anyway.
- `MaxConcurrentRuns = 0` (unlimited, default) preserves all existing behavior. Admins who want limits must explicitly configure them — no risk of accidentally throttling existing users.
- Gate-branching changes `resolveGate()` return semantics. Existing recipes with no `Branches` on `human_gate` steps see exactly the same failure behavior as before (guard: `if len(step.Branches) > 0`).

---

## Implementation plan

| Phase | Scope | Target |
|---|---|---|
| P0 | Gate conditional branching — `resolveGate()` change (OSS + Pro) | 0.9.x |
| P0 | `LoopDelaySecs` + `LoopOnError` fields on `Step` (OSS + Pro) | 0.9.x |
| P0 | HPA fields on `Agent` model + Pro migration + OSS in-memory | 0.9.x |
| P0 | `AgentForm` Scaling section in basic tab | 0.9.x |
| P1 | Step-level rate limiting (`StepRateLimit` + `rate.Limiter` in engine) | 0.9.x |
| P1 | Agent-level invoke rate limiting (`InvokeRateLimit` + A2A gateway check) | 0.9.x |
| P1 | Recipe-level concurrency limit (`MaxConcurrentRuns` + semaphore) | 0.9.x |
| P1 | Pro DB migrations for all rate-limit fields | 0.9.x |
| P2 | Rate-limit state exposed in Pro dashboard (current usage vs limit per agent) | TBD |

---

## Key files

| File | Change |
|---|---|
| `agentoven/control-plane/pkg/models/models.go` | `Step`: `LoopDelaySecs`, `LoopOnError`, `StepRateLimit`. `Recipe`: `MaxConcurrentRuns`. `Agent`: `HPAEnabled`, `HPAMinReplicas`, `HPAMaxReplicas`, `HPACPUTarget`, `InvokeRateLimit` |
| `agentoven/control-plane/internal/workflow/engine.go` | `resolveGate()` branch-on-reject; loop delay + `LoopOnError`; step-rate-limiter map; recipe semaphore acquire/release |
| `agentoven/control-plane/internal/api/handlers/handlers.go` | Agent invoke + A2A: per-agent rate limiter check (HTTP 429); recipe bake: semaphore check (HTTP 429) |
| `agentoven-pro/internal/store/postgres.go` | Migrations: `loop_delay_secs`, `loop_on_error`, `step_rate_limit` on recipe step JSON (no column — stored in `steps` JSONB); `max_concurrent_runs` on `recipes`; `hpa_enabled`, `hpa_min_replicas`, `hpa_max_replicas`, `hpa_cpu_target`, `invoke_rate_limit` on `agents` |
| `agentoven-pro/dashboard/src/pages/Agents.tsx` | `AgentForm` + `AgentEditForm`: HPA Scaling section in basic tab |

## Related ADRs

- ADR-0007: Control Plane as A2A Gateway — rate limiting noted as future capability, delivered here
- ADR-0009: Pluggable Test Runner Architecture — recipe execution model context
- ADR-0011: OSS Local Test Runner — workflow engine first introduced
- ADR-0022: Dashboard Nav Grouping & Gate UX — gate approval UX (complementary; visual layer)
