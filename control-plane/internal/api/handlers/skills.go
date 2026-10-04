package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/skills"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ══════════════════════════════════════════════════════════════
// ── Skills ────────────────────────────────────────────────────
// ══════════════════════════════════════════════════════════════
//
// A skill is the open, vendor-neutral Agent Skills format
// (https://agentskills.io) — a SKILL.md bundle, optionally declaring bundled
// MCP servers — registered into a kitchen. Registration requires at least
// one model provider: that provider is what reviews the skill's declared
// intent against its actual instructions/code before it is ever usable by an
// agent. This is a heuristic filter, not a sandbox — see internal/skills.
//
// RegisterSkill accepts four sources (models.SkillSource): "inline" (SKILL.md
// text + files posted directly), "upload" (a bundle staged via the chunked
// upload endpoints below), "path" (a directory already on the control
// plane's own filesystem), and "git" (cloned at a ref). Only "path" and "git"
// can be refreshed later without re-uploading.

// resolveSkillBundle fetches a skill's bundle from whatever source a request
// names, enforcing the size limits in internal/skills either way.
func (h *Handlers) resolveSkillBundle(ctx context.Context, kitchen string, req skillRegisterRequest, uploads *skills.UploadStore) (skills.Bundle, error) {
	switch models.SkillSource(req.Source) {
	case models.SkillSourceInline:
		if strings.TrimSpace(req.SkillMD) == "" {
			return nil, fmt.Errorf("source 'inline' requires 'skill_md'")
		}
		bundle := skills.Bundle{skills.ManifestFilename: []byte(req.SkillMD)}
		for path, b64 := range req.Files {
			data, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, fmt.Errorf("file %q is not valid base64: %w", path, err)
			}
			if len(data) > skills.MaxSkillFileSize {
				return nil, fmt.Errorf("file %q is %d bytes, exceeding the per-file limit of %d", path, len(data), skills.MaxSkillFileSize)
			}
			bundle[path] = data
		}
		if len(bundle) > skills.MaxSkillFiles {
			return nil, fmt.Errorf("skill bundle has %d files, exceeding the limit of %d", len(bundle), skills.MaxSkillFiles)
		}
		return bundle, nil

	case models.SkillSourceUpload:
		if strings.TrimSpace(req.UploadID) == "" {
			return nil, fmt.Errorf("source 'upload' requires 'upload_id'")
		}
		path, err := uploads.Commit(req.UploadID)
		if err != nil {
			return nil, err
		}
		defer uploads.Discard(req.UploadID)
		return skills.ExtractZipFile(path)

	case models.SkillSourcePath:
		if strings.TrimSpace(req.Path) == "" {
			return nil, fmt.Errorf("source 'path' requires 'path'")
		}
		return skills.LoadDir(req.Path)

	case models.SkillSourceGit:
		if strings.TrimSpace(req.GitURL) == "" {
			return nil, fmt.Errorf("source 'git' requires 'git_url'")
		}
		return skills.CloneGit(ctx, req.GitURL, req.GitRef)

	default:
		return nil, fmt.Errorf("unknown skill source %q (expected inline, upload, path, or git)", req.Source)
	}
}

// skillRegisterRequest is POST /api/v1/skills/register's body.
type skillRegisterRequest struct {
	Source   string            `json:"source"`
	SkillMD  string            `json:"skill_md,omitempty"`
	Files    map[string]string `json:"files,omitempty"` // relative path → base64 content
	UploadID string            `json:"upload_id,omitempty"`
	Path     string            `json:"path,omitempty"`
	GitURL   string            `json:"git_url,omitempty"`
	GitRef   string            `json:"git_ref,omitempty"`
	// Credentials maps a bundled MCP server's name (SkillMCPServer.Name) to
	// the KitchenCredential name supplying its token/key — distinct from
	// SkillMCPServer.CredentialRef, which the manifest author can also set
	// directly; a value here takes precedence, since the kitchen admin doing
	// the registering, not the skill's author, decides which credential is
	// trusted with it.
	Credentials map[string]string `json:"credentials,omitempty"`
}

// RegisterSkill fetches, verifies, and (if accepted) activates a skill.
// POST /api/v1/skills/register
func (h *Handlers) RegisterSkill(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

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

	var req skillRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	bundle, err := h.resolveSkillBundle(r.Context(), kitchen, req, h.SkillUploads)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	manifest, err := bundle.Manifest()
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid SKILL.md: "+err.Error())
		return
	}

	verdict, reasoning, providerUsed, err := skills.VerifyIntent(r.Context(), h.Router, kitchen, manifest, bundle)
	if err != nil {
		respondError(w, http.StatusBadGateway, "skill verification failed: "+err.Error())
		return
	}

	now := time.Now().UTC()
	skill := &models.Skill{
		ID:                    uuid.New().String(),
		Kitchen:               kitchen,
		Name:                  manifest.Name,
		Source:                models.SkillSource(req.Source),
		SourceRef:             sourceRefFor(req),
		Manifest:              manifest,
		VerificationVerdict:   string(verdict),
		VerificationReasoning: reasoning,
		VerifiedAt:            &now,
		VerifiedByProvider:    providerUsed,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if id := pkgmw.GetIdentity(r.Context()); id != nil {
		skill.CreatedBy = id.Subject
	}

	h.finalizeSkillRegistration(w, r, kitchen, skill, verdict, reasoning, bundle, req.Credentials)
}

