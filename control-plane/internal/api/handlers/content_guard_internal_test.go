package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A handler that hands respondJSON a trace, a span, a score, a session or a message, whole or in a list or
// a map, gets a 500 and no content out: the only way to send one is through traceOut and its siblings.
func TestRespondJSONRefusesContentThatWasNotCheckedForAccess(t *testing.T) {
	tr := models.Trace{ID: "t", InputText: "SECRET"}
	for name, v := range map[string]interface{}{
		"trace": tr, "pointer": &tr, "list": []models.Trace{tr}, "pointer list": []*models.Trace{&tr},
		"span": models.Span{ID: "s"}, "score": models.TraceScore{Comment: "SECRET"},
		"session":  models.Session{Messages: []models.ChatMessage{{Content: "SECRET"}}},
		"in a map": map[string]interface{}{"traces": []models.Trace{tr}}, "in a list of any": []interface{}{tr},
		"message": models.ChatMessage{Content: "SECRET"},
	} {
		rec := httptest.NewRecorder()
		respondJSON(rec, http.StatusOK, v)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "SECRET") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// Checked content goes out, and so does everything that is not content.
	rec := httptest.NewRecorder()
	respondJSON(rec, http.StatusOK, readable{tr})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "SECRET") {
		t.Errorf("a checked trace is sent: %d %s", rec.Code, rec.Body)
	}
	for name, v := range map[string]interface{}{"public": tr.Public(), "map": map[string]string{"a": "b"}, "nil": nil, "list": []string{"a"}, "public list": models.PublicTraces([]models.Trace{tr})} {
		rec := httptest.NewRecorder()
		respondJSON(rec, http.StatusOK, v)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "SECRET") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
