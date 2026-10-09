package skills_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/skills"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func skill(name string) string {
	return "---\nname: " + name + "\ndescription: Does " + name + ".\n---\nBody of " + name + ".\n"
}

func skillNames(p *skills.Plugin) string {
	var n []string
	for _, s := range p.Skills {
		n = append(n, s.Name)
	}
	return strings.Join(n, ",")
}

func TestParsePluginClaudeLayout(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".claude-plugin/plugin.json": `{"name":"deploy-tools","description":"Deploys things","version":"1.2.0","license":"MIT"}`,
		"skills/deploy/SKILL.md":     skill("deploy"),
		"skills/deploy/scripts/x.sh": "echo hi",
		"skills/rollback/SKILL.md":   skill("rollback"),
		"commands/status.md":         "status",
		"agents/reviewer.md":         "reviewer",
		"hooks/hooks.json":           "{}",
		".mcp.json": `{"mcpServers":{
			"api":{"type":"http","url":"https://mcp.example.com/mcp"},
			"local":{"command":"node","args":["server.js"]},
			"legacy":{"type":"sse","url":"https://mcp.example.com/sse"}}}`,
	})

	p, err := skills.ParsePlugin(root, skills.PluginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Format != "claude" || p.Name != "deploy-tools" || p.Version != "1.2.0" || p.License != "MIT" {
		t.Errorf("plugin = %+v", p)
	}
	if skillNames(p) != "deploy,rollback" {
		t.Errorf("skills = %s", skillNames(p))
	}
	if len(p.Servers) != 1 || p.Servers[0].Name != "api" || p.Servers[0].Transport != "mcp" || p.Servers[0].Endpoint != "https://mcp.example.com/mcp" {
		t.Errorf("servers = %+v", p.Servers)
	}

	skipped := map[string]string{}
	for _, s := range p.Skipped {
		skipped[s.Component+":"+s.Name] = s.Reason
	}
	for _, want := range []string{"mcp_server:local", "mcp_server:legacy", "commands:1", "agents:1", "hooks:"} {
		if _, ok := skipped[want]; !ok {
			t.Errorf("missing skipped %q in %v", want, skipped)
		}
	}

	// A skill loads as an ordinary bundle rooted at its own directory.
	b, err := p.LoadSkill(root, p.Skills[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b["SKILL.md"]; !ok || string(b["scripts/x.sh"]) != "echo hi" {
		t.Errorf("bundle files = %v", keys(b))
	}
}

func keys(b skills.Bundle) []string {
	var k []string
	for f := range b {
		k = append(k, f)
	}
	return k
}

func TestParsePluginCodexLayout(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".codex-plugin/plugin.json": `{"name":"vercel","description":"Deploy to Vercel","skills":"./skills","apps":"./.app.json"}`,
		".app.json":                 `{"apps":{"vercel":{"id":"x"}}}`,
		"skills/deploy/SKILL.md":    skill("vercel-deploy"),
		".mcp.json":                 `{"mcpServers":{"vercel":{"type":"http","url":"https://mcp.vercel.com"}}}`,
	})
	p, err := skills.ParsePlugin(root, skills.PluginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Format != "codex" || skillNames(p) != "vercel-deploy" || len(p.Servers) != 1 {
		t.Errorf("plugin = %+v", p)
	}
	if len(p.Skills) != 1 {
		t.Errorf("a skills dir named twice (default and manifest) must be read once: %v", p.Skills)
	}
	var sawApps bool
	for _, s := range p.Skipped {
		sawApps = sawApps || s.Component == "apps"
	}
	if !sawApps {
		t.Errorf("skipped = %+v", p.Skipped)
	}
}

func TestParsePluginWithoutManifestNeedsAName(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"skills/a/SKILL.md": skill("a")})
	if _, err := skills.ParsePlugin(root, skills.PluginOptions{}); err == nil {
		t.Fatal("expected an error with no name")
	}
	p, err := skills.ParsePlugin(root, skills.PluginOptions{Name: "my-plugin"})
	if err != nil || p.Name != "my-plugin" || p.Format != "skills" || skillNames(p) != "a" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestParsePluginSkillPathsFromAMarketplaceEntry(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"skills/xlsx/SKILL.md":  skill("xlsx"),
		"skills/pdf/SKILL.md":   skill("pdf"),
		"skills/other/SKILL.md": skill("other"),
	})
	p, err := skills.ParsePlugin(root, skills.PluginOptions{Name: "document-skills", SkillPaths: []string{"./skills/xlsx", "./skills/pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	if skillNames(p) != "pdf,xlsx" {
		t.Errorf("only the listed skills: %s", skillNames(p))
	}
}

func TestParsePluginSingleSkillDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"SKILL.md": skill("solo"), "reference.md": "ref"})
	p, err := skills.ParsePlugin(root, skills.PluginOptions{Name: "solo-repo"})
	if err != nil || skillNames(p) != "solo" || p.Skills[0].Dir != "." {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestParsePluginReportsABadSkillWithoutLosingTheRest(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".claude-plugin/plugin.json": `{"name":"p"}`,
		"skills/good/SKILL.md":       skill("good"),
		"skills/bad/SKILL.md":        "no frontmatter at all",
		"skills/dupe/SKILL.md":       skill("good"),
	})
	p, err := skills.ParsePlugin(root, skills.PluginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if skillNames(p) != "good" || len(p.Skipped) != 2 {
		t.Errorf("skills = %s, skipped = %+v", skillNames(p), p.Skipped)
	}
}

