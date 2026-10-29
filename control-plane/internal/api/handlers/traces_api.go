package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/middleware"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// traceFilterFrom reads the trace list's query parameters:
//
//	agent, recipe, status, model, provider, tag, session, parent   exact matches
//	roots=true                       only traces with no parent
//	q                                text in the input, the output, the agent name or the ID
//	environment, agent_version       exact match (ADR-0039)
//	min_cost, max_cost               dollars
//	min_duration, max_duration       milliseconds
//	range=1h|24h|7d|30d              or since / until as RFC 3339 times
//	sort=created_at|cost|duration|tokens, order=asc|desc
//	limit (default 500, at most 5000), offset
//	include_archived=true            also show traces of archived recipes (hidden by default)
func (h *Handlers) traceFilterFrom(r *http.Request, kitchen string) store.TraceFilter {
	q := r.URL.Query()
	f := store.TraceFilter{
		AgentName: q.Get("agent"), RecipeName: q.Get("recipe"), Status: q.Get("status"),
		Model: q.Get("model"), Provider: q.Get("provider"), Tag: q.Get("tag"),
		SessionID: q.Get("session"), ParentTraceID: q.Get("parent"),
		Environment: q.Get("environment"), AgentVersion: q.Get("agent_version"),
		RootsOnly: q.Get("roots") == "true", Search: strings.TrimSpace(q.Get("q")),
		SortBy: q.Get("sort"), Asc: strings.EqualFold(q.Get("order"), "asc"),
		Limit: 500,
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 5000 {
		f.Limit = n
	}
	if n, err := strconv.Atoi(q.Get("offset")); err == nil && n > 0 {
		f.Offset = n
	}
	f.MinCostUSD, _ = strconv.ParseFloat(q.Get("min_cost"), 64)
	f.MaxCostUSD, _ = strconv.ParseFloat(q.Get("max_cost"), 64)
	f.MinDurationMs, _ = strconv.ParseInt(q.Get("min_duration"), 10, 64)
	f.MaxDurationMs, _ = strconv.ParseInt(q.Get("max_duration"), 10, 64)
	if d, ok := map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}[q.Get("range")]; ok {
		f.Since = time.Now().Add(-d)
	}
	if t, err := time.Parse(time.RFC3339, q.Get("since")); err == nil {
		f.Since = t
	}
	if t, err := time.Parse(time.RFC3339, q.Get("until")); err == nil {
		f.Until = t
	}
	// The runs of an archived recipe stay in the store, so they are auditable, but are not shown unless asked for.
	if !strings.EqualFold(q.Get("include_archived"), "true") {
		if recipes, err := h.Store.ListRecipes(r.Context(), kitchen); err == nil {
			for _, rec := range recipes {
				if rec.IsArchived() {
					f.ExcludeRecipes = append(f.ExcludeRecipes, rec.Name)
				}
			}
		}
	}
	return f
}

// ListTraces lists traces, filtered, sorted and paged (see traceFilterFrom). The total the filter
// matches is in the X-Total-Count header.
// GET /api/v1/traces
func (h *Handlers) ListTraces(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	filter := h.traceFilterFrom(r, kitchen)

	traces, err := h.Store.ListTracesFiltered(r.Context(), kitchen, filter)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if traces == nil {
		traces = []models.Trace{}
	}
	if st, err := h.Store.TraceStats(r.Context(), kitchen, filter); err == nil {
		w.Header().Set("X-Total-Count", strconv.Itoa(st.Count))
		w.Header().Set("Access-Control-Expose-Headers", "X-Total-Count")
	}
	respondJSON(w, http.StatusOK, traces)
}

// TraceStats summarises the traces a filter matches: how many, how many failed, tokens, cost, latency.
// GET /api/v1/traces/stats
func (h *Handlers) TraceStats(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	st, err := h.Store.TraceStats(r.Context(), kitchen, h.traceFilterFrom(r, kitchen))
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, st)
}

// TraceSession is a conversation: the traces that share a session ID.
type TraceSession struct {
	SessionID   string    `json:"session_id"`
	Traces      int       `json:"traces"`
	Agents      []string  `json:"agents"`
	TotalTokens int64     `json:"total_tokens"`
	TotalCost   float64   `json:"total_cost_usd"`
	FirstAt     time.Time `json:"first_at"`
	LastAt      time.Time `json:"last_at"`
	Errors      int       `json:"errors"`
}

