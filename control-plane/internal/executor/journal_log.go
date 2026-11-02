package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// RecordLog is where a LogJournal keeps a run's journal records: an append-only list per run, plus
// a small index entry per run. It is the seam that lets a database (Pro keeps them in PostgreSQL)
// hold the journal so that several control-plane replicas share it, which a directory of files on
// one machine cannot do. Records are opaque to the log; it stores and returns them in order.
//
// Writers are serialised per run, across processes: Update runs its function with the run locked,
// and nothing else can append to that run until it returns.
type RecordLog interface {
	// Create adds a run with its first record. It fails if the run already exists.
	Create(ctx context.Context, meta RunMeta, first []byte) error
	// Update runs fn with the run locked against every other writer. Whatever fn appends is
	// stored atomically with the index change; if fn returns an error nothing is stored.
	// It reports an error wrapping ErrRunNotFound for a run that does not exist.
	Update(ctx context.Context, traceID string, fn func(tx RecordTx) error) error
	// Read returns every record of the run in order, without locking it.
	Read(ctx context.Context, traceID string) ([][]byte, error)
	// ListUnfinished returns the index entries of runs not marked finished that are not a
	// subagent's, newest start last. kitchen "" lists every kitchen.
	ListUnfinished(ctx context.Context, kitchen string) ([]RunMeta, error)
	// ListAll returns every run's index entry (finished or not), for the janitor.
	ListAll(ctx context.Context) ([]RunMeta, error)
	// Delete removes a run and its records. A run that does not exist is not an error.
	Delete(ctx context.Context, traceID string) error
}

// RecordTx is a run's log while it is locked for writing.
type RecordTx interface {
	// Count is how many records the run has.
	Count() (int, error)
	// Records returns them all, in order.
	Records() ([][]byte, error)
	// Append adds records after the last one.
	Append(recs ...[]byte) error
	// Finish marks the run finished in its index entry.
	Finish(outcome string, at time.Time) error
}

// RunMeta is a run's index entry: enough to find and filter runs without reading their records.
type RunMeta struct {
	TraceID       string
	Kitchen       string
	AgentName     string
	ParentTraceID string
	StartedAt     time.Time
	LastUpdate    time.Time
	Done          bool
	Outcome       string
	DoneAt        time.Time
}

// ErrRunNotFound is what a RecordLog says for a run it does not hold.
var ErrRunNotFound = errors.New("journal: run not found")

// LogJournal is a Journal over a RecordLog. It has the semantics of FileJournal (the records and
// their replay are the same code), with the storage left to the log, so that a RecordLog shared
// between processes makes the journal shared too.
type LogJournal struct {
	log RecordLog

	mu   sync.Mutex
	runs map[string]*logRun
}

// logRun is the writer's memory of a run between appends: how much of the history is stored (so a
// commit writes only what is new) and how many records that memory was built from. If the log holds
// a different number, another process has written to the run since, and the memory is rebuilt.
type logRun struct {
	records   int
	persisted int
	tail      uint64
}

var _ Journal = (*LogJournal)(nil)

// NewLogJournal returns a journal over log.
func NewLogJournal(log RecordLog) *LogJournal {
	return &LogJournal{log: log, runs: map[string]*logRun{}}
}

func encode(rec journalRecord) ([]byte, error) {
	b, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("encode journal record: %w", err)
	}
	return b, nil
}

