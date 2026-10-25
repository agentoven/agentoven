package contracts

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// SubagentRuntime lets a running agent spawn subagents it describes itself, in the middle of
// a task. The community edition does not install one: its agents delegate, one hop at a time,
// to agents that are registered (agentoven_delegate), which is enough for static
// orchestration. An edition that installs a runtime offers every agent the `task` and `tasks`
// tools and routes them here.
//
// The runtime decides policy: whether spawning is allowed, how many at once, whether a person
// must approve, what to audit. It does not run anything itself. It runs subagents through
// SubagentHost, which is the executor's own machinery, so a spawned subagent is always bound
// by the parent's budget (it is charged to it as it spends), the parent's guardrails, the
// depth guard and the journal. A runtime cannot hand out more than its parent has, and cannot
// skip the rules a registered agent would be held to.
type SubagentRuntime interface {
	// Enabled reports whether parent is offered the subagent tools at all.
	Enabled(parent *models.Agent) bool

	// GatedTools names the subagent tools that need a person's approval before they run, for
	// parent. They are held with the same mechanism as Agent.ApprovalTools.
	GatedTools(parent *models.Agent) []string

	// Spawn runs one subagent and returns its result. An error here means the spawn was
	// refused or could not run; the parent's model is told, and the parent's run continues.
	Spawn(ctx context.Context, parent models.SubagentParent, spec models.SubagentSpec, host SubagentHost) (models.SubagentResult, error)

	// SpawnMany runs several at once and returns their results in the order of specs. The
	// host shares the parent's remaining budget between them, so a wide fan-out cannot
	// oversubscribe it.
	SpawnMany(ctx context.Context, parent models.SubagentParent, specs []models.SubagentSpec, host SubagentHost) ([]models.SubagentResult, error)
}

// SubagentHost is what the executor offers a runtime to run subagents with.
type SubagentHost interface {
	// Run runs one subagent to completion.
	Run(ctx context.Context, spec models.SubagentSpec) (models.SubagentResult, error)
	// RunMany runs the specs concurrently, each with an equal share of what the parent can
	// spare, and waits for all of them. Results are in the order of specs.
	RunMany(ctx context.Context, specs []models.SubagentSpec) []models.SubagentResult
}
