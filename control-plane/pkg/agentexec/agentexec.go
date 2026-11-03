// Package agentexec is the public entry point to AgentOven's managed-agent
// executor.
//
// The executor itself lives under internal/, which Go's visibility rule keeps
// closed to other modules. This package is the curated surface across that
// boundary — the same approach pkg/contracts already takes for the store — so
// an embedder can run an agent's agentic loop, and supply its own tool gateway,
// without reaching into internals.
//
// The tool gateway is the interesting parameter. Anything that can answer a
// tools/call can stand in for the real MCP gateway, which is what lets a caller
// run an agent against substituted tools while the executor stays unaware that
// anything was substituted.
package agentexec

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Types aliased so callers outside this module can name them. Aliases cross the
// internal boundary where imports cannot.
type (
	// ToolGateway dispatches a tool call and returns the result.
	ToolGateway = executor.ToolGateway
	// ExecutionTrace records the full execution history of one Execute call.
	ExecutionTrace = executor.ExecutionTrace
	// Turn is one iteration of the agentic loop.
	Turn = executor.Turn
	// ToolCall is a tool invocation requested by the model.
	ToolCall = executor.ToolCall
	// ToolResult is the result of executing a tool call.
	ToolResult = executor.ToolResult
	// ModelRouter routes LLM requests to configured providers.
	ModelRouter = router.ModelRouter
	// Store is the control plane's persistence interface.
	Store = store.Store
	// PausedError is returned when a run stopped for a person's decision at its budget.
	PausedError = executor.PausedError

	// The journal and what is needed to keep one somewhere other than a local directory (see
	// NewLogJournal). A database-backed edition implements RecordLog and TaskLeases.
	Journal         = executor.Journal
	JournalHeader   = executor.JournalHeader
	JournalState    = executor.JournalState
	JournalSummary  = executor.JournalSummary
	PendingApproval = executor.PendingApproval
	StallInfo       = executor.StallInfo
	RecordLog       = executor.RecordLog
	RecordTx        = executor.RecordTx
	RunMeta         = executor.RunMeta
	LogJournal      = executor.LogJournal
	TaskLeases      = executor.TaskLeases
	LeaseState      = executor.LeaseState
	// Fence is a task runner's proof that it still holds the task (see FenceFrom): a RecordLog that
	// receives a context carrying one must refuse the write, with ErrFenced, unless the lease is
	// still held at Fence.Epoch.
	Fence = executor.Fence
	// TaskSnapshot is a background task as a caller sees it; TaskManager runs them.
	TaskSnapshot = executor.TaskSnapshot
	TaskManager  = executor.TaskManager
)

// ErrRunNotFound is what a RecordLog returns for a run it does not hold.
var ErrRunNotFound = executor.ErrRunNotFound

// ErrFenced is what a RecordLog returns for a write by a replica that no longer holds the task.
var ErrFenced = executor.ErrFenced

// WithFence returns ctx carrying f, for a runner that holds a task's lease and wants the journal to
// refuse its writes once it no longer does. The task manager does this itself for the tasks it runs.
func WithFence(ctx context.Context, f Fence) context.Context { return executor.WithFence(ctx, f) }

// FenceFrom returns the fence on ctx, set when the run is a task that holds a lease.
func FenceFrom(ctx context.Context) (Fence, bool) { return executor.FenceFrom(ctx) }

// NewLogJournal returns an execution journal that keeps its records in log. With a log that several
// control-plane replicas share (a database table), the replicas share their runs and can take over
// each other's; see TaskLeases for who runs a task.
func NewLogJournal(log RecordLog) *LogJournal { return executor.NewLogJournal(log) }

// NewMemoryLeases returns an in-memory task lease table, for tests and single-process use.
func NewMemoryLeases() TaskLeases { return executor.NewMemoryLeases(nil) }

// Errors a caller may need to tell apart from an ordinary failure.
var (
	// ErrBudgetExhausted ends a run that hit its budget and has no journal to pause into
	// (as with a Runner from New, which keeps none) or was ended by a person.
	ErrBudgetExhausted = executor.ErrBudgetExhausted
	// ErrUnpricedModel refuses a dollar cap on a model with no known price.
	ErrUnpricedModel = executor.ErrUnpricedModel
)

// WithBudget caps one Execute call. The agent's own Budget still applies: the two are
// merged by taking the stricter limit of each kind, so a caller can tighten a run but never
// loosen it.
func WithBudget(ctx context.Context, b models.RunBudget) context.Context {
	return contracts.WithBudget(ctx, b)
}

// Runner runs a managed agent through its agentic loop: prompt, model, tool,
// repeat, until a text response or the turn cap.
type Runner interface {
	Execute(
		ctx context.Context,
		agent *models.Agent,
		userMessage string,
		resolved *models.ResolvedIngredients,
		promptVars map[string]string,
		thinkingEnabled bool,
		sessionID ...string,
	) (string, *ExecutionTrace, error)
}

// New creates a Runner. Pass a custom ToolGateway to route the agent's tool
// calls somewhere other than the real MCP gateway; pass the gateway itself for
// ordinary execution.
func New(s Store, r *ModelRouter, gw ToolGateway, sess contracts.SessionStore) Runner {
	return executor.NewExecutor(s, r, gw, sess)
}
