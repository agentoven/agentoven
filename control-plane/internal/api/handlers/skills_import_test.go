package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/skills"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
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

func skillMD(name string) string {
	return "---\nname: " + name + "\ndescription: Does " + name + ".\n---\nBody of " + name + ".\n"
}

// bearerMCPServer is a remote MCP server that wants "Bearer <token>".
func bearerMCPServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	type args struct {
		Text string `json:"text"`
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "hosted", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, a args) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + a.Text}}}, nil, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// importFixture is a plugin on disk plus handlers that read it instead of
// cloning (the real fetch only accepts https git URLs).
func importFixture(t *testing.T, verdictJSON string, mcpURL string) (*handlers.Handlers, func(map[string]interface{}) *httptest.ResponseRecorder, string) {
	t.Helper()
	h, s, _ := newSkillTestHandlers(t, verdictJSON)
	h.MCPGateway = mcpgw.NewGateway(s)
	seedSkillProvider(t, s, "default")

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		".claude-plugin/plugin.json": `{"name":"acme","description":"Acme tools","version":"2.0.0","license":"MIT"}`,
		"skills/alpha/SKILL.md":      skillMD("alpha"),
		"skills/alpha/ref.md":        "reference",
		"skills/beta/SKILL.md":       skillMD("beta"),
		"commands/run.md":            "run",
		".mcp.json": `{"mcpServers":{
			"hosted":{"type":"http","url":"` + mcpURL + `","headers":{"Authorization":"Bearer ${ACME_TOKEN}"}},
			"private":{"type":"http","url":"https://private.example.com/mcp","headers":{"Authorization":"Bearer ${OTHER}"}},
			"local":{"command":"node","args":["x.js"]}}}`,
	})
	h.FetchSkillPlugin = func(_ context.Context, spec skills.ImportSpec) (*skills.Plugin, string, func(), error) {
		p, err := skills.ParsePlugin(dir, skills.PluginOptions{Name: spec.Name, SkillPaths: spec.SkillPaths})
		return p, dir, func() {}, err
	}

	do := func(body map[string]interface{}) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/import", strings.NewReader(string(raw)))
		req = withKitchen(req, "default")
		rec := httptest.NewRecorder()
		h.ImportSkillPlugin(rec, req)
		return rec
	}
	return h, do, dir
}

type importResp struct {
	Plugin  map[string]string `json:"plugin"`
	Skills  []skills.PluginSkill
	Servers []struct {
		Name            string
		NeedsCredential bool `json:"needs_credential"`
	}
	ServerSkill string `json:"server_skill"`
	Skipped     []skills.Skipped
	Results     []struct {
		Name, Status, Reasoning, Error string
	}
}

