package guardrails

import (
	"context"
	"fmt"
	"sync"

	"github.com/agentoven/agentoven/control-plane/internal/secretref"
	"github.com/agentoven/agentoven/control-plane/internal/secretseal"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A rule's secret (a llamaguard api_key) is kept in the kitchen's vault and the rule holds a reference
// (secretseal). Every list of rules that is about to be enforced comes out of Effective, so reading the
// references back here means no caller can enforce a rule that still holds one, and no stored or returned
// copy ever holds the value.

var (
	vaultMu     sync.RWMutex
	vaultSource func() secretref.Vault
)

// UseVault tells Effective where sealed rule secrets are read from. The source is asked on every call, so a
// vault installed after start-up (Pro) is picked up; the community edition has none and rules hold their values.
func UseVault(src func() secretref.Vault) {
	vaultMu.Lock()
	vaultSource = src
	vaultMu.Unlock()
}

func currentVault() secretref.Vault {
	vaultMu.RLock()
	defer vaultMu.RUnlock()
	if vaultSource == nil {
		return nil
	}
	return vaultSource()
}

// Effective returns the guardrails that apply to an agent: its own rules (own — its list, or that list
// adjusted by an environment policy) with the kitchen's workspace rules from src applied on top
// (MergeWithWorkspace), with the secrets sealed into them read back. With no source, or no workspace rules
// for the kitchen, it is own unchanged.
//
// It fails closed. If the source cannot be read, any enabled workspace rule is unreadable, or a rule's sealed
// secret cannot be read, it returns a *PolicyError and the caller must refuse to run the agent.
func Effective(ctx context.Context, src contracts.WorkspaceGuardrailSource, kitchen, agent string, own []models.Guardrail) ([]models.Guardrail, error) {
	rules, err := effective(ctx, src, kitchen, agent, own)
	if err != nil {
		return nil, err
	}
	v := currentVault()
	if v == nil {
		return rules, nil
	}
	var out []models.Guardrail
	for i, r := range rules {
		if len(secretseal.Refs(r.Config)) == 0 {
			continue
		}
		if out == nil {
			out = append([]models.Guardrail(nil), rules...)
		}
		cfg, err := secretseal.Unseal(ctx, v, kitchen, agent, r.Config)
		if err != nil {
			return nil, &PolicyError{Kitchen: kitchen, Agent: agent, Err: fmt.Errorf("rule %q: %w", r.ID, err)}
		}
		out[i].Config = cfg
	}
	if out == nil {
		return rules, nil
	}
	return out, nil
}
