package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/secretref"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// memVault is a vault that keeps credentials in memory, per kitchen.
type memVault struct {
	mu    sync.Mutex
	creds map[string]string
	fail  bool
}

func newMemVault() *memVault { return &memVault{creds: map[string]string{}} }

func (v *memVault) Read(_ context.Context, kitchen, name string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if val, ok := v.creds[kitchen+"/"+name]; ok {
		return val, nil
	}
	return "", errors.New("not found")
}
func (v *memVault) Write(_ context.Context, kitchen, name, value, _ string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fail {
		return errors.New("vault down")
	}
	v.creds[kitchen+"/"+name] = value
	return nil
}
func (v *memVault) Delete(_ context.Context, kitchen, name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.creds, kitchen+"/"+name)
	return nil
}
func (v *memVault) has(kitchen, name string) bool {
	_, err := v.Read(context.Background(), kitchen, name)
	return err == nil
}

func vaultHandlers(t *testing.T) (*handlers.Handlers, *memVault, *router.ModelRouter) {
	t.Helper()
	h, s := newTestHandlers(t)
	mr := router.NewModelRouter(s)
	h.Router = mr
	res := secretref.New()
	v := newMemVault()
	res.SetVault(v)
	h.SecretResolver = res
	mr.SetSecretResolver(res)
	return h, v, mr
}

func putProvider(h *handlers.Handlers, name, body string) int {
	return callAgentAPI(h.UpdateProvider, http.MethodPut, "/api/v1/providers/"+name, map[string]string{"providerName": name}, body).Code
}

func TestAPastedProviderKeyGoesToTheVaultAndOnlyAReferenceIsStored(t *testing.T) {
	h, v, mr := vaultHandlers(t)
	w := callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil,
		`{"name":"openai","kind":"openai","models":["gpt-4o"],"config":{"api_key":"sk-live-PASTED-1234567","max_tokens":2048}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sk-live-PASTED") {
		t.Errorf("the key was echoed: %s", w.Body.String())
	}
	stored, _ := h.Store.GetProvider(context.Background(), "default", "openai")
	if _, still := stored.Config["api_key"]; still {
		t.Errorf("the key is still in the stored provider: %v", stored.Config)
	}
	if stored.SecretRef != "vault://provider-openai" || stored.Config["max_tokens"] != float64(2048) {
		t.Errorf("secret_ref = %q config = %v", stored.SecretRef, stored.Config)
	}
	if got, _ := v.Read(context.Background(), "default", "provider-openai"); got != "sk-live-PASTED-1234567" {
		t.Errorf("vault holds %q", got)
	}
	// A model call (and a hosted process starting) reads the key through the reference.
	if got, err := mr.ProviderKey(context.Background(), "default", "openai"); err != nil || got != "sk-live-PASTED-1234567" {
		t.Errorf("ProviderKey = %q, %v", got, err)
	}
}

func TestRotatingAKeyChangesTheVaultCredentialInPlaceAndTakesEffectAtOnce(t *testing.T) {
	h, v, mr := vaultHandlers(t)
	callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil, `{"name":"openai","kind":"openai","config":{"api_key":"sk-old-OLDOLDOLD"}}`)
	if code := putProvider(h, "openai", `{"config":{"api_key":"sk-new-NEWNEWNEW"}}`); code != http.StatusOK {
		t.Fatalf("rotate: %d", code)
	}
	stored, _ := h.Store.GetProvider(context.Background(), "default", "openai")
	if stored.SecretRef != "vault://provider-openai" || stored.Config["api_key"] != nil {
		t.Errorf("after rotation: %q %v", stored.SecretRef, stored.Config)
	}
	if got, _ := mr.ProviderKey(context.Background(), "default", "openai"); got != "sk-new-NEWNEWNEW" {
		t.Errorf("the very next call used %q", got)
	}
	// Editing a visible setting with the masked provider sent back changes no key.
	if code := putProvider(h, "openai", `{"endpoint":"https://proxy.example/v1","config":{"api_key":"sk-n****"}}`); code != http.StatusOK {
		t.Fatal(code)
	}
	if got, _ := v.Read(context.Background(), "default", "provider-openai"); got != "sk-new-NEWNEWNEW" {
		t.Errorf("a masked key overwrote the credential: %q", got)
	}
}

func TestPoolKeysEachGetTheirOwnCredential(t *testing.T) {
	h, v, _ := vaultHandlers(t)
	w := callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil,
		`{"name":"pool","kind":"openai","api_keys":[{"key":"sk-pool-AAAAAAAA","label":"a","enabled":true},{"key":"sk-pool-BBBBBBBB","label":"b","enabled":true}]}`)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}
	stored, _ := h.Store.GetProvider(context.Background(), "default", "pool")
	for i, want := range []string{"vault://provider-pool-key-1", "vault://provider-pool-key-2"} {
		if stored.APIKeys[i].Key != "" || stored.APIKeys[i].SecretRef != want {
			t.Errorf("pool[%d] = %+v", i, stored.APIKeys[i])
		}
	}
	if !v.has("default", "provider-pool-key-1") || !v.has("default", "provider-pool-key-2") {
		t.Error("pool keys are not in the vault")
	}
}

func TestADeletedProviderTakesItsOwnCredentialsButNotOneItOnlyPointedAt(t *testing.T) {
	h, v, _ := vaultHandlers(t)
	_ = v.Write(context.Background(), "default", "shared-team-key", "sk-shared-SHAREDSHARED", "")
	callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil, `{"name":"mine","kind":"openai","config":{"api_key":"sk-mine-MINEMINEMINE"}}`)
	callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil, `{"name":"theirs","kind":"openai","secret_ref":"vault://shared-team-key"}`)
	for _, n := range []string{"mine", "theirs"} {
		if w := callAgentAPI(h.DeleteProvider, http.MethodDelete, "/api/v1/providers/"+n, map[string]string{"providerName": n}, ""); w.Code != http.StatusNoContent {
			t.Fatalf("delete %s: %d", n, w.Code)
		}
	}
	if v.has("default", "provider-mine") {
		t.Error("the provider's own credential was left behind")
	}
	if !v.has("default", "shared-team-key") {
		t.Error("a credential the provider only pointed at was deleted")
	}
}

func TestAProviderCannotRotateACredentialItDoesNotOwn(t *testing.T) {
	h, v, _ := vaultHandlers(t)
	_ = v.Write(context.Background(), "default", "shared-team-key", "sk-shared-SHAREDSHARED", "")
	callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil, `{"name":"theirs","kind":"openai","secret_ref":"vault://shared-team-key"}`)
	if code := putProvider(h, "theirs", `{"config":{"api_key":"sk-hijack-HIJACKHIJACK"}}`); code != http.StatusBadRequest {
		t.Errorf("rotating a shared credential through a provider: %d", code)
	}
	if got, _ := v.Read(context.Background(), "default", "shared-team-key"); got != "sk-shared-SHAREDSHARED" {
		t.Errorf("the shared credential changed: %q", got)
	}
}

func TestAVaultReferenceMustPointAtARealCredentialInThisKitchen(t *testing.T) {
	h, v, _ := vaultHandlers(t)
	_ = v.Write(context.Background(), "other-kitchen", "their-key", "sk-other-OTHEROTHER", "")
	for _, ref := range []string{"vault://does-not-exist", "vault://their-key", "vault://"} {
		w := callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil, `{"name":"x","kind":"openai","secret_ref":"`+ref+`"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", ref, w.Code, w.Body.String())
		}
	}
}

