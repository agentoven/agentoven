package store

import (
	"context"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func newResumeStore(t *testing.T) *MemoryStore {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := NewMemoryStore()
	if err := m.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m
}

func addRun(t *testing.T, m *MemoryStore, id string, status models.RecipeRunStatus, parent string, age time.Duration) {
	t.Helper()
	err := m.CreateRecipeRun(context.Background(), &models.RecipeRun{ID: id, RecipeID: "r", Kitchen: "k", Status: status, ParentRunID: parent, StartedAt: time.Now().Add(-age)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOnlyTopLevelRunsThatNobodyHoldsAreClaimed(t *testing.T) {
	ctx := context.Background()
	m := newResumeStore(t)
	addRun(t, m, "old", models.RecipeRunRunning, "", time.Hour)
	addRun(t, m, "paused", models.RecipeRunPaused, "", time.Hour)
	addRun(t, m, "done", models.RecipeRunCompleted, "", time.Hour)
	addRun(t, m, "child", models.RecipeRunRunning, "old", time.Hour)
	addRun(t, m, "fresh", models.RecipeRunRunning, "", time.Second) // its starter is about to claim it

	got, err := m.ClaimResumableRuns(ctx, "A", time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, r := range got {
		ids[r.Run.ID] = true
	}
	if len(ids) != 2 || !ids["old"] || !ids["paused"] {
		t.Fatalf("claimed %v, want old and paused", ids)
	}
	// Held by A, so B gets nothing; once A gives the lease up B gets it.
	if again, _ := m.ClaimResumableRuns(ctx, "B", time.Minute, 10); len(again) != 0 {
		t.Fatalf("B claimed %d runs A holds", len(again))
	}
	if ok, _ := m.ClaimRun(ctx, "old", "B", time.Minute, ""); ok {
		t.Fatal("B leased a run A holds")
	}
	_ = m.ReleaseRunLeases(ctx, "A")
	if again, _ := m.ClaimResumableRuns(ctx, "B", time.Minute, 1); len(again) != 1 {
		t.Fatalf("B claimed %d runs after A released, want 1 (the limit)", len(again))
	}
}

func TestALeaseIsRenewedByItsOwnerOnlyAndKeepsTheRecipeDigest(t *testing.T) {
	ctx := context.Background()
	m := newResumeStore(t)
	addRun(t, m, "r1", models.RecipeRunRunning, "", time.Second)
	if ok, _ := m.ClaimRun(ctx, "r1", "A", 50*time.Millisecond, "digest-1"); !ok {
		t.Fatal("A could not lease its own new run")
	}
	if held, _ := m.RenewRunLeases(ctx, "B", time.Minute); len(held) != 0 {
		t.Fatalf("B renewed %v", held)
	}
	time.Sleep(80 * time.Millisecond) // A's lease ran out
	if held, _ := m.RenewRunLeases(ctx, "A", time.Minute); len(held) != 0 {
		t.Fatalf("A renewed a lease that had run out: %v", held)
	}
	runs, _ := m.ClaimResumableRuns(ctx, "B", time.Minute, 10)
	if len(runs) != 1 || runs[0].Digest != "digest-1" {
		t.Fatalf("B claimed %+v, want r1 with A's digest", runs)
	}
}

func TestATimerIsArmedOnceAndSurvivesAReloadOfTheSnapshot(t *testing.T) {
	ctx := context.Background()
	m := newResumeStore(t)
	left, err := m.ArmRunTimer(ctx, "run", "wait", time.Hour)
	if err != nil || left < 59*time.Minute {
		t.Fatalf("armed: %v %v", left, err)
	}
	// Arming again does not move it.
	if left2, _ := m.ArmRunTimer(ctx, "run", "wait", 10*time.Hour); left2 > time.Hour {
		t.Fatalf("a second arm moved the timer: %v", left2)
	}
	if err := m.Close(); err != nil { // flushes the snapshot
		t.Fatal(err)
	}
	again := NewMemoryStore() // the process restarted: same HOME, so the same snapshot
	if left3, _ := again.ArmRunTimer(ctx, "run", "wait", 10*time.Hour); left3 > time.Hour || left3 < 59*time.Minute {
		t.Fatalf("after a restart the timer has %v left, want what was left of the first", left3)
	}
	_ = again.ClearRunTimer(ctx, "run", "wait")
	if left4, _ := again.ArmRunTimer(ctx, "run", "wait", 2*time.Hour); left4 < time.Hour+30*time.Minute {
		t.Fatalf("a cleared timer was not forgotten: %v", left4)
	}
}

func TestAnApprovalIsResolvedOnlyWhilePending(t *testing.T) {
	ctx := context.Background()
	m := newResumeStore(t)
	rec := &models.ApprovalRecord{ID: "1", GateKey: "r:s", Kitchen: "k", Status: "pending"}
	_ = m.CreateApproval(ctx, rec)
	won := *rec
	won.Status = "approved"
	if ok, _ := m.ResolveApproval(ctx, &won); !ok {
		t.Fatal("the first decision lost")
	}
	lose := *rec
	lose.Status = "rejected"
	if ok, _ := m.ResolveApproval(ctx, &lose); ok {
		t.Fatal("a second decision took effect")
	}
	if got, _ := m.GetApproval(ctx, "r:s"); got.Status != "approved" {
		t.Fatalf("status %q", got.Status)
	}
	if ok, _ := m.ResolveApproval(ctx, &models.ApprovalRecord{GateKey: "none:none", Status: "approved"}); ok {
		t.Fatal("a decision on a gate that does not exist took effect")
	}
}
