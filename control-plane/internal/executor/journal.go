package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Journal durably records an episode's progress, turn by turn, so a run that
// is interrupted — a crashed process, a restart, a deploy mid-loop — can be
// resumed from its last completed turn instead of starting over and paying
// for every tool call and model turn a second time.
//
// This is deliberately the narrower, honest version of durable execution: a
// journal records what happened and Resume replays it, but nothing in this
// package decides *when* to call Resume after a crash — that is a product
// policy (resume everything on startup? only what a person asks for? only
// runs younger than an hour?) that belongs to whatever embeds the executor,
// not to the executor itself. ListUnfinished is the primitive that policy is
// built on.
type Journal interface {
	// Start records the header for a new run. Must be called before any
	// AppendTurn/MarkPaused/MarkDone for the same TraceID.
	Start(ctx context.Context, header JournalHeader) error

	// AppendTurn records one completed turn and the full message history as
	// of right after it — the state a resume needs to pick up cleanly. It is the
	// schema-1 writer, kept so old callers and old journals keep working; the
	// executor writes AppendAssistant / AppendToolResult / CommitTurn instead, which
	// grow with the run rather than with its square.
	AppendTurn(ctx context.Context, traceID string, turn Turn, messages []models.ChatMessage, usage models.TokenUsage) error

	// AppendAssistant records the model's reply the moment it comes back, before any
	// tool runs: the call was paid for and its usage, text and tool calls survive a
	// crash that happens while the tools run. calls is empty for a final answer.
	AppendAssistant(ctx context.Context, traceID string, turn int, msg models.ChatMessage, calls []ToolCall, usage models.TokenUsage, latencyMs int64) error

	// AppendToolResult records one tool call's outcome as soon as it finishes, so a
	// crash part-way through a turn keeps the calls that already ran and a resume knows
	// which side effects have happened.
	AppendToolResult(ctx context.Context, traceID string, turn int, result ToolResult) error

	// CommitTurn ends a turn. messages is the whole history as of now; the journal
	// stores only what was added since the previous commit (or, when the history was
	// rewritten rather than extended, a full snapshot). turn.Request is not stored.
	CommitTurn(ctx context.Context, traceID string, turn Turn, messages []models.ChatMessage) error

	// Snapshot replaces the stored history with messages. Call it after rewriting the
	// history in place (compaction); CommitTurn also falls back to it when it notices
	// the history no longer extends what it stored. usage is what the rewrite itself cost
	// (the summarizing call), so it counts toward the run's spend.
	Snapshot(ctx context.Context, traceID string, messages []models.ChatMessage, usage models.TokenUsage) error

	// AppendNotes records the run's working notes (see agentoven_notes), whole, each time
	// they change.
	AppendNotes(ctx context.Context, traceID string, notes map[string]string) error

	// MarkStalled records that the run stopped because it kept repeating itself. Like a
	// budget pause it happens between turns, so the last commit is the checkpoint.
	MarkStalled(ctx context.Context, traceID string, stall StallInfo) error

	// MarkPaused records that the run stopped to wait for a human decision on
	// a pending tool call, and the message history as of right after the
	// model's request for it — the assistant's tool_calls message is already
	// in messages, but no tool result for it yet — so Resume has exactly what
	// it needs to append results and continue.
	MarkPaused(ctx context.Context, traceID string, pending PendingApproval, messages []models.ChatMessage) error

	// MarkBudgetPaused records that the run stopped at its budget. No messages are
	// written: the pause happens at the top of the loop, after the previous turn
	// was journaled, so that turn's record is already the checkpoint.
	MarkBudgetPaused(ctx context.Context, traceID string, pause models.BudgetPause) error

	// ClaimResume ends a pause so exactly one caller continues the run. It runs
	// under the run's lock: it reads the journal, and only if the run is still
	// paused does it append a "resumed" record (carrying ext, if the pause was a
	// budget one) and return true. A second concurrent caller sees the run is no
	// longer paused and gets false. Without it, two resumes of one approval would
	// both execute the approved tool.
	ClaimResume(ctx context.Context, traceID string, ext *models.BudgetExtension) (bool, error)

	// MarkDone records the run's outcome: the final content on success, or
	// runErr's message on failure. Either way, the run is finished and
	// ListUnfinished will no longer report it.
	MarkDone(ctx context.Context, traceID string, content string, runErr error) error

	// NoteRecovery records that an automatic recovery of the run was started and returns
	// how many have now been started, so a run that keeps dying is given up on instead of
	// being restarted forever.
	NoteRecovery(ctx context.Context, traceID string) (int, error)

	// MarkOutcome is MarkDone with the kind of ending spelled out (see the Outcome*
	// constants), for the endings that are not simply success or failure.
	MarkOutcome(ctx context.Context, traceID, outcome, content string, runErr error) error

	// Load reconstructs a run's state from its journal: the header it
	// started with, the message history as of the last completed turn, how
	// many turns completed, and whether it is still paused.
	Load(ctx context.Context, traceID string) (*JournalState, error)

	// ListUnfinished returns every run in kitchen whose journal has no "done"
	// record yet — the candidates a caller might choose to Resume after a
	// restart. kitchen="" lists across all kitchens.
	ListUnfinished(ctx context.Context, kitchen string) ([]JournalSummary, error)

	// Discard removes a finished run's journal. Safe to call on a run that is
	// still in progress — doing so abandons its resumability, which is the
	// caller's call to make, not this interface's to prevent.
	Discard(ctx context.Context, traceID string) error
}

