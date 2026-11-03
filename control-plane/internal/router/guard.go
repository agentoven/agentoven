package router

import (
	"context"
	"errors"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// The router is the one place every model call passes (ADR-0049, ADR-0051). A call is judged in a fixed
// order: the kitchen's guardrails on the prompt (ContentGuard.Input), then, in callProvider, the response
// cache, the spend gate (which also holds the rate limits) and the provider, then the guardrails on the
// reply (ContentGuard.Output). A route, a hosted process, a recipe step or a document reader that calls
// Route or RouteStream gets all of it without having to ask, so a new door cannot be built that skips a
// layer. TestEveryProviderCallPassesTheChain keeps it that way.
//
// A caller that has already judged the exchange with the agent's own rules as well as the kitchen's (the
// executor's loop, the agent invoke handlers) says so with ContextGuardrailsApplied; the router then does
// not judge it a second time. It is a statement by code in this process: nothing a request carries can
// make it.

// ContentGuard is the router's view of the guardrails; the edition's server installs one (SetContentGuard).
// Every method fails closed: an error is a refusal, because a guard that cannot be consulted is not a
// guard that said yes.
type ContentGuard interface {
	// Input judges the prompt and returns the request to send: the one given, or a copy with what a rule
	// redacted taken out. A *models.GuardrailBlockedError refuses the call.
	Input(ctx context.Context, req *models.RouteRequest) (*models.RouteRequest, error)
	// Output judges the reply (its text, its reasoning and the arguments of its tool calls) and returns the
	// reply to hand on: the one given, or a copy with redactions applied. A *models.GuardrailBlockedError
	// refuses it.
	Output(ctx context.Context, req *models.RouteRequest, resp *models.RouteResponse) (*models.RouteResponse, error)
	// HoldsOutput reports whether any rule applies to the reply, so a stream must be held back and checked
	// whole before any of it is released.
	HoldsOutput(ctx context.Context, req *models.RouteRequest) (bool, error)
}

type guardBox struct{ g ContentGuard }

// SetContentGuard installs the guard every model call is judged by. Pass nil to remove it. It is safe to
// call while the router is in use.
func (mr *ModelRouter) SetContentGuard(g ContentGuard) {
	if g == nil {
		mr.contentGuard.Store(nil)
		return
	}
	mr.contentGuard.Store(&guardBox{g: g})
}

type ctxKeyGuardrailsApplied struct{}

// ContextGuardrailsApplied marks a call whose caller has judged the exchange with the agent's rules and the
// kitchen's, so the router leaves it alone. Set it only where that is true.
func ContextGuardrailsApplied(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyGuardrailsApplied{}, true)
}

type ctxKeySystemPromptTrusted struct{}

// ContextSystemPromptTrusted marks a call whose system messages were written by the platform's own operator
// and not by the caller (a hosted agent's process sends the prompt it was baked with), so the guard leaves the
// system messages alone and judges what else the call carries.
func ContextSystemPromptTrusted(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeySystemPromptTrusted{}, true)
}

// SystemPromptTrusted reports whether the call was marked with ContextSystemPromptTrusted.
func SystemPromptTrusted(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeySystemPromptTrusted{}).(bool)
	return v
}

func guardrailsApplied(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeyGuardrailsApplied{}).(bool)
	return v
}

// guard is the guard that applies to this call, nil when none is installed or the caller has judged it.
func (mr *ModelRouter) guard(ctx context.Context) ContentGuard {
	if guardrailsApplied(ctx) {
		return nil
	}
	if box := mr.contentGuard.Load(); box != nil {
		return box.g
	}
	return nil
}

// guardInput is the first step of every call: the request to send, or the refusal.
func (mr *ModelRouter) guardInput(ctx context.Context, req *models.RouteRequest) (*models.RouteRequest, error) {
	g := mr.guard(ctx)
	if g == nil {
		return req, nil
	}
	out, err := g.Input(ctx, req)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("the content guard returned no request")
	}
	return out, nil
}

// ReplyJudge is a caller's own judgement of a reply, for a caller that judges with more than the kitchen's rules
// (an agent's own as well) and needs the refusal or the redaction to happen before the router records the call, so
// the trace and the capture the router writes hold what the caller was allowed to see and nothing else.
type ReplyJudge func(ctx context.Context, req *models.RouteRequest, resp *models.RouteResponse) (*models.RouteResponse, error)

type ctxKeyReplyJudge struct{}

// ContextReplyJudge makes the router run j on the reply, in place of the content guard (the caller has judged the
// prompt itself: use it together with ContextGuardrailsApplied).
func ContextReplyJudge(ctx context.Context, j ReplyJudge) context.Context {
	return context.WithValue(ctx, ctxKeyReplyJudge{}, j)
}