func sourceRefFor(req skillRegisterRequest) string {
	switch models.SkillSource(req.Source) {
	case models.SkillSourcePath:
		return req.Path
	case models.SkillSourceGit:
		if req.GitRef != "" {
			return req.GitURL + "@" + req.GitRef
		}
		return req.GitURL
	default:
		return ""
	}
}

// finalizeSkillRegistration applies a verdict: reject persists and refuses,
// needs_review persists inert and waits for a human, accept registers the
// skill's bundled MCP tools and activates it. Shared by RegisterSkill and
// RefreshSkill, which both end at "I have a bundle and a verdict, now what".
func (h *Handlers) finalizeSkillRegistration(w http.ResponseWriter, r *http.Request, kitchen string, skill *models.Skill, verdict skills.Verdict, reasoning string, bundle skills.Bundle, credentialOverrides map[string]string) {
	switch verdict {
	case skills.VerdictReject:
		skill.Status = models.SkillStatusRejected
		if err := h.Store.CreateSkill(r.Context(), skill); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
			"status":    skill.Status,
			"reasoning": reasoning,
		})
		return

	case skills.VerdictNeedsReview:
		skill.Status = models.SkillStatusNeedsReview
		if err := h.Store.CreateSkill(r.Context(), skill); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, http.StatusAccepted, map[string]interface{}{
			"status":    skill.Status,
			"reasoning": reasoning,
			"note":      "inconclusive automated review — an admin must approve or reject this skill before it can be used",
		})
		return

	case skills.VerdictAccept:
		registered, err := h.registerSkillTools(r.Context(), kitchen, skill.Name, skill.Manifest, credentialOverrides)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "accepted skill, but failed to register its tools: "+err.Error())
			return
		}
		skill.RegisteredTools = registered
		skill.Status = models.SkillStatusAccepted
		if err := h.Store.CreateSkill(r.Context(), skill); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, skill)
		return

	default:
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("unexpected verdict %q", verdict))
	}
}

// registerSkillTools creates one MCPTool row per bundled MCP server a
// skill's manifest declares, namespaced "<skill>.<server>" to avoid
// colliding with the kitchen's own tools, and returns their names. Dispatch
// for these needs no new code at all: they go through the same gateway path
// (internal/mcpgw) any other registered tool does.
func (h *Handlers) registerSkillTools(ctx context.Context, kitchen, skillName string, manifest *models.SkillManifest, credentialOverrides map[string]string) ([]string, error) {
	registered := make([]string, 0, len(manifest.MCPServers))
	for _, srv := range manifest.MCPServers {
		toolName := skillName + "." + srv.Name

		authConfig, err := h.resolveSkillServerAuth(ctx, kitchen, srv, credentialOverrides)
		if err != nil {
			return registered, fmt.Errorf("resolving credentials for %q: %w", srv.Name, err)
		}

		tool := &models.MCPTool{
			ID:           uuid.New().String(),
			Name:         toolName,
			Description:  srv.Description,
			Kitchen:      kitchen,
			Endpoint:     srv.Endpoint,
			Transport:    srv.Transport,
			AuthConfig:   authConfig,
			Capabilities: []string{"tool"},
			Enabled:      true,
			CreatedAt:    time.Now().UTC(),
			UpdatedAt:    time.Now().UTC(),
		}
		if err := h.Store.CreateTool(ctx, tool); err != nil {
			return registered, fmt.Errorf("registering tool %q: %w", toolName, err)
		}
		registered = append(registered, toolName)
	}
	return registered, nil
}

