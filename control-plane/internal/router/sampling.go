package router

import (
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Sampling parameters and the output limit.
//
// RouteRequest has always carried Temperature, TopP, StopSequences and MaxTokens ("drivers map them to
// provider-specific request fields"), but no driver sent them: a caller that set a temperature, and the
// executor's budget, which bounds a call with MaxTokens, were silently ignored. They are sent now, only
// when the caller set them, and in the form each provider takes.
//
// A reasoning model (one that thinks) does not accept a temperature or a top_p on most providers, and
// Anthropic does not allow either with extended thinking, so they are left out for those calls rather than
// failing the request. The output limit is always honoured.

// applyOpenAIParams adds the request's sampling parameters and output limit to an OpenAI-shaped body
// (OpenAI, Azure OpenAI, Ollama, OpenRouter and compatible servers).
func (mr *ModelRouter) applyOpenAIParams(oaiReq *openAIRequest, provider *models.ModelProvider, model string, req *models.RouteRequest) {
	reasoning := mr.canThink(provider.Kind, model)
	if !reasoning {
		oaiReq.Temperature = req.Temperature
		oaiReq.TopP = req.TopP
		if len(req.StopSequences) > 0 {
			oaiReq.Stop = req.StopSequences
		}
	}
	if req.MaxTokens == nil {
		return
	}
	n := *req.MaxTokens
	if mr.tokenParam(provider.Kind, model) == "max_completion_tokens" {
		oaiReq.MaxCompletionTokens = &n
	} else {
		oaiReq.MaxTokens = &n
	}
}

// tokenParam is the name of the output-limit field the model takes: the catalog's answer, else
// max_completion_tokens for OpenAI itself (every current chat model takes it) and max_tokens elsewhere.
func (mr *ModelRouter) tokenParam(kind, model string) string {
	if mr.catalog != nil {
		if c := mr.catalog.Lookup(kind, model); c != nil && c.TokenParamName != "" {
			return c.TokenParamName
		}
	}
	if kind == "openai" {
		return "max_completion_tokens"
	}
	return "max_tokens"
}
