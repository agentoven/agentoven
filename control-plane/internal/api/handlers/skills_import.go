package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/skills"
)

// ══════════════════════════════════════════════════════════════
// ── Plugin import ─────────────────────────────────────────────
// ══════════════════════════════════════════════════════════════
//
// Importing a plugin (Claude Code or Codex layout) from a git location the
// caller names takes its skills and remote MCP servers and registers each
// through the same verify-then-register path as POST /skills/register; the
// rest of a plugin (commands, hooks, local stdio servers) is reported as
// skipped. There is no catalog here: browsing registries, with approved
// sources and an allowlist, is AgentOven Pro.

// importWorkers is how many skills are verified at once.
const importWorkers = 4

// skillImportRequest is POST /api/v1/skills/import's body: where the plugin
// is, plus how to import it.
type skillImportRequest struct {
	skills.ImportSpec
	// Only names the skills to take; empty takes all of them.
	Only []string `json:"only,omitempty"`
	// DryRun returns what would be imported without registering anything.
	DryRun bool `json:"dry_run,omitempty"`
	// Credentials maps an MCP server's name to the kitchen credential that
	// authenticates it, as in /skills/register.
	Credentials map[string]string `json:"credentials,omitempty"`
}

type importResult struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // accepted, needs_review, rejected, error
	Reasoning string `json:"reasoning,omitempty"`
	Error     string `json:"error,omitempty"`
}

type skillImportResponse struct {
	*skills.ImportPlan
	Results []importResult `json:"results,omitempty"`
}

// ImportSkillPlugin imports a plugin's skills and MCP servers.
// POST /api/v1/skills/import
func (h *Handlers) ImportSkillPlugin(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	var req skillImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !req.DryRun {
		providers, err := h.Store.ListProviders(r.Context(), kitchen)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(providers) == 0 {
			respondError(w, http.StatusPreconditionFailed,
				"this kitchen has no registered model providers yet — register one first, since it's what reviews a skill's intent before accepting it")
			return
		}
	}

	fetch := h.FetchSkillPlugin
	if fetch == nil {
		fetch = skills.FetchPlugin
	}
	plugin, root, cleanup, err := fetch(r.Context(), req.ImportSpec)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer cleanup()

	plan, err := plugin.PlanImport(req.ImportSpec, root, req.Only, req.Credentials, req.DryRun)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := skillImportResponse{ImportPlan: plan}
	if req.DryRun {
		respondJSON(w, http.StatusOK, resp)
		return
	}

	resp.Results = make([]importResult, len(plan.Items))
	sem := make(chan struct{}, importWorkers)
	var wg sync.WaitGroup
	for i, item := range plan.Items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, item skills.ImportItem) {
			defer wg.Done()
			defer func() { <-sem }()
			resp.Results[i] = h.importOne(r.Context(), kitchen, item, req.Credentials)
		}(i, item)
	}
	wg.Wait()

	respondJSON(w, http.StatusOK, resp)
}

// importOne registers one skill of a plugin and describes the outcome.
func (h *Handlers) importOne(ctx context.Context, kitchen string, item skills.ImportItem, creds map[string]string) importResult {
	res := importResult{Name: item.Name}
	if _, err := h.Store.GetSkill(ctx, kitchen, item.Name); err == nil {
		res.Status, res.Error = "error", "a skill with this name is already registered; remove it first to import again"
		return res
	}
	bundle, err := item.Load()
	if err != nil {
		res.Status, res.Error = "error", err.Error()
		return res
	}
	status, body := h.registerBundle(ctx, kitchen, models.SkillSourcePlugin, item.Ref, bundle, creds)
	switch status {
	case http.StatusCreated:
		res.Status = "accepted"
	case http.StatusAccepted:
		res.Status = "needs_review"
	case http.StatusUnprocessableEntity:
		res.Status = "rejected"
	default:
		res.Status = "error"
	}
	switch b := body.(type) {
	case map[string]string:
		res.Error = b["error"]
	case map[string]interface{}:
		res.Reasoning, _ = b["reasoning"].(string)
	case *models.Skill:
		res.Reasoning = b.VerificationReasoning
	}
	return res
}
