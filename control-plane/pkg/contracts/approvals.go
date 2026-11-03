package contracts

import (
	"context"
	"time"
)

// ApprovalRequest tells an observer that a person has just been asked to decide something. It names
// the record and nothing else on purpose: what the person would be signing is read, when it is needed,
// from the gate's DecisionDescriber, so a message about the request can never show something the
// signing sheet would not.
type ApprovalRequest struct {
	Kitchen string
	// Gate is one of the Gate* constants.
	Gate string
	// RecordID is what the gate's describer reads: the trace id of a paused run, the "run:step" key of a
	// recipe gate, the approval id of a promotion.
	RecordID string
	// Deadline is how long the source itself will wait before it gives up (a recipe step's
	// max_wait_minutes), zero when it waits for ever. An observer that expires requests must not
	// wait longer than this.
	Deadline time.Duration
}

// ApprovalObserver is told when an approval is requested. The community edition installs none; Pro's
// installs the delivery, reminder and escalation of ADR-0033 / Pro ADR-0050. It is called on the
// goroutine that created the request, so it must return quickly and must not fail the request: an
// observer that cannot keep up drops the notification, never the approval.
type ApprovalObserver interface {
	ApprovalRequested(ctx context.Context, req ApprovalRequest)
}

// ApprovalExpirer ends a request nobody answered in time. It is a refusal, never an approval: a
// recipe gate takes its configured timeout path, a paused run ends as cancelled. It reports false when
// the request had already been decided or was no longer pending, and then changes nothing.
type ApprovalExpirer interface {
	ExpireApproval(ctx context.Context, kitchen, gate, recordID, reason string) (bool, error)
}
