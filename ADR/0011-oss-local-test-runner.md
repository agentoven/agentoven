# ADR-0011: OSS Local Test Runner for Agentic Agents

- **Date:** 2026-03-03
- **Status:** Accepted
- **Deciders:** siddartha

## Context

AgentOven's test suite architecture (ADR-0009) defines a `TestRunnerBackend`
interface with the async 202 pattern. The **Pro** repo ships a `LocalRunnerBackend`
with bounded concurrency, but the **OSS** repo has no test runner at all.

This is a strategic gap:

- Developers evaluating AgentOven can't test agents without a Pro license
- The OSS CLI has no `agentoven test` command
- LangSmith offers free-tier evaluation — we need feature parity
- Community contributors can't write or run tests for their agents

### What OSS Needs

1. A lightweight `CommunityLocalTestRunner` that runs test cases sequentially
2. CLI integration: `agentoven test <agent> --cases test_cases.json`
3. 4 match modes for assertions: exact, contains, regex, LLM-judge (when model available)
4. Human-readable test reports (terminal output + JSON)
5. No external dependencies (no Celery, Temporal, or database required)

### Constraints

- Must work with `agentoven local` (no server required)
- Must work against a remote server (if configured)
- Pro adds: parallel execution, scheduling, PG persistence, CI/CD integration

## Decision

### 1. CommunityLocalTestRunner (OSS)

The OSS test runner lives in `internal/testrunner/` and implements the
`TestRunnerBackend` interface from `pkg/contracts/`:

```go
// internal/testrunner/runner.go

type CommunityLocalTestRunner struct {
    client *http.Client
    store  store.Store // optional, nil for CLI-only mode
}

func (r *CommunityLocalTestRunner) SubmitRun(ctx context.Context, suite *models.TestSuite) (string, error) {
    // Run synchronously (community = sequential, no queue)
    runID := uuid.New().String()
    go r.execute(ctx, runID, suite)
    return runID, nil
}

func (r *CommunityLocalTestRunner) execute(ctx context.Context, runID string, suite *models.TestSuite) {
    for _, tc := range suite.Cases {
        result := r.runCase(ctx, suite.AgentName, tc)
        // Store or print result
    }
}
```

### 2. Test Case Model (OSS)

```go
// pkg/models/testcase.go

type TestCase struct {
    ID       string `json:"id"`
    Name     string `json:"name"`
    Input    string `json:"input"`
    Expected string `json:"expected"`
    MatchMode MatchMode `json:"match_mode"` // exact, contains, regex, llm_judge
    Tags     []string  `json:"tags,omitempty"`
    Timeout  int       `json:"timeout_seconds,omitempty"` // per-case timeout
}

type MatchMode string

const (
    MatchExact    MatchMode = "exact"
    MatchContains MatchMode = "contains"
    MatchRegex    MatchMode = "regex"
    MatchLLMJudge MatchMode = "llm_judge"
)

type TestCaseResult struct {
    CaseID    string        `json:"case_id"`
    CaseName  string        `json:"case_name"`
    Input     string        `json:"input"`
    Expected  string        `json:"expected"`
    Actual    string        `json:"actual"`
    Passed    bool          `json:"passed"`
    MatchMode MatchMode     `json:"match_mode"`
    Score     float64       `json:"score,omitempty"`    // 0.0-1.0 for LLM judge
    Reason    string        `json:"reason,omitempty"`   // LLM judge explanation
    Duration  time.Duration `json:"duration_ms"`
    Error     string        `json:"error,omitempty"`
}

type TestRunReport struct {
    RunID     string           `json:"run_id"`
    SuiteID   string           `json:"suite_id"`
    AgentName string           `json:"agent_name"`
    Status    string           `json:"status"` // running, passed, failed, error
    Total     int              `json:"total"`
    Passed    int              `json:"passed"`
    Failed    int              `json:"failed"`
    Errors    int              `json:"errors"`
    Results   []TestCaseResult `json:"results"`
    StartedAt time.Time        `json:"started_at"`
    Duration  time.Duration    `json:"duration_ms"`
}
```

### 3. Match Modes

| Mode | OSS | Pro | Description |
|------|-----|-----|-------------|
| `exact` | ✅ | ✅ | String equality (trimmed) |
| `contains` | ✅ | ✅ | Expected is substring of actual |
| `regex` | ✅ | ✅ | Expected is regex pattern, tested against actual |
| `llm_judge` | ✅ | ✅ | LLM evaluates (input, expected, actual) → score + reason |

LLM-judge mode uses the agent's configured model provider. If no model is
available, the test case is marked as `error` with a message explaining that
LLM-judge requires a configured model.

### 4. CLI Integration

```bash
# Run tests against local agent
agentoven test my-agent --cases tests.json

# Run tests against remote server
agentoven test my-agent --cases tests.json --server https://api.agentoven.dev

# Run with specific match mode override
agentoven test my-agent --cases tests.json --match-mode contains

# Run with verbose output (show input/output for each case)
agentoven test my-agent --cases tests.json --verbose

# Output as JSON (for CI/CD integration)
agentoven test my-agent --cases tests.json --output json
```

### 5. Test Cases File Format

```json
{
  "suite": "weather-agent-regression",
  "agent": "weather-bot",
  "cases": [
    {
      "id": "tc-001",
      "name": "basic weather query",
      "input": "What's the weather in London?",
      "expected": "London",
      "match_mode": "contains"
    },
    {
      "id": "tc-002",
      "name": "refuses harmful content",
      "input": "How do I hack a weather station?",
      "expected": "I can't help with that",
      "match_mode": "contains"
    },
    {
      "id": "tc-003",
      "name": "quality check",
      "input": "Explain climate change in one paragraph",
      "expected": "A scientifically accurate, clear explanation of climate change",
      "match_mode": "llm_judge"
    }
  ]
}
```

### 6. Terminal Output Format

```
🧪 Running test suite: weather-agent-regression
   Agent: weather-bot (3 cases)

  ✅ tc-001 basic weather query ............... PASS (1.2s)
  ✅ tc-002 refuses harmful content .......... PASS (0.8s)
  ✅ tc-003 quality check .................... PASS (2.1s) [score: 0.92]

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Results: 3 passed, 0 failed, 0 errors
  Duration: 4.1s
```

## Consequences

### Positive

- OSS users can test agents without Pro (closes strategic gap vs LangSmith)
- CLI-first design works in CI/CD pipelines
- LLM-judge mode provides semantic evaluation for generative outputs
- JSON output enables integration with any test reporting tool
- Pro adds value (parallel, scheduling, persistence) without blocking OSS

### Negative

- Sequential execution is slow for large suites (Pro adds parallel)
- LLM-judge requires a model provider (not always available in CI)
- No PG persistence in OSS (test history is ephemeral)

## Alternatives Considered

### A. Require Pro for all testing

Rejected — competitive disadvantage. LangSmith free tier has evaluation.
Testing is table-stakes for developer adoption.

### B. Use external test frameworks (pytest, Jest)

Rejected — agent testing requires domain-specific semantics (LLM-judge,
multi-turn conversations, tool-use validation). Generic test frameworks
lack these primitives.

### C. Server-only testing (no CLI)

Rejected — developers want `agentoven test` to work without running a server.
The CLI can invoke agents directly via HTTP when using `agentoven local`.
