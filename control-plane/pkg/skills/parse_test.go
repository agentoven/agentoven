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
