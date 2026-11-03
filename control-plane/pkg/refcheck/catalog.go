package refcheck

import (
	"sort"
	"strings"
)

// OpenModelKinds are the provider kinds that serve models their list does not name: OpenRouter and
// LiteLLM route to whatever the account can reach, Ollama serves whatever was pulled. A model id that is
// not in such a provider's list is accepted (with a warning), because the list is only what was written
// down, not what exists. For every other kind the list is the whole truth, unless it is empty (a provider
// registered without one accepts any id, as bakes always have).
var OpenModelKinds = map[string]bool{"openrouter": true, "litellm": true, "ollama": true}

// Provider is a model provider as the checks see it.
type Provider struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	Models []string `json:"models"`
	// Healthy is the result of the provider's last connectivity test; nil when it was never tested.
	Healthy *bool `json:"healthy,omitempty"`
}

// Open reports whether the provider may serve a model id that is not in its list.
func (p Provider) Open() bool { return OpenModelKinds[p.Kind] || len(p.Models) == 0 }

// Has reports whether the model is in the provider's list.
func (p Provider) Has(model string) bool {
	for _, m := range p.Models {
		if m == model {
			return true
		}
	}
	return false
}

// Tool is a registered tool.
type Tool struct {
	Name         string   `json:"name"`
	Enabled      bool     `json:"enabled"`
	Capabilities []string `json:"capabilities"`
}

// Skill is a registered skill. Status is "accepted" for the only state that may run.
type Skill struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Agent is an agent as a reference sees it.
type Agent struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Names is a set of names an edition may or may not be able to list: the community edition has no
// scenarios and no roles, so Known is false there and a reference to one is not checked rather than
// refused.
type Names struct {
	Known bool     `json:"known"`
	Names []string `json:"names"`
}

// Catalog is a snapshot of what one kitchen has: the names a configuration may refer to.
type Catalog struct {
	Providers []Provider `json:"providers"`
	// Prompts maps a prompt's name to its latest version.
	Prompts      map[string]int `json:"prompts"`
	Tools        []Tool         `json:"tools"`
	Skills       []Skill        `json:"skills"`
	Agents       []Agent        `json:"agents"`
	Recipes      []string       `json:"recipes"`
	Channels     []string       `json:"channels"`
	Environments []string       `json:"environments"`
	Credentials  []string       `json:"credentials"`
	Scenarios    Names          `json:"scenarios"`
	Roles        Names          `json:"roles"`
}

func (c *Catalog) provider(name string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

func (c *Catalog) providerNames() []string {
	out := make([]string, len(c.Providers))
	for i, p := range c.Providers {
		out[i] = p.Name
	}
	return sorted(out)
}

// allModels lists every model of every provider (sorted, once each), for a reference that names a model
// without saying whose.
func (c *Catalog) allModels() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range c.Providers {
		for _, m := range p.Models {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return sorted(out)
}

func (c *Catalog) tool(name string) (Tool, bool) {
	for _, t := range c.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

func (c *Catalog) toolNames(capability string) []string {
	var out []string
	for _, t := range c.Tools {
		if capability == "" || hasCap(t.Capabilities, capability) {
			out = append(out, t.Name)
		}
	}
	return sorted(out)
}

func (c *Catalog) skill(name string) (Skill, bool) {
	for _, s := range c.Skills {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

func (c *Catalog) skillNames() []string {
	out := make([]string, len(c.Skills))
	for i, s := range c.Skills {
		out[i] = s.Name
	}
	return sorted(out)
}

func (c *Catalog) agent(name string) (Agent, bool) {
	for _, a := range c.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

func (c *Catalog) agentNames() []string {
	var out []string
	for _, a := range c.Agents {
		if a.Status != "retired" { // a retired agent is not offered as something to pick
			out = append(out, a.Name)
		}
	}
	return sorted(out)
}

func (c *Catalog) promptNames() []string {
	out := make([]string, 0, len(c.Prompts))
	for n := range c.Prompts {
		out = append(out, n)
	}
	return sorted(out)
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func sorted(ss []string) []string {
	out := append([]string(nil), ss...)
	sort.Strings(out)
	return out
}
