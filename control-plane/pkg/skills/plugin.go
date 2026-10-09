package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"gopkg.in/yaml.v3"
)

// A plugin is a directory that bundles skills with other parts: Claude Code
// plugins (.claude-plugin/plugin.json) and Codex plugins
// (.codex-plugin/plugin.json) share one layout. ParsePlugin keeps what an
// AgentOven agent can use, which is the skills and the remote MCP servers, and
// reports everything else in Skipped so an importer can say what it left out.

// Plugin is what ParsePlugin found.
type Plugin struct {
	Name        string
	Description string
	Version     string
	License     string
	// Format is "claude", "codex", or "skills" when the directory has no
	// plugin manifest and only holds skills.
	Format string
	Skills []PluginSkill
	// Servers are the plugin's MCP servers that can be reached over HTTP.
	Servers []models.SkillMCPServer
	Skipped []Skipped
}

// PluginSkill is one SKILL.md inside a plugin.
type PluginSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Dir is the skill's directory relative to the plugin root.
	Dir string `json:"dir"`
}

// Skipped is a part of a plugin that was left out, and why.
type Skipped struct {
	Component string `json:"component"` // skill, mcp_server, commands, agents, hooks, ...
	Name      string `json:"name,omitempty"`
	Reason    string `json:"reason"`
}

// PluginOptions carries what a marketplace entry says about a plugin that its
// own directory may not.
type PluginOptions struct {
	// Name is used when the plugin has no manifest, or one without a name.
	Name string
	// SkillPaths, if set, are the skill directories to take (relative to the
	// plugin root) instead of scanning for them.
	SkillPaths []string
}

type pluginManifest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	License     string          `json:"license"`
	Skills      json.RawMessage `json:"skills"`
	MCPServers  json.RawMessage `json:"mcpServers"`
}

// manifestPaths are the plugin manifests, in the order they are looked for.
var manifestPaths = []struct{ rel, format string }{
	{".claude-plugin/plugin.json", "claude"},
	{".codex-plugin/plugin.json", "codex"},
}

// ParsePlugin reads the plugin in root.
func ParsePlugin(root string, opt PluginOptions) (*Plugin, error) {
	p := &Plugin{Format: "skills", Name: opt.Name}

	var m pluginManifest
	for _, mp := range manifestPaths {
		data, err := os.ReadFile(filepath.Join(root, mp.rel))
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON: %w", mp.rel, err)
		}
		p.Format = mp.format
		break
	}
	if m.Name != "" {
		p.Name = m.Name
	}
	if p.Name == "" {
		return nil, fmt.Errorf("the plugin has no name (no manifest name, and none was given)")
	}
	p.Description, p.Version, p.License = m.Description, m.Version, m.License

	if err := p.findSkills(root, opt, m.Skills); err != nil {
		return nil, err
	}
	p.findServers(root, m.MCPServers)
	p.noteUnsupported(root)
	return p, nil
}

// LoadSkill reads one of the plugin's skills as an ordinary skill bundle.
func (p *Plugin) LoadSkill(root string, s PluginSkill) (Bundle, error) {
	dir, err := InsideRoot(root, s.Dir)
	if err != nil {
		return nil, err
	}
	return LoadDir(dir)
}

// ServerSkill returns a skill that carries the plugin's MCP servers, so they
// register like any skill's servers do: the name (the plugin's, or
// "<plugin>-tools" when one of its own skills has it) and its bundle. It is
// meant for a plugin with Servers.
func (p *Plugin) ServerSkill() (string, Bundle) {
	name := skillSlug(p.Name)
	for _, s := range p.Skills {
		if s.Name == name {
			name += "-tools"
			break
		}
	}
	desc := strings.TrimSpace(p.Description)
	if desc == "" {
		desc = "Tools from the " + p.Name + " plugin's MCP servers."
	}
	fm, _ := yaml.Marshal(frontmatter{Name: name, Description: desc, MCPServers: p.Servers})
	body := "Tools from the MCP servers of the " + p.Name + " plugin. Use them when the task needs what that service offers.\n"
	return name, Bundle{ManifestFilename: []byte("---\n" + string(fm) + "---\n\n" + body)}
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// skillSlug turns a plugin name into a skill name: lowercase letters, digits
// and single hyphens, at most 64 characters.
func skillSlug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 64 {
		s = strings.TrimRight(s[:64], "-")
	}
	return s
}