// JournalHeader is the information a run needs recorded once, at the start,
// to be reconstructible later: not just what to resume, but what it was
// resuming *as* — the exact agent and resolved ingredients the run started
// with, not whatever they have become by the time someone resumes it. An
// agent re-baked between a crash and its resume must not silently change the
// tools or prompt an in-flight run is held to.
type JournalHeader struct {
	TraceID         string                      `json:"trace_id"`
	Kitchen         string                      `json:"kitchen"`
	Agent           *models.Agent               `json:"agent"`
	Resolved        *models.ResolvedIngredients `json:"resolved"`
	UserMessage     string                      `json:"user_message"`
	PromptVars      map[string]string           `json:"prompt_vars,omitempty"`
	ThinkingEnabled bool                        `json:"thinking_enabled,omitempty"`
	SessionID       string                      `json:"session_id,omitempty"`
	StartedAt       time.Time                   `json:"started_at"`
	// StartedBy is who started the run (see startedBy); empty for a system caller.
	StartedBy string `json:"started_by,omitempty"`
	// Budget is the limits the run started under, after tightening by the agent,
	// the request and any parent's grant. Extensions granted later are separate
	// records, so a resume rebuilds exactly the current limits.
	Budget *models.RunBudget `json:"budget,omitempty"`
	// ParentTraceID is set for a subagent's run. It lives and dies inside its
	// parent's tool call, so it is never offered as resumable on its own.
	ParentTraceID string `json:"parent_trace_id,omitempty"`
	// Schema is the journal format the run was written with (2 from this version on;
	// absent means 1). A file written under schema 2 cannot be resumed by an older
	// binary: it does not know the delta records.
	Schema int `json:"schema,omitempty"`
	// LongRun marks a background task, which gets the long-run defences in context.go
	// (compaction, capped tool results, working notes, stall detection, model retries). It is
	// recorded so a resume treats the run the way it began.
	LongRun bool `json:"long_run,omitempty"`
	// InitialMessages are the messages the first model call is sent. Without them a run
	// that dies before finishing its first turn has nothing to resume from.
	InitialMessages []models.ChatMessage `json:"initial_messages,omitempty"`
	// Hosted marks a one-call run that exists only to hold a tool call a hosted agent process asked the control
	// plane to make, while a person decides (hosted_tools.go). It has no model loop to resume.
	Hosted *HostedCall `json:"hosted,omitempty"`
}

// journalSchema is the format this binary writes.
const journalSchema = 2

// Outcomes recorded on a finished run.
const (
	OutcomeCompleted       = "completed"
	OutcomeFailed          = "failed"
	OutcomeMaxTurns        = "max_turns"
	OutcomeBudgetExhausted = "budget_exhausted"
	OutcomeAbandoned       = "abandoned"
	OutcomeCancelled       = "cancelled"
	OutcomeStalled         = "stalled"
)

// journalRecord is one line of a run's journal file. Exactly one of the
// pointer/value fields is populated per Type.
type journalRecord struct {
	// "header" | "turn" (schema 1) | "assistant" | "tool_result" | "commit" | "snapshot" |
	// "paused" | "budget_paused" | "resumed" | "done"
	Type     string               `json:"type"`
	Time     time.Time            `json:"time"`
	Header   *JournalHeader       `json:"header,omitempty"`
	Turn     *Turn                `json:"turn,omitempty"`
	Messages []models.ChatMessage `json:"messages,omitempty"`
	Usage    models.TokenUsage    `json:"usage,omitempty"`
	Pending  *PendingApproval     `json:"pending,omitempty"`
	Content  string               `json:"content,omitempty"`
	Err      string               `json:"err,omitempty"`

	BudgetPause *models.BudgetPause     `json:"budget_pause,omitempty"`
	Extension   *models.BudgetExtension `json:"extension,omitempty"`

	// schema 2
	TurnNo    int                  `json:"turn_no,omitempty"`
	Assistant *models.ChatMessage  `json:"assistant,omitempty"`
	Calls     []ToolCall           `json:"calls,omitempty"`
	Result    *ToolResult          `json:"result,omitempty"`
	LatencyMs int64                `json:"latency_ms,omitempty"`
	Delta     []models.ChatMessage `json:"delta,omitempty"`
	Outcome   string               `json:"outcome,omitempty"`
	Notes     map[string]string    `json:"notes,omitempty"`
	Stall     *StallInfo           `json:"stall,omitempty"`
}

