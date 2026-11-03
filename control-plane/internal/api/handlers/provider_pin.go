package handlers

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A provider's credential is sent to its endpoint on every call, so whoever can change the endpoint of a
// provider that holds a credential can have the credential sent to a host of their choosing. The credential
// is therefore pinned to the host it was given for: repointing the provider at another host needs either
// the right to (ProviderRepoint, a permission in an edition with roles) or a credential entered in the
// same request that replaces every credential the provider holds.

// sameOrigin reports whether two endpoints are the same scheme, host and port. A provider with no endpoint
// calls its driver's default host, which counts as a host of its own.
func sameOrigin(a, b string) bool {
	pa, errA := url.Parse(strings.TrimSpace(a))
	pb, errB := url.Parse(strings.TrimSpace(b))
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(pa.Scheme, pb.Scheme) && strings.EqualFold(pa.Host, pb.Host)
}

// holdsCredential reports whether the provider has anything it sends to its endpoint.
func holdsCredential(p *models.ModelProvider) bool {
	if k, _ := p.Config["api_key"].(string); k != "" {
		return true
	}
	return p.SecretRef != "" || len(p.APIKeys) > 0
}

// freshKey is a key a client typed in this request: present and not a mask read back from a response.
func freshKey(k string) bool { return k != "" && !models.IsMasked(k) }

// replacesCredential reports whether the update brings new credentials for all the provider holds: a new
// pool in which every entry carries a key typed now, or (when the provider has no pool) one new key. What is left of the old credential after such an update is nothing.
func replacesCredential(req *models.ModelProvider, stored *models.ModelProvider) bool {
	if req.APIKeys != nil {
		if len(req.APIKeys) == 0 {
			return false
		}
		for _, e := range req.APIKeys {
			if !freshKey(e.Key) || e.SecretRef != "" {
				return false
			}
		}
		return true
	}
	if len(stored.APIKeys) > 0 {
		return false
	}
	// One new key rotates the credential in place when the stored reference is one the provider made
	// (sealProviderKeys); a reference to someone else's credential is not replaced by it.
	if k, _ := req.Config["api_key"].(string); freshKey(k) && req.SecretRef == "" && (stored.SecretRef == "" || ownsKey(stored.Name, stored.SecretRef)) {
		return true
	}
	// A reference to a credential someone else stored is a choice of which secret to send to the new host,
	// not a credential typed now: it needs the permission.
	return false
}

// mayRepoint decides whether an update that sets the endpoint of provider to newEndpoint is allowed.
func (h *Handlers) mayRepoint(r *http.Request, stored *models.ModelProvider, req *models.ModelProvider) bool {
	if req.Endpoint == "" || sameOrigin(req.Endpoint, stored.Endpoint) || !holdsCredential(stored) {
		return true
	}
	if replacesCredential(req, stored) {
		return true
	}
	return h.ProviderRepoint == nil || h.ProviderRepoint(r)
}
