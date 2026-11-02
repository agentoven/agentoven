package handlers_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type keyObserver struct {
	mu    sync.Mutex
	calls []string
}

func (o *keyObserver) ProviderKeyChanged(_ context.Context, kitchen, provider string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, kitchen+"/"+provider)
}
func (o *keyObserver) got() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.calls...)
}

func TestUpdateProviderCanChangeThePoolStrategyAndTLSSetting(t *testing.T) {
	h, v, _ := vaultHandlers(t)
	callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil,
		`{"name":"pool","kind":"openai","rotation_strategy":"random","api_keys":[{"key":"sk-pool-AAAAAAAA","label":"a","enabled":true},{"key":"sk-pool-BBBBBBBB","label":"b","enabled":true}]}`)
	ctx := context.Background()

	// Read it the way a client does, edit one visible thing and send the pool back unchanged.
	if code := putProvider(h, "pool", `{"rotation_strategy":"weighted","tls_skip_verify":true,"api_keys":[
		{"key":"","secret_ref":"vault://provider-pool-key-1","label":"a","enabled":true,"weight":5},
		{"key":"","secret_ref":"vault://provider-pool-key-2","label":"b","enabled":false}]}`); code != http.StatusOK {
		t.Fatalf("update: %d", code)
	}
	got, _ := h.Store.GetProvider(ctx, "default", "pool")
	if got.RotationStrategy != "weighted" || !got.TLSSkipVerify {
		t.Errorf("strategy %q tls %v", got.RotationStrategy, got.TLSSkipVerify)
	}
	if len(got.APIKeys) != 2 || got.APIKeys[0].Weight != 5 || got.APIKeys[1].Enabled || got.APIKeys[0].SecretRef != "vault://provider-pool-key-1" {
		t.Errorf("pool = %+v", got.APIKeys)
	}
	// Unsent fields are left alone.
	if code := putProvider(h, "pool", `{"endpoint":"https://x.example/v1"}`); code != http.StatusOK {
		t.Fatal(code)
	}
	got, _ = h.Store.GetProvider(ctx, "default", "pool")
	if got.RotationStrategy != "weighted" || !got.TLSSkipVerify || len(got.APIKeys) != 2 {
		t.Errorf("an update that sends none of them must not change them: %+v", got)
	}

	// Dropping a key from the pool takes the credential this provider made for it.
	if code := putProvider(h, "pool", `{"api_keys":[{"secret_ref":"vault://provider-pool-key-1","label":"a","enabled":true}]}`); code != http.StatusOK {
		t.Fatal(code)
	}
	if v.has("default", "provider-pool-key-2") || !v.has("default", "provider-pool-key-1") {
		t.Error("the dropped key's credential should be removed and the kept one left")
	}

	if code := putProvider(h, "pool", `{"rotation_strategy":"fastest"}`); code != http.StatusBadRequest {
		t.Errorf("an unknown strategy must be refused, got %d", code)
	}
}

func TestANewPoolKeyNeverOverwritesACredentialAnotherEntryUses(t *testing.T) {
	h, v, _ := vaultHandlers(t)
	callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil,
		`{"name":"p","kind":"openai","api_keys":[{"key":"sk-AAAAAAAAAAA","label":"a","enabled":true},{"key":"sk-BBBBBBBBBBB","label":"b","enabled":true}]}`)
	// Reorder, and add a new key in front: entry "b" (key-2) is now second, "a" (key-1) third.
	if code := putProvider(h, "p", `{"api_keys":[
		{"key":"sk-NEWNEWNEWNEW","label":"c","enabled":true},
		{"secret_ref":"vault://provider-p-key-2","label":"b","enabled":true},
		{"secret_ref":"vault://provider-p-key-1","label":"a","enabled":true}]}`); code != http.StatusOK {
		t.Fatal(code)
	}
	ctx := context.Background()
	if got, _ := v.Read(ctx, "default", "provider-p-key-1"); got != "sk-AAAAAAAAAAA" {
		t.Errorf("a's credential was overwritten: %q", got)
	}
	if got, _ := v.Read(ctx, "default", "provider-p-key-2"); got != "sk-BBBBBBBBBBB" {
		t.Errorf("b's credential was overwritten: %q", got)
	}
	stored, _ := h.Store.GetProvider(ctx, "default", "p")
	ref := stored.APIKeys[0].SecretRef
	if ref != "vault://provider-p-key-3" {
		t.Errorf("the new key should have taken a free credential, got %q", ref)
	}
	if got, _ := v.Read(ctx, "default", "provider-p-key-3"); got != "sk-NEWNEWNEWNEW" {
		t.Errorf("new credential holds %q", got)
	}
}

