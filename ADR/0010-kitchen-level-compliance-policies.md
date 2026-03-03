# ADR-0010: Kitchen-Level Compliance Policies

- **Date:** 2026-03-03
- **Status:** Accepted
- **Deciders:** siddartha

## Context

Enterprise customers operate in regulated industries (finance, healthcare, government)
where AI agents must comply with specific policies — PII handling, data residency,
model allow/deny lists, prompt guardrails, and audit requirements. Today, AgentOven
has no mechanism to enforce compliance at the workspace (kitchen) level.

Without kitchen-level compliance:

- A developer can bake an agent with any model, including unapproved ones
- PII can flow through agents without redaction or logging
- Data residency requirements (EU-only, HIPAA-scoped) cannot be enforced
- There's no way to mandate prompt guardrails or content filters per workspace

### Requirements

1. Compliance policies are **per-kitchen** — different teams have different regs
2. Policies are enforced at **bake time** (prevent non-compliant agents from deploying)
   and at **invoke time** (prevent non-compliant inputs/outputs)
3. OSS defines the `CompliancePolicy` model and enforcement hooks
4. Pro provides enhanced validators (LLM-judge, regex PII detection, external policy engines)
5. Policies must be auditable — every enforcement decision is logged

## Decision

### 1. CompliancePolicy Model (OSS)

Add a `CompliancePolicy` struct to `pkg/models/`:

```go
type CompliancePolicy struct {
    ID          string            `json:"id"`
    KitchenID   string            `json:"kitchen_id"`
    Name        string            `json:"name"`
    Description string            `json:"description,omitempty"`
    Version     int               `json:"version"`
    Enabled     bool              `json:"enabled"`
    CreatedAt   time.Time         `json:"created_at"`
    UpdatedAt   time.Time         `json:"updated_at"`

    // Model governance
    AllowedModels    []string `json:"allowed_models,omitempty"`    // whitelist
    DeniedModels     []string `json:"denied_models,omitempty"`     // blacklist
    AllowedProviders []string `json:"allowed_providers,omitempty"` // e.g. ["openai", "anthropic"]

    // Data governance
    PIIPolicy      PIIPolicy      `json:"pii_policy"`
    DataResidency  DataResidency  `json:"data_residency"`

    // Prompt governance
    PromptGuardrails PromptGuardrails `json:"prompt_guardrails"`

    // Audit requirements
    AuditLevel AuditLevel `json:"audit_level"` // none, basic, full
}

type PIIPolicy struct {
    Enabled       bool     `json:"enabled"`
    Action        string   `json:"action"`        // "block", "redact", "log"
    PIICategories []string `json:"pii_categories"` // ["email", "phone", "ssn", "credit_card"]
}

type DataResidency struct {
    Enabled        bool     `json:"enabled"`
    AllowedRegions []string `json:"allowed_regions"` // ["us-east-1", "eu-west-1"]
    DeniedRegions  []string `json:"denied_regions"`
}

type PromptGuardrails struct {
    Enabled           bool     `json:"enabled"`
    MaxPromptLength   int      `json:"max_prompt_length,omitempty"`
    DeniedTopics      []string `json:"denied_topics,omitempty"`
    RequireSystemPrompt bool   `json:"require_system_prompt"`
}

type AuditLevel string

const (
    AuditNone  AuditLevel = "none"
    AuditBasic AuditLevel = "basic" // log agent name, model, timestamp
    AuditFull  AuditLevel = "full"  // log full input/output (Pro)
)
```

### 2. CompliancePolicyValidator Interface (OSS)

```go
// pkg/contracts/compliance.go

type CompliancePolicyValidator interface {
    // ValidateAtBake checks the agent config against kitchen compliance policies.
    // Called during BakeAgent before the agent is marked ready.
    ValidateAtBake(ctx context.Context, agent *models.Agent, policy *models.CompliancePolicy) (*ComplianceReport, error)

    // ValidateAtInvoke checks the input against compliance policies.
    // Called during InvokeAgent before the request reaches the model.
    ValidateAtInvoke(ctx context.Context, input string, policy *models.CompliancePolicy) (*ComplianceReport, error)

    // ValidateOutput checks the model output against compliance policies.
    // Called after model response, before returning to the caller.
    ValidateOutput(ctx context.Context, output string, policy *models.CompliancePolicy) (*ComplianceReport, error)
}

type ComplianceReport struct {
    Compliant  bool               `json:"compliant"`
    Violations []ComplianceViolation `json:"violations,omitempty"`
    Timestamp  time.Time          `json:"timestamp"`
}

type ComplianceViolation struct {
    Rule     string `json:"rule"`
    Severity string `json:"severity"` // "error", "warning"
    Message  string `json:"message"`
    Field    string `json:"field,omitempty"`
}
```

### 3. OSS Implementation (CommunityComplianceValidator)

- Validates `AllowedModels` / `DeniedModels` at bake time
- Validates `AllowedProviders` at bake time
- Checks `MaxPromptLength` at invoke time
- Logs violations to zerolog

### 4. Pro Implementation

Pro provides a `ProComplianceValidator` with enhanced capabilities including
PII detection, content filtering, data residency enforcement, and external
policy engine integration. See the Pro repo for implementation details.

### 5. API Endpoints

```
GET    /api/v1/compliance-policies         — list policies for kitchen
POST   /api/v1/compliance-policies         — create policy
GET    /api/v1/compliance-policies/{id}    — get policy
PUT    /api/v1/compliance-policies/{id}    — update policy
DELETE /api/v1/compliance-policies/{id}    — delete policy
POST   /api/v1/compliance-policies/validate — dry-run validation
```

### 6. Enforcement Integration

- `BakeAgent` handler calls `ValidateAtBake()` after ingredient resolution
- `InvokeAgent` handler calls `ValidateAtInvoke()` before model call
- Model Router calls `ValidateOutput()` after model response
- All violations are stored as audit events

## Consequences

### Positive

- Enterprises get kitchen-scoped compliance without global config pollution
- OSS users get basic model governance (allow/deny lists) for free
- Compliance reports are auditable and stored alongside traces
- Extensible — Pro can add LLM-based content filtering, PII detection

### Negative

- Adds latency to invoke path (compliance checks on every request)
- PII detection is inherently imperfect (false positives/negatives)
- Complex policy interactions need careful precedence rules

## Alternatives Considered

### A. Global compliance only

Rejected — enterprises have team-specific regulations. A healthcare team and
a marketing team have vastly different compliance needs.

### B. External policy engine only (OPA/Cedar)

Rejected — adds an external dependency for basic use cases. OSS users want
model allow/deny lists without deploying OPA. Pro can integrate OPA as an
enhanced validator.

### C. Middleware-only approach

Rejected — compliance needs to inspect resolved ingredients (bake time) and
model outputs (post-invoke), not just HTTP headers. A pure middleware approach
misses semantic context.
