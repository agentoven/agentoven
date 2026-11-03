package contracts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// Electronic signatures on human decisions (Pro ADR-0041).
//
// Every point where a person's decision lets something proceed or stops it is a gate. A handler
// that decides one asks for the decision's subject (what the person was shown, and a digest of it),
// asks the DecisionSigner to verify the person signed exactly that, applies the decision, and tells
// the signer how it went. The community edition installs no signer and decides as it always has.

// Gate names. A gate added later is signed by default: see the registry in the handlers.
const (
	GateToolApproval    = "tool_approval"
	GateBudgetExtension = "budget_extension"
	// GateBudgetRaise raises or removes a period budget (a kitchen's, an agent's, a provider's or an API
	// key's monthly allowance, ADR-0051). It is the decision GateBudgetExtension is for one run, taken
	// for everyone who shares the budget; lowering a budget only protects and is not a gate.
	GateBudgetRaise    = "budget_raise"
	GateResumeGuidance = "resume_guidance"
	GateRecipeGate     = "recipe_gate"
	GateWaitRelease    = "wait_release"
	GatePromotion      = "environment_promotion"
	GateSkillApproval  = "skill_approval"
	GateBotAction      = "bot_action"
	GateLiveToolReplay = "live_tool_replay"
	GateDatasetPublish = "dataset_publish"
	GateDatasetPurge   = "dataset_purge"
	// GateLegalHoldRelease lifts a legal hold, on a dataset or on a kitchen's audit trail. Placing a
	// hold is not a gate: it only protects. Lifting one is signed by the legal team (ADR-0043).
	GateLegalHoldRelease = "legal_hold_release"
	// GateApprovalSkip runs a recipe on an environment that requires approval without that approval.
	// A Head Chef or Restaurateur may, outside a GxP kitchen, and the skip is signed like any decision.
	GateApprovalSkip = "approval_skip"
	// GateSecretBinding approves or revokes letting a secret be sent on behalf of a skill to a server
	// (ADR-0035): the person is shown the secret, the skill, the host and the header it goes in.
	GateSecretBinding = "secret_binding"
	// GateSecretMigrate moves secrets from one secret provider to another (ADR-0035): the person is shown
	// exactly which secrets move, and the digest covers that list.
	GateSecretMigrate = "secret_migrate"
	// GateAuditErasure erases a person from a kitchen's audit trail (GDPR Art. 17): the display copies of
	// their identity on the audit events and the pseudonym mapping. It is signed by the legal team alone,
	// who are shown the scope; the person erased may not sign it (ADR-0043).
	GateAuditErasure = "audit_erasure"
	// GateGxPSwitch switches a kitchen's GxP protections off (the gxp_enabled setting, or a GxP compliance
	// requirement removed). Turning them on only protects and is not a gate. A Head Chef signs it, shown
	// the settings before and after (ADR-0041).
	GateGxPSwitch = "gxp_switch"
	// GateApproverPolicy replaces the kitchen's approver policy (who may approve what) in a GxP kitchen.
	// The person is shown the policy before and after; elsewhere the change is not signed but its content
	// is recorded on the audit trail.
	GateApproverPolicy = "approver_policy"
	// GateTriggerChange switches an inbound trigger on or off, or changes the mapping that turns an incoming
	// event into a run's input, in a GxP kitchen (owner decision S12, 2026-11-03). What the trigger does to
	// the record is shown before and after.
	GateTriggerChange = "trigger_change"
	MeaningApproved    = "approved"
	MeaningRejected    = "rejected"
)

