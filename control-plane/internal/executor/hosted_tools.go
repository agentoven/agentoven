package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A tool call a hosted agent process asked the control plane to make (ADR-0044) is run the way a turn of the Go loop runs
// it: the allowlist, the guardrails on its arguments, the approval gate, then dispatchToolCall (schema, fence, timeout,
// the MCP gateway with credentials, egress and native transports, the guardrails on the result, the cap).
//
// A call that needs a person is held as a one-call paused run in the journal, so the approvals queue, the observers, the
// signed decision and POST /agents/{a}/runs/{id}/resume are the ones every paused run uses. The process's request waits.
// The journal carries the decision, so any replica can take it: ResumeRun records it and the waiting request acts on it.

// Kinds of refusal a hosted process is told about.
const (
	HostedUnknownTool      = "unknown_tool"
	HostedGuardrailBlocked = "guardrail_blocked"
	HostedApprovalDenied   = "approval_denied"
	HostedApprovalExpired  = "approval_expired"
	HostedApprovalNeeded   = "approval_unavailable"
	HostedScopeClosed      = "scope_closed"
)

// DefaultHostedApprovalWait is how long a hosted call waits for a person.
// It is shorter than the relayed call that carries the process's request (backendHTTPClient: five minutes), which
// is the limit that matters; raising one without the others changes nothing.
const DefaultHostedApprovalWait = 4 * time.Minute

// HostedCall is the marker of a held call (JournalHeader.Hosted).
type HostedCall struct {
	// Run is the run of the relayed call the process is handling.
	Run string `json:"run"`
	// Deadline is when the process stops waiting; a decision after it has nothing to act on.
	Deadline time.Time `json:"deadline"`
}

// ErrHostedDecisionRecorded is what resume returns for a held call: the decision is recorded, the call that waits acts on it.
var ErrHostedDecisionRecorded = errors.New("executor: the decision on a tool call held for a hosted process was recorded")

// HostedToolRequest is one call a process asked for.
type HostedToolRequest struct {
	Agent     *models.Agent
	Resolved  *models.ResolvedIngredients
	Run       string // the relayed call's run
	CallID    string
	Name      string
	Arguments map[string]interface{}
	// StillOpen reports whether the process is still waiting for the answer (its scope is open). It is asked before a
	// held call acts on an approval.
	StillOpen func() bool
}

// HostedToolOutcome is what to tell the process.
type HostedToolOutcome struct {
	Result ToolResult
	Kind   string // "" for a call that ran (its result may still be a tool error)
	// Approval says how the gate went: "", "not_required", "approved", "denied", "expired".
	Approval string
	// HeldRun is the journal run that held the call, when it needed a person.
	HeldRun string
}

func approvalWait() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("AGENTOVEN_TOOL_APPROVAL_WAIT")); err == nil && d > 0 {
		return d
	}
	return DefaultHostedApprovalWait
}

// hostedPoll is how often a held call looks for the decision in the journal.
var hostedPoll = 500 * time.Millisecond

func refusal(req HostedToolRequest, kind, msg string) HostedToolOutcome {
	return HostedToolOutcome{Kind: kind, Result: ToolResult{ToolCallID: req.CallID, Name: req.Name, IsError: true, Content: msg}}
}

// RunHostedToolCall runs one call a hosted process asked for.
func (e *Executor) RunHostedToolCall(ctx context.Context, req HostedToolRequest) HostedToolOutcome {
	agent := req.Agent
	var schema map[string]interface{}
	found := false
	for _, t := range e.ToolsFor(agent, req.Resolved) {
		// The virtual delegation tool is not brokered: it is not a kitchen tool and the runner has its own path for it.
		if t.Name == req.Name && t.Name != "agentoven_delegate" {
			schema, found = t.Schema, true
			break
		}
	}
	if !found {
		return refusal(req, HostedUnknownTool, "no such tool: "+req.Name)
	}
	ctx = ensureDelegationRoot(ctx, agent.Name)
	if req.Arguments == nil {
		req.Arguments = map[string]interface{}{}
	}
	calls := []ToolCall{{ID: req.CallID, Name: req.Name, Arguments: req.Arguments}}
	_, guardHeld, holds := e.guardToolCalls(ctx, agent, &models.RouteResponse{}, calls)
	tc := calls[0]
	if tc.Refused {
		return refusal(req, HostedGuardrailBlocked, "Error: tool call arguments blocked by guardrails")
	}
	gated := mergeNames(e.gatedNames(ctx, agent, calls), guardHeld)
	if len(gated) == 0 {
		res := e.dispatchToolCall(ctx, agent, tc, schema)
		return HostedToolOutcome{Result: res, Approval: "not_required"}
	}
	out := e.holdHostedCall(ctx, req, tc, gated, holds)
	if out.Approval != "approved" {
		return out
	}
	if req.StillOpen != nil && !req.StillOpen() {
		out.Kind = HostedScopeClosed
		out.Result = ToolResult{ToolCallID: req.CallID, Name: req.Name, IsError: true, Content: "the process that asked for this call is no longer waiting, so it was not run"}
		return out
	}
	res := e.dispatchToolCall(withGuardrailApproval(ctx), agent, tc, schema)
	out.Result, out.Kind = res, ""
	return out
}

