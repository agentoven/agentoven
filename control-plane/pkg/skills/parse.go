// Package skills implements the open, vendor-neutral Agent Skills format
// (https://agentskills.io) for AgentOven: parsing a SKILL.md bundle,
// enforcing its size limits, and running the provider-based intent check a
// skill must pass before it is usable by any agent.
package skills

import (
	"fmt"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"gopkg.in/yaml.v3"
)

// frontmatterDelim is the YAML frontmatter fence SKILL.md (and every other
// Agent Skills-compatible file) uses: the file starts with a line of exactly
// "---", the frontmatter runs until the next such line, and everything after
// that is the markdown body.
const frontmatterDelim = "---"

// frontmatter mirrors models.SkillManifest's yaml-tagged fields exactly;
// kept separate so SkillManifest.Instructions (not a frontmatter field) never
// has a stray yaml tag that could confuse a future frontmatter round-trip.
type frontmatter struct {
	Name         string                  `yaml:"name"`
	Description  string                  `yaml:"description"`
	License      string                  `yaml:"license,omitempty"`
	AllowedTools []string                `yaml:"allowed-tools,omitempty"`
	MCPServers   []models.SkillMCPServer `yaml:"mcp_tools,omitempty"`
}

// ParseManifest parses a SKILL.md file's raw bytes into a SkillManifest.
// name and description are required, matching every Agent Skills-compatible
// client's own validation — a skill without them can never reach the
// discovery stage (progressive disclosure has nothing to show).
func ParseManifest(data []byte) (*models.SkillManifest, error) {
	content := string(data)
	fm, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, err
	}

	var parsed frontmatter
	if err := yaml.Unmarshal([]byte(fm), &parsed); err != nil {
		return nil, fmt.Errorf("invalid SKILL.md frontmatter: %w", err)
	}
	if strings.TrimSpace(parsed.Name) == "" {
		return nil, fmt.Errorf("SKILL.md frontmatter must set 'name'")
	}
	if strings.TrimSpace(parsed.Description) == "" {
		return nil, fmt.Errorf("SKILL.md frontmatter must set 'description'")
	}

	for i, srv := range parsed.MCPServers {
		if strings.TrimSpace(srv.Name) == "" {
			return nil, fmt.Errorf("mcp_tools[%d] must set 'name'", i)
		}
		if srv.Transport != "http" && srv.Transport != "sse" {
			return nil, fmt.Errorf("mcp_tools[%d] (%s): transport must be 'http' or 'sse', got %q", i, srv.Name, srv.Transport)
		}
		if strings.TrimSpace(srv.Endpoint) == "" {
			return nil, fmt.Errorf("mcp_tools[%d] (%s): endpoint is required", i, srv.Name)
		}
	}

	return &models.SkillManifest{
		Name:         strings.TrimSpace(parsed.Name),
		Description:  strings.TrimSpace(parsed.Description),
		License:      parsed.License,
		AllowedTools: parsed.AllowedTools,
		MCPServers:   parsed.MCPServers,
		Instructions: strings.TrimSpace(body),
	}, nil
}

// splitFrontmatter separates a SKILL.md's leading "---"-fenced YAML block
// from its markdown body. A file with no frontmatter fence at all is
// rejected rather than silently treated as body-only — every Agent
// Skills-compatible file carries at least name+description up front.
func splitFrontmatter(content string) (frontmatterYAML, body string, err error) {
	trimmed := strings.TrimLeft(content, "\n\r\t ")
	if !strings.HasPrefix(trimmed, frontmatterDelim) {
		return "", "", fmt.Errorf("SKILL.md must start with a '---' YAML frontmatter block")
	}
	rest := trimmed[len(frontmatterDelim):]
	idx := strings.Index(rest, "\n"+frontmatterDelim)
	if idx == -1 {
		return "", "", fmt.Errorf("SKILL.md frontmatter block is never closed with a second '---'")
	}
	frontmatterYAML = rest[:idx]
	after := rest[idx+len("\n"+frontmatterDelim):]
	if nl := strings.IndexByte(after, '\n'); nl != -1 {
		body = after[nl+1:]
	}
	return frontmatterYAML, body, nil
}