// resolveSkillServerAuth builds an MCPTool.AuthConfig for a skill's bundled
// MCP server from a named KitchenCredential — never from a raw secret in the
// manifest itself, matching how every other credential-bearing ingredient in
// this codebase works. A request's credentialOverrides (keyed by server
// name) take precedence over the manifest's own CredentialRef, since the
// kitchen admin registering the skill decides which credential it gets, not
// the skill's author.
func (h *Handlers) resolveSkillServerAuth(ctx context.Context, kitchen string, srv models.SkillMCPServer, credentialOverrides map[string]string) (map[string]interface{}, error) {
	if srv.AuthType == "" {
		return nil, nil
	}
	ref := srv.CredentialRef
	if override, ok := credentialOverrides[srv.Name]; ok && override != "" {
		ref = override
	}
	if ref == "" {
		return nil, fmt.Errorf("declares auth_type %q but no credential_ref was given", srv.AuthType)
	}
	cred, err := h.Store.GetKitchenCredential(ctx, kitchen, ref)
	if err != nil {
		return nil, fmt.Errorf("credential %q not found in kitchen %q", ref, kitchen)
	}

	switch srv.AuthType {
	case "bearer":
		return map[string]interface{}{"type": "bearer", "token": cred.Value}, nil
	case "api-key":
		header := srv.AuthHeader
		if header == "" {
			header = "X-API-Key"
		}
		return map[string]interface{}{"type": "api-key", "header": header, "key": cred.Value}, nil
	default:
		return nil, fmt.Errorf("unsupported auth_type %q", srv.AuthType)
	}
}

// ListSkills returns every skill registered in this kitchen.
// GET /api/v1/skills
func (h *Handlers) ListSkills(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	list, err := h.Store.ListSkills(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"skills": list})
}

// GetSkill returns one skill.
// GET /api/v1/skills/{name}
func (h *Handlers) GetSkill(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	name := chi.URLParam(r, "name")
	skill, err := h.Store.GetSkill(r.Context(), kitchen, name)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, skill)
}

// DeleteSkill removes a skill and every MCPTool row it registered — an agent
// still carrying this skill as an ingredient will simply fail to resolve it
// on its next bake, the same way deleting any other ingredient's backing
// resource behaves.
// DELETE /api/v1/skills/{name}
func (h *Handlers) DeleteSkill(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	name := chi.URLParam(r, "name")

	skill, err := h.Store.GetSkill(r.Context(), kitchen, name)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	for _, toolName := range skill.RegisteredTools {
		if err := h.Store.DeleteTool(r.Context(), kitchen, toolName); err != nil {
			log.Warn().Err(err).Str("skill", name).Str("tool", toolName).Msg("Failed to delete skill's registered tool")
		}
	}
	if err := h.Store.DeleteSkill(r.Context(), kitchen, name); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RefreshSkill re-fetches a skill from its original source (git or path
// only — inline and upload sources have nothing live to refresh from) and
// re-runs the full verify-then-register pipeline, replacing its previous
// tool registrations.
// PATCH /api/v1/skills/{name}/refresh
func (h *Handlers) RefreshSkill(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	name := chi.URLParam(r, "name")

	existing, err := h.Store.GetSkill(r.Context(), kitchen, name)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	var bundle skills.Bundle
	switch existing.Source {
	case models.SkillSourceGit:
		url, ref := existing.SourceRef, ""
		if i := strings.LastIndex(existing.SourceRef, "@"); i != -1 {
			url, ref = existing.SourceRef[:i], existing.SourceRef[i+1:]
		}
		bundle, err = skills.CloneGit(r.Context(), url, ref)
	case models.SkillSourcePath:
		bundle, err = skills.LoadDir(existing.SourceRef)
	default:
		respondError(w, http.StatusBadRequest, fmt.Sprintf("skill source %q has nothing to refresh from", existing.Source))
		return
	}
	if err != nil {
		respondError(w, http.StatusBadGateway, "refresh failed: "+err.Error())
		return
	}

	manifest, err := bundle.Manifest()
	if err != nil {
		respondError(w, http.StatusBadGateway, "refreshed bundle has an invalid SKILL.md: "+err.Error())
		return
	}

	for _, toolName := range existing.RegisteredTools {
		if err := h.Store.DeleteTool(r.Context(), kitchen, toolName); err != nil {
			log.Warn().Err(err).Str("skill", name).Str("tool", toolName).Msg("Failed to delete stale skill tool before refresh")
		}
	}

	verdict, reasoning, providerUsed, err := skills.VerifyIntent(r.Context(), h.Router, kitchen, manifest, bundle)
	if err != nil {
		respondError(w, http.StatusBadGateway, "skill verification failed: "+err.Error())
		return
	}

	now := time.Now().UTC()
	existing.Manifest = manifest
	existing.RegisteredTools = nil
	existing.VerificationVerdict = string(verdict)
	existing.VerificationReasoning = reasoning
	existing.VerifiedAt = &now
	existing.VerifiedByProvider = providerUsed
	existing.UpdatedAt = now

	h.finalizeSkillRegistration(w, r, kitchen, existing, verdict, reasoning, bundle, nil)
}

// skillReviewRequest is the body for approve/reject — an optional note from
// the human reviewer, recorded distinctly from the automated verdict.
type skillReviewRequest struct {
	Note string `json:"note,omitempty"`
}

// ApproveSkill lets a human activate a skill the automated check left at
// needs_review. Only valid from that status — an already-accepted or
// already-rejected skill is not something this endpoint flips.
// POST /api/v1/skills/{name}/approve
func (h *Handlers) ApproveSkill(w http.ResponseWriter, r *http.Request) {
	h.reviewSkill(w, r, true)
}

// RejectSkill is ApproveSkill's counterpart.
// POST /api/v1/skills/{name}/reject
func (h *Handlers) RejectSkill(w http.ResponseWriter, r *http.Request) {
	h.reviewSkill(w, r, false)
}

func (h *Handlers) reviewSkill(w http.ResponseWriter, r *http.Request, approve bool) {
	kitchen := middleware.GetKitchen(r.Context())
	name := chi.URLParam(r, "name")

	skill, err := h.Store.GetSkill(r.Context(), kitchen, name)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if skill.Status != models.SkillStatusNeedsReview {
		respondError(w, http.StatusConflict, fmt.Sprintf("skill %q is %q, not needs_review", name, skill.Status))
		return
	}

	var req skillReviewRequest
	if r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	note := req.Note
	if note == "" {
		note = "no note given"
	}
	reviewer := "unknown"
	if id := pkgmw.GetIdentity(r.Context()); id != nil {
		reviewer = id.Subject
	}
	skill.VerificationReasoning += fmt.Sprintf("\n\n[human review by %s] %s", reviewer, note)
	skill.UpdatedAt = time.Now().UTC()

	if !approve {
		skill.Status = models.SkillStatusRejected
		if err := h.Store.UpdateSkill(r.Context(), skill); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, skill)
		return
	}

	if skill.Manifest == nil {
		respondError(w, http.StatusInternalServerError, "skill has no manifest on record")
		return
	}
	registered, err := h.registerSkillTools(r.Context(), kitchen, skill.Name, skill.Manifest, nil)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "approved, but failed to register tools: "+err.Error())
		return
	}
	skill.RegisteredTools = registered
	skill.Status = models.SkillStatusAccepted
	if err := h.Store.UpdateSkill(r.Context(), skill); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, skill)
}

