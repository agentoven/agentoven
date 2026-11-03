package executor_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
)

func startRun(t *testing.T, j *executor.LogJournal, id, kitchen string) {
	t.Helper()
	a := testAgent()
	a.Kitchen = kitchen
	if err := j.Start(context.Background(), executor.JournalHeader{TraceID: id, Kitchen: kitchen, Agent: a, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// Review finding 11: the janitor listed every run's metadata on every replica every hour. It now reads
// the runs that are due, in batches.
func TestSweepReadsOnlyTheRunsThatAreDueAndPagesThroughMany(t *testing.T) {
	log := newMemLog()
	j := executor.NewLogJournal(log)
	ctx := context.Background()
	const due, fresh = 1100, 300 // more than one batch of due runs
	for i := 0; i < due; i++ {
		id := fmt.Sprintf("old-%05d", i)
		startRun(t, j, id, "default")
		_ = j.MarkDone(ctx, id, "x", nil)
		log.backdate(id, 10*24*time.Hour)
	}
	for i := 0; i < fresh; i++ {
		id := fmt.Sprintf("new-%05d", i)
		startRun(t, j, id, "default")
		_ = j.MarkDone(ctx, id, "x", nil)
	}
	removed, _ := j.Sweep(ctx, time.Now(), 7*24*time.Hour, 0)
	if removed != due {
		t.Fatalf("removed %d, want %d", removed, due)
	}
	if log.sweepReads != due {
		t.Fatalf("the janitor read %d index entries, want only the %d that were due (not the %d that were not)", log.sweepReads, due, fresh)
	}
	if left, _ := log.ListUnfinished(ctx, ""); len(left) != 0 {
		t.Fatal("no unfinished runs were created")
	}
	if removed, _ := j.Sweep(ctx, time.Now(), 7*24*time.Hour, 0); removed != 0 || log.sweepReads != due {
		t.Fatalf("a second pass has nothing due and reads nothing: removed %d reads %d", removed, log.sweepReads)
	}
}

// Review finding 11: journals are content stores, and the legal hold did not know about them.
func TestSweepLeavesAKitchenUnderLegalHoldAlone(t *testing.T) {
	log := newMemLog()
	j := executor.NewLogJournal(log)
	ctx := context.Background()
	for _, k := range []string{"held", "free"} {
		for _, kind := range []string{"done", "stale"} {
			id := kind + "-" + k
			startRun(t, j, id, k)
			if kind == "done" {
				_ = j.MarkDone(ctx, id, "x", nil)
			}
			log.backdate(id, 30*24*time.Hour)
		}
	}
	asked := 0
	j.SetHeld(func(_ context.Context, kitchen string) bool { asked++; return kitchen == "held" })
	removed, abandoned := j.Sweep(ctx, time.Now(), 7*24*time.Hour, 14*24*time.Hour)
	if removed != 1 || abandoned != 1 {
		t.Fatalf("only the free kitchen's runs are swept: removed %d abandoned %d", removed, abandoned)
	}
	if _, err := j.Load(ctx, "done-held"); err != nil {
		t.Fatal("a held kitchen's finished journal must stay")
	}
	if st, _ := j.Load(ctx, "stale-held"); st == nil || st.Outcome == executor.OutcomeAbandoned {
		t.Fatal("a held kitchen's stale run is not ended either")
	}
	if asked != 2 {
		t.Fatalf("the hold is asked once per kitchen per sweep, asked %d times", asked)
	}
}