func TestAMaskedPoolKeyKeepsTheStoredOneWithoutAVault(t *testing.T) {
	h, s := newTestHandlers(t)
	h.Router = router.NewModelRouter(s)
	ctx := context.Background()
	_ = h.Store.CreateProvider(ctx, &models.ModelProvider{ID: "1", Name: "plain", Kitchen: "default", Kind: "openai",
		APIKeys: []models.APIKeyEntry{{Key: "sk-plain-AAAAAAAA", Label: "a", Enabled: true}, {Key: "sk-plain-BBBBBBBB", Label: "b", Enabled: true}}})
	body := `{"api_keys":[{"key":"sk-p****","label":"b","enabled":true},{"key":"sk-p****","label":"a","enabled":false}]}`
	if code := putProvider(h, "plain", body); code != http.StatusOK {
		t.Fatal(code)
	}
	got, _ := h.Store.GetProvider(ctx, "default", "plain")
	if got.APIKeys[0].Key != "sk-plain-BBBBBBBB" || got.APIKeys[1].Key != "sk-plain-AAAAAAAA" || got.APIKeys[1].Enabled {
		t.Errorf("masked keys must be matched by label and restored: %+v", got.APIKeys)
	}
	if code := putProvider(h, "plain", `{"api_keys":[{"key":"sk-p****","label":"new","enabled":true},{"key":"sk-p****","label":"x","enabled":true},{"key":"sk-p****","label":"y","enabled":true}]}`); code != http.StatusBadRequest {
		t.Errorf("a masked key with nothing to restore must be refused, got %d", code)
	}
}

func TestTheKeyObserverIsToldWhenAProviderKeyChangesAndOnlyThen(t *testing.T) {
	h, _, _ := vaultHandlers(t)
	obs := &keyObserver{}
	h.KeyRotation = obs
	callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil, `{"name":"openai","kind":"openai","config":{"api_key":"sk-old-OLDOLDOLD"}}`)
	if len(obs.got()) != 0 {
		t.Fatalf("creating a provider starts no process: %v", obs.got())
	}
	// Rotation in place leaves the reference as it was, and still counts.
	if code := putProvider(h, "openai", `{"config":{"api_key":"sk-new-NEWNEWNEW"}}`); code != http.StatusOK {
		t.Fatal(code)
	}
	if got := obs.got(); len(got) != 1 || got[0] != "default/openai" {
		t.Fatalf("after a rotation: %v", got)
	}
	// Editing something else, or sending the masked key back, is not a key change.
	putProvider(h, "openai", `{"endpoint":"https://proxy.example/v1","config":{"api_key":"sk-n****"}}`)
	putProvider(h, "openai", `{"rotation_strategy":"random","tls_skip_verify":true}`)
	if len(obs.got()) != 1 {
		t.Errorf("unrelated updates must not restart anything: %v", obs.got())
	}
	// Pointing the provider at another reference is a change; so is editing the pool.
	putProvider(h, "openai", `{"secret_ref":"env://OPENAI_KEY"}`)
	putProvider(h, "openai", `{"api_keys":[{"key":"sk-pool-1234567","label":"a","enabled":true}]}`)
	if len(obs.got()) != 3 {
		t.Errorf("a new reference and a new pool are both key changes: %v", obs.got())
	}
	// No observer, no change in behaviour.
	h.KeyRotation = nil
	if code := putProvider(h, "openai", `{"config":{"api_key":"sk-other-OTHEROTHER"}}`); code != http.StatusOK {
		t.Fatal(code)
	}
}