// Run statuses as recorded in the journal and reported by Load/ListUnfinished.
const (
	RunRunning = "running"
	RunPaused  = "paused"
	RunDone    = "done"
)

// JournalState is a run reconstructed from its journal.
type JournalState struct {
	Header         JournalHeader
	Messages       []models.ChatMessage
	CompletedTurns int
	Status         string
	Pending        *PendingApproval
	Usage          models.TokenUsage
	FinalContent   string
	FinalErr       string

	// Base is the budget the run started under, and Budget what it is now after
	// every extension a person has granted. Both nil for an unbudgeted run.
	Base        *models.RunBudget
	Budget      *models.RunBudget
	Extensions  []models.BudgetExtension
	BudgetPause *models.BudgetPause
	// ActiveMs is working time so far: the sum of completed turns, never time spent
	// paused or down.
	ActiveMs int64
	// Claimed is true when a pause was ended by ClaimResume but the resumed turn was
	// never journaled, so whether its approved tool calls ran is unknown.
	Claimed bool
	// InFlight is the turn the run was in the middle of: the model had answered and
	// some or all of its tool calls may have run, but the turn was never committed.
	// Nil between turns.
	InFlight *InFlightTurn
	// Outcome is how a finished run ended (see the Outcome* constants).
	Outcome string
	// Recoveries counts the automatic recoveries started for the run (see NoteRecovery).
	Recoveries int
	// Notes are the run's working notes as last saved.
	Notes map[string]string
	// StallPause is set while the run is paused for repeating itself.
	StallPause *StallInfo
}

// InFlightTurn is a turn that was started and not committed.
type InFlightTurn struct {
	Turn      int
	Assistant models.ChatMessage
	Calls     []ToolCall
	// Results holds the calls that finished, by tool call ID.
	Results map[string]ToolResult
}

// JournalSummary is what ListUnfinished reports — enough to decide whether to
// resume a run without loading its whole message history.
type JournalSummary struct {
	TraceID     string    `json:"trace_id"`
	Kitchen     string    `json:"kitchen"`
	AgentName   string    `json:"agent_name"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	LastUpdate  time.Time `json:"last_update"`
	TurnsLogged int       `json:"turns_logged"`
	// PauseKind says what a paused run is waiting for: "approval" or "budget".
	PauseKind string `json:"pause_kind,omitempty"`
}

// FileJournal is the OSS-default Journal: one append-only JSONL file per run
// under baseDir, named by trace ID, plus a small <trace>.meta.json beside it that lets a
// scan skip finished runs without reading them. No database required — matches the same
// "lean, self-contained" idiom as the local retention archiver (internal/retention) and
// the memory store's JSON snapshot.
//
// Appends within one run are serialized by a per-trace lock so concurrent
// writers (a resumed run racing a crash-recovery scan, say) cannot interleave
// partial JSON lines; across runs, writes are independent files and need no
// shared lock.
type FileJournal struct {
	baseDir string
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	runs    map[string]*fileRun
}

// fileRun is what the writer remembers about a run between appends: how much of the
// message history is already on disk, so a commit can write only what is new.
type fileRun struct {
	persisted int    // messages stored so far
	tail      uint64 // fingerprint of the last stored message
	meta      journalMeta
}

// journalMeta is the sidecar index. It is derived data: losing it costs a replay, never a run.
type journalMeta struct {
	TraceID       string    `json:"trace_id"`
	Kitchen       string    `json:"kitchen"`
	AgentName     string    `json:"agent_name"`
	ParentTraceID string    `json:"parent_trace_id,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	Done          bool      `json:"done"`
	Outcome       string    `json:"outcome,omitempty"`
	DoneAt        time.Time `json:"done_at,omitempty"`
}

// NewFileJournal creates a journal rooted at baseDir, creating it if needed.
// Defaults to ~/.agentoven/journal when baseDir is empty, matching the
// ~/.agentoven/archive convention used elsewhere.
func NewFileJournal(baseDir string) (*FileJournal, error) {
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory for default journal path: %w", err)
		}
		baseDir = filepath.Join(home, ".agentoven", "journal")
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create journal directory %s: %w", baseDir, err)
	}
	return &FileJournal{baseDir: baseDir, locks: make(map[string]*sync.Mutex), runs: make(map[string]*fileRun)}, nil
}

func (j *FileJournal) lockFor(traceID string) *sync.Mutex {
	j.mu.Lock()
	defer j.mu.Unlock()
	l, ok := j.locks[traceID]
	if !ok {
		l = &sync.Mutex{}
		j.locks[traceID] = l
	}
	return l
}

func (j *FileJournal) path(traceID string) string {
	return filepath.Join(j.baseDir, traceID+".jsonl")
}

func (j *FileJournal) metaPath(traceID string) string {
	return filepath.Join(j.baseDir, traceID+".meta.json")
}

