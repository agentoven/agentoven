package models

import "time"

// PublicApproval is a human-gate approval as a reader without the right to read content may see it:
// that a decision is waiting, for which step, and who decided it, but not what it is about. The gate's
// Metadata holds the step's inputs (what a user typed, what a tool returned) and Comments holds what an
// approver wrote about them, so both are withheld, as a trace's bodies are (PublicTrace).
type PublicApproval struct {
	ID              string     `json:"id"`
	GateKey         string     `json:"gate_key"`
	RunID           string     `json:"run_id"`
	StepName        string     `json:"step_name"`
	Kitchen         string     `json:"kitchen"`
	Status          string     `json:"status"`
	ApproverID      string     `json:"approver_id,omitempty"`
	ApproverEmail   string     `json:"approver_email,omitempty"`
	ApproverChannel string     `json:"approver_channel,omitempty"`
	RequestedAt     time.Time  `json:"requested_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	MaxWaitMinutes  int        `json:"max_wait_minutes,omitempty"`
	// Metadata is the labels of the gate, not its inputs.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	// ContentWithheld says inputs or comments may exist that this reader may not see.
	ContentWithheld bool `json:"content_withheld"`
}

// Public returns the approval as a reader without content access may see it.
func (a ApprovalRecord) Public() PublicApproval {
	return PublicApproval{
		ID: a.ID, GateKey: a.GateKey, RunID: a.RunID, StepName: a.StepName, Kitchen: a.Kitchen, Status: a.Status,
		ApproverID: a.ApproverID, ApproverEmail: a.ApproverEmail, ApproverChannel: a.ApproverChannel,
		RequestedAt: a.RequestedAt, ResolvedAt: a.ResolvedAt, MaxWaitMinutes: a.MaxWaitMinutes, Metadata: publicMetadata(a.Metadata), ContentWithheld: true,
	}
}

// PublicApprovals converts a list.
func PublicApprovals(as []ApprovalRecord) []PublicApproval {
	out := make([]PublicApproval, len(as))
	for i, a := range as {
		out[i] = a.Public()
	}
	return out
}