// DecisionSubject is what a person is shown and what a signature is bound to.
type DecisionSubject struct {
	Gate     string `json:"gate"`
	RecordID string `json:"record_id"`
	Kitchen  string `json:"kitchen"`
	// Digest is the SHA-256 of the canonical form of exactly what is being decided: the record as it
	// stands now plus the content of the decision (a stated reason, an amount).
	Digest string `json:"digest"`
	Title  string `json:"title"`
	// Display is shown to the signer before they sign.
	Display map[string]interface{} `json:"display"`
	// Requester is who started the thing being decided (an e-mail or subject), empty for a system.
	// A GxP kitchen bars the requester from signing it.
	Requester string `json:"requester,omitempty"`
	// Scope narrows who may decide: an environment slug, an agent name. It selects the approver
	// policy that applies.
	Scope string `json:"scope,omitempty"`
	// Approvers are the constraints the record itself declares on who may decide it (a recipe step's
	// approver_roles, approver_emails and approver_domain). They apply on top of the kitchen's
	// approver policy; nil means the record declares none.
	Approvers *ApproverConstraints `json:"approvers,omitempty"`
	// Meanings are the signatures this gate accepts.
	Meanings []string `json:"meanings"`
}

// DecisionDescriber reads the live record behind a gate. payload is the content of the intended
// decision (a note, an extension) and is part of what is signed.
type DecisionDescriber func(ctx context.Context, kitchen, recordID string, payload map[string]interface{}) (*DecisionSubject, error)

// SignedDecision is a verified signature, returned by DecisionSigner.Sign.
type SignedDecision struct {
	SignatureID string    `json:"signature_id"`
	Gate        string    `json:"gate"`
	RecordID    string    `json:"record_id"`
	Meaning     string    `json:"meaning"`
	Digest      string    `json:"digest"`
	SignerID    string    `json:"signer_id"` // the verified subject of the session
	SignerEmail string    `json:"signer_email"`
	PrintedName string    `json:"printed_name"`
	SignerRole  string    `json:"signer_role"`
	SignedAt    time.Time `json:"signed_at"`
}

// DecisionSigner verifies and records signatures. Pro installs one; the community edition has none.
type DecisionSigner interface {
	// Sign checks that the caller is a person who signed exactly this subject with this meaning
	// (a valid single-use signing token on the request), that they may decide it, and records the
	// signature before the decision is applied. A refusal is an *AuthError.
	Sign(r *http.Request, subject *DecisionSubject, meaning string) (*SignedDecision, error)
	// Applied reports whether the decision that the signature covers was then applied.
	Applied(r *http.Request, sig *SignedDecision, applyErr error)
}

// DigestOf returns the SHA-256 (hex) of v's canonical JSON: maps with sorted keys, no whitespace.
// The same value gives the same digest wherever it is computed. Numbers keep their literal text, so two
// integers above 2^53 do not collapse into one float. A value that cannot be encoded has no digest and
// DigestOf returns "": the signing service refuses an empty digest, because two unrelated records that
// both failed to encode would otherwise share one.
func DigestOf(v interface{}) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic interface{}
	if err := dec.Decode(&generic); err != nil {
		return ""
	}
	sum := sha256.Sum256(canonicalJSON(generic))
	return hex.EncodeToString(sum[:])
}

func canonicalJSON(v interface{}) []byte {
	switch t := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := []byte{'{'}
		for i, k := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			kb, _ := json.Marshal(k)
			out = append(out, kb...)
			out = append(out, ':')
			out = append(out, canonicalJSON(t[k])...)
		}
		return append(out, '}')
	case []interface{}:
		out := []byte{'['}
		for i, e := range t {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, canonicalJSON(e)...)
		}
		return append(out, ']')
	default:
		b, _ := json.Marshal(t)
		return b
	}
}

type requestKey struct{}

// WithRequest carries the request that started something into the code it calls, for the part of that
// code that runs before the request ends and must take a signature from it (the recipe runner's
// approval skip). Do not keep the request beyond the call.
func WithRequest(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, requestKey{}, r)
}

// RequestFrom returns the request WithRequest stored, nil when there is none (a scheduled run).
func RequestFrom(ctx context.Context) *http.Request {
	r, _ := ctx.Value(requestKey{}).(*http.Request)
	return r
}