// fingerprint identifies a message cheaply, so a commit can tell the history it is handed
// still starts with what was stored. It is a guard against a caller that rewrote history
// without saying so, not a security measure.
func fingerprint(m models.ChatMessage) uint64 {
	h := fnv.New64a()
	h.Write([]byte(m.Role))
	h.Write([]byte{0})
	h.Write([]byte(m.Name))
	h.Write([]byte{0})
	h.Write([]byte(m.ToolCallID))
	h.Write([]byte{0})
	h.Write([]byte(m.Content))
	for _, tc := range m.ToolCalls {
		h.Write([]byte{0})
		h.Write([]byte(tc.ID))
	}
	return h.Sum64()
}

// append writes records under the run's lock, keeping the writer's memory of the run (and
// its sidecar) in step with what reached the disk.
func (j *FileJournal) append(traceID string, recs ...journalRecord) error {
	l := j.lockFor(traceID)
	l.Lock()
	defer l.Unlock()
	// The first write this process makes to a run clears a torn last line, so a record is
	// never glued onto the end of a half-written one.
	if _, err := j.runLocked(traceID); err != nil {
		return err
	}
	for _, rec := range recs {
		if err := j.appendLocked(traceID, rec); err != nil {
			return err
		}
	}
	return nil
}

// appendLocked writes one record. The caller holds the run's lock.
func (j *FileJournal) appendLocked(traceID string, rec journalRecord) error {
	f, err := os.OpenFile(j.path(traceID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open journal for %s: %w", traceID, err)
	}
	defer f.Close()

	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode journal record for %s: %w", traceID, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write journal record for %s: %w", traceID, err)
	}
	return nil
}

// runLocked returns the writer's view of a run, rebuilding it from disk the first time this
// process touches it (after a restart, or a resume of a run another process began). A torn
// last line, from a crash mid-write, is cut off here so the next append starts on a clean
// line. The caller holds the run's lock.
func (j *FileJournal) runLocked(traceID string) (*fileRun, error) {
	j.mu.Lock()
	r, ok := j.runs[traceID]
	j.mu.Unlock()
	if ok {
		return r, nil
	}
	if err := j.trimTornTail(traceID); err != nil {
		return nil, err
	}
	records, err := j.readAll(traceID)
	if err != nil {
		return nil, err
	}
	st, err := replay(records)
	if err != nil {
		return nil, err
	}
	r = &fileRun{persisted: len(st.Messages), meta: metaFromState(traceID, st)}
	if n := len(st.Messages); n > 0 {
		r.tail = fingerprint(st.Messages[n-1])
	}
	j.mu.Lock()
	j.runs[traceID] = r
	j.mu.Unlock()
	return r, nil
}

func metaFromState(traceID string, st *JournalState) journalMeta {
	m := journalMeta{TraceID: traceID, Kitchen: st.Header.Kitchen, ParentTraceID: st.Header.ParentTraceID, StartedAt: st.Header.StartedAt}
	if st.Header.Agent != nil {
		m.AgentName = st.Header.Agent.Name
	}
	if st.Status == RunDone {
		m.Done, m.Outcome = true, st.Outcome
	}
	return m
}

// trimTornTail removes a final line that was cut off before its newline.
func (j *FileJournal) trimTornTail(traceID string) error {
	f, err := os.OpenFile(j.path(traceID), os.O_RDWR, 0o644)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("journal: no run found for trace %s", traceID)
		}
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return err
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, fi.Size()-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	// Walk back to the previous newline in blocks.
	const block = 64 * 1024
	end := fi.Size()
	cut := int64(0)
	buf := make([]byte, block)
	for end > 0 {
		start := end - block
		if start < 0 {
			start = 0
		}
		n, err := f.ReadAt(buf[:end-start], start)
		if err != nil && err != io.EOF {
			return err
		}
		if k := bytes.LastIndexByte(buf[:n], '\n'); k >= 0 {
			cut = start + int64(k) + 1
			break
		}
		end = start
	}
	log.Warn().Str("trace_id", traceID).Int64("dropped_bytes", fi.Size()-cut).Msg("Journal ended mid-record (a crash during a write); dropping the partial record")
	return f.Truncate(cut)
}

func (j *FileJournal) writeMeta(m journalMeta) {
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	tmp := j.metaPath(m.TraceID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, j.metaPath(m.TraceID)); err != nil {
		_ = os.Remove(tmp)
	}
}

func (j *FileJournal) readMeta(traceID string) (journalMeta, bool) {
	raw, err := os.ReadFile(j.metaPath(traceID))
	if err != nil {
		return journalMeta{}, false
	}
	var m journalMeta
	if json.Unmarshal(raw, &m) != nil || m.TraceID != traceID {
		return journalMeta{}, false
	}
	return m, true
}

