package router

import (
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A provider that answers with a redirect must not be able to take the credential to another host.
func TestProviderClientRefusesCrossHostRedirect(t *testing.T) {
	var hits atomic.Int32
	var gotKey atomic.Value
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotKey.Store(r.Header.Get("x-api-key"))
	}))
	defer attacker.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, attacker.URL+"/steal", http.StatusTemporaryRedirect)
		case "/within":
			http.Redirect(w, r, "/ok", http.StatusTemporaryRedirect)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer provider.Close()

	mr := NewModelRouter(nil)
	for name, client := range map[string]*http.Client{
		"default client":    mr.client,
		"custom-TLS client": mr.clientFor(&models.ModelProvider{Name: "p", CABundle: "x"}),
	} {
		req, _ := http.NewRequest("POST", provider.URL+"/away", nil)
		req.Header.Set("x-api-key", "sk-secret")
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			t.Errorf("%s: followed a redirect to another host", name)
		}
		req, _ = http.NewRequest("POST", provider.URL+"/within", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: a redirect within the host was refused: %v", name, err)
		}
		resp.Body.Close()
	}
	if hits.Load() != 0 {
		t.Fatalf("the other host was reached %d times with key %v", hits.Load(), gotKey.Load())
	}
}