func TestParsePluginServerAuthAndPlaceholders(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".claude-plugin/plugin.json": `{"name":"p","mcpServers":{
			"bearer":{"type":"http","url":"https://a.test/mcp","headers":{"Authorization":"Bearer ${TOKEN}"}},
			"key":{"url":"https://b.test/mcp","headers":{"X-Api-Key":"${K}"}},
			"basic":{"type":"http","url":"https://c.test","headers":{"Authorization":"Basic abc"}},
			"many":{"type":"http","url":"https://d.test","headers":{"X-A":"1","X-B":"2"}},
			"templated":{"type":"http","url":"https://${TENANT}.example.com/mcp"}}}`,
	})
	p, err := skills.ParsePlugin(root, skills.PluginOptions{Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range p.Servers {
		got[s.Name] = s.AuthType + "/" + s.AuthHeader
		if s.CredentialRef != "" {
			t.Errorf("a plugin never names a credential: %+v", s)
		}
	}
	if len(got) != 2 || got["bearer"] != "bearer/" || got["key"] != "api-key/X-Api-Key" {
		t.Errorf("servers = %v", got)
	}
	if len(p.Skipped) != 3 {
		t.Errorf("skipped = %+v", p.Skipped)
	}
}

func TestParsePluginServersFromAFileThePluginNames(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".claude-plugin/plugin.json": `{"name":"p","mcpServers":"./mcp/servers.json"}`,
		"mcp/servers.json":           `{"mcpServers":{"a":{"type":"http","url":"https://a.test/mcp"}}}`,
		".mcp.json":                  `{"b":{"type":"http","url":"https://b.test/mcp"}}`, // top-level form
	})
	p, err := skills.ParsePlugin(root, skills.PluginOptions{})
	if err != nil || len(p.Servers) != 2 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestParsePluginRefusesPathsOutsideThePlugin(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, map[string]string{"secret/SKILL.md": skill("stolen")})
	root := t.TempDir()
	write(t, root, map[string]string{
		".claude-plugin/plugin.json": `{"name":"p","skills":["../` + filepath.Base(outside) + `/secret"],"mcpServers":"../x.json"}`,
	})
	// A symlinked skills directory pointing out of the plugin.
	if err := os.Symlink(outside, filepath.Join(root, "skills")); err != nil {
		t.Fatal(err)
	}
	p, err := skills.ParsePlugin(root, skills.PluginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Skills) != 0 {
		t.Errorf("read a skill from outside the plugin: %+v", p.Skills)
	}
	if len(p.Skipped) == 0 {
		t.Error("the refusals should be reported")
	}
}

func TestLoadDirDoesNotFollowSymlinks(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, dir, map[string]string{"SKILL.md": skill("x")})
	if err := os.Symlink(secret, filepath.Join(dir, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	b, err := skills.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b["leak.txt"]; ok {
		t.Fatal("a symlink was followed into the bundle")
	}
}

func TestServerSkillIsAValidSkillCarryingTheServers(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".claude-plugin/plugin.json": `{"name":"My Plugin","description":"Does stuff: with a colon"}`,
		"skills/my-plugin/SKILL.md":  skill("my-plugin"),
		".mcp.json":                  `{"mcpServers":{"api":{"type":"http","url":"https://a.test/mcp","headers":{"Authorization":"Bearer x"}}}}`,
	})
	p, err := skills.ParsePlugin(root, skills.PluginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	name, bundle := p.ServerSkill()
	if name != "my-plugin-tools" {
		t.Errorf("name = %q (its own skill already has the plugin's name)", name)
	}
	m, err := bundle.Manifest()
	if err != nil {
		t.Fatalf("not a valid SKILL.md: %v\n%s", err, bundle["SKILL.md"])
	}
	if m.Name != name || m.Description != "Does stuff: with a colon" || len(m.MCPServers) != 1 ||
		m.MCPServers[0].Transport != "mcp" || m.MCPServers[0].AuthType != "bearer" {
		t.Errorf("manifest = %+v", m)
	}
}

func TestRequireHTTPS(t *testing.T) {
	for _, ok := range []string{"https://github.com/a/b.git", "https://example.com/x"} {
		if err := skills.RequireHTTPS(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://github.com/a/b", "file:///etc", "/etc/passwd", "git@github.com:a/b.git", "ssh://git@github.com/a/b", "https://", ""} {
		if err := skills.RequireHTTPS(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestReadPluginInfoPrefersACodexShortDescription(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{".codex-plugin/plugin.json": `{"name":"x","description":"long long","version":"1.0","license":"MIT","interface":{"shortDescription":"short"}}`})
	info, ok := skills.ReadPluginInfo(dir)
	if !ok || info.Description != "short" || info.Version != "1.0" || info.License != "MIT" {
		t.Errorf("info = %+v ok=%v", info, ok)
	}
	if _, ok := skills.ReadPluginInfo(t.TempDir()); ok {
		t.Error("a directory with no manifest has no info")
	}
}
