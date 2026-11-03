package executor

import (
	"context"
	"errors"
	"fmt"
)

// ErrFenced is what a write or a tool dispatch is refused with when the replica attempting it no
// longer holds the task's lease. It is not a failure of the run: the run belongs to another replica
// now, and nothing is recorded about it here.
var ErrFenced = errors.New("executor: this replica no longer holds the task's lease")

// Fence is a task runner's proof that it still owns the task. A lease alone is not enough: a process
// that is frozen for longer than the lease ttl (a VM pause, SIGSTOP, a long garbage collection)
// wakes up believing it holds the task while another replica has adopted it, and the renewal loop
// that would tell it so has not run yet. So the claim is checked where it matters, at the moment of
// acting: before a tool is dispatched (Verify) and, in a shared journal, inside the transaction that
// appends a record (the RecordLog reads the fence from the context and compares Epoch with the
// lease's). The check uses the lease store's clock and the epoch, not the process's own clock, so a
// pause that the local monotonic clock did not see is caught too.
type Fence struct {
	TaskID string
	Owner  string
	// Epoch is the lease's fencing token when this replica took the task (TaskLeases.Epoch).
	Epoch int64

	leases TaskLeases
	lost   func(why string)
}

type fenceKey struct{}

// WithFence returns ctx carrying f. The task manager installs it on the context a task runs under;
// every context derived from that one (tool calls, journal writes, subagents) carries it.
func WithFence(ctx context.Context, f Fence) context.Context {
	return context.WithValue(ctx, fenceKey{}, f)
}

// FenceFrom returns the fence on ctx, if the run is a task that holds a lease.
func FenceFrom(ctx context.Context) (Fence, bool) {
	f, ok := ctx.Value(fenceKey{}).(Fence)
	return f, ok
}

// Verify reports ErrFenced when the lease is no longer held at this epoch. It fails closed: when the
// lease store cannot be asked, the answer is an error too, because an action that cannot be repeated
// safely must not be taken on a guess. A failed check also stops the local run.
func (f Fence) Verify(ctx context.Context) error {
	if f.leases == nil {
		return fmt.Errorf("executor: a fence with no lease store to check against")
	}
	epoch, held, err := f.leases.Epoch(context.WithoutCancel(ctx), f.TaskID, f.Owner)
	switch {
	case err != nil:
		return fmt.Errorf("executor: could not confirm the task's lease: %w", err)
	case !held || epoch != f.Epoch:
		if f.lost != nil {
			f.lost("the lease was taken over while this replica was not running")
		}
		return ErrFenced
	}
	return nil
}