// holdHostedCall journals the call as a paused run, tells the approval observers, and waits for the decision.
func (e *Executor) holdHostedCall(ctx context.Context, req HostedToolRequest, tc ToolCall, gated []string, holds []GuardrailHold) HostedToolOutcome {
	agent := req.Agent
	if e.journal == nil {
		// A pause nobody can decide is not a pause: the call is refused rather than left to hang.
		return refusal(req, HostedApprovalNeeded, "this tool needs a person's approval and this server keeps no journal of paused runs, so it cannot be asked")
	}
	wait := approvalWait()
	traceID := uuid.New().String()
	now := time.Now().UTC()
	rawArgs, _ := json.Marshal(tc.Arguments)
	var asked models.ToolCallResult
	asked.ID, asked.Type = tc.ID, "function"
	asked.Function.Name, asked.Function.Arguments = tc.Name, string(rawArgs)
	user := models.ChatMessage{Role: "user", Content: "A tool call asked for by the hosted process of agent " + agent.Name + " (run " + req.Run + ")"}
	hdr := JournalHeader{
		TraceID: traceID, Kitchen: agent.Kitchen, Agent: agent, Resolved: req.Resolved, UserMessage: user.Content,
		StartedAt: now, Schema: journalSchema, InitialMessages: []models.ChatMessage{user}, StartedBy: startedBy(ctx),
		Hosted: &HostedCall{Run: req.Run, Deadline: now.Add(wait)},
	}
	if err := e.journal.Start(ctx, hdr); err != nil {
		log.Error().Err(err).Str("agent", agent.Name).Msg("hosted tool call: the hold could not be journaled; the call is refused")
		return refusal(req, HostedApprovalNeeded, "the approval request could not be recorded, so the call was not run")
	}
	pending := PendingApproval{Turn: 1, ToolCalls: []ToolCall{tc}, GatedNames: gated, GuardrailHolds: holds}
	assistant := models.ChatMessage{Role: "assistant", ToolCalls: []models.ToolCallResult{asked}}
	if err := e.journal.MarkPaused(ctx, traceID, pending, []models.ChatMessage{user, assistant}); err != nil {
		_ = e.journal.MarkOutcome(ctx, traceID, OutcomeFailed, "", err)
		return refusal(req, HostedApprovalNeeded, "the approval request could not be recorded, so the call was not run")
	}
	e.pausedFor(ctx, &runState{traceID: traceID, agent: agent}, PauseApproval)
	e.auditRun(ctx, agent.Kitchen, "tool.brokered_call_held", map[string]interface{}{
		"agent": agent.Name, "run": req.Run, "held_run": traceID, "trace_id": traceID, "tool": tc.Name, "wait": wait.String(),
	})

	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(hostedPoll)
	defer tick.Stop()
	for {
		js, err := e.journal.Load(ctx, traceID)
		if err == nil && js.Status == RunDone {
			return hostedDecision(req, js, traceID)
		}
		reason := ""
		select {
		case <-tick.C:
			if req.StillOpen != nil && !req.StillOpen() {
				reason = "the process that asked for this call left before anyone decided"
			}
		case <-deadline.C:
			reason = fmt.Sprintf("nobody decided within %s", wait)
		case <-ctx.Done():
			reason = "the request was abandoned before anyone decided"
		}
		if reason == "" {
			continue
		}
		// Ending the hold goes through the same claim a person's decision does, so exactly one of the two wins.
		won, _ := e.ExpirePause(context.WithoutCancel(ctx), agent.Kitchen, traceID, reason)
		if won {
			out := refusal(req, HostedApprovalExpired, "approval expired: "+reason+"; the tool was not run")
			out.Approval, out.HeldRun = "expired", traceID
			return out
		}
		// A person decided first (the claim is theirs, the outcome is being written): act on that, and give the
		// writer a moment to finish.
		for i := 0; i < 20; i++ {
			if js, err := e.journal.Load(context.WithoutCancel(ctx), traceID); err == nil && js.Status == RunDone {
				return hostedDecision(req, js, traceID)
			}
			time.Sleep(hostedPoll)
		}
		out := refusal(req, HostedApprovalExpired, "approval expired: "+reason+"; the tool was not run")
		out.Approval, out.HeldRun = "expired", traceID
		return out
	}
}

