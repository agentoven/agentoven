package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/documents"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestDocumentErrorsKeepTheirMeaning(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{&documents.ProviderError{Err: fmt.Errorf("all providers failed: %w", models.ErrSpendRefused)}, http.StatusPaymentRequired},
		{&documents.ProviderError{Err: fmt.Errorf("slow down: %w", models.ErrRateLimited)}, http.StatusTooManyRequests},
		{&documents.ProviderError{Err: errors.New("upstream 500")}, http.StatusBadGateway},
		{fmt.Errorf("%w: legacy", documents.ErrUnsupported), http.StatusUnsupportedMediaType},
		{fmt.Errorf("%w: too big", documents.ErrTooLarge), http.StatusRequestEntityTooLarge},
		{errors.New("documents: not a valid Office file"), http.StatusUnprocessableEntity},
		{context.DeadlineExceeded, http.StatusGatewayTimeout},
		{errTooBusy, http.StatusTooManyRequests},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		respondDocumentError(rec, c.err)
		if rec.Code != c.want {
			t.Errorf("%v: %d, want %d", c.err, rec.Code, c.want)
		}
	}
}

// When every reading slot is taken, a further upload is answered at once with a 429, and the slots
// are given back when the work is done.
func TestExtractRefusesWhenAllReadingSlotsAreTaken(t *testing.T) {
	var held []func()
	for {
		release, ok := takeDocumentSlot()
		if !ok {
			break
		}
		held = append(held, release)
	}
	h := &Handlers{}
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/extract?name=a.txt", bytes.NewReader([]byte("hello")))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		h.ExtractDocument(rec, req.WithContext(pkgmw.SetKitchen(req.Context(), "k")))
		return rec
	}
	rec := post()
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	for _, release := range held {
		release()
	}
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("slots must be released: %d %s", rec.Code, rec.Body.String())
	}
}
