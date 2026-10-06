package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/go-chi/chi/v5"
)

func modalityHandlers(t *testing.T) (*handlers.Handlers, store.Store) {
	t.Helper()
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	return handlers.New(s, router.NewModelRouter(s), nil, nil, nil, sessions.NewMemorySessionStore(), nil), s
}

func createProvider(h *handlers.Handlers, body string) *httptest.ResponseRecorder {
	req := withKitchen(httptest.NewRequest(http.MethodPost, "/providers", strings.NewReader(body)), "default")
	rec := httptest.NewRecorder()
	h.CreateProvider(rec, req)
	return rec
}

func TestProviderCreationValidatesModalities(t *testing.T) {
	h, _ := modalityHandlers(t)

	for name, tc := range map[string]struct{ body, want string }{
		"typo":                   {`{"name":"p","kind":"openai","config":{"modalities":{"audoi":{}}}}`, "unknown modality"},
		"bad setting":            {`{"name":"p","kind":"openai","config":{"modalities":{"audio":{"stt":"x"}}}}`, "unknown setting"},
		"realtime on claude":     {`{"name":"p","kind":"anthropic","config":{"modalities":{"realtime":{"enabled":true}}}}`, "no realtime API"},
		"speech model on claude": {`{"name":"p","kind":"anthropic","config":{"modalities":{"audio":{"stt_model":"x"}}}}`, "no speech API"},
	} {
		rec := createProvider(h, tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: want 400 containing %q, got %d %s", name, tc.want, rec.Code, rec.Body.String())
		}
	}

	ok := createProvider(h, `{"name":"p","kind":"openai","config":{"modalities":{"audio":{"stt_model":"whisper-1"},"realtime":{"model":"gpt-realtime"},"pdf":{"enabled":false}}}}`)
	if ok.Code != http.StatusCreated {
		t.Fatalf("a valid modalities config must be accepted, got %d %s", ok.Code, ok.Body.String())
	}
}

func TestAgentCardListsWhatTheAgentsProviderOffers(t *testing.T) {
	h, s := modalityHandlers(t)
	ctx := context.Background()
	_ = s.CreateProvider(ctx, &models.ModelProvider{Name: "oai", Kitchen: "default", Kind: "openai", Models: []string{"gpt-4o"},
		Config: map[string]interface{}{"modalities": map[string]interface{}{"pdf": map[string]interface{}{"enabled": false}}}})
	_ = s.CreateProvider(ctx, &models.ModelProvider{Name: "claude", Kitchen: "default", Kind: "anthropic", Models: []string{"claude-sonnet-4"}})
	for name, prov := range map[string][2]string{"voice": {"oai", "gpt-4o"}, "writer": {"claude", "claude-sonnet-4"}} {
		_ = s.CreateAgent(ctx, &models.Agent{Name: name, Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
			ModelProvider: prov[0], ModelName: prov[1]})
	}

	card := func(agent string) models.AgentCard {
		req := withChiParam(withKitchen(httptest.NewRequest(http.MethodGet, "/card", nil), "default"), "agentName", agent)
		rec := httptest.NewRecorder()
		h.GetAgentCard(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", agent, rec.Code, rec.Body.String())
		}
		var c models.AgentCard
		if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}

	if got := strings.Join(card("voice").Modalities, ","); got != "text,image,audio,realtime" {
		t.Fatalf("OpenAI agent with pdf switched off: modalities = %q", got)
	}
	if got := strings.Join(card("writer").Modalities, ","); got != "text,image,pdf" {
		t.Fatalf("Anthropic agent: modalities = %q", got)
	}
	c := card("voice")
	if !c.Capabilities.Vision || strings.Join(c.OutputModes, ",") != "text,audio" {
		t.Fatalf("card modes must follow the modalities, got vision=%v out=%v", c.Capabilities.Vision, c.OutputModes)
	}
}

func updateProvider(h *handlers.Handlers, name, body string) *httptest.ResponseRecorder {
	req := withChiParam(withKitchen(httptest.NewRequest(http.MethodPut, "/providers/"+name, strings.NewReader(body)), "default"), "providerName", name)
	rec := httptest.NewRecorder()
	h.UpdateProvider(rec, req)
	return rec
}

func TestProviderResponsesCarryEffectiveModalities(t *testing.T) {
	h, _ := modalityHandlers(t)

	rec := createProvider(h, `{"name":"oai","kind":"openai","models":["gpt-4o"],"config":{"api_key":"k","modalities":{"pdf":{"enabled":false}}}}`)
	var created models.ModelProvider
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if got := strings.Join(created.Modalities, ","); got != "text,image,audio,realtime" {
		t.Fatalf("create response modalities = %q", got)
	}

	get := withChiParam(withKitchen(httptest.NewRequest(http.MethodGet, "/providers/oai", nil), "default"), "providerName", "oai")
	grec := httptest.NewRecorder()
	h.GetProvider(grec, get)
	var got models.ModelProvider
	_ = json.Unmarshal(grec.Body.Bytes(), &got)
	if strings.Join(got.Modalities, ",") != "text,image,audio,realtime" {
		t.Fatalf("get response modalities = %q", got.Modalities)
	}

	list := httptest.NewRecorder()
	h.ListProviders(list, withKitchen(httptest.NewRequest(http.MethodGet, "/providers", nil), "default"))
	var all []models.ModelProvider
	_ = json.Unmarshal(list.Body.Bytes(), &all)
	if len(all) != 1 || strings.Join(all[0].Modalities, ",") != "text,image,audio,realtime" {
		t.Fatalf("list response = %+v", all)
	}
}

