package executor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// TaskStatus is where a background task stands.
type TaskStatus string

const (
	TaskQueued          TaskStatus = "queued"    // accepted, waiting for a free slot
	TaskRunning         TaskStatus = "running"   // the loop is working
	TaskPaused          TaskStatus = "paused"    // waiting for a person: a budget extension or a tool approval
	TaskCompleted       TaskStatus = "completed" // finished with an answer
	TaskMaxTurns        TaskStatus = "max_turns" // ran out of turns and answered with what it had
	TaskFailed          TaskStatus = "failed"    // could not be run at all
	TaskCancelled       TaskStatus = "cancelled"
	TaskBudgetExhausted TaskStatus = "budget_exhausted" // ended at its budget
	TaskInterrupted     TaskStatus = "interrupted"      // stopped without finishing (a crash, a lost model call); resumable
	TaskAbandoned       TaskStatus = "abandoned"        // idle for so long the journal janitor ended it
	TaskStalled         TaskStatus = "stalled"          // ended by a person after it kept repeating itself
)

// Terminal reports whether nothing more will happen to a task in this status.
func (s TaskStatus) Terminal() bool {
	switch s {
	case TaskCompleted, TaskMaxTurns, TaskFailed, TaskCancelled, TaskBudgetExhausted, TaskAbandoned, TaskStalled:
		return true
	}
	return false
}

var (
	// ErrTaskQueueFull means the task was not accepted: every slot is busy and the queue is full.
	ErrTaskQueueFull = errors.New("executor: too many tasks running, try again shortly")
	// ErrTaskNotFound is returned for an unknown task and for one in another kitchen alike.
	ErrTaskNotFound = errors.New("executor: task not found")
	// ErrTaskNotResumable means the task is not stopped (it is running, queued or finished).
	ErrTaskNotResumable = errors.New("executor: task is not paused or interrupted")
)

// EventTaskStatus is appended to a task's event log whenever its status changes.
const EventTaskStatus EventType = "task_status"

// TaskSpec describes a task to start.
type TaskSpec struct {
	// ID is the task's ID and its trace ID. The server picks it; never take it from a client.
	ID        string
	Agent     *models.Agent
	Resolved  *models.ResolvedIngredients
	Message   models.ChatMessage
	Vars      map[string]string
	SessionID string
	Thinking  bool
	Metadata  map[string]string
	// Redact is applied to every event before a viewer can see it. It is how output
	// guardrails reach a task's stream; it may blank or replace what the event carries.
	Redact func(Event) Event
}

