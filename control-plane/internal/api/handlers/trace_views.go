package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Saved views of the trace list live on the server so the dashboard, the CLI and the SDKs share them
// (ADR-0039). A view is a bookmark: it holds a filter, never a trace, and reading one grants nothing
// the list endpoint would not. A private view is its owner's; a shared one is the kitchen's, and a
// Head Chef (or a Restaurateur) may change or remove anyone's shared view.

const (
	maxViewNameLen  = 80
	maxViewFieldLen = 200
	maxViewsPerUser = 200
)

// viewCaller is who is asking: the subject that owns what they save, a name to show, their role.
func viewCaller(r *http.Request) (subject, name, role string) {
	id := middleware.GetIdentity(r.Context())
	if id == nil {
		return "anonymous", "", ""
	}
	name = id.DisplayName
	if name == "" {
		name = id.Email
	}
	return id.Subject, name, id.Role
}

// validTraceViewFilter says what is wrong with a filter, or "".
func validTraceViewFilter(f models.TraceViewFilter) string {
	for name, v := range map[string]string{"agent": f.Agent, "recipe": f.Recipe, "status": f.Status, "model": f.Model, "provider": f.Provider,
		"tag": f.Tag, "session": f.Session, "environment": f.Environment, "agent_version": f.AgentVersion, "q": f.Query} {
		if len(v) > maxViewFieldLen {
			return name + " is too long"
		}
	}
	switch f.Range {
	case "", "1h", "24h", "7d", "30d":
	default:
		return "range must be 1h, 24h, 7d or 30d"
	}
	switch f.Sort {
	case "", "created_at", "cost", "duration", "tokens":
	default:
		return "sort must be created_at, cost, duration or tokens"
	}
	switch f.Order {
	case "", "asc", "desc":
	default:
		return "order must be asc or desc"
	}
	if f.MinCost < 0 || f.MaxCost < 0 || f.MinDuration < 0 || f.MaxDuration < 0 {
		return "cost and duration bounds cannot be negative"
	}
	return ""
}

type traceViewBody struct {
	Name   string                 `json:"name"`
	Filter models.TraceViewFilter `json:"filter"`
	Shared bool                   `json:"shared"`
}

func decodeTraceView(w http.ResponseWriter, r *http.Request) (traceViewBody, bool) {
	var b traceViewBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields() // a filter the list does not understand is refused, not silently dropped
	if err := dec.Decode(&b); err != nil {
		respondError(w, http.StatusBadRequest, "invalid view: "+err.Error())
		return b, false
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > maxViewNameLen {
		respondError(w, http.StatusBadRequest, "a view needs a name of 1 to "+strconv.Itoa(maxViewNameLen)+" characters")
		return b, false
	}
	if msg := validTraceViewFilter(b.Filter); msg != "" {
		respondError(w, http.StatusBadRequest, msg)
		return b, false
	}
	return b, true
}

// ListTraceViews returns the caller's own views and the kitchen's shared ones.
// GET /api/v1/trace-views
func (h *Handlers) ListTraceViews(w http.ResponseWriter, r *http.Request) {
	subject, _, _ := viewCaller(r)
	views, err := h.Store.ListTraceViews(r.Context(), middleware.GetKitchen(r.Context()), subject)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, views)
}