func decodeImport(t *testing.T, rec *httptest.ResponseRecorder) importResp {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var r importResp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r importResp) statuses() string {
	var out []string
	for _, x := range r.Results {
		out = append(out, x.Name+"="+x.Status)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

const accept = `{"verdict":"accept","reasoning":"fine"}`

func TestImportDryRunShowsThePlanAndRegistersNothing(t *testing.T) {
	ts := bearerMCPServer(t, "sk-1")
	h, do, _ := importFixture(t, accept, ts.URL)

	r := decodeImport(t, do(map[string]interface{}{"git_url": "https://example.com/acme.git", "dry_run": true}))
	if r.Plugin["name"] != "acme" || r.Plugin["version"] != "2.0.0" || r.Plugin["license"] != "MIT" || r.Plugin["format"] != "claude" {
		t.Errorf("plugin = %v", r.Plugin)
	}
	if len(r.Skills) != 2 || r.ServerSkill != "acme" {
		t.Errorf("skills = %+v, server skill = %q", r.Skills, r.ServerSkill)
	}
	needs := map[string]bool{}
	for _, s := range r.Servers {
		needs[s.Name] = s.NeedsCredential
	}
	if len(needs) != 2 || !needs["hosted"] || !needs["private"] {
		t.Errorf("servers = %v", needs)
	}
	if len(r.Results) != 0 {
		t.Errorf("a dry run registered: %+v", r.Results)
	}
	if list, _ := h.Store.ListSkills(context.Background(), "default"); len(list) != 0 {
		t.Errorf("skills were stored: %d", len(list))
	}
}

func TestImportRegistersSkillsAndServers(t *testing.T) {
	ts := bearerMCPServer(t, "sk-1")
	h, do, _ := importFixture(t, accept, ts.URL)
	if err := h.Store.CreateKitchenCredential(context.Background(), &models.KitchenCredential{ID: "c", Kitchen: "default", Name: "acme-key", Value: "sk-1"}); err != nil {
		t.Fatal(err)
	}

	body := map[string]interface{}{
		"git_url": "https://example.com/acme.git", "git_sha": "abc123", "path": "plugins/acme",
		"credentials": map[string]string{"hosted": "acme-key"},
	}
	r := decodeImport(t, do(body))
	if r.statuses() != "acme=accepted,alpha=accepted,beta=accepted" {
		t.Fatalf("results = %+v", r.Results)
	}

	// "private" had no credential, "local" is stdio, commands are unsupported: all said so.
	skipped := map[string]bool{}
	for _, s := range r.Skipped {
		skipped[s.Component+":"+s.Name] = true
	}
	if !skipped["mcp_server:private"] || !skipped["mcp_server:local"] || !skipped["commands:1"] {
		t.Errorf("skipped = %+v", r.Skipped)
	}

	alpha, err := h.Store.GetSkill(context.Background(), "default", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if alpha.Source != models.SkillSourcePlugin || alpha.SourceRef != "https://example.com/acme.git@abc123#plugins/acme/skills/alpha" || alpha.Status != models.SkillStatusAccepted {
		t.Errorf("alpha = %+v", alpha)
	}

	// The servers' skill carries the remote tools, callable with the stored credential.
	srvSkill, err := h.Store.GetSkill(context.Background(), "default", "acme")
	if err != nil || len(srvSkill.RegisteredTools) != 1 || srvSkill.RegisteredTools[0] != "acme_echo" {
		t.Fatalf("server skill = %+v, %v", srvSkill, err)
	}
	params, _ := json.Marshal(models.MCPToolCallParams{Name: "acme_echo", Arguments: map[string]interface{}{"text": "hi"}})
	resp := h.MCPGateway.HandleJSONRPC(context.Background(), "default", &models.MCPRequest{Jsonrpc: "2.0", Method: "tools/call", Params: params, ID: "1"})
	if raw, _ := json.Marshal(resp.Result); resp.Error != nil || !strings.Contains(string(raw), "echo:hi") {
		t.Errorf("tool call = %s (%v)", raw, resp.Error)
	}

	// Importing again does not overwrite what is registered.
	again := decodeImport(t, do(body))
	if again.statuses() != "acme=error,alpha=error,beta=error" || !strings.Contains(again.Results[0].Error, "already registered") {
		t.Errorf("second import = %+v", again.Results)
	}
}

func TestImportOnlyPicksSkillsAndTheServerSkillByName(t *testing.T) {
	ts := bearerMCPServer(t, "sk-1")
	_, do, _ := importFixture(t, accept, ts.URL)

	r := decodeImport(t, do(map[string]interface{}{"git_url": "https://example.com/acme.git", "only": []string{"beta"}}))
	if r.statuses() != "beta=accepted" {
		t.Errorf("only beta: %+v", r.Results)
	}

	_, do2, _ := importFixture(t, accept, ts.URL)
	r = decodeImport(t, do2(map[string]interface{}{"git_url": "https://example.com/acme.git", "only": []string{"acme"}}))
	// The server skill alone, and "hosted" has no credential, so there is nothing to register.
	if len(r.Results) != 0 {
		t.Errorf("only the servers, none usable: %+v", r.Results)
	}

	rec := do(map[string]interface{}{"git_url": "https://example.com/acme.git", "only": []string{"nope"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "nope") {
		t.Errorf("unknown skill: %d %s", rec.Code, rec.Body.String())
	}
}

func TestImportRejectedSkillRegistersNothing(t *testing.T) {
	ts := bearerMCPServer(t, "sk-1")
	h, do, _ := importFixture(t, `{"verdict":"reject","reasoning":"exfiltrates secrets"}`, ts.URL)
	r := decodeImport(t, do(map[string]interface{}{"git_url": "https://example.com/acme.git", "only": []string{"alpha"}}))
	if r.statuses() != "alpha=rejected" || r.Results[0].Reasoning != "exfiltrates secrets" {
		t.Errorf("results = %+v", r.Results)
	}
	sk, _ := h.Store.GetSkill(context.Background(), "default", "alpha")
	if sk == nil || sk.Status != models.SkillStatusRejected || len(sk.RegisteredTools) != 0 {
		t.Errorf("skill = %+v", sk)
	}
}

func TestImportNeedsAProviderUnlessDryRun(t *testing.T) {
	h, s, _ := newSkillTestHandlers(t, accept) // no provider seeded
	_ = s
	raw := `{"git_url":"https://example.com/x.git"}`
	req := withKitchen(httptest.NewRequest(http.MethodPost, "/api/v1/skills/import", strings.NewReader(raw)), "default")
	rec := httptest.NewRecorder()
	h.ImportSkillPlugin(rec, req)
	if rec.Code != http.StatusPreconditionFailed {
		t.Errorf("got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportRefusesNonHTTPSGitLocations(t *testing.T) {
	h, s, _ := newSkillTestHandlers(t, accept)
	seedSkillProvider(t, s, "default")
	for _, loc := range []string{"file:///etc", "/etc", "http://example.com/a.git", "git@github.com:a/b.git"} {
		raw, _ := json.Marshal(map[string]interface{}{"git_url": loc, "dry_run": true})
		req := withKitchen(httptest.NewRequest(http.MethodPost, "/api/v1/skills/import", strings.NewReader(string(raw))), "default")
		rec := httptest.NewRecorder()
		h.ImportSkillPlugin(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "https://") {
			t.Errorf("%s: %d %s", loc, rec.Code, rec.Body.String())
		}
	}
}

func TestImportRefusesAPathOutsideTheRepo(t *testing.T) {
	// Paths are checked before anything is cloned.
	for _, spec := range []skills.ImportSpec{
		{GitURL: "https://example.com/a.git", Path: "../../etc"},
		{GitURL: "https://example.com/a.git", Path: "/etc"},
		{GitURL: "https://example.com/a.git", SkillPaths: []string{"../secret"}},
	} {
		if _, _, _, err := skills.FetchPlugin(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "outside the repository") {
			t.Errorf("%+v: err = %v", spec, err)
		}
	}
}
