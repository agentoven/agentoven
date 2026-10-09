package skills

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type fakeLister struct {
	tools []models.RemoteTool
	err   error
	auth  map[string]interface{}
}

func (f *fakeLister) ListRemoteTools(_ context.Context, _ string, auth map[string]interface{}) ([]models.RemoteTool, error) {
	f.auth = auth
	return f.tools, f.err
}

func TestToolsForServerPlainTransport(t *testing.T) {
	for _, tr := range []string{"http", "sse"} {
		srv := models.SkillMCPServer{Name: "api", Description: "d", Transport: tr, Endpoint: "http://x"}
		tools, err := ToolsForServer(context.Background(), nil, "k", "my-skill", srv, nil, nil)
		if err != nil || len(tools) != 1 {
			t.Fatalf("%s: %v %v", tr, tools, err)
		}
		if tools[0].Name != "my-skill_api" || tools[0].Transport != tr || tools[0].RemoteName != "" || tools[0].Description != "d" {
			t.Errorf("%s: %+v", tr, tools[0])
		}
	}
}

func TestToolsForServerMCPExpandsRemoteTools(t *testing.T) {
	l := &fakeLister{tools: []models.RemoteTool{
		{Name: "firecrawl_scrape", Description: "Scrape a page", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"url": map[string]interface{}{"type": "string"}}}},
		{Name: "search", Description: "Search the web"}, // no schema at all
	}}
	discovery := map[string]interface{}{"type": "bearer", "token": "resolved-value"}
	stored := map[string]interface{}{"type": "bearer", "credential_ref": "firecrawl-key"}
	srv := models.SkillMCPServer{Name: "hosted", Transport: "mcp", Endpoint: "https://mcp.example/v2/mcp"}

	tools, err := ToolsForServer(context.Background(), l, "k", "firecrawl", srv, discovery, stored)
	if err != nil || len(tools) != 2 {
		t.Fatalf("%v %v", tools, err)
	}
	if l.auth["token"] != "resolved-value" {
		t.Error("discovery must be done with the resolved credential")
	}

	scrape, search := tools[0], tools[1]
	// The skill prefix is not doubled; a bare name gets it. Neither has a dot.
	if scrape.Name != "firecrawl_scrape" || search.Name != "firecrawl_search" {
		t.Errorf("names = %q, %q", scrape.Name, search.Name)
	}
	if scrape.RemoteName != "firecrawl_scrape" || search.RemoteName != "search" {
		t.Errorf("remote names = %q, %q", scrape.RemoteName, search.RemoteName)
	}
	for _, tl := range tools {
		if tl.Transport != "mcp" || tl.Endpoint != srv.Endpoint || !tl.Enabled || tl.ID == "" {
			t.Errorf("%s: %+v", tl.Name, tl)
		}
		// The tool keeps the credential's NAME and the skill that owns it, never the value.
		if tl.AuthConfig["credential_ref"] != "firecrawl-key" || tl.AuthConfig["token"] != nil || tl.Skill != "firecrawl" {
			t.Errorf("%s stored auth/skill = %v / %q", tl.Name, tl.AuthConfig, tl.Skill)
		}
		if !providerSafe(tl.Name) {
			t.Errorf("%q is not a valid function name for every provider", tl.Name)
		}
	}
	if scrape.Schema["description"] != "Scrape a page" || scrape.Schema["properties"] == nil {
		t.Errorf("schema = %v", scrape.Schema)
	}
	if search.Schema["type"] != "object" {
		t.Errorf("a tool with no schema must still be an object schema: %v", search.Schema)
	}
	if scrape.ID == search.ID {
		t.Error("tool ids must be unique")
	}
}

func TestToolsForServerMCPErrors(t *testing.T) {
	srv := models.SkillMCPServer{Name: "hosted", Transport: "mcp", Endpoint: "https://x"}
	if _, err := ToolsForServer(context.Background(), nil, "k", "s", srv, nil, nil); err == nil {
		t.Error("no lister should be an error")
	}
	if _, err := ToolsForServer(context.Background(), &fakeLister{err: errors.New("401")}, "k", "s", srv, nil, nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v", err)
	}
	if _, err := ToolsForServer(context.Background(), &fakeLister{}, "k", "s", srv, nil, nil); err == nil {
		t.Error("a server with no tools should be an error")
	}
}

func providerSafe(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// Names that skill authors or remote servers choose must still come out valid.
func TestToolNamesAreAlwaysProviderSafe(t *testing.T) {
	long := strings.Repeat("x", 100)
	srv := models.SkillMCPServer{Name: "my server.v2", Transport: "http", Endpoint: "http://x"}
	plain, _ := ToolsForServer(context.Background(), nil, "k", "my.skill", srv, nil, nil)
	if plain[0].Name != "my_skill_my_server_v2" {
		t.Errorf("plain name = %q", plain[0].Name)
	}

	l := &fakeLister{tools: []models.RemoteTool{{Name: "web.search"}, {Name: "has space"}, {Name: long}}}
	mcpSrv := models.SkillMCPServer{Name: "s", Transport: "mcp", Endpoint: "https://x"}
	tools, err := ToolsForServer(context.Background(), l, "k", "my.skill", mcpSrv, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		if !providerSafe(tl.Name) {
			t.Errorf("%q is not provider-safe", tl.Name)
		}
	}
	for _, tl := range tools {
		if tl.Skill != "my.skill" {
			t.Errorf("every tool records its skill: %q", tl.Skill)
		}
	}
	if tools[0].Name != "my_skill_web_search" || tools[0].RemoteName != "web.search" {
		t.Errorf("got %q (remote %q)", tools[0].Name, tools[0].RemoteName)
	}
}