// InsideRoot joins rel onto root and refuses anything that resolves outside
// it, whether by ".." or by a symlink.
func InsideRoot(root, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the plugin", rel)
	}
	full := filepath.Join(root, clean)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("path %q: %w", rel, err)
	}
	if realFull != realRoot && !strings.HasPrefix(realFull, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the plugin", rel)
	}
	return full, nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// stringsOrString reads a JSON value that is either one string or a list.
func stringsOrString(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(raw, &many)
	return many
}

func (p *Plugin) findSkills(root string, opt PluginOptions, manifestSkills json.RawMessage) error {
	var places []string
	switch {
	case len(opt.SkillPaths) > 0:
		places = opt.SkillPaths
	default:
		places = append([]string{"skills"}, stringsOrString(manifestSkills)...)
	}

	seenDir, seenName := map[string]bool{}, map[string]bool{}
	add := func(dir string) {
		if seenDir[dir] {
			return
		}
		seenDir[dir] = true
		full, err := InsideRoot(root, dir)
		if err != nil {
			p.Skipped = append(p.Skipped, Skipped{Component: "skill", Name: dir, Reason: err.Error()})
			return
		}
		data, err := os.ReadFile(filepath.Join(full, ManifestFilename))
		if err != nil {
			return
		}
		man, err := ParseManifest(data)
		if err != nil {
			p.Skipped = append(p.Skipped, Skipped{Component: "skill", Name: dir, Reason: err.Error()})
			return
		}
		if seenName[man.Name] {
			p.Skipped = append(p.Skipped, Skipped{Component: "skill", Name: dir, Reason: fmt.Sprintf("another skill in this plugin is already named %q", man.Name)})
			return
		}
		seenName[man.Name] = true
		p.Skills = append(p.Skills, PluginSkill{Name: man.Name, Description: man.Description, Dir: filepath.ToSlash(dir)})
	}

	for _, place := range places {
		place = filepath.ToSlash(filepath.Clean(filepath.FromSlash(place)))
		full, err := InsideRoot(root, place)
		if err != nil {
			// The default "skills" directory is allowed not to exist.
			if place != "skills" {
				p.Skipped = append(p.Skipped, Skipped{Component: "skill", Name: place, Reason: err.Error()})
			}
			continue
		}
		if fileExists(filepath.Join(full, ManifestFilename)) {
			add(place)
			continue
		}
		entries, _ := os.ReadDir(full)
		for _, e := range entries {
			if e.IsDir() { // a symlinked directory reports as a symlink, not a directory
				add(filepath.ToSlash(filepath.Join(place, e.Name())))
			}
		}
	}

	// A directory that is itself one skill, with no skills/ folder.
	if len(p.Skills) == 0 && len(opt.SkillPaths) == 0 && fileExists(filepath.Join(root, ManifestFilename)) {
		add(".")
	}
	sort.SliceStable(p.Skills, func(i, j int) bool { return p.Skills[i].Dir < p.Skills[j].Dir })
	return nil
}

// mcpEntry is one server in a .mcp.json or a plugin manifest.
type mcpEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Command string            `json:"command"`
	Headers map[string]string `json:"headers"`
}

func (p *Plugin) findServers(root string, manifestServers json.RawMessage) {
	if data, err := os.ReadFile(filepath.Join(root, ".mcp.json")); err == nil {
		var file map[string]json.RawMessage
		if json.Unmarshal(data, &file) == nil {
			if inner, ok := file["mcpServers"]; ok {
				p.addServers(root, inner)
			} else {
				// Some plugins list the servers at the top level.
				all, _ := json.Marshal(file)
				p.addServers(root, all)
			}
		}
	}
	p.addServers(root, manifestServers)
}

