package retention

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A run's journal is keyed by its trace's ID and holds the same content; it goes when the trace does.
func TestTheJanitorTellsTheJournalWhichTracesItRemoved(t *testing.T) {
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	old := models.Trace{ID: "old", Kitchen: "k", CreatedAt: time.Now().Add(-30 * 24 * time.Hour)}
	if err := s.CreateTrace(ctx, &old); err != nil {
		t.Fatal(err)
	}

	j := NewJanitor(s, time.Hour)
	var gone []string
	j.SetOnTraceDeleted(func(_ context.Context, kitchen, id string) { gone = append(gone, kitchen+"/"+id) })
	var stats CycleStats
	j.purgeTraces(ctx, []models.Trace{old, {ID: "missing", Kitchen: "k"}}, &stats)
	if stats.TracesPurged != len(gone) {
		t.Fatalf("hook calls %v do not match the %d traces purged", gone, stats.TracesPurged)
	}
	if len(gone) == 0 || gone[0] != "k/old" {
		t.Fatalf("the hook must name the kitchen and the trace: %v", gone)
	}
	if _, err := s.GetTrace(ctx, "old"); err == nil {
		t.Fatal("the trace is gone")
	}
}

func TestThereIsNoHookByDefault(t *testing.T) {
	j := NewJanitor(nil, time.Hour)
	var stats CycleStats
	j.purgeTraces(context.Background(), nil, &stats) // must not panic with no hook and no traces
}
