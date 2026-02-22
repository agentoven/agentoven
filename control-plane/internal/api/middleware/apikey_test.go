package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
)

func TestAPIKeyAuth_Disabled(t *testing.T) {
	os.Unsetenv("AGENTOVEN_API_KEYS")

	auth := middleware.NewAPIKeyAuth()
	if auth.Enabled() {
		t.Error("Expected auth to be disabled when AGENTOVEN_API_KEYS is not set")
	}

	// When disabled, all requests should pass through
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Disabled auth: status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAPIKeyAuth_ValidKey(t *testing.T) {
	os.Setenv("AGENTOVEN_API_KEYS", "test-key-1,test-key-2")
	defer os.Unsetenv("AGENTOVEN_API_KEYS")

	auth := middleware.NewAPIKeyAuth()
	if !auth.Enabled() {
		t.Fatal("Expected auth to be enabled")
	}

	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test with Bearer token
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer test-key-1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Valid Bearer key: status = %d, want %d", w.Code, http.StatusOK)
	}

	// Test with X-API-Key header
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req2.Header.Set("X-API-Key", "test-key-2")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("Valid X-API-Key: status = %d, want %d", w2.Code, http.StatusOK)
	}
}

func TestAPIKeyAuth_InvalidKey(t *testing.T) {
	os.Setenv("AGENTOVEN_API_KEYS", "valid-key")
	defer os.Unsetenv("AGENTOVEN_API_KEYS")

	auth := middleware.NewAPIKeyAuth()
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Invalid key: status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAPIKeyAuth_MissingKey(t *testing.T) {
	os.Setenv("AGENTOVEN_API_KEYS", "valid-key")
	defer os.Unsetenv("AGENTOVEN_API_KEYS")

	auth := middleware.NewAPIKeyAuth()
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Missing key: status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAPIKeyAuth_PublicPaths(t *testing.T) {
	os.Setenv("AGENTOVEN_API_KEYS", "valid-key")
	defer os.Unsetenv("AGENTOVEN_API_KEYS")

	auth := middleware.NewAPIKeyAuth()
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	publicPaths := []string{"/health", "/version", "/.well-known/agent.json", "/a2a"}
	for _, path := range publicPaths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Public path %q: status = %d, want %d", path, w.Code, http.StatusOK)
		}
	}
}

func TestAPIKeyAuth_AddRemoveKey(t *testing.T) {
	os.Unsetenv("AGENTOVEN_API_KEYS")

	auth := middleware.NewAPIKeyAuth()
	if auth.Enabled() {
		t.Fatal("Should start disabled")
	}

	// Add a key at runtime
	auth.AddKey("runtime-key")
	if !auth.Enabled() {
		t.Error("Should be enabled after AddKey")
	}

	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req.Header.Set("X-API-Key", "runtime-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Runtime key: status = %d, want %d", w.Code, http.StatusOK)
	}

	// Remove the key
	auth.RemoveKey("runtime-key")
	if auth.Enabled() {
		t.Error("Should be disabled after removing last key")
	}
}
