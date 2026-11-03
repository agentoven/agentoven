package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/netguard"
)

// What a tool registration may carry and where it may point (review 3, finding 13).

// toolBody is a tool as a caller sends it, with `enabled` as a pointer: an absent field means "on" when
// a tool is created and "unchanged" when one is updated, and an explicit false means off in both.
type toolBody struct {
	models.MCPTool
	EnabledSet *bool `json:"enabled"`
}

func (b *toolBody) decode(data []byte) error {
	if err := json.Unmarshal(data, b); err != nil {
		return err
	}
	if b.EnabledSet != nil {
		b.MCPTool.Enabled = *b.EnabledSet
	}
	return nil
}

// enabledOnCreate is the state a new tool starts in: on unless the caller said false.
func (b *toolBody) enabledOnCreate() bool { return b.EnabledSet == nil || *b.EnabledSet }

// checkToolEndpoint refuses a tool endpoint that points at the control plane's own network (see
// pkg/netguard). A stdio tool has a command, not a URL, and a native tool none.
func (h *Handlers) checkToolEndpoint(ctx context.Context, kitchen, transport, endpoint string) error {
	if endpoint == "" || transport == "stdio" || transport == "native" {
		return nil
	}
	return netguard.Default().CheckURL(ctx, kitchen, endpoint)
}

// restoreMaskedAuth keeps the stored secret when a caller sends back what GET showed. Reading a tool
// returns its secrets as "****"; a client that reads, edits and writes the tool back sends that mask,
// and storing it would replace the real secret with four asterisks. Each masked value is replaced by the
// stored one (and dropped when there is none: a mask is never a value).
func restoreMaskedAuth(stored, incoming map[string]interface{}) map[string]interface{} {
	if incoming == nil {
		return nil
	}
	out := make(map[string]interface{}, len(incoming))
	for k, v := range incoming {
		if s, ok := v.(string); ok && models.IsMasked(s) {
			if old, had := stored[k]; had {
				out[k] = old
			}
			continue
		}
		out[k] = v
	}
	return out
}

// toolCredentialName is the credential that holds a tool's secret.
func toolCredentialName(tool string) string { return "tool-" + tool }

// literalSecret returns the authentication shape a literal secret in an auth config has: the type, the
// value, and the field it was in; ok is false when the config holds no literal secret this code can seal.
func literalSecret(cfg map[string]interface{}) (authType, value string, ok bool) {
	if ref, _ := cfg["credential_ref"].(string); ref != "" {
		return "", "", false
	}
	t, _ := cfg["type"].(string)
	if tok, _ := cfg["token"].(string); tok != "" && (t == "" || t == "bearer") {
		return "bearer", tok, true
	}
	if key, _ := cfg["key"].(string); key != "" && (t == "" || t == "api-key" || t == "api_key") {
		return "api-key", key, true
	}
	return "", "", false
}

// sealToolAuth moves a literal secret in a tool's authentication into the kitchen's credential vault and
// leaves a reference in its place, so the tool row, an export of it and a backup of the database hold
// no secret (the same rule as a provider's key). It does nothing when there is no vault (the community
// edition) or the config holds no literal secret, and running it again changes nothing. The credential
// written is returned so a caller whose save then fails can remove it.
func (h *Handlers) sealToolAuth(ctx context.Context, kitchen, tool string, cfg map[string]interface{}) (map[string]interface{}, string, error) {
	if h.SecretResolver == nil || h.keyVault() == nil {
		return cfg, "", nil
	}
	authType, value, ok := literalSecret(cfg)
	if !ok {
		return cfg, "", nil
	}
	name := toolCredentialName(tool)
	now := time.Now().UTC()
	cred, err := h.Store.GetKitchenCredential(ctx, kitchen, name)
	switch {
	case err == nil:
		cred.Value, cred.UpdatedAt = value, now
		if err := h.Store.UpdateKitchenCredential(ctx, cred); err != nil {
			return cfg, "", fmt.Errorf("could not store the secret of tool %q in the vault: %w", tool, err)
		}
		name = "" // rotated in place: nothing new to remove if the save fails
	default:
		if _, missing := err.(*store.ErrNotFound); !missing {
			return cfg, "", fmt.Errorf("could not store the secret of tool %q in the vault: %w", tool, err)
		}
		if err := h.Store.CreateKitchenCredential(ctx, &models.KitchenCredential{
			ID: uuid.NewString(), Kitchen: kitchen, Name: toolCredentialName(tool), Value: value,
			Label: "Authentication of tool " + tool, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return cfg, "", fmt.Errorf("could not store the secret of tool %q in the vault: %w", tool, err)
		}
	}
	sealed := map[string]interface{}{"type": authType, "credential_ref": toolCredentialName(tool)}
	if hdr, _ := cfg["header"].(string); hdr != "" {
		sealed["header"] = hdr
	}
	return sealed, name, nil
}

// dropToolCredential removes a credential sealToolAuth just created.
func (h *Handlers) dropToolCredential(ctx context.Context, kitchen, name string) {
	if name != "" {
		_ = h.Store.DeleteKitchenCredential(ctx, kitchen, name)
	}
}

// SealAllTools moves the literal secrets of every kitchen's tools into the vault, which is how tools
// registered before the vault sealed them are brought across (Pro, at startup behind the same flag as the
// provider keys, AGENTOVEN_SEAL_PROVIDER_KEYS). Moved and Failed name tools as "kitchen/tool". With
// dryRun it only lists them. Running it again moves nothing.
func (h *Handlers) SealAllTools(ctx context.Context, dryRun bool) (SealReport, error) {
	rep := SealReport{DryRun: dryRun, Moved: []string{}, Failed: map[string]string{}}
	if h.SecretResolver == nil || h.keyVault() == nil {
		return rep, errNoKeyVault
	}
	kitchens, err := h.Store.ListKitchens(ctx)
	if err != nil {
		return rep, err
	}
	for _, k := range kitchens {
		tools, err := h.Store.ListTools(ctx, k.Name)
		if err != nil {
			return rep, fmt.Errorf("kitchen %q: %w", k.Name, err)
		}
		for i := range tools {
			t := &tools[i]
			if _, _, ok := literalSecret(t.AuthConfig); !ok {
				continue
			}
			label := k.Name + "/" + t.Name
			if dryRun {
				rep.Moved = append(rep.Moved, label)
				continue
			}
			sealed, created, err := h.sealToolAuth(ctx, k.Name, t.Name, t.AuthConfig)
			if err == nil {
				t.AuthConfig = sealed
				t.UpdatedAt = time.Now().UTC()
				if err = h.Store.UpdateTool(ctx, t); err != nil {
					h.dropToolCredential(ctx, k.Name, created)
				}
			}
			if err != nil {
				rep.Failed[label] = err.Error()
				continue
			}
			rep.Moved = append(rep.Moved, label)
		}
	}
	return rep, nil
}