// addServers takes the forms a plugin manifest allows: an object of servers, a
// path to a JSON file holding one, or a list of either.
func (p *Plugin) addServers(root string, raw json.RawMessage) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return
	}
	switch raw[0] {
	case '{':
		var servers map[string]json.RawMessage
		if json.Unmarshal(raw, &servers) != nil {
			return
		}
		names := make([]string, 0, len(servers))
		for n := range servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			var e mcpEntry
			if json.Unmarshal(servers[n], &e) != nil {
				continue
			}
			p.addServer(n, e)
		}
	case '"':
		var path string
		if json.Unmarshal(raw, &path) != nil {
			return
		}
		full, err := InsideRoot(root, path)
		if err != nil {
			p.Skipped = append(p.Skipped, Skipped{Component: "mcp_server", Name: path, Reason: err.Error()})
			return
		}
		if data, err := os.ReadFile(full); err == nil {
			var file map[string]json.RawMessage
			if json.Unmarshal(data, &file) == nil {
				if inner, ok := file["mcpServers"]; ok {
					p.addServers(root, inner)
				} else {
					p.addServers(root, data)
				}
			}
		}
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil {
			for _, it := range items {
				p.addServers(root, it)
			}
		}
	}
}

func (p *Plugin) addServer(name string, e mcpEntry) {
	skip := func(reason string) {
		p.Skipped = append(p.Skipped, Skipped{Component: "mcp_server", Name: name, Reason: reason})
	}
	for _, s := range p.Servers {
		if s.Name == name {
			return // already declared by the other file
		}
	}
	switch {
	case e.Command != "":
		skip("runs a local process (stdio), which a server cannot host")
		return
	case e.URL == "":
		skip("has no url")
		return
	case e.Type != "" && e.Type != "http" && e.Type != "streamable-http":
		skip(fmt.Sprintf("uses the %q transport; only streamable HTTP is supported", e.Type))
		return
	case strings.Contains(e.URL, "${"):
		skip("its url has a ${placeholder} that must be configured first")
		return
	}

	srv := models.SkillMCPServer{Name: name, Transport: models.MCPTransportStreamableHTTP, Endpoint: e.URL}
	var others []string
	for k, v := range e.Headers {
		if strings.EqualFold(k, "Authorization") {
			if !strings.HasPrefix(v, "Bearer ") {
				skip("sends an Authorization header that is not a Bearer token")
				return
			}
			srv.AuthType = "bearer"
			continue
		}
		others = append(others, k)
	}
	switch {
	case len(others) > 1 || (len(others) == 1 && srv.AuthType != ""):
		skip("needs more than one custom header")
		return
	case len(others) == 1:
		srv.AuthType, srv.AuthHeader = "api-key", others[0]
	}
	p.Servers = append(p.Servers, srv)
}

// noteUnsupported records the plugin parts AgentOven has no use for.
func (p *Plugin) noteUnsupported(root string) {
	count := func(dir string) int {
		entries, _ := os.ReadDir(filepath.Join(root, dir))
		n := 0
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				n++
			}
		}
		return n
	}
	if n := count("commands"); n > 0 {
		p.Skipped = append(p.Skipped, Skipped{Component: "commands", Name: fmt.Sprintf("%d", n), Reason: "slash commands belong to Claude Code and Codex"})
	}
	if n := count("agents"); n > 0 {
		p.Skipped = append(p.Skipped, Skipped{Component: "agents", Name: fmt.Sprintf("%d", n), Reason: "plugin subagents belong to Claude Code and Codex"})
	}
	if fileExists(filepath.Join(root, "hooks", "hooks.json")) || fileExists(filepath.Join(root, "hooks.json")) {
		p.Skipped = append(p.Skipped, Skipped{Component: "hooks", Reason: "hooks run inside Claude Code and Codex"})
	}
	if fileExists(filepath.Join(root, ".app.json")) {
		p.Skipped = append(p.Skipped, Skipped{Component: "apps", Reason: "connector apps are specific to Codex"})
	}
	if fileExists(filepath.Join(root, ".lsp.json")) {
		p.Skipped = append(p.Skipped, Skipped{Component: "lsp_servers", Reason: "language servers are an editor feature"})
	}
}

