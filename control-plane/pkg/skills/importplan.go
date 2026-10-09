package skills

import (
	"fmt"
	"path"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// MaxImportSkills bounds how many skills one import takes, since each is reviewed by a
// model. A larger plugin is imported in parts with "only".
const MaxImportSkills = 40

// ImportServer is one of a plugin's MCP servers, as an import will treat it.
type ImportServer struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	// NeedsCredential is set when the server wants a key and none was named for it.
	NeedsCredential bool `json:"needs_credential,omitempty"`
}

// ImportItem is one skill an import will register.
type ImportItem struct {
	Name string
	// Ref is recorded as the skill's source reference: "<git url>@<pin>#<path>".
	Ref string
	// Load reads the skill's bundle.
	Load func() (Bundle, error)
}

// ImportPlan is what importing a plugin would register. It is the dry-run answer, and
// Items is what a real import then registers (or, in Pro, stages).
type ImportPlan struct {
	Plugin  map[string]string `json:"plugin"`
	Skills  []PluginSkill     `json:"skills"`
	Servers []ImportServer    `json:"servers"`
	// ServerSkill names the skill that carries the plugin's MCP servers, as "only" takes it.
	ServerSkill string       `json:"server_skill,omitempty"`
	Skipped     []Skipped    `json:"skipped"`
	Items       []ImportItem `json:"-"`
}

// PlanImport decides what to import from a plugin read from root: only (skill names, and
// the server skill's name; empty means all) narrows it, and credentials maps an MCP server
// to the kitchen credential that authenticates it. A server that needs a key and has none
// is left out of a real import, and flagged in a dry run so the caller can supply one.
func (p *Plugin) PlanImport(spec ImportSpec, root string, only []string, credentials map[string]string, dryRun bool) (*ImportPlan, error) {
	serverSkillName, _ := p.ServerSkill()

	// The skill that carries the servers is picked by name like any other.
	includeServers, wanted := len(only) == 0, []string(nil)
	for _, n := range only {
		if n == serverSkillName {
			includeServers = true
		} else {
			wanted = append(wanted, n)
		}
	}
	chosen := p.Skills
	switch {
	case len(only) > 0 && len(wanted) == 0:
		chosen = nil // only the MCP servers were asked for
	case len(wanted) > 0:
		var err error
		if chosen, err = chooseSkills(p.Skills, wanted); err != nil {
			return nil, err
		}
	}
	if len(chosen) > MaxImportSkills {
		return nil, fmt.Errorf("the plugin has %d skills, more than the %d one import takes; pass \"only\" with the ones you want", len(chosen), MaxImportSkills)
	}

	plan := &ImportPlan{
		Plugin: map[string]string{
			"name": p.Name, "description": p.Description, "version": p.Version,
			"license": p.License, "format": p.Format,
		},
		Skills:  chosen,
		Skipped: append([]Skipped(nil), p.Skipped...),
	}
	if len(p.Servers) > 0 {
		plan.ServerSkill = serverSkillName
	}

	var usable []models.SkillMCPServer
	for _, srv := range p.Servers {
		needs := srv.AuthType != "" && credentials[srv.Name] == ""
		plan.Servers = append(plan.Servers, ImportServer{Name: srv.Name, Endpoint: srv.Endpoint, NeedsCredential: needs})
		switch {
		case !needs:
			srv.CredentialRef = credentials[srv.Name]
			usable = append(usable, srv)
		case !dryRun:
			plan.Skipped = append(plan.Skipped, Skipped{
				Component: "mcp_server", Name: srv.Name,
				Reason: fmt.Sprintf("needs a credential; pass credentials[%q] naming a kitchen credential", srv.Name),
			})
		}
	}

	ref := func(dir string) string {
		pin := spec.GitSHA
		if pin == "" {
			pin = spec.GitRef
		}
		r := spec.GitURL
		if pin != "" {
			r += "@" + pin
		}
		return r + "#" + strings.TrimPrefix(path.Join(spec.Path, dir), "/")
	}
	for _, s := range chosen {
		s := s
		plan.Items = append(plan.Items, ImportItem{Name: s.Name, Ref: ref(s.Dir), Load: func() (Bundle, error) { return p.LoadSkill(root, s) }})
	}
	if includeServers && len(usable) > 0 {
		withServers := *p
		withServers.Servers = usable
		name, bundle := withServers.ServerSkill()
		plan.Items = append(plan.Items, ImportItem{Name: name, Ref: ref(".mcp"), Load: func() (Bundle, error) { return bundle, nil }})
	}
	return plan, nil
}

// chooseSkills filters a plugin's skills to the names asked for.
func chooseSkills(all []PluginSkill, only []string) ([]PluginSkill, error) {
	want := map[string]bool{}
	for _, n := range only {
		want[n] = true
	}
	var out []PluginSkill
	for _, s := range all {
		if want[s.Name] {
			out = append(out, s)
			delete(want, s.Name)
		}
	}
	if len(want) > 0 {
		var missing []string
		for n := range want {
			missing = append(missing, n)
		}
		return nil, fmt.Errorf("the plugin has no skill named %s", strings.Join(missing, ", "))
	}
	return out, nil
}