func TestSealDryRunReportsWithoutChangingAnythingAndSealAllCoversEveryKitchen(t *testing.T) {
	h, v, _ := vaultHandlers(t)
	ctx := context.Background()
	for _, k := range []string{"default", "other"} {
		_ = h.Store.CreateKitchen(ctx, &models.Kitchen{ID: k, Name: k})
	}
	_ = h.Store.CreateProvider(ctx, &models.ModelProvider{ID: "1", Name: "a", Kitchen: "default", Kind: "openai",
		Config: map[string]interface{}{"api_key": "sk-legacy-AAAAAAAAAA"}})
	_ = h.Store.CreateProvider(ctx, &models.ModelProvider{ID: "2", Name: "b", Kitchen: "other", Kind: "openai",
		APIKeys: []models.APIKeyEntry{{Key: "sk-legacy-BBBBBBBBBB", Enabled: true}}})
	_ = h.Store.CreateProvider(ctx, &models.ModelProvider{ID: "3", Name: "c", Kitchen: "default", Kind: "openai", SecretRef: "env://X"})

	rep, err := h.SealAllProviders(ctx, true)
	if err != nil || !rep.DryRun || len(rep.Moved) != 2 || len(rep.Failed) != 0 {
		t.Fatalf("dry run: %+v %v", rep, err)
	}
	if v.has("default", "provider-a") || v.has("other", "provider-b-key-1") {
		t.Fatal("a dry run wrote to the vault")
	}
	a, _ := h.Store.GetProvider(ctx, "default", "a")
	if a.Config["api_key"] != "sk-legacy-AAAAAAAAAA" || a.SecretRef != "" {
		t.Fatalf("a dry run changed a provider: %+v", a)
	}

	for i := 0; i < 2; i++ {
		rep, err = h.SealAllProviders(ctx, false)
		if err != nil || len(rep.Failed) != 0 {
			t.Fatalf("run %d: %+v %v", i, rep, err)
		}
		if want := 2 * boolToInt(i == 0); len(rep.Moved) != want {
			t.Errorf("run %d moved %v, want %d (it must be idempotent)", i, rep.Moved, want)
		}
	}
	a, _ = h.Store.GetProvider(ctx, "default", "a")
	b, _ := h.Store.GetProvider(ctx, "other", "b")
	if a.Config["api_key"] != nil || a.SecretRef != "vault://provider-a" || b.APIKeys[0].Key != "" || b.APIKeys[0].SecretRef != "vault://provider-b-key-1" {
		t.Errorf("after sealing: %+v / %+v", a, b)
	}
	if !v.has("default", "provider-a") || !v.has("other", "provider-b-key-1") {
		t.Error("keys are not in their kitchens' vault")
	}

	// A failure is reported per provider and leaves the provider as it was.
	_ = h.Store.CreateProvider(ctx, &models.ModelProvider{ID: "4", Name: "d", Kitchen: "default", Kind: "openai",
		Config: map[string]interface{}{"api_key": "sk-legacy-DDDDDDDDDD"}})
	v.fail = true
	rep, _ = h.SealAllProviders(ctx, false)
	if len(rep.Failed) != 1 || !strings.Contains(rep.Failed["default/d"], "vault") {
		t.Errorf("failure report: %+v", rep)
	}
	d, _ := h.Store.GetProvider(ctx, "default", "d")
	if d.Config["api_key"] != "sk-legacy-DDDDDDDDDD" {
		t.Error("a provider whose key could not be vaulted must keep its key")
	}
}

func TestSealAllWithoutAVaultSaysSo(t *testing.T) {
	h, _ := newTestHandlers(t)
	if _, err := h.SealAllProviders(context.Background(), false); err == nil {
		t.Error("there is nothing to seal into")
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