func (j *FileJournal) Start(_ context.Context, header JournalHeader) error {
	if header.TraceID == "" {
		return fmt.Errorf("journal: header needs a trace ID")
	}
	if header.Schema == 0 {
		header.Schema = journalSchema
	}
	l := j.lockFor(header.TraceID)
	l.Lock()
	defer l.Unlock()
	if err := j.appendLocked(header.TraceID, journalRecord{Type: "header", Time: time.Now().UTC(), Header: &header}); err != nil {
		return err
	}
	run := &fileRun{persisted: len(header.InitialMessages)}
	if n := len(header.InitialMessages); n > 0 {
		run.tail = fingerprint(header.InitialMessages[n-1])
	}
	run.meta = journalMeta{TraceID: header.TraceID, Kitchen: header.Kitchen, ParentTraceID: header.ParentTraceID, StartedAt: header.StartedAt}
	if header.Agent != nil {
		run.meta.AgentName = header.Agent.Name
	}
	j.mu.Lock()
	j.runs[header.TraceID] = run
	j.mu.Unlock()
	j.writeMeta(run.meta)
	return nil
}

// AppendTurn is the schema-1 turn record, full history included. After it the writer's
// memory of the run is rebuilt on the next append.
func (j *FileJournal) AppendTurn(_ context.Context, traceID string, turn Turn, messages []models.ChatMessage, usage models.TokenUsage) error {
	l := j.lockFor(traceID)
	l.Lock()
	defer l.Unlock()
	j.mu.Lock()
	delete(j.runs, traceID)
	j.mu.Unlock()
	return j.appendLocked(traceID, journalRecord{Type: "turn", Time: time.Now().UTC(), Turn: &turn, Messages: messages, Usage: usage})
}

func (j *FileJournal) AppendAssistant(_ context.Context, traceID string, turn int, msg models.ChatMessage, calls []ToolCall, usage models.TokenUsage, latencyMs int64) error {
	return j.append(traceID, journalRecord{Type: "assistant", Time: time.Now().UTC(), TurnNo: turn, Assistant: &msg, Calls: calls, Usage: usage, LatencyMs: latencyMs})
}

func (j *FileJournal) AppendToolResult(_ context.Context, traceID string, turn int, result ToolResult) error {
	return j.append(traceID, journalRecord{Type: "tool_result", Time: time.Now().UTC(), TurnNo: turn, Result: &result})
}

func (j *FileJournal) CommitTurn(_ context.Context, traceID string, turn Turn, messages []models.ChatMessage) error {
	l := j.lockFor(traceID)
	l.Lock()
	defer l.Unlock()
	run, err := j.runLocked(traceID)
	if err != nil {
		return err
	}

	// What the journal keeps of the turn is its bookkeeping. The request is the whole
	// history again, and the calls and results are already stored as their own records.
	slim := Turn{Number: turn.Number, Response: turn.Response, LatencyMs: turn.LatencyMs, Usage: turn.Usage}

	rec := journalRecord{Type: "commit", Time: time.Now().UTC(), Turn: &slim}
	if run.persisted <= len(messages) && (run.persisted == 0 || fingerprint(messages[run.persisted-1]) == run.tail) {
		rec.Delta = messages[run.persisted:]
	} else {
		// The history is not an extension of what is stored: write all of it.
		rec.Messages = messages
	}
	if err := j.appendLocked(traceID, rec); err != nil {
		return err
	}
	run.persisted = len(messages)
	if n := len(messages); n > 0 {
		run.tail = fingerprint(messages[n-1])
	}
	return nil
}

func (j *FileJournal) AppendNotes(_ context.Context, traceID string, notes map[string]string) error {
	return j.append(traceID, journalRecord{Type: "notes", Time: time.Now().UTC(), Notes: notes})
}

func (j *FileJournal) MarkStalled(_ context.Context, traceID string, stall StallInfo) error {
	return j.append(traceID, journalRecord{Type: "stalled", Time: time.Now().UTC(), Stall: &stall})
}

func (j *FileJournal) Snapshot(_ context.Context, traceID string, messages []models.ChatMessage, usage models.TokenUsage) error {
	l := j.lockFor(traceID)
	l.Lock()
	defer l.Unlock()
	run, err := j.runLocked(traceID)
	if err != nil {
		return err
	}
	if err := j.appendLocked(traceID, journalRecord{Type: "snapshot", Time: time.Now().UTC(), Messages: messages, Usage: usage}); err != nil {
		return err
	}
	run.persisted = len(messages)
	run.tail = 0
	if n := len(messages); n > 0 {
		run.tail = fingerprint(messages[n-1])
	}
	return nil
}