func TestProviderUpdateMergesModalitiesPerModality(t *testing.T) {
	h, s := modalityHandlers(t)
	createProvider(h, `{"name":"oai","kind":"openai","models":["gpt-4o"],"config":{"api_key":"k","modalities":{"pdf":{"enabled":false},"audio":{"stt_model":"whisper-1","tts_model":"tts-1"}}}}`)

	// change one audio model: pdf and the other audio model must survive
	if rec := updateProvider(h, "oai", `{"config":{"modalities":{"audio":{"tts_model":"tts-1-hd"}}}}`); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	p, _ := s.GetProvider(context.Background(), "default", "oai")
	mod, _ := p.Config["modalities"].(map[string]interface{})
	audio, _ := mod["audio"].(map[string]interface{})
	pdf, _ := mod["pdf"].(map[string]interface{})
	if audio["stt_model"] != "whisper-1" || audio["tts_model"] != "tts-1-hd" || pdf["enabled"] != false {
		t.Fatalf("modalities must merge per modality, got %+v", mod)
	}

	// null deletes a key; an emptied modality goes back to its default
	updateProvider(h, "oai", `{"config":{"modalities":{"pdf":{"enabled":null}}}}`)
	p, _ = s.GetProvider(context.Background(), "default", "oai")
	mod, _ = p.Config["modalities"].(map[string]interface{})
	if _, still := mod["pdf"]; still {
		t.Fatalf("clearing the only key must drop the pdf entry, got %+v", mod)
	}
	if got := strings.Join(h.Router.Modalities(p, "gpt-4o"), ","); !strings.Contains(got, "pdf") {
		t.Fatalf("pdf must be back on by default, got %q", got)
	}

	// an invalid update is refused and changes nothing
	if rec := updateProvider(h, "oai", `{"config":{"modalities":{"smell":{}}}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestProviderCreateDoesNotEchoTheAPIKey(t *testing.T) {
	h, _ := modalityHandlers(t)
	rec := createProvider(h, `{"name":"oai","kind":"openai","config":{"api_key":"sk-very-secret-key"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-very-secret-key") {
		t.Fatalf("the create response must mask the API key: %s", rec.Body.String())
	}
}

func TestProviderUpdateKeepsTheDefaultFlagWhenNotSent(t *testing.T) {
	h, s := modalityHandlers(t)
	createProvider(h, `{"name":"oai","kind":"openai","is_default":true,"config":{"api_key":"k"}}`)

	updateProvider(h, "oai", `{"config":{"modalities":{"pdf":{"enabled":false}}}}`)
	p, _ := s.GetProvider(context.Background(), "default", "oai")
	if !p.IsDefault {
		t.Fatal("an update that does not mention is_default must not un-default the provider")
	}

	updateProvider(h, "oai", `{"is_default":false}`)
	p, _ = s.GetProvider(context.Background(), "default", "oai")
	if p.IsDefault {
		t.Fatal("an explicit is_default:false must still clear it")
	}
}

// Spoken input is checked by input guardrails on the session path, not just typed text.
func TestSessionGuardrailsSeeTheTranscriptOfSpokenInput(t *testing.T) {
	t.Setenv("AGENTOVEN_JOURNAL_DISABLE", "true")
	oai := newFakeOpenAI(t)
	oai.transcript = "please do the forbidden thing"

	h, s := modalityHandlers(t)
	h.Guardrails = &guardrails.CommunityGuardrailService{}
	ctx := context.Background()
	_ = s.CreateProvider(ctx, &models.ModelProvider{Name: "oai", Kitchen: "default", Kind: "openai", Endpoint: oai.URL,
		Models: []string{"gpt-4o"}, IsDefault: true, Config: map[string]interface{}{"api_key": "k"}})
	_ = s.CreateAgent(ctx, &models.Agent{Name: "a", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		ModelProvider: "oai", ModelName: "gpt-4o",
		Guardrails: []models.Guardrail{{ID: "no-forbidden", Kind: models.GuardrailContentFilter, Stage: models.GuardrailStageInput, Enabled: true,
			Config: map[string]interface{}{"blocked_words": []interface{}{"forbidden"}}}}})
	_ = h.Sessions.CreateSession(ctx, &models.Session{ID: "s1", AgentName: "a", Kitchen: "default", Status: models.SessionActive})

	body := `{"content_parts":[{"type":"audio","media":{"mime_type":"audio/wav","data":"UklGRg=="}}]}`
	req := withKitchen(httptest.NewRequest(http.MethodPost, "/m", strings.NewReader(body)), "default")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("agentName", "a")
	rctx.URLParams.Add("sessionID", "s1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.SendSessionMessage(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "blocked by guardrails") {
		t.Fatalf("spoken input that breaks an input guardrail must be blocked, got %d %s", rec.Code, rec.Body.String())
	}
}
