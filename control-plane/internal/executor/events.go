package executor

import (
	"sync"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// EventType names the kind of thing an Event reports as ExecuteStream drives
// the loop, mirroring how a caller actually wants to render it: tokens as
// they arrive, each tool call's start and finish, and the turn/run
// boundaries around them.
type EventType string

const (
	EventTurnStart     EventType = "turn_start"
	EventToken         EventType = "token"          // a content delta from the model
	EventThinkingToken EventType = "thinking_token" // an extended-thinking/reasoning delta
	EventToolCallStart EventType = "tool_call_start"
	EventToolCallEnd   EventType = "tool_call_end"
	EventTurnEnd       EventType = "turn_end"
	EventPaused        EventType = "paused" // the run stopped for human approval
	// EventBudgetWarning fires once as use of a budget dimension crosses a threshold.
	EventBudgetWarning EventType = "budget_warning"
	// EventBudgetPaused fires when the run stops at its budget to wait for a decision.
	EventBudgetPaused EventType = "budget_paused"
	// EventContextCompacted fires when older messages were replaced by a summary.
	EventContextCompacted EventType = "context_compacted"
	// EventStallWarning fires when the run repeats a tool call enough to be warned about it.
	EventStallWarning EventType = "stall_warning"
	// EventStallPaused fires when the run stops for repeating itself.
	EventStallPaused EventType = "stall_paused"
	EventDone        EventType = "done"  // the run produced a final answer
	EventError       EventType = "error" // the run failed
)

// Event is one thing that happened while ExecuteStream drove the loop.
// Exactly the fields relevant to Type are populated.
type Event struct {
	Type EventType `json:"type"`
	Turn int       `json:"turn,omitempty"`

	// Content carries a token (EventToken/EventThinkingToken) or the final
	// answer text (EventDone). The lowercase keys match the frames the Python
	// agent runner streams, so one client can read either runtime.
	Content string `json:"content,omitempty"`

	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`

	Pending *PendingApproval `json:"pending,omitempty"`
	Trace   *ExecutionTrace  `json:"trace,omitempty"`
	Err     string           `json:"err,omitempty"`

	BudgetWarning *BudgetWarning       `json:"budget_warning,omitempty"`
	BudgetPause   *models.BudgetPause  `json:"budget_pause,omitempty"`
	Budget        *models.BudgetStatus `json:"budget,omitempty"`
	// Usage is the run's cumulative usage so far; set on EventTurnEnd.
	Usage *models.TokenUsage `json:"usage,omitempty"`
	Stall *StallInfo         `json:"stall,omitempty"`
}

// eventSink wraps a caller's onEvent callback so concurrent goroutines
// (parallel tool-call dispatch) can emit events without racing each other or
// the caller's own state. nil-safe: a nil sink's emit is a no-op, so the
// non-streaming Execute path can share the same loop code with onEvent
// simply absent rather than specially-cased.
type eventSink struct {
	mu sync.Mutex
	fn func(Event) error
}

func newEventSink(fn func(Event) error) *eventSink {
	if fn == nil {
		return nil
	}
	return &eventSink{fn: fn}
}

// emit calls the sink's callback, swallowing its error into a log-worthy
// return only where the caller wants to abort — see emitErr.
func (s *eventSink) emit(ev Event) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.fn(ev) // errors from informational events are not fatal to the run
}

// emitErr calls the sink's callback and returns its error, for the one event
// (EventToken, realistically) whose callback failing should abort the run —
// e.g. a caller whose HTTP client disconnected mid-stream.
func (s *eventSink) emitErr(ev Event) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fn(ev)
}