const (
	hostedApprovedMark = "approved"
	hostedDeniedMark   = "denied: "
)

// hostedDecision reads how a held call ended: a person's approval or denial, or an expiry recorded by whoever ended it.
func hostedDecision(req HostedToolRequest, js *JournalState, traceID string) HostedToolOutcome {
	switch {
	case js.Outcome == OutcomeCompleted && js.FinalContent == hostedApprovedMark:
		return HostedToolOutcome{Approval: "approved", HeldRun: traceID}
	case js.Outcome == OutcomeCancelled && strings.HasPrefix(js.FinalErr, hostedDeniedMark):
		note := strings.TrimPrefix(js.FinalErr, hostedDeniedMark)
		if note == "" {
			note = "no reason given"
		}
		out := refusal(req, HostedApprovalDenied, "Human denied this tool call: "+note)
		out.Approval, out.HeldRun = "denied", traceID
		return out
	}
	why := js.FinalErr
	if why == "" {
		why = "the request was ended"
	}
	out := refusal(req, HostedApprovalExpired, "approval expired: "+why+"; the tool was not run")
	out.Approval, out.HeldRun = "expired", traceID
	return out
}

// decideHosted records a person's decision on a held call. The claim is the journal's, so a late approval cannot win over
// an expiry or a second decision. After the call's deadline nobody is waiting and the run is ended instead.
func (e *Executor) decideHosted(ctx context.Context, js *JournalState, decision *ResumeDecision) error {
	traceID := js.Header.TraceID
	if js.Status != RunPaused {
		return fmt.Errorf("executor: run %s is not waiting for a decision", traceID)
	}
	if decision == nil {
		return fmt.Errorf("executor: run %s is paused, a ResumeDecision is required", traceID)
	}
	if decision.Extend != nil {
		return fmt.Errorf("executor: run %s is a held tool call; it has no budget to extend", traceID)
	}
	if time.Now().After(js.Header.Hosted.Deadline) {
		if _, err := e.ExpirePause(ctx, js.Header.Kitchen, traceID, "the process that asked for this call stopped waiting"); err != nil {
			return err
		}
		return fmt.Errorf("executor: run %s was a tool call held for a process that is no longer waiting; nothing was run", traceID)
	}
	claimed, err := e.journal.ClaimResume(ctx, traceID, nil)
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("executor: run %s is already being resumed", traceID)
	}
	agent := ""
	if js.Header.Agent != nil {
		agent = js.Header.Agent.Name
	}
	if decision.Approved {
		err = e.journal.MarkOutcome(ctx, traceID, OutcomeCompleted, hostedApprovedMark, nil)
	} else {
		err = e.journal.MarkOutcome(ctx, traceID, OutcomeCancelled, "", errors.New(hostedDeniedMark+decision.Note))
	}
	if err != nil {
		return fmt.Errorf("executor: the decision on run %s was claimed but could not be recorded: %w", traceID, err)
	}
	e.auditRun(ctx, js.Header.Kitchen, "tool.brokered_call_decided", map[string]interface{}{
		"agent": agent, "held_run": traceID, "trace_id": traceID, "approved": decision.Approved,
	})
	return ErrHostedDecisionRecorded
}

// ArgsDigest identifies a call's arguments in an audit event without carrying their values.
func ArgsDigest(args map[string]interface{}) string {
	raw, _ := json.Marshal(args)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// AuditRun reports a run-level event to the audit sink the executor was given (SetAudit); the tool route uses it for the
// event of each brokered call so that it sits beside the events of the Go loop.
func (e *Executor) AuditRun(ctx context.Context, kitchen, action string, details map[string]interface{}) {
	e.auditRun(ctx, kitchen, action, details)
}