func (j *FileJournal) MarkPaused(_ context.Context, traceID string, pending PendingApproval, messages []models.ChatMessage) error {
	l := j.lockFor(traceID)
	l.Lock()
	defer l.Unlock()
	run, err := j.runLocked(traceID)
	if err != nil {
		return err
	}
	if err := j.appendLocked(traceID, journalRecord{Type: "paused", Time: time.Now().UTC(), Pending: &pending, Messages: messages}); err != nil {
		return err
	}
	// A pause stores the whole history, so later commits continue from it.
	run.persisted = len(messages)
	run.tail = 0
	if n := len(messages); n > 0 {
		run.tail = fingerprint(messages[n-1])
	}
	return nil
}

func (j *FileJournal) MarkBudgetPaused(_ context.Context, traceID string, pause models.BudgetPause) error {
	return j.append(traceID, journalRecord{Type: "budget_paused", Time: time.Now().UTC(), BudgetPause: &pause})
}

func (j *FileJournal) ClaimResume(_ context.Context, traceID string, ext *models.BudgetExtension) (bool, error) {
	l := j.lockFor(traceID)
	l.Lock()
	defer l.Unlock()
	if _, err := j.runLocked(traceID); err != nil {
		return false, err
	}

	records, err := j.readAll(traceID)
	if err != nil {
		return false, err
	}
	st, err := replay(records)
	if err != nil {
		return false, err
	}
	if st.Status != RunPaused {
		return false, nil // someone else already resumed it, or it never paused
	}
	rec := journalRecord{Type: "resumed", Time: time.Now().UTC(), Extension: ext}
	if err := j.appendLocked(traceID, rec); err != nil {
		return false, err
	}
	return true, nil
}

func (j *FileJournal) MarkDone(ctx context.Context, traceID string, content string, runErr error) error {
	outcome := OutcomeCompleted
	switch {
	case errors.Is(runErr, ErrBudgetExhausted):
		outcome = OutcomeBudgetExhausted
	case runErr != nil:
		outcome = OutcomeFailed
	}
	return j.MarkOutcome(ctx, traceID, outcome, content, runErr)
}

func (j *FileJournal) NoteRecovery(_ context.Context, traceID string) (int, error) {
	l := j.lockFor(traceID)
	l.Lock()
	defer l.Unlock()
	if _, err := j.runLocked(traceID); err != nil {
		return 0, err
	}
	if err := j.appendLocked(traceID, journalRecord{Type: "recovery", Time: time.Now().UTC()}); err != nil {
		return 0, err
	}
	records, err := j.readAll(traceID)
	if err != nil {
		return 0, err
	}
	st, err := replay(records)
	if err != nil {
		return 0, err
	}
	return st.Recoveries, nil
}

func (j *FileJournal) MarkOutcome(_ context.Context, traceID, outcome, content string, runErr error) error {
	l := j.lockFor(traceID)
	l.Lock()
	defer l.Unlock()
	if _, err := j.runLocked(traceID); err != nil {
		return err
	}
	rec := journalRecord{Type: "done", Time: time.Now().UTC(), Content: content, Outcome: outcome}
	if runErr != nil {
		rec.Err = runErr.Error()
	}
	if err := j.appendLocked(traceID, rec); err != nil {
		return err
	}
	j.finishLocked(traceID, outcome, rec.Time)
	return nil
}

// finishLocked records in the sidecar that a run is over and drops the writer's memory of it.
func (j *FileJournal) finishLocked(traceID, outcome string, at time.Time) {
	j.mu.Lock()
	run := j.runs[traceID]
	delete(j.runs, traceID)
	j.mu.Unlock()

	var meta journalMeta
	if run != nil {
		meta = run.meta
	} else if m, ok := j.readMeta(traceID); ok {
		meta = m
	} else if records, err := j.readAll(traceID); err == nil {
		if st, err := replay(records); err == nil {
			meta = metaFromState(traceID, st)
		}
	}
	if meta.TraceID == "" {
		return
	}
	meta.Done, meta.Outcome, meta.DoneAt = true, outcome, at
	j.writeMeta(meta)
}

// readAll replays every record in a run's journal file, in order. A last line that was cut
// off by a crash is ignored; a damaged line anywhere else is an error, because the history
// after it cannot be trusted.
func (j *FileJournal) readAll(traceID string) ([]journalRecord, error) {
	f, err := os.Open(j.path(traceID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("journal: no run found for trace %s: %w", traceID, ErrRunNotFound)
		}
		return nil, err
	}
	defer f.Close()

	var records []journalRecord
	r := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := r.ReadBytes('\n')
		atEOF := err == io.EOF
		if err != nil && !atEOF {
			return nil, err
		}
		if t := bytes.TrimSpace(line); len(t) > 0 {
			var rec journalRecord
			if uerr := json.Unmarshal(t, &rec); uerr != nil {
				if atEOF {
					break // torn final line
				}
				return nil, fmt.Errorf("journal: corrupt record in %s: %w", traceID, uerr)
			}
			records = append(records, rec)
		}
		if atEOF {
			break
		}
	}
	return records, nil
}