func decode(raw [][]byte) ([]journalRecord, error) {
	out := make([]journalRecord, 0, len(raw))
	for i, b := range raw {
		var rec journalRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			return nil, fmt.Errorf("journal: corrupt record %d: %w", i, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

func (j *LogJournal) getRun(id string) *logRun {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.runs[id]
}

func (j *LogJournal) setRun(id string, r *logRun) {
	j.mu.Lock()
	if r == nil {
		delete(j.runs, id)
	} else {
		j.runs[id] = r
	}
	j.mu.Unlock()
}

// sync makes the writer's memory of the run match the log, rebuilding it when the log was written
// by someone else (or this process has not touched the run yet).
func (j *LogJournal) sync(tx RecordTx, id string) (*logRun, error) {
	n, err := tx.Count()
	if err != nil {
		return nil, err
	}
	if r := j.getRun(id); r != nil && r.records == n {
		return r, nil
	}
	raw, err := tx.Records()
	if err != nil {
		return nil, err
	}
	recs, err := decode(raw)
	if err != nil {
		return nil, err
	}
	st, err := replay(recs)
	if err != nil {
		return nil, err
	}
	r := &logRun{records: len(raw), persisted: len(st.Messages)}
	if m := len(st.Messages); m > 0 {
		r.tail = fingerprint(st.Messages[m-1])
	}
	j.setRun(id, r)
	return r, nil
}

// write appends records under the run's lock and keeps the memory in step.
func (j *LogJournal) write(ctx context.Context, traceID string, fn func(tx RecordTx, run *logRun) ([]journalRecord, error)) (err error) {
	defer func() {
		if err != nil {
			j.setRun(traceID, nil) // the writer's memory may be ahead of what was stored
		}
	}()
	return j.log.Update(ctx, traceID, func(tx RecordTx) error {
		run, err := j.sync(tx, traceID)
		if err != nil {
			return err
		}
		recs, err := fn(tx, run)
		if err != nil {
			return err
		}
		raw := make([][]byte, 0, len(recs))
		for _, r := range recs {
			b, err := encode(r)
			if err != nil {
				return err
			}
			raw = append(raw, b)
		}
		if err := tx.Append(raw...); err != nil {
			return err
		}
		run.records += len(raw)
		return nil
	})
}

func (j *LogJournal) appendOne(ctx context.Context, traceID string, rec journalRecord) error {
	return j.write(ctx, traceID, func(RecordTx, *logRun) ([]journalRecord, error) { return []journalRecord{rec}, nil })
}

func (j *LogJournal) Start(ctx context.Context, header JournalHeader) error {
	if header.TraceID == "" {
		return fmt.Errorf("journal: header needs a trace ID")
	}
	if header.Schema == 0 {
		header.Schema = journalSchema
	}
	rec := journalRecord{Type: "header", Time: time.Now().UTC(), Header: &header}
	raw, err := encode(rec)
	if err != nil {
		return err
	}
	meta := RunMeta{TraceID: header.TraceID, Kitchen: header.Kitchen, ParentTraceID: header.ParentTraceID, StartedAt: header.StartedAt}
	if header.Agent != nil {
		meta.AgentName = header.Agent.Name
	}
	if err := j.log.Create(ctx, meta, raw); err != nil {
		return err
	}
	r := &logRun{records: 1, persisted: len(header.InitialMessages)}
	if n := len(header.InitialMessages); n > 0 {
		r.tail = fingerprint(header.InitialMessages[n-1])
	}
	j.setRun(header.TraceID, r)
	return nil
}

func (j *LogJournal) AppendTurn(ctx context.Context, traceID string, turn Turn, messages []models.ChatMessage, usage models.TokenUsage) error {
	j.setRun(traceID, nil)
	return j.appendOne(ctx, traceID, journalRecord{Type: "turn", Time: time.Now().UTC(), Turn: &turn, Messages: messages, Usage: usage})
}

func (j *LogJournal) AppendAssistant(ctx context.Context, traceID string, turn int, msg models.ChatMessage, calls []ToolCall, usage models.TokenUsage, latencyMs int64) error {
	return j.appendOne(ctx, traceID, journalRecord{Type: "assistant", Time: time.Now().UTC(), TurnNo: turn, Assistant: &msg, Calls: calls, Usage: usage, LatencyMs: latencyMs})
}

func (j *LogJournal) AppendToolResult(ctx context.Context, traceID string, turn int, result ToolResult) error {
	return j.appendOne(ctx, traceID, journalRecord{Type: "tool_result", Time: time.Now().UTC(), TurnNo: turn, Result: &result})
}

func (j *LogJournal) CommitTurn(ctx context.Context, traceID string, turn Turn, messages []models.ChatMessage) error {
	return j.write(ctx, traceID, func(_ RecordTx, run *logRun) ([]journalRecord, error) {
		slim := Turn{Number: turn.Number, Response: turn.Response, LatencyMs: turn.LatencyMs, Usage: turn.Usage}
		rec := journalRecord{Type: "commit", Time: time.Now().UTC(), Turn: &slim}
		if run.persisted <= len(messages) && (run.persisted == 0 || fingerprint(messages[run.persisted-1]) == run.tail) {
			rec.Delta = messages[run.persisted:]
		} else {
			rec.Messages = messages
		}
		run.persisted = len(messages)
		run.tail = 0
		if n := len(messages); n > 0 {
			run.tail = fingerprint(messages[n-1])
		}
		return []journalRecord{rec}, nil
	})
}

func (j *LogJournal) Snapshot(ctx context.Context, traceID string, messages []models.ChatMessage, usage models.TokenUsage) error {
	return j.write(ctx, traceID, func(_ RecordTx, run *logRun) ([]journalRecord, error) {
		run.persisted = len(messages)
		run.tail = 0
		if n := len(messages); n > 0 {
			run.tail = fingerprint(messages[n-1])
		}
		return []journalRecord{{Type: "snapshot", Time: time.Now().UTC(), Messages: messages, Usage: usage}}, nil
	})
}

func (j *LogJournal) AppendNotes(ctx context.Context, traceID string, notes map[string]string) error {
	return j.appendOne(ctx, traceID, journalRecord{Type: "notes", Time: time.Now().UTC(), Notes: notes})
}

func (j *LogJournal) MarkStalled(ctx context.Context, traceID string, stall StallInfo) error {
	return j.appendOne(ctx, traceID, journalRecord{Type: "stalled", Time: time.Now().UTC(), Stall: &stall})
}

func (j *LogJournal) MarkPaused(ctx context.Context, traceID string, pending PendingApproval, messages []models.ChatMessage) error {
	return j.write(ctx, traceID, func(_ RecordTx, run *logRun) ([]journalRecord, error) {
		run.persisted = len(messages)
		run.tail = 0
		if n := len(messages); n > 0 {
			run.tail = fingerprint(messages[n-1])
		}
		return []journalRecord{{Type: "paused", Time: time.Now().UTC(), Pending: &pending, Messages: messages}}, nil
	})
}

func (j *LogJournal) MarkBudgetPaused(ctx context.Context, traceID string, pause models.BudgetPause) error {
	return j.appendOne(ctx, traceID, journalRecord{Type: "budget_paused", Time: time.Now().UTC(), BudgetPause: &pause})
}

// ClaimResume ends a pause so exactly one caller continues the run, however many replicas race for it.
func (j *LogJournal) ClaimResume(ctx context.Context, traceID string, ext *models.BudgetExtension) (bool, error) {
	claimed := false
	err := j.write(ctx, traceID, func(tx RecordTx, _ *logRun) ([]journalRecord, error) {
		raw, err := tx.Records()
		if err != nil {
			return nil, err
		}
		recs, err := decode(raw)
		if err != nil {
			return nil, err
		}
		st, err := replay(recs)
		if err != nil {
			return nil, err
		}
		if st.Status != RunPaused {
			return nil, nil // someone else already resumed it, or it never paused
		}
		claimed = true
		return []journalRecord{{Type: "resumed", Time: time.Now().UTC(), Extension: ext}}, nil
	})
	return claimed, err
}

func (j *LogJournal) MarkDone(ctx context.Context, traceID string, content string, runErr error) error {
	outcome := OutcomeCompleted
	switch {
	case errors.Is(runErr, ErrBudgetExhausted):
		outcome = OutcomeBudgetExhausted
	case runErr != nil:
		outcome = OutcomeFailed
	}
	return j.MarkOutcome(ctx, traceID, outcome, content, runErr)
}

func (j *LogJournal) NoteRecovery(ctx context.Context, traceID string) (int, error) {
	n := 0
	err := j.log.Update(ctx, traceID, func(tx RecordTx) error {
		raw, err := tx.Records()
		if err != nil {
			return err
		}
		b, err := encode(journalRecord{Type: "recovery", Time: time.Now().UTC()})
		if err != nil {
			return err
		}
		if err := tx.Append(b); err != nil {
			return err
		}
		recs, err := decode(append(raw, b))
		if err != nil {
			return err
		}
		st, err := replay(recs)
		if err != nil {
			return err
		}
		n = st.Recoveries
		return nil
	})
	j.setRun(traceID, nil) // the record count moved under the writer's memory; rebuild it
	return n, err
}

func (j *LogJournal) MarkOutcome(ctx context.Context, traceID, outcome, content string, runErr error) error {
	rec := journalRecord{Type: "done", Time: time.Now().UTC(), Content: content, Outcome: outcome}
	if runErr != nil {
		rec.Err = runErr.Error()
	}
	err := j.log.Update(ctx, traceID, func(tx RecordTx) error {
		b, err := encode(rec)
		if err != nil {
			return err
		}
		if err := tx.Append(b); err != nil {
			return err
		}
		return tx.Finish(outcome, rec.Time)
	})
	if err == nil {
		j.setRun(traceID, nil)
	}
	return err
}

func (j *LogJournal) Load(ctx context.Context, traceID string) (*JournalState, error) {
	raw, err := j.log.Read(ctx, traceID)
	if err != nil {
		return nil, err
	}
	recs, err := decode(raw)
	if err != nil {
		return nil, err
	}
	return replay(recs)
}

func (j *LogJournal) ListUnfinished(ctx context.Context, kitchen string) ([]JournalSummary, error) {
	metas, err := j.log.ListUnfinished(ctx, kitchen)
	if err != nil {
		return nil, err
	}
	var out []JournalSummary
	for _, m := range metas {
		raw, err := j.log.Read(ctx, m.TraceID)
		if err != nil || len(raw) == 0 {
			continue
		}
		recs, err := decode(raw)
		if err != nil {
			continue
		}
		st, err := replay(recs)
		if err != nil || st.Status == RunDone || st.Header.ParentTraceID != "" {
			continue
		}
		if kitchen != "" && st.Header.Kitchen != kitchen {
			continue
		}
		name := ""
		if st.Header.Agent != nil {
			name = st.Header.Agent.Name
		}
		out = append(out, JournalSummary{
			TraceID: m.TraceID, Kitchen: st.Header.Kitchen, AgentName: name, Status: st.Status,
			StartedAt: st.Header.StartedAt, LastUpdate: recs[len(recs)-1].Time, TurnsLogged: st.CompletedTurns, PauseKind: pauseKindOf(st),
		})
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt.Before(out[k].StartedAt) })
	return out, nil
}

func (j *LogJournal) Discard(ctx context.Context, traceID string) error {
	j.setRun(traceID, nil)
	return j.log.Delete(ctx, traceID)
}

// Sweep removes the journals of runs that finished more than doneRetention ago, and ends runs that
// nothing has written to for staleAfter, recording them as abandoned. A paused run is waiting for a
// person and is never stale. A zero duration disables that half. Several replicas may sweep at once.
func (j *LogJournal) Sweep(ctx context.Context, now time.Time, doneRetention, staleAfter time.Duration) (removed, abandoned int) {
	metas, err := j.log.ListAll(ctx)
	if err != nil {
		return 0, 0
	}
	for _, m := range metas {
		if m.Done {
			at := m.DoneAt
			if at.IsZero() {
				at = m.LastUpdate
			}
			if doneRetention > 0 && now.Sub(at) > doneRetention && j.Discard(ctx, m.TraceID) == nil {
				removed++
			}
			continue
		}
		if staleAfter > 0 && now.Sub(m.LastUpdate) > staleAfter {
			st, err := j.Load(ctx, m.TraceID)
			if err != nil || st.Status == RunPaused || st.Status == RunDone {
				continue
			}
			if j.MarkOutcome(ctx, m.TraceID, OutcomeAbandoned, "", fmt.Errorf("abandoned: no activity for %s", staleAfter.Round(time.Hour))) == nil {
				abandoned++
			}
		}
	}
	return removed, abandoned
}

// StartJanitor sweeps once now and then every interval until ctx ends.
func (j *LogJournal) StartJanitor(ctx context.Context, interval, doneRetention, staleAfter time.Duration) {
	sweep := func() {
		if r, a := j.Sweep(ctx, time.Now(), doneRetention, staleAfter); r > 0 || a > 0 {
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
