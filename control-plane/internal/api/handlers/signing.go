package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/workflow"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// Signed decisions (Pro ADR-0041). The community edition installs no DecisionSigner and every
// handler below decides exactly as before. With one, a decision is applied only after the signer has
// verified that the authenticated person signed this record, as it is now, with this meaning.

var (
	describerMu sync.RWMutex
)

// SetDecisionSigner makes every decision gate require a signature (nil turns that off).
func (h *Handlers) SetDecisionSigner(s contracts.DecisionSigner) { h.DecisionSigner = s }

// RegisterDecisionDescriber registers how a gate's record is read and digested. The gates owned by
// this package are registered when the handlers are built; Pro registers its own (promotions, bot
// actions, datasets). A gate with no describer cannot be signed, and so cannot be decided while a
// signer is installed.
func (h *Handlers) RegisterDecisionDescriber(gate string, fn contracts.DecisionDescriber) {
	describerMu.Lock()
	defer describerMu.Unlock()
	if h.describers == nil {
		h.describers = map[string]contracts.DecisionDescriber{}
	}
	h.describers[gate] = fn
}

// DecisionGates lists the registered gates, sorted.
func (h *Handlers) DecisionGates() []string {
	describerMu.RLock()
	defer describerMu.RUnlock()
	out := make([]string, 0, len(h.describers))
	for g := range h.describers {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// DescribeDecision reads the record behind a gate as it is now. The signing challenge calls it to show
// the person what they would sign; the decision handler calls it again to check nothing changed.
func (h *Handlers) DescribeDecision(ctx context.Context, gate, kitchen, recordID string, payload map[string]interface{}) (*contracts.DecisionSubject, error) {
	describerMu.RLock()
	fn := h.describers[gate]
	describerMu.RUnlock()
	if fn == nil {
		return nil, fmt.Errorf("unknown decision gate %q", gate)
	}
	return fn(ctx, kitchen, recordID, payload)
}

// signDecision is the one call every decision handler makes before it applies a decision. It
// returns (nil, true) when no signer is installed. When it returns false it has answered the request.
func (h *Handlers) signDecision(w http.ResponseWriter, r *http.Request, gate, recordID, meaning string, payload map[string]interface{}) (*contracts.SignedDecision, bool) {
	if h.DecisionSigner == nil {
		return nil, true
	}
	kitchen := middleware.GetKitchen(r.Context())
	subject, err := h.DescribeDecision(r.Context(), gate, kitchen, recordID, payload)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return nil, false
	}
	sig, err := h.DecisionSigner.Sign(r, subject, meaning)
	if err != nil {
		var ae *contracts.AuthError
		if errors.As(err, &ae) {
			respondJSON(w, ae.Status, map[string]string{"error": ae.Code, "message": ae.Message})
			return nil, false
		}
		log.Error().Err(err).Str("gate", gate).Msg("decision signing failed")
		respondError(w, http.StatusInternalServerError, "could not verify the signature")
		return nil, false
	}
	return sig, true
}

// decisionApplied tells the signer how the decision went. A signature whose decision was not
// applied stays on record, marked so.
func (h *Handlers) decisionApplied(r *http.Request, sig *contracts.SignedDecision, applyErr error) {
	if h.DecisionSigner != nil && sig != nil {
		h.DecisionSigner.Applied(r, sig, applyErr)
	}
}

// signedBy is the person a decision is recorded against: the verified signer when the decision was
// signed, otherwise whatever the caller said (the community edition, as before).
func signedBy(sig *contracts.SignedDecision, id, email string) (string, string) {
	if sig != nil {
		return sig.SignerID, sig.SignerEmail
	}
	return id, email
}

func meaningOf(approved bool) string {
	if approved {
		return contracts.MeaningApproved
	}
	return contracts.MeaningRejected
}

// ── the gates this package owns ──────────────────────────────

// RegisterOwnDecisionGates registers the gates this package decides. New calls it; a Handlers built by hand
// (a test) must.
func (h *Handlers) RegisterOwnDecisionGates() {
	for _, g := range []string{contracts.GateToolApproval, contracts.GateBudgetExtension, contracts.GateResumeGuidance} {
		h.RegisterDecisionDescriber(g, h.describeRun(g))
	}
	h.RegisterDecisionDescriber(contracts.GateRecipeGate, h.describeRecipeGate)
	h.RegisterDecisionDescriber(contracts.GateWaitRelease, h.describeWaitRelease)
	h.RegisterDecisionDescriber(contracts.GateSkillApproval, h.describeSkill)
}

// gateOfRun is the gate that unblocks a paused run, "" when it is not paused.
func gateOfRun(js *executor.JournalState) string {
	switch executor.PauseKindOf(js) {
	case string(executor.PauseApproval):
		return contracts.GateToolApproval
	case string(executor.PauseBudget):
		return contracts.GateBudgetExtension
	case string(executor.PauseStall):
		return contracts.GateResumeGuidance
	}
	return ""
}

func (h *Handlers) describeRun(gate string) contracts.DecisionDescriber {
	return func(ctx context.Context, kitchen, traceID string, payload map[string]interface{}) (*contracts.DecisionSubject, error) {
		if h.Executor == nil {
			return nil, fmt.Errorf("runs are not available")
		}
		js, err := h.Executor.RunState(ctx, kitchen, traceID)
		if err != nil {
			return nil, err
		}
		actual := gateOfRun(js)
		if actual == "" {
			return nil, fmt.Errorf("run %s is not waiting for a decision", traceID)
		}
		if actual != gate {
			return nil, fmt.Errorf("run %s is waiting for %s, not %s", traceID, actual, gate)
		}
		agent := ""
		if js.Header.Agent != nil {
			agent = js.Header.Agent.Name
		}
		display := map[string]interface{}{"agent": agent, "run": traceID}
		digest := map[string]interface{}{"gate": gate, "kitchen": kitchen, "run": traceID, "agent": agent}
		switch gate {
		case contracts.GateToolApproval:
			calls := []map[string]interface{}{}
			turn := 0
			if js.Pending != nil {
				turn = js.Pending.Turn
				for _, c := range js.Pending.ToolCalls {
					calls = append(calls, map[string]interface{}{"name": c.Name, "arguments": c.Arguments})
				}
			}
			display["turn"], display["tool_calls"] = turn, calls
			digest["turn"], digest["tool_calls"] = turn, calls
			addNote(display, digest, payload)
		case contracts.GateBudgetExtension:
			if js.BudgetPause != nil {
				display["paused_at"] = js.BudgetPause
				digest["paused_at"] = js.BudgetPause
			}
			if ext, ok := payload["extend"]; ok {
				n := normalizeExtension(ext)
				display["extension"], digest["extension"] = n, n
			}
			addNote(display, digest, payload)
		case contracts.GateResumeGuidance:
			if js.StallPause != nil {
				display["stalled_on"] = js.StallPause
				digest["stalled_on"] = js.StallPause
			}
			if note, ok := payload["note"]; ok {
				display["guidance"], digest["guidance"] = note, note
			}
		}
		return &contracts.DecisionSubject{
			Gate: gate, RecordID: traceID, Kitchen: kitchen,
			Digest:    contracts.DigestOf(digest),
			Title:     "Decide a paused run of " + agent,
			Display:   display,
			Requester: js.Header.StartedBy,
			Scope:     agent,
			Meanings:  []string{contracts.MeaningApproved, contracts.MeaningRejected},
		}, nil
	}
}

// addNote puts the free-text note of a decision in what is shown and in what is signed. The note is not
// decoration: on a refusal the model is told "Human denied this tool call: <note>", so a note a person did not
// sign would be words put in a signer's mouth. An empty note adds nothing, so a decision without one digests as
// it always did.
func addNote(display, digest, payload map[string]interface{}) {
	if note, _ := payload["note"].(string); strings.TrimSpace(note) != "" {
		display["note"], digest["note"] = note, note
	}
}

func (h *Handlers) describeRecipeGate(ctx context.Context, kitchen, gateKey string, _ map[string]interface{}) (*contracts.DecisionSubject, error) {
	notFound := fmt.Errorf("no gate %q in this kitchen", gateKey)
	rec, err := h.Store.GetApproval(ctx, gateKey)
	if err != nil || rec == nil {
		return nil, notFound
	}
	// A gate belongs to the kitchen of its record, or when the record names none, of its run. A record
	// that names no kitchen is the default kitchen's, never every kitchen's.
	owner := rec.Kitchen
	if owner == "" {
		if run, rerr := h.Store.GetRecipeRun(ctx, rec.RunID); rerr == nil && run != nil {
			owner = run.Kitchen
		}
	}
	if !ownedBy(owner, kitchen) {
		return nil, notFound
	}
	if rec.Status != "pending" && rec.Status != "waiting" {
		return nil, fmt.Errorf("gate %q is %s, not pending", gateKey, rec.Status)
	}
	requester := ""
	if h.Store != nil {
		if run, rerr := h.Store.GetRecipeRun(ctx, rec.RunID); rerr == nil && run != nil {
			requester = run.TriggeredBy
		}
	}
	return &contracts.DecisionSubject{
		Gate: contracts.GateRecipeGate, RecordID: gateKey, Kitchen: kitchen,
		Digest: contracts.DigestOf(map[string]interface{}{
			"gate": contracts.GateRecipeGate, "kitchen": kitchen, "gate_key": rec.GateKey, "run": rec.RunID, "step": rec.StepName,
			"requested_at": rec.RequestedAt.UTC().Format("2006-01-02T15:04:05.000000Z"), "inputs": rec.Metadata,
		}),
		Title:     "Approve step " + rec.StepName + " of run " + rec.RunID,
		Display:   map[string]interface{}{"run": rec.RunID, "step": rec.StepName, "inputs": rec.Metadata, "requested_at": rec.RequestedAt},
		Requester: requester,
		Scope:     rec.StepName,
		Approvers: h.stepApprovers(ctx, rec),
		Meanings:  []string{contracts.MeaningApproved, contracts.MeaningRejected},
	}, nil
}

// stepApprovers reads the approver constraints the recipe step behind a gate declares, so an edition
// that verifies the signer first can refuse an unlisted person before a signature is spent. It is
// best effort: when the run, the recipe or the step cannot be found it returns nil and the workflow
// engine, which fails closed, decides when the decision lands.
func (h *Handlers) stepApprovers(ctx context.Context, rec *models.ApprovalRecord) *contracts.ApproverConstraints {
	if h.Store == nil {
		return nil
	}
	run, err := h.Store.GetRecipeRun(ctx, rec.RunID)
	if err != nil || run == nil {
		return nil
	}
	kitchen := rec.Kitchen
	if kitchen == "" {
		kitchen = run.Kitchen
	}
	recipe, err := h.Store.GetRecipe(ctx, kitchen, run.RecipeID)
	if err != nil || recipe == nil {
		all, lerr := h.Store.ListRecipes(ctx, kitchen)
		if lerr != nil {
			return nil
		}
		recipe = nil
		for i := range all {
			if all[i].ID == run.RecipeID || all[i].Name == run.RecipeID {
				recipe = &all[i]
				break
			}
		}
		if recipe == nil {
			return nil
		}
	}
	for i := range recipe.Steps {
		st := &recipe.Steps[i]
		if st.Name != rec.StepName {
			continue
		}
		c := &contracts.ApproverConstraints{Emails: st.ApproverEmails, Roles: st.ApproverRoles, Domain: st.ApproverDomain, SameTenant: st.RequireSameTenant}
		if c.Empty() {
			return nil
		}
		return c
	}
	return nil
}

// waitingStep returns the wait step of a run that is waiting now, only if the run belongs to this
// kitchen. A run of another kitchen, a run with no kitchen recorded and a step that is not waiting are
// all "not found": which of them it is does not leave the kitchen.
func (h *Handlers) waitingStep(ctx context.Context, kitchen, runID, stepName string) (*models.RecipeRun, *models.StepResult, error) {
	notFound := fmt.Errorf("no waiting step %q of run %q in this kitchen", stepName, runID)
	if h.Store == nil || kitchen == "" {
		return nil, nil, notFound
	}
	run, err := h.Store.GetRecipeRun(ctx, runID)
	if err != nil || run == nil || !ownedBy(run.Kitchen, kitchen) {
		return nil, nil, notFound
	}
	for i := range run.StepResults {
		if r := &run.StepResults[i]; r.StepName == stepName && r.Status == "waiting" {
			return run, r, nil
		}
	}
	return nil, nil, notFound
}

func (h *Handlers) describeWaitRelease(ctx context.Context, kitchen, key string, _ map[string]interface{}) (*contracts.DecisionSubject, error) {
	runID, step, ok := strings.Cut(key, ":")
	if !ok {
		return nil, fmt.Errorf("a wait is identified as <run id>:<step>")
	}
	run, res, err := h.waitingStep(ctx, kitchen, runID, step)
	if err != nil {
		return nil, err
	}
	wake := ""
	if res.WakeAt != nil {
		wake = res.WakeAt.UTC().Format("2006-01-02T15:04:05.000000Z")
	}
	return &contracts.DecisionSubject{
		Gate: contracts.GateWaitRelease, RecordID: key, Kitchen: kitchen,
		Digest: contracts.DigestOf(map[string]interface{}{
			"gate": contracts.GateWaitRelease, "kitchen": kitchen, "run": run.ID, "recipe": run.RecipeID, "step": step,
			"started_at": res.StartedAt.UTC().Format("2006-01-02T15:04:05.000000Z"), "wakes_at": wake,
		}),
		Title:     "Release the waiting step " + step + " of run " + run.ID,
		Display:   map[string]interface{}{"run": run.ID, "recipe": run.RecipeID, "step": step, "wakes_at": res.WakeAt},
		Requester: run.TriggeredBy,
		Scope:     step,
		Meanings:  []string{contracts.MeaningApproved},
	}, nil
}

func (h *Handlers) describeSkill(ctx context.Context, kitchen, name string, payload map[string]interface{}) (*contracts.DecisionSubject, error) {
	skill, err := h.Store.GetSkill(ctx, kitchen, name)
	if err != nil || skill == nil {
		return nil, fmt.Errorf("no skill %q in this kitchen", name)
	}
	if skill.Status != models.SkillStatusNeedsReview {
		return nil, fmt.Errorf("skill %q is %q, not needs_review", name, skill.Status)
	}
	digest := map[string]interface{}{
		"gate": contracts.GateSkillApproval, "kitchen": kitchen, "skill": skill.Name, "content": skillDigest(skill),
		"verdict": skill.VerificationVerdict, "reasoning": skill.VerificationReasoning,
	}
	display := map[string]interface{}{
		"skill": skill.Name, "content_digest": skillDigest(skill), "verdict": skill.VerificationVerdict, "analysis": skill.VerificationReasoning,
	}
	addNote(display, digest, payload) // the reviewer's note is appended to the skill's analysis, so it is signed
	return &contracts.DecisionSubject{
		Gate: contracts.GateSkillApproval, RecordID: name, Kitchen: kitchen,
		Digest:   contracts.DigestOf(digest),
		Title:    "Review skill " + skill.Name,
		Display:  display,
		Scope:    skill.Name,
		Meanings: []string{contracts.MeaningApproved, contracts.MeaningRejected},
	}, nil
}

// skillDigest identifies the skill content that was analysed: its manifest, as stored.
func skillDigest(s *models.Skill) string { return contracts.DigestOf(s.Manifest) }

// signRunDecision signs the decision on a paused run: the gate follows from why the run paused. A run
// that is not paused has no decision to sign (it is being recovered, not decided).
func (h *Handlers) signRunDecision(w http.ResponseWriter, r *http.Request, kitchen, traceID string, d *executor.ResumeDecision) (*contracts.SignedDecision, bool) {
	js, err := h.Executor.RunState(r.Context(), kitchen, traceID)
	if err != nil {
		// With a signer installed a run whose state cannot be read is not decided: proceeding would let the
		// decision through unsigned if the resume path then reads it. Not found stays a 404.
		if h.DecisionSigner == nil {
			return nil, true
		}
		if errors.Is(err, executor.ErrNoSuchRun) {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			log.Error().Err(err).Str("run", traceID).Msg("signing: could not read the state of a run")
			respondError(w, http.StatusServiceUnavailable, "could not read the state of this run, try again")
		}
		return nil, false
	}
	gate := gateOfRun(js)
	if gate == "" {
		return nil, true
	}
	payload := map[string]interface{}{}
	if d.Note != "" {
		payload["note"] = d.Note
	}
	if d.Extend != nil {
		payload["extend"] = d.Extend
	}
	sig, ok := h.signDecision(w, r, gate, traceID, meaningOf(d.Approved), payload)
	if !ok {
		return nil, false
	}
	if sig != nil && d.Extend != nil {
		d.Extend.By = sig.SignerID // the signer, never the caller's say-so
	}
	return sig, true
}

// decisionApplyError is the error to report for a decision: a run that paused again or ended at its
// budget after being resumed was resumed, so the decision was applied.
func decisionApplyError(err error) error {
	var pe *executor.PausedError
	if err == nil || errors.As(err, &pe) || errors.Is(err, executor.ErrBudgetExhausted) {
		return nil
	}
	return err
}

// normalizeExtension makes a budget extension from a request and one built in code digest alike: it is
// decoded into the model, which drops fields that do not belong and the omitted zero values, and the
// server-set By is cleared.
func normalizeExtension(v interface{}) interface{} {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var e models.BudgetExtension
	if json.Unmarshal(raw, &e) != nil {
		return v
	}
	e.By = ""
	return e
}

// approveSignedGate decides a recipe gate on the signed path: the approver is the verified signer,
// whatever the body said, and the recipe's approver constraints (people, roles, domain, tenant) are
// checked against the signer.
func (h *Handlers) approveSignedGate(w http.ResponseWriter, r *http.Request, runID, stepName string, approved bool, channel, comments string) {
	gateKey := runID + ":" + stepName
	sig, ok := h.signDecision(w, r, contracts.GateRecipeGate, gateKey, meaningOf(approved), nil)
	if !ok {
		return
	}
	issuer := ""
	if id := pkgmw.GetIdentity(r.Context()); id != nil {
		issuer = id.Claims["iss"]
	}
	// The channel is where the decision was made, and it was made through the signing API: a label
	// from the body (a bridge claiming "slack") is not believed.
	channel = "signature"
	found, err := h.Workflow.ApproveGateAs(runID, stepName, approved, workflow.Approver{
		ID: sig.SignerID, Email: sig.SignerEmail, Issuer: issuer, Role: sig.SignerRole,
	}, channel, comments)
	h.decisionApplied(r, sig, err)
	if err != nil {
		respondError(w, http.StatusForbidden, err.Error())
		return
	}
	if !found {
		respondError(w, http.StatusNotFound, fmt.Sprintf("No pending gate '%s' for run '%s'", stepName, runID))
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"run_id": runID, "step": stepName, "approved": approved, "signature_id": sig.SignatureID,
		"signed_by": sig.PrintedName, "signed_at": sig.SignedAt, "meaning": sig.Meaning,
	})
}
