package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// fakeSigner stands in for Pro's signer: it records what it was asked, and refuses or signs.
type fakeSigner struct {
	refuse  *contracts.AuthError
	signer  string
	got     []*contracts.DecisionSubject
	meaning []string
	applied []error
}

func (f *fakeSigner) Sign(_ *http.Request, s *contracts.DecisionSubject, meaning string) (*contracts.SignedDecision, error) {
	f.got, f.meaning = append(f.got, s), append(f.meaning, meaning)
	if f.refuse != nil {
		return nil, f.refuse
	}
	return &contracts.SignedDecision{SignatureID: "sig-1", Gate: s.Gate, RecordID: s.RecordID, Meaning: meaning, Digest: s.Digest,
		SignerID: f.signer, SignerEmail: f.signer + "@x.test", PrintedName: "Signer", SignerRole: "head_chef"}, nil
}
func (f *fakeSigner) Applied(_ *http.Request, _ *contracts.SignedDecision, err error) {
	f.applied = append(f.applied, err)
}

func TestASkillReviewIsASignedGate(t *testing.T) {
	h, s := newTestHandlers(t)
	ctx := context.Background()
	if err := s.CreateSkill(ctx, &models.Skill{ID: "s1", Kitchen: "default", Name: "pdf-tools", Status: models.SkillStatusNeedsReview,
		Manifest: &models.SkillManifest{Name: "pdf-tools"}, VerificationVerdict: "needs_review", VerificationReasoning: "reads files"}); err != nil {
		t.Fatal(err)
	}
	post := func(path string) *httptest.ResponseRecorder {
		req := withChiParam(withKitchen(httptest.NewRequest("POST", path, strings.NewReader(`{"note":"fine"}`)), "default"), "name", "pdf-tools")
		rec := httptest.NewRecorder()
		h.RejectSkill(rec, req)
		return rec
	}

	// Without a signer the review goes through, as it always did.
	h2, s2 := newTestHandlers(t)
	s2.CreateSkill(ctx, &models.Skill{ID: "s2", Kitchen: "default", Name: "pdf-tools", Status: models.SkillStatusNeedsReview, Manifest: &models.SkillManifest{Name: "pdf-tools"}})
	rec := httptest.NewRecorder()
	h2.RejectSkill(rec, withChiParam(withKitchen(httptest.NewRequest("POST", "/", strings.NewReader(`{}`)), "default"), "name", "pdf-tools"))
	if rec.Code != http.StatusOK {
		t.Fatalf("community edition: %d %s", rec.Code, rec.Body)
	}

	// With a signer that refuses, nothing changes and the refusal is passed on.
	fs := &fakeSigner{refuse: &contracts.AuthError{Status: 403, Code: "signature_required", Message: "sign it"}}
	h.RegisterOwnDecisionGates()
	h.SetDecisionSigner(fs)
	rec = post("/")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "signature_required") {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	if sk, _ := s.GetSkill(ctx, "default", "pdf-tools"); sk.Status != models.SkillStatusNeedsReview {
		t.Fatalf("a refused signature must not change the skill: %s", sk.Status)
	}
	if len(fs.applied) != 0 {
		t.Fatal("Applied must not be called for a decision that was refused")
	}

	// With a signer that signs, the decision is applied, recorded against the signer, and reported.
	fs.refuse, fs.signer = nil, "ana"
	rec = post("/")
	if rec.Code != http.StatusOK {
		t.Fatalf("signed: %d %s", rec.Code, rec.Body)
	}
	sk, _ := s.GetSkill(ctx, "default", "pdf-tools")
	if sk.Status != models.SkillStatusRejected || !strings.Contains(sk.VerificationReasoning, "[human review by ana]") {
		t.Fatalf("skill = %s / %q", sk.Status, sk.VerificationReasoning)
	}
	if len(fs.applied) != 1 || fs.applied[0] != nil {
		t.Fatalf("applied = %v", fs.applied)
	}
	last := fs.got[len(fs.got)-1]
	if last.Gate != contracts.GateSkillApproval || last.RecordID != "pdf-tools" || len(last.Digest) != 64 || fs.meaning[len(fs.meaning)-1] != contracts.MeaningRejected {
		t.Fatalf("subject = %+v meaning=%v", last, fs.meaning)
	}
}

func TestEveryGateThisPackageDecidesHasADescriber(t *testing.T) {
	h, _ := newTestHandlers(t)
	h.RegisterOwnDecisionGates()
	want := []string{"budget_extension", "recipe_gate", "resume_guidance", "skill_approval", "tool_approval", "wait_release"}
	got := h.DecisionGates()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("gates = %v, want %v", got, want)
	}
}

// The note of a skill review is appended to the skill's record, so it is signed with the decision.
func TestASkillReviewNoteIsSigned(t *testing.T) {
	h, s := newTestHandlers(t)
	if err := s.CreateSkill(context.Background(), &models.Skill{ID: "s2", Kitchen: "default", Name: "ocr-tools", Status: models.SkillStatusNeedsReview,
		Manifest: &models.SkillManifest{Name: "ocr-tools"}, VerificationVerdict: "needs_review", VerificationReasoning: "reads images"}); err != nil {
		t.Fatal(err)
	}
	h.RegisterOwnDecisionGates()
	describe := func(payload map[string]interface{}) string {
		sub, err := h.DescribeDecision(context.Background(), contracts.GateSkillApproval, "default", "ocr-tools", payload)
		if err != nil {
			t.Fatal(err)
		}
		return sub.Digest
	}
	if describe(nil) == describe(map[string]interface{}{"note": "looks fine"}) || describe(map[string]interface{}{"note": "a"}) == describe(map[string]interface{}{"note": "b"}) {
		t.Fatal("the note is part of what is signed")
	}
	fs := &fakeSigner{refuse: &contracts.AuthError{Status: 403, Code: "signature_required", Message: "sign it"}}
	h.SetDecisionSigner(fs)
	req := withChiParam(withKitchen(httptest.NewRequest("POST", "/api/v1/skills/ocr-tools/reject", strings.NewReader(`{"note":"looks fine"}`)), "default"), "name", "ocr-tools")
	h.RejectSkill(httptest.NewRecorder(), req)
	if len(fs.got) != 1 || fs.got[0].Digest != describe(map[string]interface{}{"note": "looks fine"}) {
		t.Fatalf("the handler asks to sign the note it will record: %+v", fs.got)
	}
}
