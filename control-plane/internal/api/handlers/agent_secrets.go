package handlers

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/internal/secretseal"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// What an agent's guardrail rules and ingredients hold in their configs that is secret (a llamaguard rule's
// api_key, a vector store's password, a URL with a token in it) is kept in the kitchen's vault and the agent
// holds a reference, as a provider's key and a tool's authentication do (provider_vault.go). The API masks
// the same values (models.RedactConfig) and secretseal uses the same definition of "secret", so what a
// reader cannot see is also not kept in the clear.
//
// Not covered: the baked configuration (ResolvedConfig) is what the agent runs on and holds the values
// the resolver read back; it is never returned by the API but it is stored (ADR-0049).

// agentConfigs calls f for every config of the agent that can hold a secret, with the scope that names it:
// the agent's rules and ingredients, and those of its draft (kept apart, so editing the draft cannot change
// the credential the baked configuration still uses).
func agentConfigs(a *models.Agent, f func(scope string, cfg *map[string]interface{})) {
	rules := func(prefix string, list []models.Guardrail) {
		for i := range list {
			id := list[i].ID
			if id == "" {
				id = fmt.Sprintf("#%d", i)
			}
			f(prefix+"guardrail:"+id, &list[i].Config)
		}
	}
	ingredients := func(prefix string, list []models.Ingredient) {
		for i := range list {
			f(prefix+"ingredient:"+string(list[i].Kind)+":"+list[i].Name, &list[i].Config)
		}
	}
	rules("", a.Guardrails)
	ingredients("", a.Ingredients)
	if a.Draft != nil {
		rules("draft:", a.Draft.Guardrails)
		ingredients("draft:", a.Draft.Ingredients)
	}
}

// agentSecretRefs lists the credentials the agent's configs point at.
func agentSecretRefs(a *models.Agent) []string {
	var refs []string
	agentConfigs(a, func(_ string, cfg *map[string]interface{}) { refs = append(refs, secretseal.Refs(*cfg)...) })
	return refs
}

// ownAgentCopy makes the slices the sealer rewrites in place the agent's own, so an agent a store handed out
// (which the store may share) is never changed through them.
func ownAgentCopy(a *models.Agent) {
	a.Guardrails = append([]models.Guardrail(nil), a.Guardrails...)
	a.Ingredients = append([]models.Ingredient(nil), a.Ingredients...)
	if a.Draft != nil {
		d := *a.Draft
		d.Guardrails = append([]models.Guardrail(nil), d.Guardrails...)
		d.Ingredients = append([]models.Ingredient(nil), d.Ingredients...)
		a.Draft = &d
	}
}

// sealAgentSecrets moves the secrets in the agent's rules and ingredients (and its draft's) into the vault.
// It changes the agent in place, returns the credentials it wrote (so a caller whose own save then fails can
// remove them), and does nothing when there is no vault or nothing to move. Running it again changes nothing.
func (h *Handlers) sealAgentSecrets(ctx context.Context, a *models.Agent) (written []string, err error) {
	if h.SecretResolver == nil || h.keyVault() == nil {
		return nil, nil
	}
	v := h.keyVault()
	ownAgentCopy(a)
	agentConfigs(a, func(scope string, cfg *map[string]interface{}) {
		if err != nil || *cfg == nil {
			return
		}
		sealed, w, e := secretseal.Seal(ctx, v, a.Kitchen, a.Name, scope, *cfg)
		written = append(written, w...)
		if e != nil {
			err = e
			return
		}
		*cfg = sealed
	})
	return written, err
}

// reapAgentSecrets deletes the credentials the agent pointed at before a change and no longer does.
func (h *Handlers) reapAgentSecrets(ctx context.Context, a *models.Agent, before []string) {
	if h.SecretResolver == nil || h.keyVault() == nil || len(before) == 0 {
		return
	}
	if err := secretseal.Reap(ctx, h.keyVault(), a.Kitchen, a.Name, before, agentSecretRefs(a)); err != nil {
		log.Warn().Err(err).Str("agent", a.Name).Msg("a credential an agent no longer uses could not be removed from the vault")
	}
}

// dropCredentials removes credentials written for a save that then failed, so a refused save leaves nothing.
// A credential in keep is one the stored agent already points at: it was rewritten in place and stays.
func (h *Handlers) dropCredentials(ctx context.Context, kitchen string, names, keep []string) {
	if len(names) == 0 || h.keyVault() == nil {
		return
	}
	held := map[string]bool{}
	for _, n := range keep {
		held[n] = true
	}
	for _, n := range names {
		if held[n] {
			continue
		}
		if err := h.keyVault().Delete(ctx, kitchen, n); err != nil {
			log.Warn().Err(err).Str("credential", n).Msg("a credential written for a save that failed could not be removed")
		}
	}
}

// SealAllAgents moves the secrets that agents made before the vault (or before this) still hold in plain
// text into the vault, in every kitchen. With dryRun it only reports which agents it would change. It is the
// migration for agents stored before sealing existed, and it is safe to run again.
func (h *Handlers) SealAllAgents(ctx context.Context, dryRun bool) (SealReport, error) {
	rep := SealReport{DryRun: dryRun, Moved: []string{}, Failed: map[string]string{}}
	if h.SecretResolver == nil || h.keyVault() == nil {
		return rep, errNoKeyVault
	}
	kitchens, err := h.Store.ListKitchens(ctx)
	if err != nil {
		return rep, err
	}
	for _, k := range kitchens {
		agents, err := h.Store.ListAgents(ctx, k.Name)
		if err != nil {
			return rep, fmt.Errorf("kitchen %q: %w", k.Name, err)
		}
		for i := range agents {
			a := agents[i]
			label := k.Name + "/" + a.Name
			if !agentHoldsLiteralSecret(&a) {
				continue
			}
			if dryRun {
				rep.Moved = append(rep.Moved, label)
				continue
			}
			held := agentSecretRefs(&a)
			written, err := h.sealAgentSecrets(ctx, &a)
			if err == nil {
				err = h.Store.UpdateAgent(ctx, &a)
			}
			if err != nil {
				h.dropCredentials(ctx, a.Kitchen, written, held)
				rep.Failed[label] = err.Error()
				continue
			}
			rep.Moved = append(rep.Moved, label)
		}
	}
	return rep, nil
}

// agentHoldsLiteralSecret says whether any config of the agent still holds a secret in the clear, which is
// what sealing would move: the same test, run against a vault that stores nothing.
func agentHoldsLiteralSecret(a *models.Agent) bool {
	cp := *a
	ownAgentCopy(&cp)
	found := false
	agentConfigs(&cp, func(scope string, cfg *map[string]interface{}) {
		if found || *cfg == nil {
			return
		}
		if _, w, _ := secretseal.Seal(context.Background(), dryRunVault{}, a.Kitchen, a.Name, scope, *cfg); len(w) > 0 {
			found = true
		}
	})
	return found
}
