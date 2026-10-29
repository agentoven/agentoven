package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func viewReq(method, url, body, kitchen string, who *contracts.Identity, viewID string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, url, nil)
	} else {
		r = httptest.NewRequest(method, url, strings.NewReader(body))
	}
	r = withKitchen(r, kitchen)
	if who != nil {
		r = r.WithContext(pkgmw.SetIdentity(r.Context(), who))
	}
	if viewID != "" {
		r = withChiParam(r, "viewId", viewID)
	}
	return r
}

func do(h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, r)
	return rec
}

var (
	ana  = &contracts.Identity{Subject: "u-ana", Email: "ana@x.test", Role: "chef", Kind: "user"}
	bo   = &contracts.Identity{Subject: "u-bo", Email: "bo@x.test", Role: "chef", Kind: "user"}
	head = &contracts.Identity{Subject: "u-head", Email: "head@x.test", Role: "head_chef", Kind: "user"}
)

func saveView(t *testing.T, h *handlers.Handlers, who *contracts.Identity, body string) models.TraceView {
	t.Helper()
	rec := do(h.CreateTraceView, viewReq("POST", "/api/v1/trace-views", body, "default", who, ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var v models.TraceView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return v
}

func names(t *testing.T, h *handlers.Handlers, who *contracts.Identity, kitchen string) []string {
	t.Helper()
	rec := do(h.ListTraceViews, viewReq("GET", "/api/v1/trace-views", "", kitchen, who, ""))
	var vs []models.TraceView
	_ = json.Unmarshal(rec.Body.Bytes(), &vs)
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Name)
	}
	return out
}

func TestSavedViewsArePrivateUntilSharedAndStayInTheirKitchen(t *testing.T) {
	h, _ := newTestHandlers(t)
	saveView(t, h, ana, `{"name":"my failures","filter":{"status":"failed","range":"24h"}}`)
	saveView(t, h, ana, `{"name":"team prod","filter":{"environment":"prod"},"shared":true}`)

	if got := names(t, h, ana, "default"); len(got) != 2 {
		t.Errorf("the owner sees both: %v", got)
	}
	if got := names(t, h, bo, "default"); len(got) != 1 || got[0] != "team prod" {
		t.Errorf("someone else sees only the shared view: %v", got)
	}
	if got := names(t, h, ana, "other-kitchen"); len(got) != 0 {
		t.Errorf("views belong to a kitchen: %v", got)
	}
}

func TestSavedViewsAreValidated(t *testing.T) {
	h, _ := newTestHandlers(t)
	for name, body := range map[string]string{
		"no name":            `{"filter":{}}`,
		"unknown filter key": `{"name":"x","filter":{"colour":"red"}}`,
		"bad range":          `{"name":"x","filter":{"range":"3y"}}`,
		"bad sort":           `{"name":"x","filter":{"sort":"mood"}}`,
		"negative bound":     `{"name":"x","filter":{"min_cost":-1}}`,
		"name too long":      `{"name":"` + strings.Repeat("a", 81) + `","filter":{}}`,
	} {
		if rec := do(h.CreateTraceView, viewReq("POST", "/x", body, "default", ana, "")); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	saveView(t, h, ana, `{"name":"dup","filter":{}}`)
	if rec := do(h.CreateTraceView, viewReq("POST", "/x", `{"name":"DUP","filter":{}}`, "default", ana, "")); rec.Code != http.StatusConflict {
		t.Errorf("a second view of the same name (any case): %d, want 409", rec.Code)
	}
	// Two people may each have a view of one name.
	saveView(t, h, bo, `{"name":"dup","filter":{}}`)
}

func TestOnlyTheOwnerOrAHeadChefChangesAView(t *testing.T) {
	h, _ := newTestHandlers(t)
	private := saveView(t, h, ana, `{"name":"mine","filter":{}}`)
	shared := saveView(t, h, ana, `{"name":"ours","filter":{},"shared":true}`)

	put := func(who *contracts.Identity, id string) int {
		return do(h.UpdateTraceView, viewReq("PUT", "/x", `{"name":"renamed `+id[:4]+`","filter":{"status":"failed"},"shared":true}`, "default", who, id)).Code
	}
	del := func(who *contracts.Identity, id string) int {
		return do(h.DeleteTraceView, viewReq("DELETE", "/x", "", "default", who, id)).Code
	}
	if c := put(bo, private.ID); c != http.StatusNotFound {
		t.Errorf("another person's private view is not found: %d", c)
	}
	if c := put(head, private.ID); c != http.StatusNotFound {
		t.Errorf("not even a Head Chef sees a private view: %d", c)
	}
	if c := put(bo, shared.ID); c != http.StatusForbidden {
		t.Errorf("a Chef cannot change someone else's shared view: %d", c)
	}
	if c := del(bo, shared.ID); c != http.StatusForbidden {
		t.Errorf("or delete it: %d", c)
	}
	if c := put(ana, private.ID); c != http.StatusOK {
		t.Errorf("the owner can: %d", c)
	}
	if c := del(head, shared.ID); c != http.StatusNoContent {
		t.Errorf("a Head Chef can delete a shared view: %d", c)
	}
	if c := del(ana, private.ID); c != http.StatusNoContent {
		t.Errorf("the owner can delete: %d", c)
	}
	if got := names(t, h, ana, "default"); len(got) != 0 {
		t.Errorf("left: %v", got)
	}
}

func TestAViewFiltersTheListAndAnExplicitParameterWins(t *testing.T) {
	h, s := newTestHandlers(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, tr := range []models.Trace{
		{ID: "a", AgentName: "support", Kitchen: "default", Status: "failed", Environment: "prod", CreatedAt: now},
		{ID: "b", AgentName: "support", Kitchen: "default", Status: "completed", Environment: "prod", CreatedAt: now},
		{ID: "c", AgentName: "triage", Kitchen: "default", Status: "failed", Environment: "staging", CreatedAt: now},
	} {
		tr := tr
		if err := s.CreateTrace(ctx, &tr); err != nil {
			t.Fatal(err)
		}
	}
	v := saveView(t, h, ana, `{"name":"prod failures","filter":{"status":"failed","environment":"prod"}}`)
	list := func(q string, who *contracts.Identity) string {
		r := viewReq("GET", "/api/v1/traces"+q, "", "default", who, "")
		rec := do(h.ListTraces, r)
		var out []models.Trace
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return ids(out)
	}
	if got := list("?view="+v.ID, ana); got != "a" {
		t.Errorf("the view's filters apply: %q", got)
	}
	if got := list("?view="+v.ID+"&status=completed", ana); got != "b" {
		t.Errorf("a named parameter overrides the view: %q", got)
	}
	// Someone who cannot see the view gets the unfiltered list, as if no view had been named.
	if got := list("?view="+v.ID, bo); !strings.Contains(got, "c") || !strings.Contains(got, "a") {
		t.Errorf("a view the caller cannot see is ignored: %q", got)
	}
}

var _ store.Store