func replyJudgeFrom(ctx context.Context) ReplyJudge {
	j, _ := ctx.Value(ctxKeyReplyJudge{}).(ReplyJudge)
	return j
}

// guardOutput is the last step of a call that got an answer: the answer to hand on, or the refusal. A refused
// answer is returned too, emptied of everything the rule judged but with its usage, so a trace can still show
// that the provider was paid for it.
func (mr *ModelRouter) guardOutput(ctx context.Context, req *models.RouteRequest, resp *models.RouteResponse) (*models.RouteResponse, error) {
	if resp == nil {
		return resp, nil
	}
	judge := replyJudgeFrom(ctx)
	if judge == nil {
		g := mr.guard(ctx)
		if g == nil {
			return resp, nil
		}
		judge = g.Output
	}
	out, err := judge(ctx, req, resp)
	if err != nil {
		withheld := *resp
		withheld.Content, withheld.ThinkingBlocks, withheld.ToolCalls, withheld.ContentParts = "", nil, nil, nil
		return &withheld, err
	}
	if out == nil {
		return resp, errors.New("the content guard returned no reply")
	}
	return out, nil
}

// RouteStream is routeStream behind the guard. With a rule that applies to replies the stream is held back:
// the chunks are collected, the whole reply is judged, and only then are they released (as one text chunk,
// the reasoning, the tool calls and the usage), so nothing a rule would have refused or redacted reaches
// the caller. Without one the stream runs as it always did.
func (mr *ModelRouter) RouteStream(ctx context.Context, req *models.RouteRequest, callback func(chunk *models.StreamChunk) error) error {
	g := mr.guard(ctx)
	if g == nil {
		return mr.routeStream(ctx, req, callback)
	}
	greq, err := mr.guardInput(ctx, req)
	if err != nil {
		return err
	}
	holds, err := g.HoldsOutput(ctx, greq)
	if err != nil {
		return err
	}
	if !holds {
		return mr.routeStream(ctx, greq, callback)
	}
	var collected models.RouteResponse
	var thinking models.ThinkingBlock
	var usage *models.TokenUsage
	err = mr.routeStream(ctx, greq, func(c *models.StreamChunk) error {
		if c == nil {
			return nil
		}
		collected.Content += c.Content
		thinking.Content += c.ThinkingContent
		if tc := c.ToolCall; tc != nil {
			if tc.ID != "" || len(collected.ToolCalls) == 0 {
				var call models.ToolCallResult
				call.ID, call.Type = tc.ID, "function"
				collected.ToolCalls = append(collected.ToolCalls, call)
			}
			last := &collected.ToolCalls[len(collected.ToolCalls)-1]
			last.Function.Name += tc.Name
			last.Function.Arguments += tc.Arguments
		}
		if c.Usage != nil {
			usage = c.Usage
		}
		if c.Error != "" {
			return errors.New(c.Error)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if thinking.Content != "" {
		collected.ThinkingBlocks = []models.ThinkingBlock{thinking}
	}
	out, err := mr.guardOutput(ctx, greq, &collected)
	if err != nil {
		return err
	}
	for _, tb := range out.ThinkingBlocks {
		if err := callback(&models.StreamChunk{ThinkingContent: tb.Content}); err != nil {
			return err
		}
	}
	for _, tc := range out.ToolCalls {
		if err := callback(&models.StreamChunk{ToolCall: &models.ToolCallChunk{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments}}); err != nil {
			return err
		}
	}
	if out.Content != "" {
		if err := callback(&models.StreamChunk{Content: out.Content}); err != nil {
			return err
		}
	}
	return callback(&models.StreamChunk{Done: true, Usage: usage})
}

// RouteVia sends a request to one provider that is not registered in a kitchen (the prompt validator's own
// endpoint and key, which the kitchen keeps in its settings) through the same chain as any other call: the
// guard on the prompt, the circuit and the retries, the response cache and the spend gate, the guard on the
// reply, and a trace. The provider is used as given and nothing falls back to another.
func (mr *ModelRouter) RouteVia(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	req, err := mr.guardInput(ctx, req)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	p := *provider // applyKey and the drivers may set a key on it: the caller's copy is left alone
	var attempts []models.RouteAttempt
	resp, err := mr.callWithRetry(ctx, &p, req)
	if err != nil {
		attempts = append(attempts, mr.attemptOf(&p, req, err, time.Since(started)))
	} else {
		mr.trackCost(req.Kitchen, req.AgentRef, resp)
		resp, err = mr.guardOutput(ctx, req, resp)
	}
	mr.finishRoute(ctx, req, resp, attempts, started, err)
	if err != nil {
		return nil, err
	}
	return resp, nil
}
