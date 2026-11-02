package handlers

import (
	"context"
	"errors"
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
	return sealInto(ctx, h.keyVault(), p)
}

// sealInto is sealProviderKeys against a given vault, so a dry run can use one that stores nothing.
func sealInto(ctx context.Context, v secretref.Vault, p *models.ModelProvider) (written []string, err error) {
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
		name := poolKeyName(p, i)
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

// ProviderKeyObserver is told when the key a provider uses has changed: a new key was saved, a pool
// or a reference was edited, or a vault credential the provider reads was rotated. Pro installs one
// that restarts the hosted processes that started with the old key. It is called after the change is
// stored and must not fail the request; it may return before its work is done.
type ProviderKeyObserver interface {
	ProviderKeyChanged(ctx context.Context, kitchen, provider string)
}

// providerKeySignature summarises where a provider's key comes from, so a change to it can be
// noticed. It is compared in memory and never logged or returned.
func providerKeySignature(p *models.ModelProvider) string {
	var b strings.Builder
	key, _ := p.Config["api_key"].(string)
	if models.IsMasked(key) {
		key = "" // a mask sent back by a client is not a key
	}
	fmt.Fprintf(&b, "%q|%q", p.SecretRef, key)
	for _, k := range p.APIKeys {
		fmt.Fprintf(&b, "|%q,%q,%t", k.Key, k.SecretRef, k.Enabled)
	}
	return b.String()
}

func validRotationStrategy(s string) bool {
	switch s {
	case "", "round-robin", "random", "weighted":
		return true
	}
	return false
}

// mergePoolUpdate returns the key pool an update sets. A client that read the provider has seen its pool
// masked (a key shows a short prefix, a vaulted one only its reference) and sends it back as it was:
// such an entry keeps the stored key or reference, matched by label and otherwise by position.
func mergePoolUpdate(incoming, stored []models.APIKeyEntry) ([]models.APIKeyEntry, error) {
	match := func(i int, label string) (models.APIKeyEntry, bool) {
		if label != "" {
			for _, s := range stored {
				if s.Label == label {
					return s, true
				}
			}
		}
		if i < len(stored) && (label == "" || stored[i].Label == "") {
			return stored[i], true
		}
		return models.APIKeyEntry{}, false
	}
	out := make([]models.APIKeyEntry, len(incoming))
	for i, k := range incoming {
		switch {
		case k.Key != "" && models.IsMasked(k.Key):
			old, ok := match(i, k.Label)
			if !ok || old.Key == "" && old.SecretRef == "" {
				return nil, fmt.Errorf("api_keys[%d] carries a masked key and there is no stored key to keep: send the key itself", i)
			}
			k.Key = old.Key
			if k.SecretRef == "" {
				k.SecretRef = old.SecretRef
			}
		case k.Key == "" && k.SecretRef == "":
			if old, ok := match(i, k.Label); ok {
				k.Key, k.SecretRef = old.Key, old.SecretRef
			}
		}
		out[i] = k
	}
	return out, nil
}

// SealReport says what sealing provider keys did, or with a dry run what it would do.
type SealReport struct {
	DryRun bool `json:"dry_run,omitempty"`
	// Moved lists the providers whose keys were moved to the vault (with a dry run, would be).
	Moved []string `json:"moved"`
	// Failed maps a provider to why its keys could not be moved. The provider is left as it was.
	Failed map[string]string `json:"failed"`
}

// dryRunVault stores nothing: sealing against it reports what would be written and changes nothing.
type dryRunVault struct{}

func (dryRunVault) Read(context.Context, string, string) (string, error) {
	return "", errors.New("not found")
}
func (dryRunVault) Write(context.Context, string, string, string, string) error { return nil }
func (dryRunVault) Delete(context.Context, string, string) error                { return nil }

// SealKitchenProviders moves the plain-text keys of every provider in one kitchen into the vault. With
// dryRun it only reports which providers it would change. Running it again changes nothing.
func (h *Handlers) SealKitchenProviders(ctx context.Context, kitchen string, dryRun bool) (SealReport, error) {
	rep := SealReport{DryRun: dryRun, Moved: []string{}, Failed: map[string]string{}}
	providers, err := h.Store.ListProviders(ctx, kitchen)
	if err != nil {
		return rep, err
	}
	var v secretref.Vault = h.keyVault()
	if dryRun {
		v = dryRunVault{}
	}
	for i := range providers {
		p := providers[i] // a copy: the store's own is never changed in place
		p.Config = copyMap(p.Config)
		p.APIKeys = append([]models.APIKeyEntry(nil), p.APIKeys...)
		written, err := sealInto(ctx, v, &p)
		if err == nil && len(written) > 0 && !dryRun {
			if err = h.Store.UpdateProvider(ctx, &p); err != nil {
				h.dropProviderKeys(ctx, &p, written...)
			}
		}
		switch {
		case err != nil:
			rep.Failed[p.Name] = err.Error()
		case len(written) > 0:
			rep.Moved = append(rep.Moved, p.Name)
		}
	}
	return rep, nil
}

// SealAllProviders is SealKitchenProviders for every kitchen, which is what a server does at startup
// (Pro, behind a flag). Moved and Failed name providers as "kitchen/provider". It does nothing and
// says so when there is no vault.
func (h *Handlers) SealAllProviders(ctx context.Context, dryRun bool) (SealReport, error) {
	all := SealReport{DryRun: dryRun, Moved: []string{}, Failed: map[string]string{}}
	if h.keyVault() == nil {
		return all, errNoKeyVault
	}
	kitchens, err := h.Store.ListKitchens(ctx)
	if err != nil {
		return all, err
	}
	for _, k := range kitchens {
		rep, err := h.SealKitchenProviders(ctx, k.Name, dryRun)
		if err != nil {
			return all, fmt.Errorf("kitchen %q: %w", k.Name, err)
		}
		for _, n := range rep.Moved {
			all.Moved = append(all.Moved, k.Name+"/"+n)
		}
		for n, why := range rep.Failed {
			all.Failed[k.Name+"/"+n] = why
		}
	}
	return all, nil
}

var errNoKeyVault = errors.New("there is no credential vault to move keys into; it is part of Pro")

// SealProviderKeys moves every provider key in the kitchen that is still stored in plain text into the
// vault. It is how providers made before the vault existed are brought across, and it is safe to run
// again. POST /api/v1/models/providers/seal-keys
func (h *Handlers) SealProviderKeys(w http.ResponseWriter, r *http.Request) {
	if h.keyVault() == nil {
		respondError(w, http.StatusConflict, errNoKeyVault.Error())
		return
	}
	rep, err := h.SealKitchenProviders(r.Context(), middleware.GetKitchen(r.Context()), false)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := http.StatusOK
	if len(rep.Failed) > 0 {
		status = http.StatusMultiStatus
	}
	respondJSON(w, status, map[string]interface{}{"moved": rep.Moved, "failed": rep.Failed})
}

// poolKeyName is the vault credential for the pool entry at index i. An entry that already points at a
// credential this provider made keeps it (rotating that credential in place). A new entry takes the
// credential its position names, unless another entry still points there, in which case it takes the
// first free one, so adding or reordering keys never overwrites a key that is in use.
func poolKeyName(p *models.ModelProvider, i int) string {
	if name, ok := secretref.VaultName(p.APIKeys[i].SecretRef); ok && ownsKey(p.Name, p.APIKeys[i].SecretRef) {
		return name
	}
	used := map[string]bool{}
	for j, k := range p.APIKeys {
		if j == i {
			continue
		}
		if name, ok := secretref.VaultName(k.SecretRef); ok {
			used[name] = true
		}
	}
	for n := i + 1; ; n++ {
		if name := providerKeyName(p.Name, n); !used[name] {
			return name
		}
	}
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
