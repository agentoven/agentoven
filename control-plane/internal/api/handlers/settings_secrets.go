package handlers

import (
	"context"
	"errors"

	"github.com/agentoven/agentoven/control-plane/internal/secretref"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// The key of a kitchen's validation model is kept in the vault like a provider's: the settings hold the
// reference and the prompt validators read the value through resolvedKitchenSettings. The community edition
// has no vault and the key stays in the settings as before.

// validationKeyCredential is the one vault credential a kitchen's settings may point at. It is the only
// reference resolved from the settings: a Chef who can edit them must not be able to name another credential
// of the kitchen and have its value sent to the validation endpoint they chose.
const validationKeyCredential = "kitchen-validation-key"

// sealValidationKey moves a plain-text validation key into the vault and leaves the reference. It reports
// whether the key was moved, so a caller whose save then fails can remove the credential.
func (h *Handlers) sealValidationKey(ctx context.Context, kitchen string, s *models.KitchenSettings) (moved bool, err error) {
	v := h.settingsVault()
	if v == nil || s.ValidationAPIKey == "" || models.IsMasked(s.ValidationAPIKey) || models.IsReference(s.ValidationAPIKey) {
		return false, nil
	}
	if err := v.Write(ctx, kitchen, validationKeyCredential, s.ValidationAPIKey, "Validation model key"); err != nil {
		return false, errors.New("the validation key could not be stored in the vault")
	}
	s.ValidationAPIKey = secretref.VaultRef(validationKeyCredential)
	return true, nil
}

func (h *Handlers) settingsVault() secretref.Vault {
	if h.SecretResolver == nil {
		return nil
	}
	return h.keyVault()
}

// resolvedKitchenSettings is the kitchen's settings with the validation key's reference replaced by its value,
// for the code that calls the validation model. Never return what it gives back to a client.
func (h *Handlers) resolvedKitchenSettings(ctx context.Context, kitchen string) (*models.KitchenSettings, error) {
	s, err := h.Store.GetKitchenSettings(ctx, kitchen)
	if err != nil || s == nil || s.ValidationAPIKey != secretref.VaultRef(validationKeyCredential) {
		return s, err
	}
	v := h.settingsVault()
	if v == nil {
		return s, nil
	}
	cp := *s
	if cp.ValidationAPIKey, err = v.Read(ctx, kitchen, validationKeyCredential); err != nil {
		cp.ValidationAPIKey = "" // no key is a validator that falls back to the built-in checks, not one that sends a reference
	}
	return &cp, nil
}

// SealAllKitchenSettings moves validation keys stored in plain text into the vault, in every kitchen.
func (h *Handlers) SealAllKitchenSettings(ctx context.Context, dryRun bool) (SealReport, error) {
	rep := SealReport{DryRun: dryRun, Moved: []string{}, Failed: map[string]string{}}
	if h.settingsVault() == nil {
		return rep, errNoKeyVault
	}
	kitchens, err := h.Store.ListKitchens(ctx)
	if err != nil {
		return rep, err
	}
	for _, k := range kitchens {
		s, err := h.Store.GetKitchenSettings(ctx, k.Name)
		if err != nil || s == nil || s.ValidationAPIKey == "" || models.IsReference(s.ValidationAPIKey) {
			continue
		}
		if dryRun {
			rep.Moved = append(rep.Moved, k.Name+"/settings")
			continue
		}
		cp := *s
		moved, err := h.sealValidationKey(ctx, k.Name, &cp)
		if err == nil && moved {
			err = h.Store.UpsertKitchenSettings(ctx, &cp)
		}
		if err != nil {
			rep.Failed[k.Name+"/settings"] = err.Error()
			continue
		}
		rep.Moved = append(rep.Moved, k.Name+"/settings")
	}
	return rep, nil
}