// ListTraceSessions groups traces by session, newest first.
// GET /api/v1/traces/sessions
func (h *Handlers) ListTraceSessions(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	f := h.traceFilterFrom(r, kitchen)
	f.Limit, f.Offset, f.SortBy, f.Asc = 5000, 0, "", false
	traces, err := h.Store.ListTracesFiltered(r.Context(), kitchen, f)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	by := map[string]*TraceSession{}
	agents := map[string]map[string]bool{}
	for _, t := range traces {
		if t.SessionID == "" {
			continue
		}
		se := by[t.SessionID]
		if se == nil {
			se = &TraceSession{SessionID: t.SessionID, FirstAt: t.CreatedAt, LastAt: t.CreatedAt}
			by[t.SessionID] = se
			agents[t.SessionID] = map[string]bool{}
		}
		se.Traces++
		se.TotalTokens += t.TotalTokens
		se.TotalCost += t.CostUSD
		if t.Status == "failed" || t.Status == "error" || t.Status == "blocked" {
			se.Errors++
		}
		if t.CreatedAt.Before(se.FirstAt) {
			se.FirstAt = t.CreatedAt
		}
		if t.CreatedAt.After(se.LastAt) {
			se.LastAt = t.CreatedAt
		}
		agents[t.SessionID][t.AgentName] = true
	}
	out := make([]TraceSession, 0, len(by))
	for id, se := range by {
		for a := range agents[id] {
			se.Agents = append(se.Agents, a)
		}
		sort.Strings(se.Agents)
		out = append(out, *se)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	respondJSON(w, http.StatusOK, out)
}

// ownTrace loads a trace and checks it belongs to the caller's kitchen.
func (h *Handlers) ownTrace(w http.ResponseWriter, r *http.Request) (*models.Trace, bool) {
	kitchen := middleware.GetKitchen(r.Context())
	trace, err := h.Store.GetTrace(r.Context(), chi.URLParam(r, "traceId"))
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return nil, false
	}
	if trace.Kitchen != "" && trace.Kitchen != kitchen {
		respondError(w, http.StatusNotFound, "trace not found")
		return nil, false
	}
	return trace, true
}

// UpdateTrace changes what may be changed about a recorded trace: its tags. What a trace says about
// what happened is a record and is not editable.
// PATCH /api/v1/traces/{traceId}  body: {"tags": ["reviewed", "bug"]}
func (h *Handlers) UpdateTrace(w http.ResponseWriter, r *http.Request) {
	trace, ok := h.ownTrace(w, r)
	if !ok {
		return
	}
	var req struct {
		Tags *[]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Tags == nil {
		respondError(w, http.StatusBadRequest, `body must be {"tags": [...]}`)
		return
	}
	seen := map[string]bool{}
	tags := []string{}
	for _, t := range *req.Tags {
		if t = strings.TrimSpace(t); t != "" && len(t) <= 64 && !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	if len(tags) > 20 {
		respondError(w, http.StatusBadRequest, "a trace can carry at most 20 tags")
		return
	}
	trace.Tags = tags
	if err := h.Store.UpdateTrace(r.Context(), trace); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, trace)
}

// AddTraceScore records feedback on a trace (or one of its spans).
// POST /api/v1/traces/{traceId}/scores  body: {"name": "quality", "value": 1, "label": "good", "comment": "...", "span_id": "..."}
func (h *Handlers) AddTraceScore(w http.ResponseWriter, r *http.Request) {
	trace, ok := h.ownTrace(w, r)
	if !ok {
		return
	}
	var req models.TraceScore
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = "quality"
	}
	if len(req.Name) > 64 || len(req.Comment) > 4000 {
		respondError(w, http.StatusBadRequest, "name is at most 64 characters and comment at most 4000")
		return
	}
	req.ID = uuid.New().String()
	req.TraceID = trace.ID
	req.CreatedAt = time.Now().UTC()
	if req.Source != "eval" && req.Source != "api" {
		req.Source = "user"
	}
	if id := pkgmw.GetIdentity(r.Context()); id != nil {
		req.CreatedBy = id.Subject
	}
	if err := h.Store.CreateTraceScore(r.Context(), &req); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, req)
}

// ListTraceScores returns a trace's feedback.
// GET /api/v1/traces/{traceId}/scores
func (h *Handlers) ListTraceScores(w http.ResponseWriter, r *http.Request) {
	trace, ok := h.ownTrace(w, r)
	if !ok {
		return
	}
	scores, err := h.Store.ListTraceScores(r.Context(), trace.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if scores == nil {
		scores = []models.TraceScore{}
	}
	respondJSON(w, http.StatusOK, scores)
}

// DeleteTraceScore removes one piece of feedback.
// DELETE /api/v1/traces/{traceId}/scores/{scoreId}
func (h *Handlers) DeleteTraceScore(w http.ResponseWriter, r *http.Request) {
	trace, ok := h.ownTrace(w, r)
	if !ok {
		return
	}
	if err := h.Store.DeleteTraceScore(r.Context(), trace.ID, chi.URLParam(r, "scoreId")); err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
