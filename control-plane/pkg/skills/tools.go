package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
)

// RemoteToolLister discovers the tools a remote MCP server offers.
// *mcpgw.Gateway satisfies it.
type RemoteToolLister interface {
	ListRemoteTools(ctx context.Context, endpoint string, auth map[string]interface{}) ([]models.RemoteTool, error)
}

// maxToolNameLen is the longest function name every model provider accepts.
const maxToolNameLen = 64

// safeToolName keeps only what every model provider accepts in a function
// name (letters, digits, "_" and "-", at most 64 characters).
func safeToolName(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	if len(b) > maxToolNameLen {
		b = b[:maxToolNameLen]
	}
	return string(b)
}

// ToolsForServer returns the MCPTool rows to register for one bundled MCP
// server. A server on the "http" or "sse" transport is one tool named
// "<skill>_<server>". A server on the "mcp" transport is asked what it
// offers and yields one tool per remote tool, so a skill pointing at a hosted
// MCP server gives the agent all of that server's tools, named
// "<skill>_<tool>" (the prefix is not repeated when the tool already carries
// it). Names are made safe for any model provider's function-name rules.
//
// discoveryAuth is the resolved authentication used once, in memory, to ask the server what
// it offers. storedAuth is what each tool keeps: the name of a credential, never its value
// (see pkg/toolauth). Every tool records the skill that registered it.
func ToolsForServer(ctx context.Context, lister RemoteToolLister, kitchen, skillName string, srv models.SkillMCPServer, discoveryAuth, storedAuth map[string]interface{}) ([]*models.MCPTool, error) {
	now := time.Now().UTC()
	base := models.MCPTool{
		Kitchen:      kitchen,
		Endpoint:     srv.Endpoint,
		Transport:    srv.Transport,
		AuthConfig:   storedAuth,
		Skill:        skillName,
		Capabilities: []string{"tool"},
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if srv.Transport != models.MCPTransportStreamableHTTP {
		t := base
		t.ID, t.Name, t.Description = uuid.New().String(), safeToolName(skillName+"_"+srv.Name), srv.Description
		return []*models.MCPTool{&t}, nil
	}

	if lister == nil {
		return nil, fmt.Errorf("server %q uses the mcp transport but no MCP client is available", srv.Name)
	}
	remote, err := lister.ListRemoteTools(ctx, srv.Endpoint, discoveryAuth)
	if err != nil {
		return nil, fmt.Errorf("discovering tools of %q: %w", srv.Name, err)
	}
	if len(remote) == 0 {
		return nil, fmt.Errorf("server %q offers no tools", srv.Name)
	}

	prefix := safeToolName(skillName) + "_"
	tools := make([]*models.MCPTool, 0, len(remote))
	for _, rt := range remote {
		t := base
		t.ID, t.RemoteName, t.Description = uuid.New().String(), rt.Name, rt.Description
		t.Name = safeToolName(rt.Name)
		if !strings.HasPrefix(t.Name, prefix) {
			t.Name = safeToolName(prefix + t.Name)
		}
		// The executor reads a tool's description from its schema.
		t.Schema = map[string]interface{}{"type": "object"}
		for k, v := range rt.InputSchema {
			t.Schema[k] = v
		}
		if rt.Description != "" {
			t.Schema["description"] = rt.Description
		}
		tools = append(tools, &t)
	}
	return tools, nil
}