func TestWithoutAVaultAVaultReferenceIsRefusedAndKeysStayAsTheyWere(t *testing.T) {
	h, s := newTestHandlers(t)
	h.Router = router.NewModelRouter(s)
	h.SecretResolver = secretref.New() // the community edition: no vault
	if w := callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil, `{"name":"a","kind":"openai","secret_ref":"vault://k"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Pro") {
		t.Errorf("vault:// without a vault: %d %s", w.Code, w.Body.String())
	}
	callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil, `{"name":"b","kind":"openai","config":{"api_key":"sk-plain-PLAINPLAIN"}}`)
	stored, _ := h.Store.GetProvider(context.Background(), "default", "b")
	if stored.Config["api_key"] != "sk-plain-PLAINPLAIN" || stored.SecretRef != "" {
		t.Errorf("the community edition changed how a key is stored: %v %q", stored.Config, stored.SecretRef)
	}
	if w := callAgentAPI(h.SealProviderKeys, http.MethodPost, "/api/v1/providers/seal-keys", nil, ""); w.Code != http.StatusConflict {
		t.Errorf("seal-keys without a vault: %d", w.Code)
	}
}

func TestAVaultFailureLeavesNoPlaintextAndNoHalfMadeProvider(t *testing.T) {
	h, v, _ := vaultHandlers(t)
	v.fail = true
	w := callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil, `{"name":"openai","kind":"openai","config":{"api_key":"sk-live-NOPENOPENOPE"}}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("create with the vault down: %d", w.Code)
	}
	if _, err := h.Store.GetProvider(context.Background(), "default", "openai"); err == nil {
		t.Error("a provider was saved although its key could not be vaulted")
	}
}