// ── Chunked upload ───────────────────────────────────────────
//
// Mirrors OpenClaw's skills.upload.begin/chunk/commit shape: a client stages
// a ZIP bundle across as many requests as it likes, then passes the
// resulting upload_id to RegisterSkill with source=upload. Nothing is
// registered or verified until that final call.

type skillUploadBeginRequest struct {
	Size int64 `json:"size"`
}

// BeginSkillUpload stages a new chunked upload.
// POST /api/v1/skills/upload/begin
func (h *Handlers) BeginSkillUpload(w http.ResponseWriter, r *http.Request) {
	var req skillUploadBeginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	id, err := h.SkillUploads.Begin(req.Size)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"upload_id": id})
}

// ChunkSkillUpload appends raw bytes to a staged upload, identified by the
// "upload_id" query parameter (the body itself is the chunk's raw bytes, not
// JSON — this is a byte stream, not a structured request).
// POST /api/v1/skills/upload/chunk?upload_id=...
func (h *Handlers) ChunkSkillUpload(w http.ResponseWriter, r *http.Request) {
	uploadID := r.URL.Query().Get("upload_id")
	if uploadID == "" {
		respondError(w, http.StatusBadRequest, "upload_id query parameter is required")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, skills.MaxSkillBundleBytes+1))
	if err != nil {
		respondError(w, http.StatusBadRequest, "failed to read chunk body: "+err.Error())
		return
	}
	if err := h.SkillUploads.WriteChunk(uploadID, data); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"received": len(data)})
}

// CommitSkillUpload finalizes a staged upload. The response's upload_id is
// what a subsequent RegisterSkill call (source=upload) needs; nothing is
// parsed or verified yet at this point, only that the declared byte count
// arrived.
// POST /api/v1/skills/upload/commit?upload_id=...
func (h *Handlers) CommitSkillUpload(w http.ResponseWriter, r *http.Request) {
	uploadID := r.URL.Query().Get("upload_id")
	if uploadID == "" {
		respondError(w, http.StatusBadRequest, "upload_id query parameter is required")
		return
	}
	if _, err := h.SkillUploads.Commit(uploadID); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Commit's returned path intentionally isn't opened/validated here — that
	// happens once, in resolveSkillBundle, when RegisterSkill actually uses
	// it. Re-staging it as "committed" lets Commit be called more than once
	// idempotently is NOT guaranteed, since Commit only checks the byte count,
	// not re-enterability; a client should commit exactly once per upload.
	respondJSON(w, http.StatusOK, map[string]interface{}{"upload_id": uploadID, "status": "committed"})
}
