package contracts

import (
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
	GateResumeGuidance  = "resume_guidance"
	GateRecipeGate      = "recipe_gate"
	GateWaitRelease     = "wait_release"
	GatePromotion       = "environment_promotion"
	GateSkillApproval   = "skill_approval"
	GateBotAction       = "bot_action"
	GateLiveToolReplay  = "live_tool_replay"
	GateDatasetPublish  = "dataset_publish"
	GateDatasetPurge    = "dataset_purge"
	MeaningApproved     = "approved"
	MeaningRejected     = "rejected"
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
// The same value gives the same digest wherever it is computed.
func DigestOf(v interface{}) string {
	raw, err := json.Marshal(v)
	if err != nil {
		raw = []byte(err.Error())
	}
	var generic interface{}
	if err := json.Unmarshal(raw, &generic); err == nil {
		raw = canonicalJSON(generic)
	}
	sum := sha256.Sum256(raw)
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