func addTo(u *models.TokenUsage, d models.TokenUsage) {
	u.InputTokens += d.InputTokens
	u.OutputTokens += d.OutputTokens
	u.TotalTokens += d.TotalTokens
	u.ThinkingTokens += d.ThinkingTokens
	u.EstimatedCost += d.EstimatedCost
}

// replay folds a run's records into its current state. It is the single
// place both Load and ListUnfinished's summaries derive their view of a run
// from, so the two can never disagree about what "paused" or "done" means.
// Schema-1 files (full history on every turn) and schema-2 files (deltas) both replay.
func replay(records []journalRecord) (*JournalState, error) {
	if len(records) == 0 || records[0].Type != "header" || records[0].Header == nil {
		return nil, fmt.Errorf("journal: missing header record")
	}
	st := &JournalState{Header: *records[0].Header, Status: RunRunning}
	st.Messages = append([]models.ChatMessage(nil), st.Header.InitialMessages...)
	if hb := st.Header.Budget; hb != nil {
		base := *hb
		st.Base, st.Budget = &base, &base
	}
	seenExt := map[string]bool{}

	for _, rec := range records[1:] {
		switch rec.Type {
		case "turn": // schema 1
			st.CompletedTurns++
			st.Messages = rec.Messages
			addTo(&st.Usage, rec.Usage)
			if rec.Turn != nil {
				st.ActiveMs += rec.Turn.LatencyMs
			}
			st.Pending = nil
			st.InFlight = nil
			st.Claimed = false
			st.Status = RunRunning
		case "assistant":
			addTo(&st.Usage, rec.Usage)
			in := &InFlightTurn{Turn: rec.TurnNo, Calls: rec.Calls, Results: map[string]ToolResult{}}
			if rec.Assistant != nil {
				in.Assistant = *rec.Assistant
			}
			st.InFlight = in
		case "tool_result":
			if st.InFlight != nil && rec.Result != nil && rec.TurnNo == st.InFlight.Turn {
				if _, dup := st.InFlight.Results[rec.Result.ToolCallID]; !dup {
					// What a subagent spent inside this call is counted as soon as the result is
					// recorded, so a crash before the turn commits cannot lose it.
					addTo(&st.Usage, rec.Result.ChildUsage)
				}
				st.InFlight.Results[rec.Result.ToolCallID] = *rec.Result
			}
		case "commit":
			st.CompletedTurns++
			if rec.Messages != nil {
				st.Messages = rec.Messages
			} else {
				st.Messages = append(st.Messages, rec.Delta...)
			}
			addTo(&st.Usage, rec.Usage) // zero under schema 2: model call counted at "assistant", subagents at "tool_result"
			if rec.Turn != nil {
				st.ActiveMs += rec.Turn.LatencyMs
			}
			st.Pending = nil
			st.InFlight = nil
			st.Claimed = false
			st.Status = RunRunning
		case "snapshot":
			st.Messages = rec.Messages
			addTo(&st.Usage, rec.Usage)
		case "notes":
			st.Notes = rec.Notes
		case "stalled":
			st.StallPause = rec.Stall
			st.Pending = nil
			st.Status = RunPaused
		case "recovery":
			st.Recoveries++
		case "paused":
			st.Pending = rec.Pending
			st.Messages = rec.Messages
			st.Status = RunPaused
			if rec.Pending != nil {
				// The model call behind the pause was paid for. Under schema 2 it was already
				// counted when its "assistant" record was read; under schema 1 this is the
				// only place it is recorded.
				if st.InFlight == nil || st.InFlight.Turn != rec.Pending.Turn {
					addTo(&st.Usage, rec.Pending.Usage)
				}
				st.ActiveMs += rec.Pending.LatencyMs
			}
		case "budget_paused":
			st.BudgetPause = rec.BudgetPause
			st.Pending = nil
			st.Status = RunPaused
		case "resumed":
			if st.Pending != nil {
				st.Claimed = true
			}
			st.BudgetPause = nil
			st.StallPause = nil
			st.Status = RunRunning
			// An extension is applied once however many times it is replayed: a retry
			// after a crash between the grant and the next turn carries the same ID.
			if ext := rec.Extension; ext != nil && st.Budget != nil && !(ext.ID != "" && seenExt[ext.ID]) {
				if ext.ID != "" {
					seenExt[ext.ID] = true
				}
				st.Extensions = append(st.Extensions, *ext)
				extended := st.Budget.Extend(*ext)
				st.Budget = &extended
			}
		case "done":
			st.Status = RunDone
			st.FinalContent = rec.Content
			st.FinalErr = rec.Err
			st.Outcome = rec.Outcome
			if st.Outcome == "" { // schema 1 recorded only success or failure
				st.Outcome = OutcomeCompleted
				if rec.Err != "" {
					st.Outcome = OutcomeFailed
				}
			}
			st.Pending = nil
			st.BudgetPause = nil
			st.StallPause = nil
			st.InFlight = nil
		}
	}
	return st, nil
}

