package skills_test

import (
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/skills"
)

func planFixture(t *testing.T) (*skills.Plugin, string) {
	t.Helper()
	root := t.TempDir()
	write(t, root, map[string]string{
		".claude-plugin/plugin.json": `{"name":"acme"}`,
		"skills/a/SKILL.md":          skill("a"),
		"skills/b/SKILL.md":          skill("b"),
		".mcp.json": `{"mcpServers":{
			"open":{"type":"http","url":"https://o.test/mcp"},
			"keyed":{"type":"http","url":"https://k.test/mcp","headers":{"Authorization":"Bearer x"}}}}`,
	})
	p, err := skills.ParsePlugin(root, skills.PluginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return p, root
}

func names(items []skills.ImportItem) string {
	var n []string
	for _, i := range items {
		n = append(n, i.Name)
	}
	return strings.Join(n, ",")
}

func TestPlanImportAllSkillsAndTheUsableServers(t *testing.T) {
	p, root := planFixture(t)
	spec := skills.ImportSpec{GitURL: "https://example.com/acme.git", GitSHA: "abc", Path: "plugins/acme"}

	plan, err := p.PlanImport(spec, root, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// "keyed" needs a credential and has none, so only "open" is registered, and it says so.
	if names(plan.Items) != "a,b,acme" || plan.ServerSkill != "acme" {
		t.Errorf("items = %s, server skill = %q", names(plan.Items), plan.ServerSkill)
	}
	var skipped bool
	for _, s := range plan.Skipped {
		skipped = skipped || (s.Component == "mcp_server" && s.Name == "keyed")
	}
	if !skipped {
		t.Errorf("skipped = %+v", plan.Skipped)
	}
	if plan.Items[0].Ref != "https://example.com/acme.git@abc#plugins/acme/skills/a" {
		t.Errorf("ref = %q", plan.Items[0].Ref)
	}
	b, err := plan.Items[0].Load()
	if err != nil || len(b) == 0 {
		t.Errorf("load: %v %v", b, err)
	}
}

func TestPlanImportDryRunFlagsServersThatNeedAKeyInsteadOfSkippingThem(t *testing.T) {
	p, root := planFixture(t)
	plan, err := p.PlanImport(skills.ImportSpec{GitURL: "https://x.test/a.git"}, root, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	needs := map[string]bool{}
	for _, s := range plan.Servers {
		needs[s.Name] = s.NeedsCredential
	}
	if !needs["keyed"] || needs["open"] {
		t.Errorf("servers = %+v", plan.Servers)
	}
	for _, s := range plan.Skipped {
		if s.Component == "mcp_server" {
			t.Errorf("a dry run must not skip a server that only needs a key: %+v", s)
		}
	}
}

func TestPlanImportWithACredentialRegistersTheServerAndRecordsItsName(t *testing.T) {
	p, root := planFixture(t)
	plan, err := p.PlanImport(skills.ImportSpec{GitURL: "https://x.test/a.git"}, root, []string{"acme"}, map[string]string{"keyed": "acme-key"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if names(plan.Items) != "acme" || len(plan.Skills) != 0 {
		t.Fatalf("only the server skill was asked for: %s", names(plan.Items))
	}
	b, _ := plan.Items[0].Load()
	m, err := b.Manifest()
	if err != nil || len(m.MCPServers) != 2 {
		t.Fatalf("%+v %v", m, err)
	}
	for _, s := range m.MCPServers {
		if s.Name == "keyed" && s.CredentialRef != "acme-key" {
			t.Errorf("the credential's name must travel in the manifest so a later step needs no map: %+v", s)
		}
	}
}

func TestPlanImportOnlyAndLimits(t *testing.T) {
	p, root := planFixture(t)
	spec := skills.ImportSpec{GitURL: "https://x.test/a.git"}
	plan, err := p.PlanImport(spec, root, []string{"b"}, nil, false)
	if err != nil || names(plan.Items) != "b" {
		t.Fatalf("%v %v", plan, err)
	}
	if _, err := p.PlanImport(spec, root, []string{"nope"}, nil, false); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("unknown skill: %v", err)
	}

	big := *p
	for i := 0; i <= skills.MaxImportSkills; i++ {
		big.Skills = append(big.Skills, skills.PluginSkill{Name: "s" + strings.Repeat("x", i), Dir: "skills/a"})
	}
	if _, err := big.PlanImport(spec, root, nil, nil, true); err == nil || !strings.Contains(err.Error(), "only") {
		t.Errorf("too many skills: %v", err)
	}
}
