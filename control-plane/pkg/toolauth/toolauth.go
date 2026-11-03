// Package toolauth is how a tool's authentication is stored, resolved and shown.
//
// A tool's AuthConfig names a credential; it does not contain one. The gateway looks the
// credential up when it calls the tool, so a rotated secret takes effect on the next call, with
// no re-registration, and no token sits in the tool's row or in an API response.
//
//	stored:   {"type": "bearer",  "credential_ref": "firecrawl-api-key"}
//	          {"type": "api-key", "credential_ref": "acme-key", "header": "X-Api-Key"}
//	          {"type": "basic",   "credential_ref": "erp-password", "username": "svc-agentoven"}
//	resolved: {"type": "bearer",  "token": "<value>"}                 (in memory, for one call)
//
// Those three are the authentication a tool can have, and Validate refuses every other shape: a field
// nothing sends is a secret stored for nothing (an OAuth client secret, a set of custom headers).
// A tool registered with a literal secret (older tools, or one posted directly to the tools API) still
// works, is moved into the credential vault where there is one (LiteralSecret finds it), and is redacted
// whenever it is returned.
package toolauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// CredentialReader is the part of the store that returns a kitchen credential's value.
type CredentialReader interface {
	GetKitchenCredential(ctx context.Context, kitchen, name string) (*models.KitchenCredential, error)
}

// Use describes where a credential is about to be sent: the skill that owns the tool and the
// endpoint it will be sent to. A credential policy (Pro) decides from it whether the credential may
// be used for this call.
type Use struct {
	Skill    string
	Endpoint string
}

// UsePolicy is implemented by a credential store that governs where a credential may go: a secret
// pinned to hosts is refused for any other endpoint, and one that needs an approved binding is
// refused for a skill with none. A store without it (the community edition) allows every use.
type UsePolicy interface {
	CheckUse(ctx context.Context, kitchen, credential string, use Use) error
}

// ResolveFor is Resolve for a call whose destination is known. When the credential store has a
// UsePolicy, it is asked before the value is read, so a credential that may not go to this endpoint is
// never even loaded for it.
func ResolveFor(ctx context.Context, creds CredentialReader, kitchen string, use Use, cfg map[string]interface{}) (map[string]interface{}, error) {
	if ref, _ := cfg["credential_ref"].(string); ref != "" {
		if p, ok := creds.(UsePolicy); ok {
			if err := p.CheckUse(ctx, kitchen, ref, use); err != nil {
				return nil, fmt.Errorf("credential %q may not be used here: %w", ref, err)
			}
		}
	}
	return Resolve(ctx, creds, kitchen, cfg)
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
	case "basic":
		out["username"], out["password"] = cfg["username"], cred.Value
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
		user, _ := cfg["username"].(string)
		pass, _ := cfg["password"].(string)
		if user != "" || pass != "" {
			req.SetBasicAuth(user, pass)
		}
	}
}

// Redacted is the form of an auth config that is safe to return: a credential's name is kept
// (it is not a secret and says what the tool uses, nor is a user name), and any literal secret is hidden.
func Redacted(cfg map[string]interface{}) map[string]interface{} {
	if cfg == nil {
		return nil
	}
	out := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		switch {
		case k == "type" || k == "header" || k == "username" || k == "credential_ref" || strings.HasSuffix(k, "_ref"):
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

// Redacted is what a scrubbed credential is replaced with.
const scrubbed = "[redacted]"

// minScrub is the shortest value worth scrubbing: replacing a two-letter value would mangle ordinary text.
const minScrub = 6

// Values returns the secret values in a resolved auth config (the token or key that is about to be sent).
func Values(resolved map[string]interface{}) []string {
	var out []string
	for _, k := range []string{"token", "key", "password"} {
		if v, _ := resolved[k].(string); len(v) >= minScrub {
			out = append(out, v)
		}
	}
	return out
}

// Scrub replaces every occurrence of the given secret values in text.
func Scrub(text string, values []string) string {
	for _, v := range values {
		text = strings.ReplaceAll(text, v, scrubbed)
	}
	return text
}

// ScrubResult removes the credential a call was made with from what came back, in the content and in an
// error, so a tool server that echoes the credential never hands it to the model. It returns its inputs
// unchanged when the call carried no credential. (A server that returns the value encoded or split is not
// caught; this is a backstop, not a guarantee.)
func ScrubResult(result *models.MCPToolResult, err error, resolved map[string]interface{}) (*models.MCPToolResult, error) {
	vals := Values(resolved)
	if len(vals) == 0 {
		return result, err
	}
	if err != nil {
		if clean := Scrub(err.Error(), vals); clean != err.Error() {
			err = errors.New(clean)
		}
	}
	if result != nil {
		for i := range result.Content {
			result.Content[i].Text = Scrub(result.Content[i].Text, vals)
		}
	}
	return result, err
}
