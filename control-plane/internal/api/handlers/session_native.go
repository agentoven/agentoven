package handlers

import (
	"net/http"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/ctxwindow"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// sendNativeSessionMessage serves a session message for an agent that runs as its own
// process (LangChain, LangGraph, Deep Agents, CrewAI, custom).
//
// The general session path sends the transcript straight to the model router, which for
// a hosted framework would mean the agent's own code (its graph, tools and prompts) is
// never run and a different model reply is returned in its place. Instead the message
// goes to the agent's process, with the session ID as its thread ID so the framework's own
// checkpointer carries the conversation. The control plane still keeps the transcript, for
// audit and for the session API.
//
// The caller has already appended the user's message to session.Messages and passed the
// input guardrails; this runs the output guardrails on the reply.
func (h *Handlers) sendNativeSessionMessage(w http.ResponseWriter, r *http.Request, agent *models.Agent, session *models.Session, content string, gr []models.Guardrail) {
	dropUserMessage := func() {
		if n := len(session.Messages); n > 0 {
			session.Messages = session.Messages[:n-1]
		}
	}

	if agent.Status != models.AgentStatusReady {
		dropUserMessage()
		respondError(w, http.StatusBadRequest, "Agent '"+agent.Name+"' is not ready (status: "+string(agent.Status)+") — bake it first")
		return
	}

	start := time.Now()
	ctx, endRun := h.beginHostedRun(withProxySession(r.Context(), session.ID), agent)
	reply, traceRecord, err := h.proxyToProcess(ctx, agent, content, nil, nil, agent.Kitchen)
	if outcome := endRun(); outcome != nil && outcome.Stopped != nil {
		dropUserMessage()
		h.respondHostedStopped(w, agent.Name, hostedRunFrom(ctx), outcome)
		return
	}
	if err != nil {
		dropUserMessage()
		respondError(w, http.StatusBadGateway, "Framework agent session call failed: "+err.Error())
		return
	}
	latencyMs := time.Since(start).Milliseconds()

	reply, ok, eval := h.outputText(r.Context(), agent, gr, reply)
	if !ok {
		dropUserMessage()
		respondOutputBlocked(w, eval)
		return
	}

	usage := models.TokenUsage{}
	if traceRecord != nil && traceRecord.Usage != nil {
		usage = *traceRecord.Usage
	}

	session.Messages = append(session.Messages, models.ChatMessage{Role: "assistant", Content: reply})
	session.TurnCount++
	session.TotalTokens += usage.TotalTokens
	session.TotalCost += usage.EstimatedCost
	session.UpdatedAt = time.Now().UTC()
	if session.MaxTurns > 0 && session.TurnCount >= session.MaxTurns {
		session.Status = models.SessionCompleted
	}
	if err := h.Sessions.UpdateSession(r.Context(), session); err != nil {
		log.Error().Err(err).Str("session", session.ID).Msg("Failed to update session")
	}

	// The framework owns its context window, so the report is only an estimate of the
	// transcript the control plane holds.
	budget := ctxwindow.EffectiveBudget(agent.ContextBudget, 0)
	used := ctxwindow.EstimateTokensForMessages(session.Messages)
	respondJSON(w, http.StatusOK, models.SessionResponse{
		SessionID:     session.ID,
		TurnNumber:    session.TurnCount,
		Content:       reply,
		FinishReason:  "stop",
		Usage:         usage,
		ContextBudget: ctxwindow.BuildReport(0, budget, used, false, 0, 0),
		LatencyMs:     latencyMs,
		Status:        session.Status,
	})
}
