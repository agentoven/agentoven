package skills_test

import (
	"context"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/skills"
)

// fakeRouter returns a scripted RouteResponse content, letting a test pin
// down exactly what VerifyIntent does with a given provider verdict without
// a real model call.
type fakeRouter struct {
	content  string
	provider string
	err      error
	lastReq  *models.RouteRequest
}

func (f *fakeRouter) Route(_ context.Context, req *models.RouteRequest) (*models.RouteResponse, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return &models.RouteResponse{Content: f.content, Provider: f.provider}, nil
}

func testManifest() *models.SkillManifest {
	return &models.SkillManifest{Name: "web_search", Description: "Searches the web.", Instructions: "Call search for current info."}
}

func TestVerifyIntentParsesAnAcceptVerdict(t *testing.T) {
	r := &fakeRouter{content: `{"verdict":"accept","reasoning":"looks fine"}`, provider: "p"}
	verdict, reasoning, provider, err := skills.VerifyIntent(context.Background(), r, "default", testManifest(), skills.Bundle{"SKILL.md": []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if provider != "p" {
		t.Fatalf("expected the serving provider's name back, got %q", provider)
	}
	if verdict != skills.VerdictAccept || reasoning != "looks fine" {
		t.Fatalf("got verdict=%q reasoning=%q", verdict, reasoning)
	}
	if r.lastReq.ResponseFormat == nil || r.lastReq.ResponseFormat.Type != "json_schema" {
		t.Fatalf("expected a json_schema response format on the verification call, got %+v", r.lastReq.ResponseFormat)
	}
}

func TestVerifyIntentParsesARejectVerdict(t *testing.T) {
	r := &fakeRouter{content: `{"verdict":"reject","reasoning":"tries to exfiltrate credentials"}`}
	verdict, reasoning, _, err := skills.VerifyIntent(context.Background(), r, "default", testManifest(), skills.Bundle{"SKILL.md": []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if verdict != skills.VerdictReject || reasoning == "" {
		t.Fatalf("got verdict=%q reasoning=%q", verdict, reasoning)
	}
}

func TestVerifyIntentTreatsMalformedJSONAsNeedsReview(t *testing.T) {
	r := &fakeRouter{content: "not json at all"}
	verdict, reasoning, _, err := skills.VerifyIntent(context.Background(), r, "default", testManifest(), skills.Bundle{"SKILL.md": []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if verdict != skills.VerdictNeedsReview || reasoning == "" {
		t.Fatalf("expected needs_review with an explanatory reasoning, got verdict=%q reasoning=%q", verdict, reasoning)
	}
}

func TestVerifyIntentTreatsUnrecognizedVerdictAsNeedsReview(t *testing.T) {
	r := &fakeRouter{content: `{"verdict":"maybe-ish","reasoning":"unsure"}`}
	verdict, _, _, err := skills.VerifyIntent(context.Background(), r, "default", testManifest(), skills.Bundle{"SKILL.md": []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if verdict != skills.VerdictNeedsReview {
		t.Fatalf("expected needs_review for an unrecognized verdict string, got %q", verdict)
	}
}

func TestVerifyIntentWithProviderPinsTheRequest(t *testing.T) {
	r := &fakeRouter{content: `{"verdict":"accept","reasoning":"fine"}`, provider: "chosen"}
	_, _, _, err := skills.VerifyIntentWithProvider(context.Background(), r, "default", "chosen", testManifest(), skills.Bundle{"SKILL.md": []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if r.lastReq.PinnedProvider != "chosen" {
		t.Fatalf("expected PinnedProvider to be set on the route request, got %q", r.lastReq.PinnedProvider)
	}
}

func TestVerifyIntentWithProviderRejectsAnEmptyProvider(t *testing.T) {
	r := &fakeRouter{content: `{"verdict":"accept","reasoning":"fine"}`}
	if _, _, _, err := skills.VerifyIntentWithProvider(context.Background(), r, "default", "", testManifest(), skills.Bundle{"SKILL.md": []byte("x")}); err == nil {
		t.Fatal("expected an error when no provider is named")
	}
}

func TestVerifyIntentPropagatesRouterErrors(t *testing.T) {
	r := &fakeRouter{err: context.DeadlineExceeded}
	if _, _, _, err := skills.VerifyIntent(context.Background(), r, "default", testManifest(), skills.Bundle{"SKILL.md": []byte("x")}); err == nil {
		t.Fatal("expected a router error to propagate rather than being swallowed")
	}
}
