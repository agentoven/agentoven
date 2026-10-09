package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/a2aauth"
	"github.com/rs/zerolog/log"
)

// signPodRequest proves to an agent pod that this call comes from the control plane. The
// token is derived for that one agent, so a pod's token opens no other pod. With no secret
// configured nothing is added, and a pod started without a token accepts the call as before.
func (h *Handlers) signPodRequest(req *http.Request, kitchen, agent string) {
	if tok := a2aauth.PodToken(h.A2ASecret, kitchen, agent); tok != "" {
		req.Header.Set(a2aauth.PodTokenHeader, tok)
	}
}

// providerOverrideAllowed reports whether a request may choose how the control plane or a
// pod connects to a model provider (a CA bundle, or skipping TLS verification). That is the
// platform's decision, made from the provider's own record and passed along by the workflow
// engine, so only the platform's own callers are believed. An ordinary caller, however
// privileged, could otherwise weaken the connection that carries the provider's API key.
func providerOverrideAllowed(r *http.Request) bool {
	return middleware.IsInternalCaller(r)
}

// stripProviderOverride removes params.provider_config from an A2A JSON-RPC body. It returns
// the body unchanged when there is nothing to remove or it is not JSON-RPC.
func stripProviderOverride(body []byte) ([]byte, bool) {
	var env map[string]json.RawMessage
	if json.Unmarshal(body, &env) != nil {
		return body, false
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(env["params"], &params) != nil {
		return body, false
	}
	if _, ok := params["provider_config"]; !ok {
		return body, false
	}
	delete(params, "provider_config")
	p, err := json.Marshal(params)
	if err != nil {
		return body, false
	}
	env["params"] = p
	out, err := json.Marshal(env)
	if err != nil {
		return body, false
	}
	log.Warn().Msg("A2A: ignored a provider TLS override from a caller that is not the platform")
	return out, true
}
