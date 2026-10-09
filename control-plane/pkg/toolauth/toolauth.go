// Package toolauth is how a tool's authentication is stored, resolved and shown.
//
// A tool's AuthConfig names a credential; it does not contain one. The gateway looks the
// credential up when it calls the tool, so a rotated secret takes effect on the next call, with
// no re-registration, and no token sits in the tool's row or in an API response.
//
//	stored:   {"type": "bearer",  "credential_ref": "firecrawl-api-key"}
//	          {"type": "api-key", "credential_ref": "acme-key", "header": "X-Api-Key"}
//	resolved: {"type": "bearer",  "token": "<value>"}                 (in memory, for one call)
//
// A tool registered with a literal token (older tools, or one posted directly to the tools API)
// still works, and is redacted whenever it is returned.
package toolauth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// CredentialReader is the part of the store that returns a kitchen credential's value.
type CredentialReader interface {
	GetKitchenCredential(ctx context.Context, kitchen, name string) (*models.KitchenCredential, error)
}

// Ref builds the stored form of a tool's authentication: a type and the name of the credential.
func Ref(authType, header, credential string) map[string]interface{} {
	cfg := map[string]interface{}{"type": authType, "credential_ref": credential}
	if header != "" {
		cfg["header"] = header
	}
	return cfg
}

// Resolve returns the authentication to apply to one call. A config that names a credential
// is looked up now; one that does not (a literal token, or none) is returned as it is. A
// credential that cannot be read is an error: a call is never made without the authentication
// it was registered with.
func Resolve(ctx context.Context, creds CredentialReader, kitchen string, cfg map[string]interface{}) (map[string]interface{}, error) {
	ref, _ := cfg["credential_ref"].(string)
	if ref == "" {
		return cfg, nil
	}
	if creds == nil {
		return nil, fmt.Errorf("credential %q cannot be read: no credential store", ref)
	}
	cred, err := creds.GetKitchenCredential(ctx, kitchen, ref)
	if err != nil {
		return nil, fmt.Errorf("credential %q is not available: %w", ref, err)
	}
	authType, _ := cfg["type"].(string)
	out := map[string]interface{}{"type": authType}
	switch authType {
	case "bearer":
		out["token"] = cred.Value
	case "api-key", "api_key":
		out["header"], out["key"] = cfg["header"], cred.Value
	default:
		return nil, fmt.Errorf("credential %q: unsupported auth type %q", ref, authType)
	}
	return out, nil
}

// Apply sets the authentication headers a resolved config describes.
func Apply(req *http.Request, cfg map[string]interface{}) {
	if cfg == nil {
		return
	}
	authType, _ := cfg["type"].(string)
	switch authType {
	case "bearer":
		if token, ok := cfg["token"].(string); ok && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	case "api-key", "api_key":
		header, _ := cfg["header"].(string)
		key, _ := cfg["key"].(string)
		if header != "" && key != "" {
			req.Header.Set(header, key)
		}
	case "basic":
		// basic auth would be set via URL or explicit header
	}
}

// Redacted is the form of an auth config that is safe to return: a credential's name is kept
// (it is not a secret and says what the tool uses), and any literal secret is hidden.
func Redacted(cfg map[string]interface{}) map[string]interface{} {
	if cfg == nil {
		return nil
	}
	out := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		switch k {
		case "type", "header", "credential_ref":
			out[k] = v
		default:
			// token, key, password, secret, and anything else a caller put here.
			out[k] = "****"
		}
	}
	return out
}

// RedactTool returns a copy of a tool that is safe to return from the API.
func RedactTool(t models.MCPTool) models.MCPTool {
	t.AuthConfig = Redacted(t.AuthConfig)
	return t
}

// RedactTools does RedactTool for a list.
func RedactTools(tools []models.MCPTool) []models.MCPTool {
	out := make([]models.MCPTool, len(tools))
	for i, t := range tools {
		out[i] = RedactTool(t)
	}
	return out
}
