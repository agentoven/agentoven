// Package secretseal moves the secrets that sit inside a free-form config (an ingredient's, a guardrail
// rule's) into the kitchen's credential vault and leaves a vault:// reference behind, and reads them back
// for the one place that needs the value.
//
// What counts as a secret is what models.RedactConfig masks in an API response: a value under a secret-looking
// key (api_key, password, a token...), under "headers", or a URL that carries a user or a token in its query.
// One definition of "secret" means a value the API hides is also a value that is not kept in the clear at
// rest, and a new secret-looking key is covered in both places at once.
//
// The community edition has no vault; there Seal is never called and the config keeps its values as before.
package secretseal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/agentoven/agentoven/control-plane/internal/secretref"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// NamePrefix starts the name of every credential this package writes. Only credentials of the right owner
// are ever resolved here or deleted by Reap, so a config that names somebody else's credential cannot make
// this code read or remove it.
const NamePrefix = "seal-"

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ownerPrefix starts the name of every credential written for owner (an agent). The owner's length comes
// first so no owner's prefix is the start of another's ("a" and "a-b").
func ownerPrefix(owner string) string {
	owner = unsafeName.ReplaceAllString(owner, "-")
	if len(owner) > 100 {
		owner = owner[:100]
	}
	return fmt.Sprintf("%s%d-%s-", NamePrefix, len(owner), owner)
}

// OwnedBy says whether a credential name is one Name made for owner. A configuration is only ever given
// back the credentials of its own owner: a name is computable by anyone who knows an agent's name and a
// rule's id, so a config that points at another agent's credential must not be able to read it.
func OwnedBy(owner, name string) bool { return strings.HasPrefix(name, ownerPrefix(owner)) }

// Name is the credential that holds the secret at path of the config identified by scope (an agent and a rule).
func Name(owner, scope, path string) string {
	sum := sha256.Sum256([]byte(owner + "\x00" + scope + "\x00" + path))
	return ownerPrefix(owner) + hex.EncodeToString(sum[:6])
}

// sealable says whether a text value found at a secret place should move to the vault: it must be set, not
// the mask a client sent back, and not already a reference to a credential.
func sealable(secret bool, s string) bool {
	if s == "" || models.IsMasked(s) || models.IsReference(s) {
		return false
	}
	return secret || models.RedactURL(s) != s
}

// Seal returns a copy of cfg in which every secret has been written to the vault (kitchen's) and replaced by
// a reference, and the names of the credentials it wrote. Sealing again changes nothing: a reference is left
// alone. If a write fails the credentials written so far are in written, so the caller can remove them.
func Seal(ctx context.Context, v secretref.Vault, kitchen, owner, scope string, cfg map[string]interface{}) (out map[string]interface{}, written []string, err error) {
	if v == nil || cfg == nil {
		return cfg, nil, nil
	}
	var walk func(path string, n interface{}, secret bool) (interface{}, error)
	walk = func(path string, n interface{}, secret bool) (interface{}, error) {
		switch val := n.(type) {
		case map[string]interface{}:
			m := make(map[string]interface{}, len(val))
			for k, inner := range val {
				r, err := walk(path+"."+k, inner, secret || models.IsSecretKey(k) || strings.EqualFold(k, "headers"))
				if err != nil {
					return nil, err
				}
				m[k] = r
			}
			return m, nil
		case []interface{}:
			l := make([]interface{}, len(val))
			for i, inner := range val {
				r, err := walk(fmt.Sprintf("%s[%d]", path, i), inner, secret)
				if err != nil {
					return nil, err
				}
				l[i] = r
			}
			return l, nil
		case string:
			if !sealable(secret, val) {
				return val, nil
			}
			name := Name(owner, scope, path)
			if err := v.Write(ctx, kitchen, name, val, "Sealed from "+scope); err != nil {
				return nil, fmt.Errorf("could not store a secret of %s in the vault: %w", scope, err)
			}
			written = append(written, name)
			return secretref.VaultRef(name), nil
		default:
			return n, nil
		}
	}
	r, err := walk("", cfg, false)
	if err != nil {
		return nil, written, err
	}
	return r.(map[string]interface{}), written, nil
}

// Unseal returns a copy of cfg with every reference this package wrote replaced by the credential's current
// value, read for kitchen. Only credentials written for owner are read. It is for the one place that uses the value (the resolver at bake, the rule
// evaluator); what it returns is never stored or sent to a client. A credential that cannot be read is an
// error: a rule or ingredient must not run without its secret as if it had none.
func Unseal(ctx context.Context, v secretref.Vault, kitchen, owner string, cfg map[string]interface{}) (map[string]interface{}, error) {
	if v == nil || cfg == nil || len(Refs(cfg)) == 0 {
		return cfg, nil
	}
	var walk func(n interface{}) (interface{}, error)
	walk = func(n interface{}) (interface{}, error) {
		switch val := n.(type) {
		case map[string]interface{}:
			m := make(map[string]interface{}, len(val))
			for k, inner := range val {
				r, err := walk(inner)
				if err != nil {
					return nil, err
				}
				m[k] = r
			}
			return m, nil
		case []interface{}:
			l := make([]interface{}, len(val))
			for i, inner := range val {
				r, err := walk(inner)
				if err != nil {
					return nil, err
				}
				l[i] = r
			}
			return l, nil
		case string:
			name, ok := sealedName(val)
			if !ok {
				return val, nil
			}
			if !OwnedBy(owner, name) {
				return nil, fmt.Errorf("this configuration points at a credential that is not its own")
			}
			secret, err := v.Read(ctx, kitchen, name)
			if err != nil {
				return nil, fmt.Errorf("the credential %q this configuration keeps a secret in cannot be read", name)
			}
			return secret, nil
		default:
			return n, nil
		}
	}
	r, err := walk(cfg)
	if err != nil {
		return nil, err
	}
	return r.(map[string]interface{}), nil
}

func sealedName(s string) (string, bool) {
	name, ok := secretref.VaultName(s)
	return name, ok && strings.HasPrefix(name, NamePrefix)
}

// Refs lists the credentials this package wrote that cfg points at, sorted and without repeats.
func Refs(cfg map[string]interface{}) []string {
	seen := map[string]bool{}
	var walk func(n interface{})
	walk = func(n interface{}) {
		switch val := n.(type) {
		case map[string]interface{}:
			for _, inner := range val {
				walk(inner)
			}
		case []interface{}:
			for _, inner := range val {
				walk(inner)
			}
		case string:
			if name, ok := sealedName(val); ok {
				seen[name] = true
			}
		}
	}
	walk(cfg)
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Reap deletes the credentials in before that are not in after: a rule or an ingredient that was removed or
// whose secret moved takes its credential with it. Errors are returned for the caller to log; a credential
// that stays is an orphan, not a leak, since nothing refers to it.
func Reap(ctx context.Context, v secretref.Vault, kitchen, owner string, before, after []string) error {
	if v == nil {
		return nil
	}
	keep := map[string]bool{}
	for _, n := range after {
		keep[n] = true
	}
	var firstErr error
	for _, n := range before {
		if !keep[n] && OwnedBy(owner, n) {
			if err := v.Delete(ctx, kitchen, n); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
