package skills_test

import (
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/skills"
)

func TestParseManifestAcceptsAWellFormedSkill(t *testing.T) {
	data := []byte(`---
name: web_search
description: Searches the web for current information.
mcp_tools:
  - name: search
    transport: http
    endpoint: https://skill-web-search.internal/mcp
---
# Web Search

Use the search tool when the user asks about current events.
`)
	m, err := skills.ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "web_search" || m.Description != "Searches the web for current information." {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if len(m.MCPServers) != 1 || m.MCPServers[0].Name != "search" || m.MCPServers[0].Endpoint != "https://skill-web-search.internal/mcp" {
		t.Fatalf("expected one parsed mcp server, got %+v", m.MCPServers)
	}
	if !strings.Contains(m.Instructions, "Use the search tool") {
		t.Fatalf("expected the markdown body as Instructions, got %q", m.Instructions)
	}
}

func TestParseManifestRejectsMissingName(t *testing.T) {
	data := []byte("---\ndescription: no name here\n---\nbody\n")
	if _, err := skills.ParseManifest(data); err == nil {
		t.Fatal("expected an error for a manifest with no name")
	}
}

func TestParseManifestRejectsMissingDescription(t *testing.T) {
	data := []byte("---\nname: nameless-desc\n---\nbody\n")
	if _, err := skills.ParseManifest(data); err == nil {
		t.Fatal("expected an error for a manifest with no description")
	}
}

func TestParseManifestRejectsNoFrontmatter(t *testing.T) {
	data := []byte("# Just a markdown file\n\nNo frontmatter at all.\n")
	if _, err := skills.ParseManifest(data); err == nil {
		t.Fatal("expected an error for a SKILL.md with no '---' frontmatter fence")
	}
}

func TestParseManifestRejectsUnclosedFrontmatter(t *testing.T) {
	data := []byte("---\nname: x\ndescription: y\nbody with no closing fence")
	if _, err := skills.ParseManifest(data); err == nil {
		t.Fatal("expected an error for frontmatter that is never closed")
	}
}

func TestParseManifestRejectsBadMCPServerTransport(t *testing.T) {
	data := []byte(`---
name: bad
description: has a bogus transport
mcp_tools:
  - name: thing
    transport: carrier-pigeon
    endpoint: https://example.com
---
body
`)
	if _, err := skills.ParseManifest(data); err == nil {
		t.Fatal("expected an error for an mcp_tools entry with an invalid transport")
	}
}

func TestParseManifestAcceptsMCPTransport(t *testing.T) {
	data := []byte(`---
name: hosted
description: uses a hosted MCP server
mcp_tools:
  - name: server
    transport: mcp
    endpoint: https://mcp.example.com/v2/mcp
    auth_type: api-key
    auth_header: X-Key
    credential_ref: hosted-key
---
body
`)
	m, err := skills.ParseManifest(data)
	if err != nil || len(m.MCPServers) != 1 || m.MCPServers[0].Transport != "mcp" {
		t.Fatalf("manifest = %+v, err = %v", m, err)
	}
	// The snake_case keys must reach the struct; an untagged field would silently drop them.
	if srv := m.MCPServers[0]; srv.AuthType != "api-key" || srv.AuthHeader != "X-Key" || srv.CredentialRef != "hosted-key" {
		t.Fatalf("auth fields lost: %+v", srv)
	}
}

func TestParseManifestRejectsMCPServerWithNoEndpoint(t *testing.T) {
	data := []byte(`---
name: bad
description: has a bogus server
mcp_tools:
  - name: thing
    transport: http
---
body
`)
	if _, err := skills.ParseManifest(data); err == nil {
		t.Fatal("expected an error for an mcp_tools entry with no endpoint")
	}
}

func TestParseManifestRejectsNamesThatShadowTheSkillsAPI(t *testing.T) {
	for _, name := range []string{"pro", "Pro", "register", "upload"} {
		data := []byte("---\nname: " + name + "\ndescription: d\n---\nbody\n")
		if _, err := skills.ParseManifest(data); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("name %q must be rejected as reserved, got %v", name, err)
		}
	}
	if _, err := skills.ParseManifest([]byte("---\nname: professional\ndescription: d\n---\nbody\n")); err != nil {
		t.Fatalf("a name that merely starts with a reserved word is fine: %v", err)
	}
}

func TestParseManifestAcceptsAllowedToolsAsListOrString(t *testing.T) {
	cases := map[string]string{
		"list":        "allowed-tools:\n  - Read\n  - Bash(git add:*)",
		"spaces":      "allowed-tools: Read Bash(git add:*)",
		"commas":      "allowed-tools: Read, Bash(git add:*)",
		"folded-text": "allowed-tools: >\n  Read\n  Bash(git add:*)",
	}
	for name, field := range cases {
		data := []byte("---\nname: x\ndescription: y\n" + field + "\n---\nbody\n")
		m, err := skills.ParseManifest(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(m.AllowedTools) != 2 || m.AllowedTools[0] != "Read" || m.AllowedTools[1] != "Bash(git add:*)" {
			t.Errorf("%s: allowed tools = %q", name, m.AllowedTools)
		}
	}
}
