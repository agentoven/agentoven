package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/internal/secretref"
	"github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A provider's keys live in the kitchen's vault when there is one: the provider keeps a vault://
// reference and the key itself is stored encrypted, never in the provider record, an export of it or a
// backup of the database. The community edition has no vault, so none of this runs there and a key
// pasted into a provider stays in its config as before.

// providerKeyName is the vault credential that holds a provider's key; pool entries get a numbered one.
func providerKeyName(provider string, pool int) string {
	if pool == 0 {
		return "provider-" + provider
	}
	return fmt.Sprintf("provider-%s-key-%d", provider, pool)
}

// ownsKey reports whether a vault credential was made for this provider. Only those are ever rewritten
// or deleted by provider changes: a reference to a credential somebody else made is theirs to manage.
func ownsKey(provider, ref string) bool {
	name, ok := secretref.VaultName(ref)
	return ok && (name == providerKeyName(provider, 0) || strings.HasPrefix(name, "provider-"+provider+"-key-"))
}

func (h *Handlers) keyVault() secretref.Vault { return h.SecretResolver.Vault() }

// checkVaultRef refuses a vault:// reference that cannot work: there is no vault, or the credential is
// not in this kitchen. Other schemes are not checked here (they resolve when a model is called).
func (h *Handlers) checkVaultRef(ctx context.Context, kitchen, ref string) error {
	if !strings.HasPrefix(ref, secretref.VaultScheme) {
		return nil
	}
	name, ok := secretref.VaultName(ref)
	if !ok {
		return fmt.Errorf("secret_ref %q names no credential", ref)
	}
	v := h.keyVault()
	if v == nil {
		return fmt.Errorf("secret_ref %q needs the credential vault, which is part of Pro", ref)
	}
	if _, err := v.Read(ctx, kitchen, name); err != nil {
		return fmt.Errorf("secret_ref %q: no credential named %q in this kitchen", ref, name)
	}
	return nil
}

// sealProviderKeys moves the keys a provider holds in plain text into the vault and leaves references
// behind. It returns the credentials it wrote (so a caller whose own save then fails can remove them)
// and says nothing when there is no vault or nothing to move. Running it again changes nothing.
//
// A key already behind a reference is not touched, except that a new key sent for a provider whose
// reference is one this code made rotates that credential in place.
func (h *Handlers) sealProviderKeys(ctx context.Context, p *models.ModelProvider) (written []string, err error) {
	v := h.keyVault()
	if v == nil {
		return nil, nil
	}
	kitchen := p.Kitchen
	put := func(name, value, label string) error {
		if err := v.Write(ctx, kitchen, name, value, label); err != nil {
			return fmt.Errorf("could not store the key for provider %q in the vault: %w", p.Name, err)
		}
		written = append(written, name)
		return nil
	}

	// Keys in the rotation pool each become a credential with their own reference.
	for i := range p.APIKeys {
		k := &p.APIKeys[i]
		if k.Key == "" || k.Key == models.RedactedValue || strings.HasSuffix(k.Key, "****") {
			continue
		}
		name := providerKeyName(p.Name, i+1)
		label := "Provider " + p.Name + " key"
		if k.Label != "" {
			label += " (" + k.Label + ")"
		}
		if err := put(name, k.Key, label); err != nil {
			return written, err
		}
		k.Key, k.SecretRef = "", secretref.VaultRef(name)
	}

	key, _ := p.Config["api_key"].(string)
	if key == "" || key == models.RedactedValue || strings.HasSuffix(key, "****") {
		return written, nil
	}
	switch {
	case p.SecretRef == "" || ownsKey(p.Name, p.SecretRef):
		// A pool takes the place of the single key, and the single key is only a fallback for when
		// every pool key is off. Leave that rare combination as the caller sent it rather than make
		// the fallback win over the pool.
		if hasEnabledKey(p.APIKeys) {
			log.Warn().Str("provider", p.Name).Msg("vault: provider has both a key pool and api_key; api_key left in place")
			return written, nil
		}
		name := providerKeyName(p.Name, 0)
		if err := put(name, key, "Provider "+p.Name+" key"); err != nil {
			return written, err
		}
		p.SecretRef = secretref.VaultRef(name)
		delete(p.Config, "api_key")
	case strings.HasPrefix(p.SecretRef, secretref.VaultScheme):
		return written, fmt.Errorf("provider %q reads its key from %s, which belongs to someone else: rotate that credential, or point secret_ref somewhere else", p.Name, p.SecretRef)
	}
	return written, nil
}

// dropProviderKeys removes the vault credentials a provider's own references point to.
func (h *Handlers) dropProviderKeys(ctx context.Context, p *models.ModelProvider, names ...string) {
	v := h.keyVault()
	if v == nil {
		return
	}
	for _, name := range names {
		if err := v.Delete(ctx, p.Kitchen, name); err != nil {
			log.Warn().Err(err).Str("provider", p.Name).Str("credential", name).Msg("vault: could not remove a provider key")
		}
	}
}

// ownedKeyNames lists the credentials a provider owns: the ones its references point to that it made.
func ownedKeyNames(p *models.ModelProvider) []string {
	var out []string
	refs := []string{p.SecretRef}
	for _, k := range p.APIKeys {
		refs = append(refs, k.SecretRef)
	}
	for _, r := range refs {
		if ownsKey(p.Name, r) {
			name, _ := secretref.VaultName(r)
			out = append(out, name)
		}
	}
	return out
}

func hasEnabledKey(keys []models.APIKeyEntry) bool {
	for _, k := range keys {
		if k.Enabled && (k.Key != "" || k.SecretRef != "") {
			return true
		}
	}
	return false
}

// SealProviderKeys moves every provider key in the kitchen that is still stored in plain text into the
// vault. It is how providers made before the vault existed are brought across, and it is safe to run
// again. POST /api/v1/models/providers/seal-keys
func (h *Handlers) SealProviderKeys(w http.ResponseWriter, r *http.Request) {
	if h.keyVault() == nil {
		respondError(w, http.StatusConflict, "there is no credential vault to move keys into; it is part of Pro")
		return
	}
	kitchen := middleware.GetKitchen(r.Context())
	providers, err := h.Store.ListProviders(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	moved, failed := []string{}, map[string]string{}
	for i := range providers {
		p := providers[i] // a copy: the store's own is never changed in place
		p.Config = copyMap(p.Config)
		p.APIKeys = append([]models.APIKeyEntry(nil), p.APIKeys...)
		written, err := h.sealProviderKeys(r.Context(), &p)
		if err == nil && len(written) > 0 {
			if err = h.Store.UpdateProvider(r.Context(), &p); err != nil {
				h.dropProviderKeys(r.Context(), &p, written...)
			}
		}
		switch {
		case err != nil:
			failed[p.Name] = err.Error()
		case len(written) > 0:
			moved = append(moved, p.Name)
		}
	}
	status := http.StatusOK
	if len(failed) > 0 {
		status = http.StatusMultiStatus
	}
	respondJSON(w, status, map[string]interface{}{"moved": moved, "failed": failed})
}

func copyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// without returns the names in all that are not in skip.
func without(all, skip []string) []string {
	drop := make(map[string]bool, len(skip))
	for _, n := range skip {
		drop[n] = true
	}
	var out []string
	for _, n := range all {
		if !drop[n] {
			out = append(out, n)
		}
	}
	return out
}