// CreateTraceView saves a view.
// POST /api/v1/trace-views  {"name": "...", "filter": {...}, "shared": false}
func (h *Handlers) CreateTraceView(w http.ResponseWriter, r *http.Request) {
	b, ok := decodeTraceView(w, r)
	if !ok {
		return
	}
	kitchen := middleware.GetKitchen(r.Context())
	subject, name, _ := viewCaller(r)
	if mine, err := h.Store.ListTraceViews(r.Context(), kitchen, subject); err == nil {
		n := 0
		for _, v := range mine {
			if v.Owner == subject {
				n++
			}
		}
		if n >= maxViewsPerUser {
			respondError(w, http.StatusUnprocessableEntity, "too many saved views: delete one first")
			return
		}
	}
	now := time.Now().UTC()
	v := &models.TraceView{ID: uuid.NewString(), Kitchen: kitchen, Owner: subject, OwnerName: name, Name: b.Name, Filter: b.Filter, Shared: b.Shared, CreatedAt: now, UpdatedAt: now}
	if err := h.Store.CreateTraceView(r.Context(), v); err != nil {
		var c *store.ErrConflict
		if errors.As(err, &c) {
			respondError(w, http.StatusConflict, "you already have a view named '"+b.Name+"'")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, v)
}

// ownView loads a view the caller may change: their own, or a shared one when they head the kitchen. A
// private view of someone else is not found.
func (h *Handlers) ownView(w http.ResponseWriter, r *http.Request) (*models.TraceView, bool) {
	kitchen := middleware.GetKitchen(r.Context())
	subject, _, role := viewCaller(r)
	v, err := h.Store.GetTraceView(r.Context(), kitchen, chi.URLParam(r, "viewId"))
	if err != nil || (v.Owner != subject && !v.Shared) {
		respondError(w, http.StatusNotFound, "view not found")
		return nil, false
	}
	if v.Owner != subject && !contracts.IsKitchenOwner(role) {
		respondError(w, http.StatusForbidden, "only the owner of a view, or a Head Chef, can change it")
		return nil, false
	}
	return v, true
}

// UpdateTraceView renames a view, changes its filter or shares or unshares it.
// PUT /api/v1/trace-views/{viewId}
func (h *Handlers) UpdateTraceView(w http.ResponseWriter, r *http.Request) {
	v, ok := h.ownView(w, r)
	if !ok {
		return
	}
	b, ok := decodeTraceView(w, r)
	if !ok {
		return
	}
	v.Name, v.Filter, v.Shared = b.Name, b.Filter, b.Shared
	if err := h.Store.UpdateTraceView(r.Context(), v); err != nil {
		var c *store.ErrConflict
		if errors.As(err, &c) {
			respondError(w, http.StatusConflict, "you already have a view named '"+b.Name+"'")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	got, _ := h.Store.GetTraceView(r.Context(), v.Kitchen, v.ID)
	respondJSON(w, http.StatusOK, got)
}

// DeleteTraceView removes a view.
// DELETE /api/v1/trace-views/{viewId}
func (h *Handlers) DeleteTraceView(w http.ResponseWriter, r *http.Request) {
	v, ok := h.ownView(w, r)
	if !ok {
		return
	}
	if err := h.Store.DeleteTraceView(r.Context(), v.Kitchen, v.ID); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// expandTraceView returns q with a saved view's filters filled in for every parameter q does not name,
// so `?view=<id>` works on the trace list, its stats and its sessions alike, and a parameter given with
// it narrows the view further or overrides it. A view the caller cannot see is ignored, as if absent.
func (h *Handlers) expandTraceView(r *http.Request, kitchen string, q url.Values) url.Values {
	id := q.Get("view")
	if id == "" {
		return q
	}
	subject, _, _ := viewCaller(r)
	v, err := h.Store.GetTraceView(r.Context(), kitchen, id)
	if err != nil || (v.Owner != subject && !v.Shared) {
		return q
	}
	out := url.Values{}
	for k, vals := range q {
		out[k] = vals
	}
	set := func(k, val string) {
		if val != "" && out.Get(k) == "" {
			out.Set(k, val)
		}
	}
	f := v.Filter
	for k, val := range map[string]string{"agent": f.Agent, "recipe": f.Recipe, "status": f.Status, "model": f.Model, "provider": f.Provider,
		"tag": f.Tag, "session": f.Session, "environment": f.Environment, "agent_version": f.AgentVersion, "q": f.Query,
		"range": f.Range, "sort": f.Sort, "order": f.Order} {
		set(k, val)
	}
	for k, val := range map[string]float64{"min_cost": f.MinCost, "max_cost": f.MaxCost} {
		if val > 0 {
			set(k, strconv.FormatFloat(val, 'f', -1, 64))
		}
	}
	for k, val := range map[string]int64{"min_duration": f.MinDuration, "max_duration": f.MaxDuration} {
		if val > 0 {
			set(k, strconv.FormatInt(val, 10))
		}
	}
	if f.RootsOnly {
		set("roots", "true")
	}
	if f.IncludeArchived {
		set("include_archived", "true")
	}
	return out
}