// TaskSnapshot is a task as a caller sees it.
type TaskSnapshot struct {
	ID        string     `json:"id"`
	Agent     string     `json:"agent"`
	Kitchen   string     `json:"kitchen"`
	Status    TaskStatus `json:"status"`
	PauseKind string     `json:"pause_kind,omitempty"`

	BudgetPause *models.BudgetPause  `json:"budget_pause,omitempty"`
	Stall       *StallInfo           `json:"stall,omitempty"`
	Pending     *PendingApproval     `json:"pending,omitempty"`
	Budget      *models.BudgetStatus `json:"budget,omitempty"`

	Turns int               `json:"turns"`
	Usage models.TokenUsage `json:"usage"`

	// Result is the final answer. It is the model's raw output: whoever serves it to a
	// caller must pass it through the agent's output guardrails first.
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`

	Attempts int               `json:"attempts"`
	Metadata map[string]string `json:"metadata,omitempty"`
	// Owner is the replica running the task, when replicas share tasks (see TaskLeases).
	Owner     string    `json:"owner,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskEvent is one entry in a task's event log.
type TaskEvent struct {
	Seq   int64     `json:"seq"`
	Time  time.Time `json:"time"`
	Event Event     `json:"event"`
}

// TaskManagerOptions bound how much runs at once.
type TaskManagerOptions struct {
	PerKitchen int // tasks running at once in one kitchen (default 4)
	Global     int // tasks running at once (default 16)
	QueueMax   int // tasks waiting for a slot before new ones are refused (default 64)
	// MaxEvents is how many events a task keeps for viewers (default 5000).
	MaxEvents int
	// Retain is how long a finished task stays in memory with its events (default 1h).
	Retain time.Duration
	// RedactFor builds the event filter for a task that is resumed without a caller (an
	// automatic recovery). Nil means events are shown as they are.
	RedactFor func(*models.Agent) func(Event) Event
}

func (o *TaskManagerOptions) defaults() {
	if o.PerKitchen <= 0 {
		o.PerKitchen = 4
	}
	if o.Global <= 0 {
		o.Global = 16
	}
	if o.QueueMax <= 0 {
		o.QueueMax = 64
	}
	if o.MaxEvents <= 0 {
		o.MaxEvents = 5000
	}
	if o.Retain <= 0 {
		o.Retain = time.Hour
	}
}

// TaskManager runs agent loops in the background. A task is a run: it has the run's trace
// ID, its durable state is the run's journal, and everything here is the live layer over
// that: the goroutine, the cancel handle, the queue and the event log viewers watch. After a
// restart the live layer is gone and a task is read back from its journal.
type TaskManager struct {
	e    *Executor
	opts TaskManagerOptions

	mu        sync.Mutex
	cond      *sync.Cond
	tasks     map[string]*task
	running   int
	waiting   int
	byKitchen map[string]int

	// leases, owner and leaseTTL are set by SetLeases; onPause by SetOnPause (see task_lease.go).
	leases     TaskLeases
	recovering map[string]bool // tasks this process is in the middle of recovering (see Recover)
	owner      string
	leaseTTL   time.Duration
	onPause    func(TaskSnapshot)
}

type task struct {
	mu       sync.Mutex
	snap     TaskSnapshot
	spec     TaskSpec
	baseCtx  context.Context
	cancel   context.CancelFunc
	canceled bool
	hasSlot  bool
	// hold is the task's lease while this replica has it; leaseLost is set when it was taken away.
	hold      *leaseHold
	leaseLost bool

	events  []TaskEvent
	nextSeq int64
	notify  chan struct{}
	max     int
}

// SetRedactFor sets the event filter used for tasks resumed without a caller (automatic
// recovery), where no request is there to supply one.
func (m *TaskManager) SetRedactFor(f func(*models.Agent) func(Event) Event) {
	m.mu.Lock()
	m.opts.RedactFor = f
	m.mu.Unlock()
}

// NewTaskManager creates a manager over e. e must have a journal: a task that cannot be
// resumed after a restart is not worth starting in the background.
func NewTaskManager(e *Executor, opts TaskManagerOptions) *TaskManager {
	opts.defaults()
	m := &TaskManager{e: e, opts: opts, tasks: map[string]*task{}, byKitchen: map[string]int{}}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// canRunLocked reports whether a task of kitchen could take a slot right now.
func (m *TaskManager) canRunLocked(kitchen string) bool {
	return m.running < m.opts.Global && m.byKitchen[kitchen] < m.opts.PerKitchen
}

func (m *TaskManager) reserveLocked(kitchen string) {
	m.running++
	m.byKitchen[kitchen]++
}

func (m *TaskManager) releaseSlot(kitchen string) {
	m.mu.Lock()
	m.running--
	m.byKitchen[kitchen]--
	if m.byKitchen[kitchen] <= 0 {
		delete(m.byKitchen, kitchen)
	}
	m.mu.Unlock()
	m.cond.Broadcast()
}

// Submit accepts a task and starts it as soon as a slot is free. reqCtx supplies only values
// (it is detached from its cancellation, so the task outlives the request that created it).
func (m *TaskManager) Submit(reqCtx context.Context, spec TaskSpec) (TaskSnapshot, error) {
	if m.e.journal == nil {
		return TaskSnapshot{}, fmt.Errorf("executor: background tasks need the execution journal, which is not enabled")
	}
	if spec.ID == "" || spec.Agent == nil || spec.Resolved == nil {
		return TaskSnapshot{}, fmt.Errorf("executor: a task needs an ID, an agent and its resolved config")
	}
	t := &task{
		spec:    spec,
		baseCtx: context.WithoutCancel(reqCtx),
		notify:  make(chan struct{}),
		max:     m.opts.MaxEvents,
		snap: TaskSnapshot{
			ID: spec.ID, Agent: spec.Agent.Name, Kitchen: spec.Agent.Kitchen, Status: TaskQueued,
			Metadata: spec.Metadata, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		},
	}
	if err := m.takeLease(reqCtx, t); err != nil {
		return TaskSnapshot{}, err
	}
	if err := m.admit(t); err != nil {
		m.dropLease(t)
		return TaskSnapshot{}, err
	}
	m.mu.Lock()
	m.tasks[spec.ID] = t
	m.mu.Unlock()

	go m.attempt(t, func(ctx context.Context, onEvent func(Event) error) (string, *ExecutionTrace, error) {
		return m.e.ExecuteStreamMessage(WithTraceID(ctx, spec.ID), spec.Agent, spec.Message, spec.Resolved, spec.Vars, spec.Thinking, onEvent, spec.SessionID)
	})
	return t.snapshot(), nil
}

// admit takes a slot for t if one is free, or queues it, or refuses it when the queue is full.
func (m *TaskManager) admit(t *task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kitchen := t.snap.Kitchen
	if m.canRunLocked(kitchen) {
		m.reserveLocked(kitchen)
		t.mu.Lock()
		t.hasSlot = true
		t.mu.Unlock()
		return nil
	}
	if m.waiting >= m.opts.QueueMax {
		return ErrTaskQueueFull
	}
	m.waiting++
	return nil
}

// acquire blocks until t has a slot, or it is cancelled while waiting (false).
func (m *TaskManager) acquire(t *task) bool {
	// A slot reserved at admission is consumed here, so each attempt owns exactly one.
	t.mu.Lock()
	has := t.hasSlot
	t.hasSlot = false
	t.mu.Unlock()
	if has {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for !m.canRunLocked(t.snap.Kitchen) {
		t.mu.Lock()
		c := t.canceled
		t.mu.Unlock()
		if c {
			m.waiting--
			return false
		}
		m.cond.Wait()
	}
	t.mu.Lock()
	c := t.canceled
	t.mu.Unlock()
	if c {
		m.waiting--
		return false
	}
	m.waiting--
	m.reserveLocked(t.snap.Kitchen)
	return true
}

// attempt runs one stretch of a task (its first, or one resume) to wherever it stops.
func (m *TaskManager) attempt(t *task, run func(ctx context.Context, onEvent func(Event) error) (string, *ExecutionTrace, error)) {
	defer m.dropLease(t)
	if !m.acquire(t) {
		m.finishCancelled(t, false)
		m.scheduleEviction(t)
		return
	}
	defer m.releaseSlot(t.snap.Kitchen)

	ctx, cancel := context.WithCancel(WithTaskMode(t.baseCtx))
	ctx = m.fencedContext(ctx, t)
	t.mu.Lock()
	if t.leaseLost { // another replica took the task while it waited for a slot
		t.mu.Unlock()
		cancel()
		m.abandonLocal(t)
		return
	}
	if t.canceled { // cancelled between getting the slot and starting
		t.mu.Unlock()
		cancel()
		m.finishCancelled(t, false)
		return
	}
	t.cancel = cancel
	t.snap.Attempts++
	t.mu.Unlock()
	t.setStatus(TaskRunning, "")

	resp, trace, err := run(ctx, func(ev Event) error {
		t.observe(ev)
		return ctx.Err()
	})
	cancel()
	m.settle(t, resp, trace, err)
	m.scheduleEviction(t)
}

// scheduleEviction drops a finished task's live record (and its event log) after a while.
// Its state stays readable from the journal; only the events are gone.
func (m *TaskManager) scheduleEviction(t *task) {
	if !t.snapshot().Status.Terminal() {
		return
	}
	time.AfterFunc(m.opts.Retain, func() {
		if t.snapshot().Status.Terminal() {
			m.mu.Lock()
			if m.tasks[t.snap.ID] == t {
				delete(m.tasks, t.snap.ID)
			}
			m.mu.Unlock()
		}
	})
}

// settle records how an attempt ended.
func (m *TaskManager) settle(t *task, resp string, trace *ExecutionTrace, err error) {
	var paused *PausedError
	t.mu.Lock()
	cancelled := t.canceled
	lost := t.leaseLost
	t.mu.Unlock()

	switch {
	case lost && err != nil:
		// The task is another replica's now: record nothing about it here, not even a failure.
		m.abandonLocal(t)
		return
	case err == nil:
		t.mu.Lock()
		t.snap.Result = resp
		t.mu.Unlock()
		if trace != nil && trace.Outcome == OutcomeMaxTurns {
			t.setStatus(TaskMaxTurns, "")
		} else {
			t.setStatus(TaskCompleted, "")
		}
	case errors.As(err, &paused):
		t.mu.Lock()
		t.snap.PauseKind, t.snap.BudgetPause, t.snap.Stall, t.snap.Pending = string(paused.Kind), paused.Budget, paused.Stall, nil
		t.mu.Unlock()
		t.setStatus(TaskPaused, "")
		defer m.firePause(t)
	case errors.Is(err, ErrPausedForApproval):
		t.mu.Lock()
		t.snap.PauseKind, t.snap.BudgetPause = string(PauseApproval), nil
		if trace != nil {
			t.snap.Pending = trace.Pending
		}
		t.mu.Unlock()
		t.setStatus(TaskPaused, "")
		defer m.firePause(t)
	case cancelled:
		m.finishCancelled(t, true)
	case errors.Is(err, ErrBudgetExhausted):
		t.setStatus(TaskBudgetExhausted, err.Error())
	case errors.Is(err, ErrStalled):
		t.setStatus(TaskStalled, err.Error())
	default:
		// The run's journal says whether it exists. One that never got as far as writing a
		// header could not be run at all; one that did is resumable.
		if _, lerr := m.e.RunState(context.Background(), t.snap.Kitchen, t.snap.ID); lerr != nil {
			t.setStatus(TaskFailed, err.Error())
		} else {
			t.setStatus(TaskInterrupted, err.Error())
		}
	}
	if trace != nil {
		t.mu.Lock()
		t.snap.Usage = trace.Usage
		if trace.Budget != nil {
			t.snap.Budget = trace.Budget
		}
		t.mu.Unlock()
	}
}

// finishCancelled records a cancellation, ending the run in its journal if it has one.
func (m *TaskManager) finishCancelled(t *task, _ bool) {
	if js, err := m.e.RunState(context.Background(), t.snap.Kitchen, t.snap.ID); err == nil && js.Status != RunDone {
		if merr := m.e.journal.MarkOutcome(context.Background(), t.snap.ID, OutcomeCancelled, "", errors.New("cancelled")); merr != nil {
			log.Warn().Err(merr).Str("task_id", t.snap.ID).Msg("Failed to record the cancellation in the journal")
		}
	}
	t.setStatus(TaskCancelled, "")
}

// abandonLocal forgets a task that another replica owns now. Its state stays in the journal.
func (m *TaskManager) abandonLocal(t *task) {
	t.setStatus(TaskInterrupted, "the task moved to another replica")
	m.mu.Lock()
	if m.tasks[t.snap.ID] == t {
		delete(m.tasks, t.snap.ID)
	}
	m.mu.Unlock()
}

func (t *task) snapshot() TaskSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snap
}

// setStatus changes the status and tells watchers.
func (t *task) setStatus(s TaskStatus, errMsg string) {
	t.mu.Lock()
	t.snap.Status = s
	if errMsg != "" {
		t.snap.Error = errMsg
	}
	t.snap.UpdatedAt = time.Now().UTC()
	snap := t.snap
	t.appendLocked(Event{Type: EventTaskStatus, Content: string(s), Err: errMsg}, &snap)
	t.mu.Unlock()
}

// observe records one executor event: it updates the live view and, once redacted, the log.
func (t *task) observe(ev Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch ev.Type {
	case EventTurnEnd:
		t.snap.Turns = ev.Turn
		if ev.Usage != nil {
			t.snap.Usage = *ev.Usage
		}
		if ev.Budget != nil {
			t.snap.Budget = ev.Budget
		}
	case EventBudgetPaused:
		t.snap.BudgetPause = ev.BudgetPause
	}
	t.appendLocked(ev, nil)
}

// maxEventText bounds the text one logged event carries; a tool result can be enormous and
// the log is held in memory for as long as viewers may want it.
const maxEventText = 8 * 1024

func (t *task) appendLocked(ev Event, _ *TaskSnapshot) {
	if t.spec.Redact != nil {
		ev = t.spec.Redact(ev)
	}
	ev.Trace = nil // the final trace is large and is the journal's job
	ev.Content = clip(ev.Content)
	if ev.ToolResult != nil {
		r := *ev.ToolResult
		r.Content = clip(r.Content)
		ev.ToolResult = &r
	}
	t.nextSeq++
	t.events = append(t.events, TaskEvent{Seq: t.nextSeq, Time: time.Now().UTC(), Event: ev})
	if over := len(t.events) - t.max; over > 0 {
		t.events = append([]TaskEvent(nil), t.events[over:]...)
	}
	close(t.notify)
	t.notify = make(chan struct{})
}

func clip(s string) string {
	if len(s) <= maxEventText {
		return s
	}
	return s[:maxEventText] + fmt.Sprintf("… [%d more bytes]", len(s)-maxEventText)
}

// find returns the live task id in kitchen. A task of another kitchen is not found.
func (m *TaskManager) find(kitchen, id string) *task {
	m.mu.Lock()
	t := m.tasks[id]
	m.mu.Unlock()
	if t == nil || t.snap.Kitchen != kitchen {
		return nil
	}
	return t
}

// Get returns a task. A live one is read from memory; any other run is read back from its
// journal, so a task survives a restart for as long as its journal does.
func (m *TaskManager) Get(ctx context.Context, kitchen, id string) (TaskSnapshot, error) {
	if t := m.find(kitchen, id); t != nil {
		return t.snapshot(), nil
	}
	return m.fromJournal(ctx, kitchen, id)
}

func (m *TaskManager) fromJournal(ctx context.Context, kitchen, id string) (TaskSnapshot, error) {
	js, err := m.e.RunState(ctx, kitchen, id)
	if err != nil {
		return TaskSnapshot{}, ErrTaskNotFound
	}
	s := snapshotOf(id, js)
	if s.Status == TaskInterrupted {
		// A run the journal calls running that no replica is running is interrupted; one a
		// replica holds the lease on is running there.
		if holder := m.holderOf(ctx, id); holder != "" {
			s.Status, s.Owner = TaskRunning, holder
		}
	}
	return s, nil
}

func snapshotOf(id string, js *JournalState) TaskSnapshot {
	s := TaskSnapshot{
		ID: id, Kitchen: js.Header.Kitchen, Turns: js.CompletedTurns, Usage: js.Usage,
		CreatedAt: js.Header.StartedAt, UpdatedAt: js.Header.StartedAt,
	}
	if js.Header.Agent != nil {
		s.Agent = js.Header.Agent.Name
	}
	if js.Budget != nil {
		s.Budget = &models.BudgetStatus{Limits: *js.Budget, SpentCostUSD: js.Usage.EstimatedCost, SpentTokens: js.Usage.TotalTokens,
			SpentActiveSecs: js.ActiveMs / 1000, Turns: js.CompletedTurns}
	}
	switch js.Status {
	case RunDone:
		switch js.Outcome {
		case OutcomeCompleted:
			s.Status, s.Result = TaskCompleted, js.FinalContent
		case OutcomeMaxTurns:
			s.Status, s.Result = TaskMaxTurns, js.FinalContent
		case OutcomeBudgetExhausted:
			s.Status = TaskBudgetExhausted
		case OutcomeCancelled:
			s.Status = TaskCancelled
		case OutcomeAbandoned:
			s.Status = TaskAbandoned
		case OutcomeStalled:
			s.Status = TaskStalled
		default:
			s.Status = TaskFailed
		}
		s.Error = js.FinalErr
	case RunPaused:
		s.Status, s.PauseKind, s.Pending, s.BudgetPause, s.Stall = TaskPaused, pauseKindOf(js), js.Pending, js.BudgetPause, js.StallPause
	default:
		s.Status = TaskInterrupted
	}
	return s
}

// List returns the kitchen's tasks that are live in this process or unfinished in the
// journal, newest first. A task that finished before the last restart is still reachable by
// ID (Get) for as long as its journal is kept, but is not listed.
func (m *TaskManager) List(ctx context.Context, kitchen string) []TaskSnapshot {
	seen := map[string]bool{}
	var out []TaskSnapshot
	m.mu.Lock()
	live := make([]*task, 0, len(m.tasks))
	for _, t := range m.tasks {
		live = append(live, t)
	}
	m.mu.Unlock()
	for _, t := range live {
		if t.snap.Kitchen == kitchen {
			out = append(out, t.snapshot())
			seen[t.snap.ID] = true
		}
	}
	if m.e.journal != nil {
		if un, err := m.e.journal.ListUnfinished(ctx, kitchen); err == nil {
			for _, u := range un {
				if seen[u.TraceID] {
					continue
				}
				if snap, err := m.fromJournal(ctx, kitchen, u.TraceID); err == nil {
					out = append(out, snap)
				}
			}
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt.After(out[k].CreatedAt) })
	return out
}

// Cancel stops a task. It is safe to call on a task that is finishing, finished or gone:
// whichever happened first stands, and the current snapshot is returned.
func (m *TaskManager) Cancel(ctx context.Context, kitchen, id string) (TaskSnapshot, error) {
	if t := m.find(kitchen, id); t != nil {
		t.mu.Lock()
		if t.snap.Status.Terminal() {
			s := t.snap
			t.mu.Unlock()
			return s, nil
		}
		status := t.snap.Status
		t.canceled = true
		cancel := t.cancel
		t.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		m.cond.Broadcast() // wakes a queued task so it notices
		if status == TaskPaused || status == TaskInterrupted {
			// Nothing is running to notice the cancel; end it here.
			m.finishCancelled(t, true)
		}
		return t.snapshot(), nil
	}
	// Not live in this process: a paused or interrupted run that was read back from the journal.
	js, err := m.e.RunState(ctx, kitchen, id)
	if err != nil {
		return TaskSnapshot{}, ErrTaskNotFound
	}
	if js.Status == RunRunning {
		// Another replica may be running it. It cannot be stopped from here, but the owner can be
		// asked: it sees the request on its next lease renewal and cancels the task itself.
		if leases, _, _ := m.leaseConfig(); leases != nil {
			if holder := m.holderOf(ctx, id); holder != "" {
				if ok, _ := leases.RequestCancel(ctx, id); ok {
					s := snapshotOf(id, js)
					s.Status, s.Owner = TaskRunning, holder
					return s, nil
				}
			}
		}
	}
	if js.Status != RunDone {
		// Ending a run nobody holds must not race another replica adopting it: a lease that has just
		// expired is still a task someone may be about to resume. Take the lease first; if someone
		// else got it in between, the run is theirs and the owner is asked to cancel it.
		if leases, owner, ttl := m.leaseConfig(); leases != nil {
			if ok, err := leases.Acquire(ctx, id, owner, ttl); err != nil || !ok {
				if rc, _ := leases.RequestCancel(ctx, id); rc {
					s := snapshotOf(id, js)
					s.Status, s.Owner = TaskRunning, m.holderOf(ctx, id)
					return s, nil
				}
				return snapshotOf(id, js), nil
			}
			defer func() { _ = leases.Release(context.WithoutCancel(ctx), id, owner) }()
		}
		_ = m.e.journal.MarkOutcome(ctx, id, OutcomeCancelled, "", errors.New("cancelled"))
		js, _ = m.e.RunState(ctx, kitchen, id)
	}
	return snapshotOf(id, js), nil
}

// Resume continues a paused or interrupted task. decision is required for a paused one (a
// budget extension, or an approval of the pending tool calls) and nil for an interrupted one.
// redact filters what viewers see of the resumed stretch; nil keeps the task's own filter.
func (m *TaskManager) Resume(ctx context.Context, kitchen, agentName, id string, decision *ResumeDecision, redact func(Event) Event) (TaskSnapshot, error) {
	t := m.find(kitchen, id)
	adopted := false
	if t == nil {
		js, err := m.e.RunState(ctx, kitchen, id)
		if err != nil || js.Header.Agent == nil || js.Header.Agent.Name != agentName {
			return TaskSnapshot{}, ErrTaskNotFound
		}
		snap := snapshotOf(id, js)
		t = &task{notify: make(chan struct{}), max: m.opts.MaxEvents, snap: snap}
		m.mu.Lock()
		redactFor := m.opts.RedactFor
		m.mu.Unlock()
		if redact == nil && redactFor != nil {
			redact = redactFor(js.Header.Agent)
		}
		t.spec = TaskSpec{ID: id, Agent: js.Header.Agent, Redact: redact}
		m.mu.Lock()
		if existing := m.tasks[id]; existing != nil { // two callers racing to adopt the same run
			t = existing
		} else {
			m.tasks[id] = t
			adopted = true
		}
		m.mu.Unlock()
	}
	// A task adopted from the journal only to be refused (it is running on another replica, or
	// finished) must not stay in the registry as if this process had it.
	forget := func() {
		if adopted {
			m.mu.Lock()
			if m.tasks[id] == t {
				delete(m.tasks, id)
			}
			m.mu.Unlock()
		}
	}
	if t.snap.Agent != agentName {
		forget()
		return TaskSnapshot{}, ErrTaskNotFound
	}
	m.refreshIfShared(ctx, kitchen, t)

	t.mu.Lock()
	if t.snap.Status != TaskPaused && t.snap.Status != TaskInterrupted {
		t.mu.Unlock()
		forget()
		return t.snapshot(), ErrTaskNotResumable
	}
	if redact != nil {
		t.spec.Redact = redact
	}
	t.baseCtx = context.WithoutCancel(ctx)
	t.canceled = false
	t.snap.Status = TaskQueued
	t.snap.PauseKind, t.snap.BudgetPause, t.snap.Stall, t.snap.Pending, t.snap.Error = "", nil, nil, nil, ""
	t.mu.Unlock()

	if err := m.takeLease(ctx, t); err != nil {
		// Another replica holds the task: put it back as the journal has it.
		m.refreshIfShared(ctx, kitchen, t)
		forget()
		if errors.Is(err, ErrTaskOwned) {
			return t.snapshot(), ErrTaskNotResumable
		}
		return t.snapshot(), err
	}
	if err := m.admit(t); err != nil {
		m.dropLease(t)
		// Put it back as it was so the caller can try again.
		if js, jerr := m.e.RunState(ctx, kitchen, id); jerr == nil {
			s := snapshotOf(id, js)
			t.mu.Lock()
			t.snap.Status, t.snap.PauseKind, t.snap.Pending, t.snap.BudgetPause, t.snap.Stall = s.Status, s.PauseKind, s.Pending, s.BudgetPause, s.Stall
			t.mu.Unlock()
		}
		return t.snapshot(), err
	}
	go m.attempt(t, func(ctx context.Context, onEvent func(Event) error) (string, *ExecutionTrace, error) {
		return m.e.ResumeStream(ctx, kitchen, agentName, id, decision, onEvent)
	})
	return t.snapshot(), nil
}

// refreshIfShared brings a task this process remembers as paused or interrupted back in line with
// the journal when replicas share tasks: another replica may have resumed, finished or cancelled it
// since. A task running here is never touched.
func (m *TaskManager) refreshIfShared(ctx context.Context, kitchen string, t *task) {
	if leases, _, _ := m.leaseConfig(); leases == nil {
		return
	}
	t.mu.Lock()
	st, active := t.snap.Status, t.hold != nil
	t.mu.Unlock()
	if active || (st != TaskPaused && st != TaskInterrupted && st != TaskRunning) {
		return // running (or about to run) here: this process knows better than the journal
	}
	js, err := m.e.RunState(ctx, kitchen, t.snap.ID)
	if err != nil {
		return
	}
	s := snapshotOf(t.snap.ID, js)
	if s.Status == TaskInterrupted {
		if holder := m.holderOf(ctx, t.snap.ID); holder != "" && holder != m.Owner() {
			s.Status, s.Owner = TaskRunning, holder
		}
	}
	t.mu.Lock()
	t.snap.Status, t.snap.PauseKind, t.snap.Pending, t.snap.BudgetPause, t.snap.Stall = s.Status, s.PauseKind, s.Pending, s.BudgetPause, s.Stall
	t.snap.Owner, t.snap.Turns, t.snap.Usage, t.snap.Result, t.snap.Error = s.Owner, s.Turns, s.Usage, s.Result, s.Error
	t.mu.Unlock()
}

// Events returns the task's events after afterSeq, whether the task has finished (so the
// caller can stop once it has delivered them), and a channel that closes when more arrive.
// For a task with no live log (read back from the journal) it returns no events and done.
func (m *TaskManager) Events(kitchen, id string, afterSeq int64) (events []TaskEvent, done bool, wait <-chan struct{}, err error) {
	t := m.find(kitchen, id)
	if t == nil {
		return nil, true, nil, ErrTaskNotFound
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ev := range t.events {
		if ev.Seq > afterSeq {
			events = append(events, ev)
		}
	}
	return events, t.snap.Status.Terminal(), t.notify, nil
}

// Recover resumes, one at a time and with growing delays, the tasks that were running when their
// process stopped. Each run is resumed at most maxRecoveries times, counted in its journal so a run
// that keeps killing the process is given up on. Without auto recovery (the default) interrupted
// tasks are only listed, and a person resumes them.
//
// When replicas share tasks (SetLeases) a task that another replica is running, or has live here, is
// left alone, and the lease is taken before the recovery is counted, so of several replicas looking
// at the same dead task exactly one recovers it.
func (m *TaskManager) Recover(ctx context.Context, maxRecoveries int, baseDelay time.Duration) {
	if m.e.journal == nil {
		return
	}
	un, err := m.e.journal.ListUnfinished(ctx, "")
	if err != nil {
		return
	}
	leases, owner, ttl := m.leaseConfig()
	for _, u := range un {
		if u.Status != RunRunning {
			continue // paused runs wait for a person; only interrupted ones are recovered
		}
		if leases != nil {
			if t := m.find(u.Kitchen, u.TraceID); t != nil {
				t.mu.Lock()
				here := t.hold != nil
				t.mu.Unlock()
				if here {
					continue // running right here
				}
			}
			if holder := m.holderOf(ctx, u.TraceID); holder != "" {
				continue // running on a replica, or already being recovered by one (this one included)
			}
			if !m.beginRecovery(u.TraceID) {
				continue // a concurrent Recover in this process has it
			}
		}
		giveUp := func() {
			if leases != nil {
				m.endRecovery(u.TraceID)
			}
		}
		js, err := m.e.journal.Load(ctx, u.TraceID)
		if err != nil {
			giveUp()
			continue
		}
		if js.Recoveries >= maxRecoveries {
			log.Warn().Str("trace_id", u.TraceID).Int("attempts", js.Recoveries).Msg("Task keeps failing across restarts; leaving it interrupted for a person to look at")
			giveUp()
			continue
		}
		var claimed bool
		if leases != nil {
			// Hold the task through the backoff delay; Resume takes the lease again (it already is ours).
			ok, aerr := leases.Acquire(ctx, u.TraceID, owner, ttl+baseDelay<<uint(maxRecoveries))
			if aerr != nil || !ok {
				giveUp()
				continue
			}
			claimed = true
		}
		n, err := m.e.journal.NoteRecovery(ctx, u.TraceID)
		if err != nil {
			if claimed {
				_ = leases.Release(ctx, u.TraceID, owner)
			}
			giveUp()
			continue
		}
		delay := baseDelay << uint(n-1)
		u := u
		time.AfterFunc(delay, func() {
			defer giveUp()
			if _, err := m.Resume(context.Background(), u.Kitchen, u.AgentName, u.TraceID, nil, nil); err != nil {
				log.Warn().Err(err).Str("trace_id", u.TraceID).Msg("Automatic task recovery did not start")
				if claimed {
					_ = leases.Release(context.Background(), u.TraceID, owner)
				}
			}
		})
	}
}
