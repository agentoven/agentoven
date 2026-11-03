package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/toolbroker"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// POST /api/v1/internal/tool-calls (ADR-0044): a hosted agent process asks the control plane to run a tool call for it.
// The only credential it accepts is the run token the control plane gave the process for the call it is handling; it
// is not a user, an API key or a service account, and the route needs no AGENTOVEN_REQUIRE_AUTH because it opens
// nothing without the token. Who is calling (kitchen, agent, run) comes from the token, never from the body.

// maxBrokerBody bounds one request. Arguments larger than this are not a tool call.
const maxBrokerBody = 1 << 20

type brokerRequest struct {
	CallID    string                 `json:"call_id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type brokerError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type brokerResponse struct {
	OK      bool         `json:"ok"`
	Content string       `json:"content,omitempty"`
	IsError bool         `json:"is_error,omitempty"`
	Error   *brokerError `json:"error,omitempty"`
	// Approval is how the approval gate went for a call that needed one: approved, denied or expired.
	Approval string `json:"approval,omitempty"`
}

// SetToolBroker installs the broker; with none (no secret to sign with) a process is given no token and cannot run a tool.
func (h *Handlers) SetToolBroker(b *toolbroker.Broker) { h.ToolBroker = b }

// openToolScope opens the scope of one relayed call to a process the platform launched, and returns the context the
// relay sends the token from. The returned func closes the scope. A bot (its own gate) and an agent whose backend is
// not ours get nothing.
func (h *Handlers) openToolScope(ctx context.Context, agent *models.Agent) (context.Context, func()) {
	if h.ToolBroker == nil || agent == nil || agent.IsAgenticBot() || !agent.IsManaged() {
		return ctx, func() {}
	}
	run := hostedRunFrom(ctx)
	if run == "" {
		run = newScopeRunID()
	}
	tok, closeScope := h.ToolBroker.Open(agent.Kitchen, agent.Name, run)
	if tok == "" {
		return ctx, closeScope
	}
	return context.WithValue(ctx, toolTokenKey{}, tok), closeScope
}

type toolTokenKey struct{}

func toolTokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(toolTokenKey{}).(string)
	return t
}

func brokerRefuse(w http.ResponseWriter, status int, kind, msg string) {
	respondJSON(w, status, brokerResponse{Error: &brokerError{Kind: kind, Message: msg}, IsError: true})
}

func (h *Handlers) brokerAudit(ctx context.Context, c toolbroker.Claims, name, outcome string, extra map[string]interface{}) {
	if h.Executor == nil {
		return
	}
	d := map[string]interface{}{"agent": c.Agent, "run": c.Run, "trace_id": c.Run, "tool": name, "outcome": outcome}
	for k, v := range extra {
		d[k] = v
	}
	h.Executor.AuditRun(ctx, c.Kitchen, "tool.brokered_call", d)
}

// BrokeredToolCall is POST /api/v1/internal/tool-calls.
func (h *Handlers) BrokeredToolCall(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	if h.ToolBroker == nil || !h.ToolBroker.Enabled() {
		brokerRefuse(w, http.StatusServiceUnavailable, "disabled", toolbroker.ErrDisabled.Error())
		return
	}
	grant, err := h.ToolBroker.Admit(tok)
	if err != nil {
		switch {
		case errors.Is(err, toolbroker.ErrScopeClosed):
			brokerRefuse(w, http.StatusForbidden, executor.HostedScopeClosed, err.Error())
		case errors.Is(err, toolbroker.ErrBudget):
			brokerRefuse(w, http.StatusTooManyRequests, "budget", err.Error())
		case errors.Is(err, toolbroker.ErrBusy):
			brokerRefuse(w, http.StatusTooManyRequests, "busy", err.Error())
		case errors.Is(err, toolbroker.ErrExpired):
			brokerRefuse(w, http.StatusUnauthorized, "expired_token", err.Error())
		default:
			w.Header().Set("WWW-Authenticate", `Bearer realm="agentoven-tool-broker"`)
			brokerRefuse(w, http.StatusUnauthorized, "invalid_token", "a valid run token is required")
		}
		return
	}
	resultBytes := 0
	defer func() { grant.Done(resultBytes) }()
	c := grant.Claims

	r.Body = http.MaxBytesReader(w, r.Body, maxBrokerBody)
	var req brokerRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" || len(req.Name) > 200 || len(req.CallID) > 200 {
		brokerRefuse(w, http.StatusBadRequest, "invalid_request", "the body must be {\"call_id\", \"name\", \"arguments\"}, at most 1 MiB")
		return
	}

	ctx := pkgmw.SetKitchen(r.Context(), c.Kitchen)
	ctx = pkgmw.SetIdentity(ctx, &contracts.Identity{Subject: "agent:" + c.Agent, Provider: "run-token", Kind: "agent", Kitchen: c.Kitchen})
	// The agent as it is stored now, not as it was when the process started: a tool taken away stops working at once.
	agent, err := h.Store.GetAgent(ctx, c.Kitchen, c.Agent)
	if err != nil || agent == nil {
		h.brokerAudit(ctx, c, req.Name, "agent_gone", nil)
		brokerRefuse(w, http.StatusForbidden, executor.HostedScopeClosed, "the agent no longer exists")
		return
	}
	resolved := agent.ResolvedConfig
	if resolved == nil {
		if resolved, err = h.Resolver.Resolve(ctx, agent); err != nil {
			brokerRefuse(w, http.StatusBadRequest, "invalid_request", "the agent's ingredients could not be resolved")
			return
		}
	}

	started := time.Now()
	out := h.Executor.RunHostedToolCall(ctx, executor.HostedToolRequest{
		Agent: agent, Resolved: resolved, Run: c.Run, CallID: req.CallID, Name: req.Name, Arguments: req.Arguments,
		StillOpen: grant.Open,
	})
	resultBytes = len(out.Result.Content)
	outcome := "ran"
	switch {
	case out.Kind != "":
		outcome = out.Kind
	case out.Result.IsError:
		outcome = "tool_error"
	}
	h.brokerAudit(ctx, c, req.Name, outcome, map[string]interface{}{
		"approval": out.Approval, "held_run": out.HeldRun, "args_sha256_8": executor.ArgsDigest(req.Arguments),
		"result_bytes": resultBytes, "duration_ms": time.Since(started).Milliseconds(),
	})
	if out.Kind != "" {
		log.Info().Str("agent", c.Agent).Str("tool", req.Name).Str("kind", out.Kind).Msg("brokered tool call refused")
	}
	resp := brokerResponse{OK: out.Kind == "" && !out.Result.IsError, Content: out.Result.Content, IsError: out.Result.IsError, Approval: out.Approval}
	if out.Kind != "" {
		resp.Error = &brokerError{Kind: out.Kind, Message: out.Result.Content}
	}
	respondJSON(w, http.StatusOK, resp)
}
