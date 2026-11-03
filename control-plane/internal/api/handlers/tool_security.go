package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/netguard"
	"github.com/agentoven/agentoven/control-plane/pkg/toolauth"
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

// sealToolAuth checks a tool's authentication and moves a literal secret in it into the kitchen's credential
// vault, leaving a reference in its place, so the tool row, an export of it and a backup of the database hold
// no secret (the same rule as a provider's key). The check comes first and applies in every edition: a shape
// this code cannot send (OAuth client credentials, a set of custom headers, a field no type takes) is refused
// with a 400 (*toolauth.InvalidError), so no secret is stored where nothing reads or seals it. Sealing does
// nothing when there is no vault (the community edition) or the config holds no literal secret, and running
// it again changes nothing. The credential written is returned so a caller whose save then fails can remove it.
func (h *Handlers) sealToolAuth(ctx context.Context, kitchen, tool string, cfg map[string]interface{}) (map[string]interface{}, string, error) {
	if err := toolauth.Validate(cfg); err != nil {
		return cfg, "", err
	}
	if h.SecretResolver == nil || h.keyVault() == nil {
		return cfg, "", nil
	}
	authType, _, value, ok := toolauth.LiteralSecret(cfg)
	if !ok {
		return cfg, "", nil
	}
	_, created, err := h.writeToolCredential(ctx, kitchen, tool, toolCredentialName(tool), "Authentication of tool "+tool, value)
	if err != nil {
		return cfg, "", err
	}
	sealed := map[string]interface{}{"type": authType, "credential_ref": toolCredentialName(tool)}
	for _, k := range []string{"header", "username"} { // names, not secrets: they stay where they are
		if v, _ := cfg[k].(string); v != "" {
			sealed[k] = v
		}
	}
	return sealed, created, nil
}

// SealToolAuth is sealToolAuth for a caller outside the handlers that registers a tool itself (Pro's
// evaluation installer), so a tool it writes holds a reference and not the secret.
func (h *Handlers) SealToolAuth(ctx context.Context, kitchen, tool string, cfg map[string]interface{}) (sealed map[string]interface{}, createdCredential string, err error) {
	return h.sealToolAuth(ctx, kitchen, tool, cfg)
}

// writeToolCredential stores value as the credential name in the kitchen: created if there is none, rotated
// in place if there is. It returns name and, when it created one, the name again as the second value so a
// failed save can remove it ("" when it only rotated).
func (h *Handlers) writeToolCredential(ctx context.Context, kitchen, tool, name, label, value string) (credName, created string, err error) {
	now := time.Now().UTC()
	cred, err := h.Store.GetKitchenCredential(ctx, kitchen, name)
	switch {
	case err == nil:
		cred.Value, cred.UpdatedAt = value, now
		if err := h.Store.UpdateKitchenCredential(ctx, cred); err != nil {
			return "", "", fmt.Errorf("could not store the secret of tool %q in the vault: %w", tool, err)
		}
		return name, "", nil // rotated in place: nothing new to remove if the save fails
	default:
		if _, missing := err.(*store.ErrNotFound); !missing {
			return "", "", fmt.Errorf("could not store the secret of tool %q in the vault: %w", tool, err)
		}
		if err := h.Store.CreateKitchenCredential(ctx, &models.KitchenCredential{
			ID: uuid.NewString(), Kitchen: kitchen, Name: name, Value: value, Label: label, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return "", "", fmt.Errorf("could not store the secret of tool %q in the vault: %w", tool, err)
		}
	}
	return name, name, nil
}

// strayCredentialName is the credential that holds a field an older registration kept in a tool's
// authentication that this code does not use: "tool-<tool>-<field>", with the field made safe as a name.
func strayCredentialName(tool, field string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, field)
	return toolCredentialName(tool) + "-" + safe
}

// sealStrayFields is the migration of a tool whose authentication has a shape Validate now refuses: every
// field with a value other than its type and names moves into its own credential and the config keeps
// "<field>_ref" naming it, so the tool's row holds no secret and nothing is lost. The tool's authentication
// stays unsupported (it is refused if saved again, and nothing sends it); the owner re-registers it as a
// bearer, api-key or basic tool, or removes it. Returns the new config and the credentials it created.
func (h *Handlers) sealStrayFields(ctx context.Context, kitchen, tool string, cfg map[string]interface{}) (map[string]interface{}, []string, error) {
	strays := toolauth.StrayFields(cfg)
	if len(strays) == 0 {
		return cfg, nil, nil
	}
	// A supported shape that carries a stray field beside its own secret (a bearer with a client secret) keeps
	// working: its token goes to the tool's credential as it would on a save, and only the rest is stray.
	authType, primary, value, hasPrimary := toolauth.LiteralSecret(cfg)
	if hasPrimary {
		delete(strays, primary)
	}
	out := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		if _, stray := strays[k]; !stray && !(hasPrimary && k == primary) {
			out[k] = v
		}
	}
	var created []string
	if hasPrimary {
		_, made, err := h.writeToolCredential(ctx, kitchen, tool, toolCredentialName(tool), "Authentication of tool "+tool, value)
		if made != "" {
			created = append(created, made)
		}
		if err != nil {
			return cfg, created, err
		}
		out["type"], out["credential_ref"] = authType, toolCredentialName(tool)
	}
	for field, value := range strays {
		name, made, err := h.writeToolCredential(ctx, kitchen, tool, strayCredentialName(tool, field), "Authentication field "+field+" of tool "+tool, value)
		if made != "" {
			created = append(created, made)
		}
		if err != nil {
			return cfg, created, err
		}
		out[field+"_ref"] = name
	}
	return out, created, nil
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
			label := k.Name + "/" + t.Name
			unsupported := toolauth.Validate(t.AuthConfig) != nil
			if _, _, _, literal := toolauth.LiteralSecret(t.AuthConfig); !unsupported && !literal {
				continue
			}
			if unsupported && len(toolauth.StrayFields(t.AuthConfig)) == 0 {
				continue // nothing in it to move
			}
			if dryRun {
				rep.Moved = append(rep.Moved, label)
				continue
			}
			var sealed map[string]interface{}
			var created []string
			var err error
			if unsupported {
				sealed, created, err = h.sealStrayFields(ctx, k.Name, t.Name, t.AuthConfig)
			} else {
				var c string
				sealed, c, err = h.sealToolAuth(ctx, k.Name, t.Name, t.AuthConfig)
				if c != "" {
					created = []string{c}
				}
			}
			if err == nil {
				t.AuthConfig = sealed
				t.UpdatedAt = time.Now().UTC()
				err = h.Store.UpdateTool(ctx, t)
			}
			if err != nil {
				for _, c := range created {
					h.dropToolCredential(ctx, k.Name, c)
				}
				rep.Failed[label] = err.Error()
				continue
			}
			rep.Moved = append(rep.Moved, label)
		}
	}
	return rep, nil
}
