package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

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
	// of right after it — the state a resume needs to pick up cleanly.
	AppendTurn(ctx context.Context, traceID string, turn Turn, messages []models.ChatMessage, usage models.TokenUsage) error

	// MarkPaused records that the run stopped to wait for a human decision on
	// a pending tool call, and the message history as of right after the
	// model's request for it — the assistant's tool_calls message is already
	// in messages, but no tool result for it yet — so Resume has exactly what
	// it needs to append results and continue.
	MarkPaused(ctx context.Context, traceID string, pending PendingApproval, messages []models.ChatMessage) error

	// MarkDone records the run's outcome: the final content on success, or
	// runErr's message on failure. Either way, the run is finished and
	// ListUnfinished will no longer report it.
	MarkDone(ctx context.Context, traceID string, content string, runErr error) error

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
}

// journalRecord is one line of a run's journal file. Exactly one of the
// pointer/value fields is populated per Type.
type journalRecord struct {
	Type     string               `json:"type"` // "header" | "turn" | "paused" | "done"
	Time     time.Time            `json:"time"`
	Header   *JournalHeader       `json:"header,omitempty"`
	Turn     *Turn                `json:"turn,omitempty"`
	Messages []models.ChatMessage `json:"messages,omitempty"`
	Usage    models.TokenUsage    `json:"usage,omitempty"`
	Pending  *PendingApproval     `json:"pending,omitempty"`
	Content  string               `json:"content,omitempty"`
	Err      string               `json:"err,omitempty"`
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
}

// FileJournal is the OSS-default Journal: one append-only JSONL file per run
// under baseDir, named by trace ID. No database required — matches the same
// "lean, self-contained" idiom as the local retention archiver
// (internal/retention) and the memory store's JSON snapshot.
//
// Appends within one run are serialized by a per-trace lock so concurrent
// writers (a resumed run racing a crash-recovery scan, say) cannot interleave
// partial JSON lines; across runs, writes are independent files and need no
// shared lock.
type FileJournal struct {
	baseDir string
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
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
	return &FileJournal{baseDir: baseDir, locks: make(map[string]*sync.Mutex)}, nil
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

func (j *FileJournal) append(traceID string, rec journalRecord) error {
	l := j.lockFor(traceID)
	l.Lock()
	defer l.Unlock()

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

func (j *FileJournal) Start(_ context.Context, header JournalHeader) error {
	if header.TraceID == "" {
		return fmt.Errorf("journal: header needs a trace ID")
	}
	return j.append(header.TraceID, journalRecord{Type: "header", Time: time.Now().UTC(), Header: &header})
}

func (j *FileJournal) AppendTurn(_ context.Context, traceID string, turn Turn, messages []models.ChatMessage, usage models.TokenUsage) error {
	return j.append(traceID, journalRecord{Type: "turn", Time: time.Now().UTC(), Turn: &turn, Messages: messages, Usage: usage})
}

func (j *FileJournal) MarkPaused(_ context.Context, traceID string, pending PendingApproval, messages []models.ChatMessage) error {
	return j.append(traceID, journalRecord{Type: "paused", Time: time.Now().UTC(), Pending: &pending, Messages: messages})
}

func (j *FileJournal) MarkDone(_ context.Context, traceID string, content string, runErr error) error {
	rec := journalRecord{Type: "done", Time: time.Now().UTC(), Content: content}
	if runErr != nil {
		rec.Err = runErr.Error()
	}
	return j.append(traceID, rec)
}

// readAll replays every record in a run's journal file, in order.
func (j *FileJournal) readAll(traceID string) ([]journalRecord, error) {
	f, err := os.Open(j.path(traceID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("journal: no run found for trace %s", traceID)
		}
		return nil, err
	}
	defer f.Close()

	var records []journalRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec journalRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("journal: corrupt record in %s: %w", traceID, err)
		}
		records = append(records, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// replay folds a run's records into its current state. It is the single
// place both Load and ListUnfinished's summaries derive their view of a run
// from, so the two can never disagree about what "paused" or "done" means.
func replay(records []journalRecord) (*JournalState, error) {
	if len(records) == 0 || records[0].Type != "header" {
		return nil, fmt.Errorf("journal: missing header record")
	}
	st := &JournalState{Header: *records[0].Header, Status: RunRunning}
	st.Messages = nil

	for _, rec := range records[1:] {
		switch rec.Type {
		case "turn":
			st.CompletedTurns++
			st.Messages = rec.Messages
			st.Usage.InputTokens += rec.Usage.InputTokens
			st.Usage.OutputTokens += rec.Usage.OutputTokens
			st.Usage.TotalTokens += rec.Usage.TotalTokens
			st.Usage.ThinkingTokens += rec.Usage.ThinkingTokens
			st.Usage.EstimatedCost += rec.Usage.EstimatedCost
			st.Pending = nil
			st.Status = RunRunning
		case "paused":
			st.Pending = rec.Pending
			st.Messages = rec.Messages
			st.Status = RunPaused
		case "done":
			st.Status = RunDone
			st.FinalContent = rec.Content
			st.FinalErr = rec.Err
			st.Pending = nil
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
		traceID := e.Name()[:len(e.Name())-len(".jsonl")]
		records, err := j.readAll(traceID)
		if err != nil || len(records) == 0 {
			continue // a journal mid-write or otherwise unreadable is skipped, not fatal to the scan
		}
		st, err := replay(records)
		if err != nil || st.Status == RunDone {
			continue
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
		})
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt.Before(out[k].StartedAt) })
	return out, nil
}

func (j *FileJournal) Discard(_ context.Context, traceID string) error {
	err := os.Remove(j.path(traceID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
