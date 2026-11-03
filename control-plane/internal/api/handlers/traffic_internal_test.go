package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type fakeLimiter struct {
	err     error
	kitchen string
	agent   string
}

func (f *fakeLimiter) Admit(_ *http.Request, kitchen, agent string) error {
	f.kitchen, f.agent = kitchen, agent
	return f.err
}

func serveLimited(h *Handlers, path string) (*httptest.ResponseRecorder, bool) {
	reached := false
	r := chi.NewRouter()
	r.With(h.LimitTraffic).Post("/agents/{agentName}/invoke", func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest("POST", path, nil)
	req = req.WithContext(pkgmw.SetKitchen(req.Context(), "acme"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec, reached
}

func TestWithoutALimiterEveryRequestGoesThrough(t *testing.T) {
	rec, reached := serveLimited(&Handlers{}, "/agents/triage/invoke")
	if rec.Code != 200 || !reached {
		t.Fatalf("the community edition limits nothing: %d", rec.Code)
	}
}

func TestALimiterIsToldTheKitchenAndTheAgentAndAnAdmittedRequestRuns(t *testing.T) {
	f := &fakeLimiter{}
	rec, reached := serveLimited(&Handlers{Traffic: f}, "/agents/triage/invoke")
	if rec.Code != 200 || !reached || f.kitchen != "acme" || f.agent != "triage" {
		t.Fatalf("%d %+v", rec.Code, f)
	}
}

func TestARateLimitIsA429ThatSaysWhenToComeBackAndTheRouteDoesNotRun(t *testing.T) {
	f := &fakeLimiter{err: &models.RateLimitedError{Scope: "agent", Limit: "invocations_per_minute", RetryAfter: 1500 * time.Millisecond, Reason: "agent triage is over its limit"}}
	rec, reached := serveLimited(&Handlers{Traffic: f}, "/agents/triage/invoke")
	if rec.Code != 429 || reached {
		t.Fatalf("%d reached=%v", rec.Code, reached)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After rounds up to whole seconds: %q", got)
	}
	for _, want := range []string{`"error":"rate_limited"`, `"scope":"agent"`, `"limit":"invocations_per_minute"`, `"retry_after":2`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s missing from %s", want, rec.Body)
		}
	}
}

func TestALimiterThatCannotAnswerIsNotTakenToHaveSaidYes(t *testing.T) {
	rec, reached := serveLimited(&Handlers{Traffic: &fakeLimiter{err: errors.New("backend down")}}, "/agents/triage/invoke")
	if rec.Code != 503 || reached || !strings.Contains(rec.Body.String(), "traffic_control_unavailable") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestAModelCallARateLimitRefusedInsideARunIsA429NotA500(t *testing.T) {
	h := &Handlers{}
	err := fmt.Errorf("model router call failed (turn 1): %w", &models.RateLimitedError{Scope: "kitchen", Limit: "tokens_per_minute", RetryAfter: 20 * time.Second, Reason: "kitchen acme is over its limit"})
	rec := httptest.NewRecorder()
	if !h.respondRunStopped(rec, "triage", nil, err) || rec.Code != 429 || rec.Header().Get("Retry-After") != "20" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	rec = httptest.NewRecorder()
	if h.respondRunStopped(rec, "triage", nil, errors.New("boom")) {
		t.Fatal("anything else is not this function's business")
	}
}

func TestRetryAfterIsAtLeastOneSecond(t *testing.T) {
	for d, want := range map[time.Duration]int{0: 1, time.Millisecond: 1, time.Second: 1, 1001 * time.Millisecond: 2, 59 * time.Second: 59} {
		if got := (&models.RateLimitedError{RetryAfter: d}).RetryAfterSeconds(); got != want {
			t.Errorf("%s: %d, want %d", d, got, want)
		}
	}
}
