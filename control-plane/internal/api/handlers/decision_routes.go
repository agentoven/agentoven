package handlers

import "github.com/agentoven/agentoven/control-plane/pkg/contracts"

// DecisionRoute is a route at which a person decides something: it lets a run, a gate, a step or a
// skill proceed, or stops it. Each one asks the DecisionSigner (Pro ADR-0041).
type DecisionRoute struct {
	Method, Pattern, Gate string
}

// DecisionRoutes is every decision route the community edition serves. A test walks the real route
// table and fails if a route that looks like a decision (approve, reject, resume, release, decide, a
// gate) is missing here, or if a listed one is no longer routed. A new decision endpoint is added
// here, calls signDecision, and has a describer, or the test fails.
var DecisionRoutes = []DecisionRoute{
	{"POST", "/api/v1/agents/{agentName}/runs/{traceID}/resume", "tool_approval | budget_extension | resume_guidance"},
	{"POST", "/api/v1/tasks/{taskID}/resume", "tool_approval | budget_extension | resume_guidance"},
	{"POST", "/api/v1/approvals/{runId}/{stepName}", contracts.GateRecipeGate},
	{"POST", "/api/v1/recipes/{recipeName}/runs/{runId}/gates/{stepName}/approve", contracts.GateRecipeGate},
	{"POST", "/api/v1/recipes/{recipeName}/runs/{runId}/steps/{stepName}/release", contracts.GateWaitRelease},
	{"POST", "/api/v1/skills/{name}/approve", contracts.GateSkillApproval},
	{"POST", "/api/v1/skills/{name}/reject", contracts.GateSkillApproval},
}
