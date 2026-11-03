package resolver

import (
	"context"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/refcheck"
)

// LoadCatalog reads what one kitchen has into the snapshot that pkg/refcheck checks a configuration
// against. Every list is the kitchen's own: another kitchen's provider, tool or agent is simply absent, so
// a reference to it is "does not exist" like any other. ext (nil in the community edition) adds what only
// Pro can list; when it cannot, those lists stay unknown and are not checked, never assumed empty.
//
// An error means the kitchen could not be read; a caller must not treat that as "nothing exists".
func LoadCatalog(ctx context.Context, st store.Store, kitchen string, ext contracts.KitchenReferences) (*refcheck.Catalog, error) {
	c := &refcheck.Catalog{Prompts: map[string]int{}}
	fail := func(what string, err error) (*refcheck.Catalog, error) {
		return nil, fmt.Errorf("read the kitchen's %s: %w", what, err)
	}

	providers, err := st.ListProviders(ctx, kitchen)
	if err != nil {
		return fail("providers", err)
	}
	models.SortProvidersForSelection(providers)
	for _, p := range providers {
		c.Providers = append(c.Providers, refcheck.Provider{Name: p.Name, Kind: p.Kind, Models: p.Models, Healthy: p.LastTestHealthy})
	}
	prompts, err := st.ListPrompts(ctx, kitchen)
	if err != nil {
		return fail("prompts", err)
	}
	for _, p := range prompts {
		c.Prompts[p.Name] = p.Version
	}
	tools, err := st.ListTools(ctx, kitchen)
	if err != nil {
		return fail("tools", err)
	}
	for _, t := range tools {
		c.Tools = append(c.Tools, refcheck.Tool{Name: t.Name, Enabled: t.Enabled, Capabilities: t.Capabilities})
	}
	skills, err := st.ListSkills(ctx, kitchen)
	if err != nil {
		return fail("skills", err)
	}
	for _, s := range skills {
		c.Skills = append(c.Skills, refcheck.Skill{Name: s.Name, Status: string(s.Status)})
	}
	agents, err := st.ListAgents(ctx, kitchen)
	if err != nil {
		return fail("agents", err)
	}
	for _, a := range agents {
		c.Agents = append(c.Agents, refcheck.Agent{Name: a.Name, Status: string(a.Status)})
	}
	recipes, err := st.ListRecipes(ctx, kitchen)
	if err != nil {
		return fail("recipes", err)
	}
	for _, r := range recipes {
		if !r.IsArchived() { // an archived recipe cannot run
			c.Recipes = append(c.Recipes, r.Name)
		}
	}
	channels, err := st.ListChannels(ctx, kitchen)
	if err != nil {
		return fail("channels", err)
	}
	for _, ch := range channels {
		c.Channels = append(c.Channels, ch.Name)
	}
	envs, err := st.ListEnvironments(ctx, kitchen)
	if err != nil {
		return fail("environments", err)
	}
	for _, e := range envs {
		c.Environments = append(c.Environments, e.Slug)
	}
	creds, err := st.ListKitchenCredentials(ctx, kitchen)
	if err != nil {
		return fail("credentials", err)
	}
	for _, cr := range creds {
		c.Credentials = append(c.Credentials, cr.Name)
	}

	if ext != nil {
		if names, err := ext.Scenarios(ctx, kitchen); err == nil {
			c.Scenarios = refcheck.Names{Known: true, Names: names}
		}
		if names, err := ext.Roles(ctx, kitchen); err == nil {
			c.Roles = refcheck.Names{Known: true, Names: names}
		}
	}
	return c, nil
}
