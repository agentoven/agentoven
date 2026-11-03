package langfuse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
)

// Review 2, finding 13: ingest and export took the kitchen from the raw X-Kitchen header. The kitchen is the
// authenticated request's (the one the role check was made in), so a header that disagrees changes nothing.
func TestIngestAndExportUseTheAuthenticatedKitchenNotTheRawHeader(t *testing.T) {
	st := store.NewMemoryStore()
	defer st.Close()
	b := NewBridge(st, "", "", "")
	ctx := pkgmw.SetKitchen(context.Background(), "ka")

	body := `{"batch":[{"id":"e1","type":"trace-create","timestamp":"2026-01-01T00:00:00Z","body":{"id":"t-imported","name":"bot","timestamp":"2026-01-01T00:00:00Z"}}]}`
	req := httptest.NewRequest("POST", "/langfuse/ingest", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("X-Kitchen", "kb")
	rec := httptest.NewRecorder()
	b.HandleIngest(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest: %d %s", rec.Code, rec.Body)
	}
	if tr, err := st.GetTrace(context.Background(), "t-imported"); err != nil || tr.Kitchen != "ka" {
		t.Fatalf("the trace landed in %+v (%v), want kitchen ka", tr, err)
	}

	// Export lists the authenticated kitchen's traces only.
	if got, _ := st.ListTraces(context.Background(), "kb", 10); len(got) != 0 {
		t.Fatalf("kb has traces: %+v", got)
	}
}

func TestIngestRefusesAnOversizedBatch(t *testing.T) {
	st := store.NewMemoryStore()
	defer st.Close()
	b := NewBridge(st, "", "", "")
	var sb strings.Builder
	sb.WriteString(`{"batch":[`)
	for i := 0; i < maxIngestEvents+1; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"id":"e","type":"other","body":{}}`)
	}
	sb.WriteString(`]}`)
	rec := httptest.NewRecorder()
	b.HandleIngest(rec, httptest.NewRequest("POST", "/langfuse/ingest", strings.NewReader(sb.String())))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	b.HandleIngest(rec, httptest.NewRequest("POST", "/langfuse/ingest", strings.NewReader(`{"batch":"`+strings.Repeat("x", maxIngestBytes+10)+`"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body: %d", rec.Code)
	}
}
