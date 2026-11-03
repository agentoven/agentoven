package handlers

import (
	"net/http"
	"reflect"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// The types whose values hold what users typed and what models and tools answered. A handler may not
// hand one to respondJSON as it is: a reader who is not allowed to see content (canReadTraceContent)
// gets the matching Public type (models.PublicTrace and the others), which lists the fields it carries,
// and a reader who is allowed gets the value wrapped by readable. Forgetting the check then is not a
// leak, it is a 500 the first time the route is called, and the route-walking test in package api
// calls every route that returns one.
var contentTypes = map[reflect.Type]bool{
	reflect.TypeOf(models.Trace{}):       true,
	reflect.TypeOf(models.Span{}):        true,
	reflect.TypeOf(models.TraceScore{}):  true,
	reflect.TypeOf(models.Session{}):     true,
	reflect.TypeOf(models.ChatMessage{}): true,
}

// readable marks a response that carries content and whose caller was checked for the right to read
// it. It exists only to be passed to respondJSON, which unwraps it.
type readable struct{ v interface{} }

// holdsRawContent reports whether v is, or is a list or map of, a content type.
func holdsRawContent(v interface{}, depth int) bool {
	if v == nil || depth > 3 {
		return false
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return contentTypes[t]
	case reflect.Slice, reflect.Array:
		e := t.Elem()
		for e.Kind() == reflect.Ptr {
			e = e.Elem()
		}
		if e.Kind() == reflect.Interface {
			rv := reflect.ValueOf(v)
			for i := 0; i < rv.Len() && i < 8; i++ {
				if holdsRawContent(rv.Index(i).Interface(), depth+1) {
					return true
				}
			}
			return false
		}
		return contentTypes[e]
	case reflect.Map:
		rv := reflect.ValueOf(v)
		it := rv.MapRange()
		for it.Next() {
			if e := it.Value(); e.Kind() == reflect.Interface || e.Kind() == reflect.Ptr || e.Kind() == reflect.Slice || e.Kind() == reflect.Struct {
				if holdsRawContent(e.Interface(), depth+1) {
					return true
				}
			}
		}
	}
	return false
}

// checkedBody is respondJSON's gate: it unwraps readable and refuses a bare content type.
func checkedBody(data interface{}) (interface{}, bool) {
	if r, ok := data.(readable); ok {
		return r.v, true
	}
	if holdsRawContent(data, 0) {
		log.Error().Str("type", reflect.TypeOf(data).String()).Msg("a handler tried to send trace content without checking the caller's right to read it; refused")
		return nil, false
	}
	return data, true
}

// traceOut is a trace as the caller may see it.
func (h *Handlers) traceOut(r *http.Request, t *models.Trace) interface{} {
	if h.canReadTraceContent(r) {
		return readable{t}
	}
	return t.Public()
}

func (h *Handlers) tracesOut(r *http.Request, ts []models.Trace) interface{} {
	if h.canReadTraceContent(r) {
		return readable{ts}
	}
	return models.PublicTraces(ts)
}

func (h *Handlers) spanOut(r *http.Request, s *models.Span) interface{} {
	if h.canReadTraceContent(r) {
		return readable{s}
	}
	return s.Public()
}

func (h *Handlers) spansOut(r *http.Request, ss []models.Span) interface{} {
	if h.canReadTraceContent(r) {
		return readable{ss}
	}
	return models.PublicSpans(ss)
}

func (h *Handlers) scoreOut(r *http.Request, s *models.TraceScore) interface{} {
	if h.canReadTraceContent(r) {
		return readable{s}
	}
	return s.Public()
}

func (h *Handlers) scoresOut(r *http.Request, ss []models.TraceScore) interface{} {
	if h.canReadTraceContent(r) {
		return readable{ss}
	}
	return models.PublicScores(ss)
}

func (h *Handlers) sessionOut(r *http.Request, s *models.Session) interface{} {
	if h.canReadTraceContent(r) {
		return readable{s}
	}
	return s.Public()
}

func (h *Handlers) sessionsOut(r *http.Request, ss []models.Session) interface{} {
	if h.canReadTraceContent(r) {
		return readable{ss}
	}
	return models.PublicSessions(ss)
}
