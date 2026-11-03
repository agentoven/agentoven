package executor

import (
	"context"
	"errors"
	"testing"
)

type brokenLeases struct {
	TaskLeases
	err error
}

func (b brokenLeases) Epoch(context.Context, string, string) (int64, bool, error) {
	return 0, false, b.err
}

// A fence whose lease store cannot be asked refuses (it fails closed), and says it is not a verdict.
func TestAFenceFailsClosedWhenTheLeaseStoreCannotBeAsked(t *testing.T) {
	f := Fence{TaskID: "t", Owner: "a", Epoch: 1, leases: brokenLeases{err: errors.New("lease store down")}}
	if err := f.Verify(context.Background()); err == nil || errors.Is(err, ErrFenced) {
		t.Fatalf("an unreachable lease store is an error, not a verdict: %v", err)
	}
}

func TestAFenceNamesTheTaskItWasLostOn(t *testing.T) {
	leases := NewMemoryLeases(nil)
	ctx := context.Background()
	leases.Acquire(ctx, "t", "a", 1<<40)
	epoch, _, _ := leases.Epoch(ctx, "t", "a")
	var lostWhy string
	f := Fence{TaskID: "t", Owner: "a", Epoch: epoch, leases: leases, lost: func(why string) { lostWhy = why }}
	if err := f.Verify(ctx); err != nil || lostWhy != "" {
		t.Fatalf("the holder passes: %v", err)
	}
	leases.Release(ctx, "t", "a")
	leases.Acquire(ctx, "t", "b", 1<<40)
	if err := f.Verify(ctx); !errors.Is(err, ErrFenced) || lostWhy == "" {
		t.Fatalf("a replaced holder is fenced and stops its local run: %v %q", err, lostWhy)
	}
	if got, ok := FenceFrom(WithFence(ctx, f)); !ok || got.Epoch != epoch {
		t.Fatal("the fence travels on the context")
	}
}
