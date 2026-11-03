package handlers

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/internal/tracetimeline"
	"github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// maxTraceFamily bounds how many traces of one recipe run are walked to gather its events.
const maxTraceFamily = 500

// traceFamily is a trace and every trace under it (a recipe run's steps and sub-runs).
func (h *Handlers) traceFamily(r *http.Request, kitchen string, root *models.Trace) []string {
	ids := []string{root.ID}
	seen := map[string]bool{root.ID: true}
	for i := 0; i < len(ids) && len(ids) < maxTraceFamily; i++ {
		kids, err := h.Store.ListTracesFiltered(r.Context(), kitchen, store.TraceFilter{ParentTraceID: ids[i], Limit: 500})
		if err != nil {
			break
		}
		for _, k := range kids {
			if !seen[k.ID] && len(ids) < maxTraceFamily {
				seen[k.ID] = true
				ids = append(ids, k.ID)
			}
		}
	}
	return ids
}

// traceAuditEvents are the audit events of a trace and the traces under it, oldest first: in chain
// order where the deployment chains its audit log (sequence numbers), else in time order.
func (h *Handlers) traceAuditEvents(r *http.Request, kitchen string, root *models.Trace) ([]models.AuditEvent, error) {
	evs, err := h.Store.ListAuditEvents(r.Context(), models.AuditFilter{Kitchen: kitchen, TraceIDs: h.traceFamily(r, kitchen, root), Limit: 5000})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].Seq != 0 && evs[j].Seq != 0 && evs[i].Seq != evs[j].Seq {
			return evs[i].Seq < evs[j].Seq
		}
		if !evs[i].Timestamp.Equal(evs[j].Timestamp) {
			return evs[i].Timestamp.Before(evs[j].Timestamp)
		}
		return evs[i].ID < evs[j].ID
	})
	return evs, nil
}

func auditActor(e models.AuditEvent) string {
	switch {
	case e.UserEmail != "":
		return e.UserEmail
	case e.ActorDisplayName != "":
		return e.ActorDisplayName
	}
	return e.PrincipalID
}

// TraceTimeline returns the trace laid out on a clock, computed here so that every client shows the
// same thing (ADR-0039 section 1; the rules are in package tracetimeline).
// GET /api/v1/traces/{traceId}/timeline?limit=&cursor=
func (h *Handlers) TraceTimeline(w http.ResponseWriter, r *http.Request) {
	trace, ok := h.ownTrace(w, r)
	if !ok {
		return
	}
	kitchen := middleware.GetKitchen(r.Context())
	spans, err := h.Store.ListSpansByTrace(r.Context(), trace.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The governance lane: what the audit log recorded about this trace. Always present, empty when
	// nothing was recorded.
	lane := tracetimeline.Lane{Name: "governance", Events: []tracetimeline.LaneEvent{}}
	evs, err := h.traceAuditEvents(r, kitchen, trace)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, e := range evs {
		lane.Events = append(lane.Events, tracetimeline.LaneEvent{
			At: e.Timestamp, Action: e.Action, Resource: e.Resource, Actor: auditActor(e),
			Status: e.ResponseStatus, Seq: e.Seq, EntryHash: e.EntryHash,
		})
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	respondJSON(w, http.StatusOK, tracetimeline.Build(trace, spans, []tracetimeline.Lane{lane}, tracetimeline.Options{Limit: limit, Cursor: r.URL.Query().Get("cursor"), WithholdErrors: !h.canReadTraceContent(r)}))
}

// TraceAudit returns the audit events of a trace (and, for a recipe run, of every trace under it) in
// order, with each event's sequence number and hash where the audit log is chained, so a reader can
// check the stretch against the chain. Events written before audit events carried a trace ID cannot
// be found this way, and the response says from when they can.
// GET /api/v1/traces/{traceId}/audit
func (h *Handlers) TraceAudit(w http.ResponseWriter, r *http.Request) {
	trace, ok := h.ownTrace(w, r)
	if !ok {
		return
	}
	kitchen := middleware.GetKitchen(r.Context())
	evs, err := h.traceAuditEvents(r, kitchen, trace)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type coverage struct {
		From     *time.Time `json:"from"`
		Complete bool       `json:"complete"`
		Note     string     `json:"note"`
	}
	cov := coverage{Note: "no audit event in this kitchen carries a trace id yet, so nothing can be said about this trace's events"}
	first, err := h.Store.ListAuditEvents(r.Context(), models.AuditFilter{Kitchen: kitchen, HasTraceID: true, Limit: 1, Ascending: true})
	if err == nil && len(first) > 0 {
		from := first[0].Timestamp
		cov.From = &from
		cov.Complete = !trace.CreatedAt.Before(from)
		cov.Note = "events are found by trace id from " + from.UTC().Format("2006-01-02") + "; events written before that are not"
		if cov.Complete {
			cov.Note = "this trace began after audit events started carrying a trace id"
		}
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"trace_id": trace.ID, "events": evs, "count": len(evs), "coverage": cov,
	})
}
