package executor

import "sync"

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
	EventDone          EventType = "done"   // the run produced a final answer
	EventError         EventType = "error"  // the run failed
)

// Event is one thing that happened while ExecuteStream drove the loop.
// Exactly the fields relevant to Type are populated.
type Event struct {
	Type EventType
	Turn int

	// Content carries a token (EventToken/EventThinkingToken) or the final
	// answer text (EventDone).
	Content string

	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`

	Pending *PendingApproval `json:"pending,omitempty"`
	Trace   *ExecutionTrace  `json:"trace,omitempty"`
	Err     string           `json:"err,omitempty"`
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