// FetchPlugin checks out the plugin an ImportSpec names and reads it. root is
// the plugin's directory (pass it to LoadSkill) and cleanup removes the
// checkout; call it when done, including after an error-free return.
func FetchPlugin(ctx context.Context, spec ImportSpec) (p *Plugin, root string, cleanup func(), err error) {
	// Everything a spec can check without a clone is checked first.
	if err = RequireHTTPS(spec.GitURL); err != nil {
		return nil, "", nil, err
	}
	for _, rel := range append([]string{spec.Path}, spec.SkillPaths...) {
		if rel == "" {
			continue
		}
		if clean := filepath.Clean(filepath.FromSlash(rel)); filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, "", nil, fmt.Errorf("path %q is outside the repository", rel)
		}
	}
	repo, cleanup, err := CheckoutGit(ctx, spec.GitURL, spec.GitRef, spec.GitSHA)
	if err != nil {
		return nil, "", nil, err
	}
	root = repo
	if spec.Path != "" {
		if root, err = InsideRoot(repo, spec.Path); err != nil {
			cleanup()
			return nil, "", nil, err
		}
	}
	p, err = ParsePlugin(root, PluginOptions{Name: spec.Name, SkillPaths: spec.SkillPaths})
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	return p, root, cleanup, nil
}

// ImportSpec says where a plugin lives: the body of POST /skills/import.
type ImportSpec struct {
	GitURL string `json:"git_url"`
	GitRef string `json:"git_ref,omitempty"`
	// GitSHA pins the exact commit to install.
	GitSHA string `json:"git_sha,omitempty"`
	// Path is the plugin's directory inside the repository (empty: the root).
	Path string `json:"path,omitempty"`
	// Name names the plugin when it has no manifest of its own.
	Name string `json:"name,omitempty"`
	// SkillPaths narrows the import to these skill directories.
	SkillPaths []string `json:"skill_paths,omitempty"`
}

// RequireHTTPS refuses a git location that is not an https:// URL, so what an
// import points at cannot reach the local filesystem (a file path or file://
// URL) or an ssh agent.
func RequireHTTPS(gitURL string) error {
	u, err := url.Parse(gitURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("git location %q must be an https:// URL", gitURL)
	}
	return nil
}

// PluginInfo is the descriptive part of a plugin manifest.
type PluginInfo struct {
	Description string
	Version     string
	License     string
}

// ReadPluginInfo reads a plugin directory's manifest (Claude or Codex layout)
// for what a listing shows. ok is false when it has none. A Codex manifest's
// long description is replaced by its short one.
func ReadPluginInfo(pluginDir string) (info PluginInfo, ok bool) {
	for _, mp := range manifestPaths {
		data, err := os.ReadFile(filepath.Join(pluginDir, mp.rel))
		if err != nil {
			continue
		}
		var m struct {
			Description string `json:"description"`
			Version     string `json:"version"`
			License     string `json:"license"`
			Interface   struct {
				ShortDescription string `json:"shortDescription"`
			} `json:"interface"`
		}
		if json.Unmarshal(data, &m) != nil {
			return PluginInfo{}, false
		}
		info = PluginInfo{Description: m.Interface.ShortDescription, Version: m.Version, License: m.License}
		if info.Description == "" {
			info.Description = m.Description
		}
		return info, true
	}
	return PluginInfo{}, false
}