func TestSealKeysMovesExistingProvidersAndIsSafeToRepeat(t *testing.T) {
	h, v, mr := vaultHandlers(t)
	ctx := context.Background()
	_ = h.Store.CreateProvider(ctx, &models.ModelProvider{ID: "1", Name: "old", Kitchen: "default", Kind: "openai",
		Config: map[string]interface{}{"api_key": "sk-legacy-LEGACYLEGACY", "max_tokens": 10}})
	_ = h.Store.CreateProvider(ctx, &models.ModelProvider{ID: "2", Name: "ext", Kitchen: "default", Kind: "openai", SecretRef: "env://X"})
	for i := 0; i < 2; i++ {
		w := callAgentAPI(h.SealProviderKeys, http.MethodPost, "/api/v1/providers/seal-keys", nil, "")
		if w.Code != http.StatusOK {
			t.Fatalf("run %d: %d %s", i, w.Code, w.Body.String())
		}
		moved := strings.Contains(w.Body.String(), `"moved":["old"]`)
		if moved != (i == 0) {
			t.Errorf("run %d: %s", i, w.Body.String())
		}
	}
	stored, _ := h.Store.GetProvider(ctx, "default", "old")
	if stored.Config["api_key"] != nil || stored.SecretRef != "vault://provider-old" || stored.Config["max_tokens"] != 10 {
		t.Errorf("after sealing: %v %q", stored.Config, stored.SecretRef)
	}
	if got, _ := mr.ProviderKey(ctx, "default", "old"); got != "sk-legacy-LEGACYLEGACY" {
		t.Errorf("the sealed key does not resolve: %q", got)
	}
	if !v.has("default", "provider-old") || v.has("default", "provider-ext") {
		t.Error("wrong credentials in the vault")
	}
}

func TestAChefCannotPointAProviderAtTheInstallationsOwnSecrets(t *testing.T) {
	h, _, _ := vaultHandlers(t)
	t.Setenv("LEAKME_SECRET", "hunter2-platform-secret")
	send := func(role, method, path, body string, params map[string]string, fn http.HandlerFunc) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = asRole(withKitchen(req, "default"), role)
		for k, v := range params {
			req = withChiParam(req, k, v)
		}
		w := httptest.NewRecorder()
		fn(w, req)
		return w.Code
	}
	create := func(role, name, body string) int {
		return send(role, http.MethodPost, "/api/v1/providers", body, nil, h.CreateProvider)
	}
	update := func(role, name, body string) int {
		return send(role, http.MethodPut, "/api/v1/providers/"+name, body, map[string]string{"providerName": name}, h.UpdateProvider)
	}
	for _, scheme := range []string{"env://LEAKME_SECRET", "k8s://kube-system/token/key", "env://DATABASE_URL"} {
		if code := create("chef", "x", `{"name":"x","kind":"openai","secret_ref":"`+scheme+`"}`); code != http.StatusForbidden {
			t.Errorf("a chef created a provider with %s: %d", scheme, code)
		}
		if code := create("head_chef", "x", `{"name":"x2","kind":"openai","api_keys":[{"label":"a","secret_ref":"`+scheme+`","enabled":true}]}`); code != http.StatusForbidden {
			t.Errorf("a pool entry with %s: %d", scheme, code)
		}
	}
	// A platform administrator may.
	if code := create("admin", "ops", `{"name":"ops","kind":"openai","secret_ref":"env://LEAKME_SECRET"}`); code != http.StatusCreated && code != http.StatusNoContent {
		t.Fatalf("an administrator creates a provider on an env reference: %d", code)
	}
	// A chef editing it leaves the administrator's reference alone, but cannot point it elsewhere.
	if code := update("chef", "ops", `{"endpoint":"https://proxy.example/v1"}`); code != http.StatusOK {
		t.Errorf("editing something else must still work: %d", code)
	}
	if code := update("chef", "ops", `{"secret_ref":"env://LEAKME_SECRET"}`); code != http.StatusOK {
		t.Errorf("re-sending the reference it already holds is not a change: %d", code)
	}
	if code := update("chef", "ops", `{"secret_ref":"env://AGENTOVEN_SA_SECRET"}`); code != http.StatusForbidden {
		t.Errorf("a chef repointed an administrator's provider at another environment variable: %d", code)
	}
	if code := update("chef", "ops", `{"api_keys":[{"key":"k","label":"a","secret_ref":"k8s://a/b/c","enabled":true}]}`); code != http.StatusForbidden {
		t.Errorf("a chef added an env/k8s pool entry: %d", code)
	}
}