func (j *FileJournal) Load(_ context.Context, traceID string) (*JournalState, error) {
	records, err := j.readAll(traceID)
	if err != nil {
		return nil, err
	}
	return replay(records)
}

func (j *FileJournal) ListUnfinished(_ context.Context, kitchen string) ([]JournalSummary, error) {
	entries, err := os.ReadDir(j.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []JournalSummary
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		traceID := strings.TrimSuffix(e.Name(), ".jsonl")

		// The sidecar answers "finished", "a subagent's" and "another kitchen's" without
		// reading the journal, which is most of them once a deployment has some history.
		if m, ok := j.readMeta(traceID); ok {
			if m.Done || m.ParentTraceID != "" || (kitchen != "" && m.Kitchen != kitchen) {
				continue
			}
		}
		records, err := j.readAll(traceID)
		if err != nil || len(records) == 0 {
			continue // a journal mid-write or otherwise unreadable is skipped, not fatal to the scan
		}
		st, err := replay(records)
		if err != nil || st.Status == RunDone {
			continue
		}
		if st.Header.ParentTraceID != "" {
			continue // a subagent's run belongs to its parent's tool call
		}
		if kitchen != "" && st.Header.Kitchen != kitchen {
			continue
		}
		out = append(out, JournalSummary{
			TraceID:     traceID,
			Kitchen:     st.Header.Kitchen,
			AgentName:   st.Header.Agent.Name,
			Status:      st.Status,
			StartedAt:   st.Header.StartedAt,
			LastUpdate:  records[len(records)-1].Time,
			TurnsLogged: st.CompletedTurns,
			PauseKind:   pauseKindOf(st),
		})
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt.Before(out[k].StartedAt) })
	return out, nil
}

func pauseKindOf(st *JournalState) string {
	if st.Status != RunPaused {
		return ""
	}
	if st.BudgetPause != nil {
		return string(PauseBudget)
	}
	if st.StallPause != nil {
		return string(PauseStall)
	}
	return string(PauseApproval)
}

// PauseKindOf says why a paused run is waiting ("approval", "budget", "stall"), "" when it is not
// paused. A decision that unblocks it is a signed gate (Pro ADR-0041).
func PauseKindOf(st *JournalState) string { return pauseKindOf(st) }

func (j *FileJournal) Discard(_ context.Context, traceID string) error {
	l := j.lockFor(traceID)
	l.Lock()
	defer l.Unlock()
	j.mu.Lock()
	delete(j.runs, traceID)
	j.mu.Unlock()
	_ = os.Remove(j.metaPath(traceID))
	err := os.Remove(j.path(traceID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Sweep removes journals of runs that finished more than doneRetention ago, and ends runs
// that have not been touched for staleAfter, recording them as abandoned (they are removed
// by a later sweep once they are as old as a finished run would be). A paused run is not
// stale: it is waiting for a person, and its pause is its record. It returns how many it
// removed and how many it ended. A zero duration disables that half.
func (j *FileJournal) Sweep(now time.Time, doneRetention, staleAfter time.Duration) (removed, abandoned int) {
	entries, err := os.ReadDir(j.baseDir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		traceID := strings.TrimSuffix(e.Name(), ".jsonl")
		info, err := e.Info()
		if err != nil {
			continue
		}

		meta, hasMeta := j.readMeta(traceID)
		if !hasMeta {
			// Written before the sidecar existed: learn what it is once, and index it.
			records, err := j.readAll(traceID)
			if err != nil {
				continue
			}
			st, err := replay(records)
			if err != nil {
				continue
			}
			meta = metaFromState(traceID, st)
			if st.Status == RunDone {
				meta.DoneAt = info.ModTime()
			}
			j.writeMeta(meta)
		}

		if meta.Done {
			at := meta.DoneAt
			if at.IsZero() {
				at = info.ModTime()
			}
			if doneRetention > 0 && now.Sub(at) > doneRetention {
				if j.Discard(context.Background(), traceID) == nil {
					removed++
				}
			}
			continue
		}
		if staleAfter > 0 && now.Sub(info.ModTime()) > staleAfter {
			st, err := j.Load(context.Background(), traceID)
			if err != nil || st.Status == RunPaused {
				continue
			}
			if j.MarkOutcome(context.Background(), traceID, OutcomeAbandoned, "", fmt.Errorf("abandoned: no activity for %s", staleAfter.Round(time.Hour))) == nil {
				abandoned++
			}
		}
	}
	return removed, abandoned
}

// StartJanitor sweeps once now and then every interval until ctx ends.
func (j *FileJournal) StartJanitor(ctx context.Context, interval, doneRetention, staleAfter time.Duration) {
	sweep := func() {
		if r, a := j.Sweep(time.Now(), doneRetention, staleAfter); r > 0 || a > 0 {
			log.Info().Int("removed", r).Int("abandoned", a).Msg("Journal janitor swept")
		}
	}
	sweep()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweep()
			}
		}
	}()
}
