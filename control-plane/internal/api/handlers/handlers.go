// Package handlers implements the HTTP handlers for the AgentOven control plane.
// Phase 2: All handlers use the Store interface (PostgreSQL-backed) instead
// of in-memory maps. New handlers added for Model Router, MCP Gateway, and
// Workflow Engine.
package handlers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"crypto/rand"
	"crypto/sha256"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/catalog"
	"github.com/agentoven/agentoven/control-plane/internal/ctxwindow"
	"github.com/agentoven/agentoven/control-plane/internal/executor"
	grails "github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	"github.com/agentoven/agentoven/control-plane/internal/notify"
	"github.com/agentoven/agentoven/control-plane/internal/process"
	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/secretref"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	inttelemetry "github.com/agentoven/agentoven/control-plane/internal/telemetry"
	"github.com/agentoven/agentoven/control-plane/internal/workflow"
	"github.com/agentoven/agentoven/control-plane/pkg/audio"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/refcheck"
	"github.com/agentoven/agentoven/control-plane/pkg/skills"
	"github.com/agentoven/agentoven/control-plane/pkg/toolauth"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

var recipeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,62}$`)

// Handlers holds all handler dependencies.
type Handlers struct {
	// traceContentGuard says whether the caller may read trace and span bodies (nil: everyone who reads a trace).
	traceContentGuard func(*http.Request) bool

	// bakes serialises the background step of bakes and re-cooks per agent (agent_bake_tail.go).
	bakes bakeSerializer

	// kitchenOwnerCheck says whether the caller administers the named kitchen (nil: the role alone
	// decides, via contracts.IsKitchenOwner). Pro installs a membership-aware check (ADR-0040).
	kitchenOwnerCheck func(r *http.Request, kitchen string) bool

	Store      store.Store
	Router     *router.ModelRouter
	MCPGateway *mcpgw.Gateway
	Workflow   *workflow.Engine
	Resolver   *resolver.Resolver
	Executor   *executor.Executor
	// CredentialOwners decides who owns a new scoped key (nil in the community edition).
	CredentialOwners contracts.CredentialOwnerPolicy
	// DecisionSigner, when set (Pro), requires every human decision to carry a signature
	// (Pro ADR-0041). The community edition has none and decides as before.
	DecisionSigner contracts.DecisionSigner
	describers     map[string]contracts.DecisionDescriber
	// Tasks runs agents in the background (nil when the execution journal is off).
	Tasks *executor.TaskManager
	// SecretResolver resolves secret references and, when a vault is installed (Pro), is where
	// provider keys are kept instead of in the provider record. Nil-safe: no vault means none.
	SecretResolver *secretref.Resolver
	// KeyRotation, when set, is told after a provider's key has changed, so a hosted process that
	// started with the old one can be restarted (Pro installs one). Nil leaves such a process on the
	// key it started with until it is next started, which is the community behaviour.
	KeyRotation ProviderKeyObserver
	// AuthorizeBudgetExtension, when set, decides whether the caller may raise a run's budget
	// (a resume that carries an extension). Raising a cap is spending someone else's money, so
	// an edition with roles can keep it to the roles that own that decision. An error refuses
	// the resume with a 403. Nil allows any caller who may resume the run.
	AuthorizeBudgetExtension func(r *http.Request) error
	// Traffic, when set, is asked before a route that starts agent work lets the request in (see
	// LimitTraffic). Nil (the community edition) limits nothing.
	Traffic contracts.TrafficLimiter
	// RunScopes accounts for the model calls hosted processes make while handling a call, so a
	// budget can cap them. Nil (the community edition) leaves their spend reported but uncapped.
	RunScopes      contracts.RunScopes
	ProcessManager *process.Manager
	// Bots is the sole manager and tracker of agentic bots (Pro). Nil in the community edition,
	// where such an agent cannot be baked and so is never invoked.
	Bots            contracts.BotControl
	PromptValidator contracts.PromptValidatorService
	Catalog         *catalog.Catalog
	Sessions        contracts.SessionStore
	Guardrails      contracts.GuardrailService
	// References lists what only the edition can say a kitchen has (scenarios, roles) for the checks that refuse
	// an agent or recipe naming something absent (pkg/refcheck). Nil in the community edition: those
	// references are then not checked.
	References contracts.KitchenReferences
	// GuardrailPolicy supplies workspace-level guardrails (Pro). nil: only agents' own apply.
	GuardrailPolicy contracts.WorkspaceGuardrailSource
	SkillUploads    *skills.UploadStore
	// A2ASecret is the key the control plane's tokens for agent pods are derived from (a2aauth).
	A2ASecret []byte

	// Notifier sends notifications and holds the connections listening to websocket channels.
	Notifier *notify.Service
	// SkillRouteGuard, if set, wraps every /api/v1/skills route. An edition that has roles uses it
	// to decide who may register, approve or delete a skill; with none, any authenticated caller may.
	SkillRouteGuard func(next http.Handler) http.Handler
	// RouteGuard, if set, wraps every route, after authentication and before the router picks a
	// handler. An edition that has roles uses it to decide, from the method and route, which role may
	// call it (Pro: internal/routeguard); with none, any caller the auth layer lets in may. It is read
	// per request, so it can be set after the router is built.
	RouteGuard func(next http.Handler) http.Handler
	// ProviderRepoint, if set, decides whether the caller may point a provider that holds a credential at a
	// different host without entering a new credential. An edition with roles installs a permission
	// check (Pro: provider:manage); with none, any caller who may update a provider may repoint it.
	ProviderRepoint func(r *http.Request) bool
	// FetchSkillPlugin checks out a plugin; nil means skills.FetchPlugin.
	FetchSkillPlugin func(ctx context.Context, spec skills.ImportSpec) (*skills.Plugin, string, func(), error)

	// RecipeExecutor is the pluggable execution backend for POST /{name}/bake.
	// OSS: nil — BakeRecipe falls through to h.Workflow.ExecuteRecipe directly.
	// Pro: ProRecipeRunner — adds role-based env gating before delegating to the engine.
	RecipeExecutor RecipeExecutorService

	// WorkloadRestarter is an optional Pro-only hook called after RecookAgent
	// completes. It bumps the updated_at timestamp on the k8s_workload record for the
	// agent, causing the operator's next reconcile to emit a changed pod-template
	// annotation and trigger a Kubernetes rolling restart.
	// OSS: nil (no-op). Pro injects a concrete implementation at startup.
	WorkloadRestarter WorkloadRestarter

	// BakeDeployments is an optional Pro hook told when a bake has made a version ready in the environment
	// it targeted, so that the version shows as that environment's deployment (owner decision A7). OSS has
	// no deployment records and leaves it nil.
	BakeDeployments BakeDeploymentRecorder

	// WorkloadScaler is an optional Pro-only hook called by CoolAgent (scale to 0)
	// and RewarmAgent (scale back to desired replicas). The operator reconciles the
	// replica change on the next tick, draining or recreating the pod.
	// OSS: nil (no k8s deployment). Pro injects a concrete implementation at startup.
	WorkloadScaler WorkloadScaler

	// K8sAgentImage is the current configured agent container image
	// (AGENTOVEN_K8S_AGENT_IMAGE). When set, recook will update the stored workload
	// image so the operator deploys the latest image rather than the frozen DB value.
	K8sAgentImage string

	// AgentEnvStore is an optional Pro-only store interface for environment-scoped
	// agent deployments. OSS leaves this nil; Pro injects the PostgresStore which
	// satisfies AgentEnvironmentStore. The A2AAgentEnvEndpoint handler uses it to
	// look up AgentEnvironment records and apply per-env guardrail policies.
	AgentEnvStore AgentEnvironmentStore

	// ServerInfo is the response for GET /api/v1/info.
	// OSS sets CommunityServerInfo; Pro overrides with license data.
	ServerInfo *models.ServerInfo
}

// AgentEnvironmentStore is the subset of Pro store methods needed by the OSS
// env-scoped A2A handler. Pro injects a concrete implementation at startup;
// OSS leaves the field nil and falls back to the legacy EnvEndpoints map.
type AgentEnvironmentStore interface {
	GetAgentEnvironment(ctx context.Context, kitchenID, agentName, envSlug string) (*models.AgentEnvironment, error)
	UpdateAgentEnvStatus(ctx context.Context, id string, status models.AgentEnvStatus, errMsg string) error
}

// RecipeExecutorService is the interface for executing recipe runs.
// The OSS Workflow Engine satisfies this interface directly.
// Pro wraps it with role-based environment gating.
type RecipeExecutorService interface {
	ExecuteRecipe(ctx context.Context, recipe *models.Recipe, kitchen string, input map[string]interface{}, envSlug string) (string, error)
}

// BakeDeploymentRecorder records that a bake put a version of an agent into an environment. envSlug is the
// environment the bake targeted ("" when the kitchen has none) and by the subject that asked for the bake.
// A failure is logged by the caller and never fails the bake: the agent is ready either way.
type BakeDeploymentRecorder interface {
	RecordBakeDeployment(ctx context.Context, kitchen, agentName, version, envSlug, by string) error
}

// WorkloadRestarter is an optional Pro-only hook for triggering a Kubernetes
// rolling restart of the agent pod after a recook. Pro injects a concrete
// implementation; OSS leaves the field nil.
// newImage: if non-empty, the workload's image column is also updated so the
// operator reconciles with the current configured image instead of the frozen DB value.
type WorkloadRestarter interface {
	TouchWorkloadForAgent(ctx context.Context, kitchenID, agentName, newImage string) error
}

// WorkloadScaler is an optional Pro-only hook for scaling an agent's k8s
// deployment to zero (cool) or back to its desired replica count (rewarm).
// OSS leaves the field nil (no k8s deployment to scale).
// Pro injects a concrete implementation backed by the PostgresStore.
type WorkloadScaler interface {
	// ScaleWorkloadForAgent sets replicas to 0 (cool) or the desired count
	// (rewarm) on the k8s_workloads record for the named agent.
	// The operator will reconcile the change on the next tick.
	ScaleWorkloadForAgent(ctx context.Context, kitchenID, agentName string, replicas int32) error
}

// New creates a new Handlers instance with all dependencies.
func New(s store.Store, mr *router.ModelRouter, gw *mcpgw.Gateway, wf *workflow.Engine, cat *catalog.Catalog, sess contracts.SessionStore, pm *process.Manager) *Handlers {
	res := resolver.NewResolver(s)
	exec := executor.NewExecutor(s, mr, gw, sess)

	// Journaling is on by default — it is pure additive, append-only local
	// files (see executor.FileJournal), not the store this repo's "OSS is
	// in-memory only" rule is about — and it is what makes an interrupted
	// agentic loop resumable instead of lost. AGENTOVEN_JOURNAL_DIR overrides
	// the default ~/.agentoven/journal path; set AGENTOVEN_JOURNAL_DISABLE=true
	// to turn it off entirely.
	//
	if os.Getenv("AGENTOVEN_JOURNAL_DISABLE") != "true" {
		if j, err := executor.NewFileJournal(os.Getenv("AGENTOVEN_JOURNAL_DIR")); err != nil {
			log.Warn().Err(err).Msg("Failed to initialize execution journal, runs will not be resumable")
		} else {
			exec.SetJournal(j)
			// Finished runs' journals are removed after a week and runs nothing has
			// touched for two weeks are ended as abandoned (a run waiting for a person is
			// never stale). AGENTOVEN_JOURNAL_RETENTION_DAYS / _STALE_DAYS change this; 0
			// turns that half off.
			j.StartJanitor(context.Background(), time.Hour,
				envDays("AGENTOVEN_JOURNAL_RETENTION_DAYS", 7), envDays("AGENTOVEN_JOURNAL_STALE_DAYS", 14))
		}
	}

	var tasks *executor.TaskManager
	if exec.Journal() != nil {
		tasks = executor.NewTaskManager(exec, executor.TaskManagerOptions{
			PerKitchen: envInt("AGENTOVEN_TASKS_PER_KITCHEN", 4),
			Global:     envInt("AGENTOVEN_TASKS_GLOBAL", 16),
			QueueMax:   envInt("AGENTOVEN_TASKS_QUEUE", 64),
		})
	}

	h := &Handlers{
		Store:           s,
		Router:          mr,
		MCPGateway:      gw,
		Workflow:        wf,
		Resolver:        res,
		Executor:        exec,
		Tasks:           tasks,
		ProcessManager:  pm,
		PromptValidator: &contracts.CommunityPromptValidator{},
		Catalog:         cat,
		Sessions:        sess,
		SkillUploads:    skills.NewUploadStore(),
	}
	exec.SetAudit(h.auditRun)
	h.InstallDelegationBoundary()
	h.startTaskRecovery()
	h.RegisterOwnDecisionGates()
	return h
}

// ══════════════════════════════════════════════════════════════
// ── Agent Handlers ───────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) ListAgents(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	// Query param filtering (R9): ?tag=key:value, ?environment=slug, ?status=ready
	tagFilter := r.URL.Query().Get("tag")
	envFilter := r.URL.Query().Get("environment")
	statusFilter := r.URL.Query().Get("status")

	var agents []models.Agent
	var err error

	switch {
	case tagFilter != "":
		// Parse "key:value" format
		parts := strings.SplitN(tagFilter, ":", 2)
		if len(parts) != 2 || parts[0] == "" {
			respondError(w, http.StatusBadRequest, "tag filter must be in key:value format")
			return
		}
		agents, err = h.Store.ListAgentsByTag(r.Context(), kitchen, parts[0], parts[1])
	case envFilter != "":
		agents, err = h.Store.ListAgentsByEnvironment(r.Context(), kitchen, envFilter)
	default:
		agents, err = h.Store.ListAgents(r.Context(), kitchen)
	}

	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if agents == nil {
		agents = []models.Agent{}
	}

	// Client-side status filter (applies on top of tag/env filter). A retired agent (DELETE keeps the row
	// for its history) is listed only when asked for: by status=retired or include_retired=true.
	if statusFilter != "" || r.URL.Query().Get("include_retired") != "true" {
		filtered := make([]models.Agent, 0, len(agents))
		for _, a := range agents {
			switch {
			case statusFilter != "" && string(a.Status) != statusFilter:
			case statusFilter == "" && a.Status == models.AgentStatusRetired:
			default:
				filtered = append(filtered, a)
			}
		}
		agents = filtered
	}

	respondJSON(w, http.StatusOK, models.RedactAgents(agents))
}

func (h *Handlers) RegisterAgent(w http.ResponseWriter, r *http.Request) {
	var req models.Agent
	body, ok := readConfigBody(w, r)
	if !ok {
		return
	}
	bodyProblems, ok := decodeConfig(w, body, &req, refcheck.ServerFields)
	if !ok {
		return
	}

	// Validate required fields
	if strings.TrimSpace(req.Name) == "" {
		respondError(w, http.StatusBadRequest, "Agent 'name' is required")
		return
	}

	kitchen := middleware.GetKitchen(r.Context())
	req.ID = uuid.New().String()
	req.Status = models.AgentStatusDraft
	req.Kitchen = kitchen
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()
	req.A2AEndpoint = "/agents/" + req.Name + "/a2a"

	// Default mode to managed if not specified
	if req.Mode == "" {
		req.Mode = models.AgentModeManaged
	}
	if req.MaxTurns == 0 { // a negative number is not "unset": the checks below name it
		req.MaxTurns = 10
	}
	if status, err := h.checkBotAgent(&req); err != nil {
		respondError(w, status, err.Error())
		return
	}
	if err := h.checkAgentGuardrails(r.Context(), kitchen, req.Guardrails); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.checkAgentWrite(w, r, kitchen, nil, &req, bodyProblems) {
		return
	}

	sealed, err := h.sealAgentSecrets(r.Context(), &req)
	if err != nil {
		h.dropCredentials(r.Context(), kitchen, sealed, nil)
		respondError(w, http.StatusInternalServerError, "the secrets in this agent's configuration could not be stored in the vault")
		return
	}
	if err := h.Store.CreateAgent(r.Context(), &req); err != nil {
		h.dropCredentials(r.Context(), kitchen, sealed, nil)
		respondCreateError(w, err, "register the agent")
		return
	}

	log.Info().Str("agent", req.Name).Str("id", req.ID).Str("kitchen", kitchen).Msg("Agent registered")
	respondJSON(w, http.StatusCreated, req.Redacted())
}

func (h *Handlers) GetAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, agent.Redacted())
}

// UpdateAgent edits an agent. It runs on a fresh read of the agent each time a concurrent write beats
// it to the row (see retryOnStale), and refuses with 409 an edit that names a revision which is no
// longer the stored one.
func (h *Handlers) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	retryOnStale(w, r, h.updateAgent)
}

func (h *Handlers) updateAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// A retired agent is edited by registering it again (POST /agents brings it back under the same id),
	// never by a PUT that would change it while it stays retired.
	if refuseRetired(w, agent) {
		return
	}

	// The body is an edit (see models.AgentEdit) and the tags, which are labels and not behaviour.
	var req struct {
		models.AgentEdit
		Tags     map[string]string `json:"tags"`
		Revision int64             `json:"revision"` // the revision the client's edit is based on, optional
	}
	body, ok := readConfigBody(w, r)
	if !ok {
		return
	}
	// An agent sent back whole carries fields that are the agent's and not the edit's; those are ignored, a name
	// that is neither is refused.
	bodyProblems, ok := decodeConfig(w, body, &req, models.Agent{}, refcheck.ServerFields)
	if !ok {
		return
	}
	if want := ifMatchRevision(r, req.Revision); want > 0 && want != agent.Revision {
		respondStaleRevision(w, agent)
		return
	}
	edit := req.AgentEdit
	// JSON null for an optional field clears it (owner decision A4); for a field the agent cannot run without
	// it is refused, not ignored, so nobody believes it was cleared.
	var refused []string
	if edit.Cleared, refused = models.NullFields(body); len(refused) > 0 {
		respondError(w, http.StatusBadRequest, strings.Join(refused, ", ")+" cannot be cleared: the agent cannot run without it. Send a new value instead")
		return
	}

	// Judge the agent as it would be with the edit before keeping any of it. An execution mode is
	// only accepted for a bot, which must be able to move into a confined mode.
	probe := agent.WithDraft()
	edit.Apply(probe)
	if !probe.IsAgenticBot() {
		edit.ExecutionMode = ""
	}
	if status, err := h.checkBotAgent(probe); err != nil {
		respondError(w, status, err.Error())
		return
	}
	if err := h.checkAgentGuardrails(r.Context(), kitchen, probe.Guardrails); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The draft is checked when it is saved, not only when it is baked: what the next bake would run must
	// name things the kitchen has. What the agent already got wrong is not held against an edit that
	// leaves it so.
	if !h.checkAgentWrite(w, r, kitchen, agent.WithDraft(), probe, bodyProblems) {
		return
	}
	// An agent that has been baked runs what was baked: the edit waits in its draft for the next bake
	// (ADR-0043), so the version a trace names is the configuration that served the call. One that has
	// never been baked runs nothing yet and takes the edit at once.
	heldBefore := agentSecretRefs(agent)
	agent.Edit(edit)
	if len(req.Tags) > 0 {
		agent.Tags = mergeTags(agent.Tags, req.Tags) // a copy: the map the store handed out is shared with its own
	}
	agent.UpdatedAt = time.Now().UTC()
	sealed, sealErr := h.sealAgentSecrets(r.Context(), agent)
	if sealErr != nil {
		h.dropCredentials(r.Context(), kitchen, sealed, heldBefore)
		respondError(w, http.StatusInternalServerError, "the secrets in this agent's configuration could not be stored in the vault")
		return
	}
	// An edit is a draft: it does not change the version. The version moves when the agent is baked
	// with a configuration that differs from the one baked before (models.VersionBumpBake).

	if err := h.Store.UpdateAgent(r.Context(), agent); err != nil {
		h.dropCredentials(r.Context(), kitchen, sealed, heldBefore)
		respondStoreError(w, err)
		return
	}
	h.reapAgentSecrets(r.Context(), agent, heldBefore)
	respondJSON(w, http.StatusOK, agent.Redacted())
}

func (h *Handlers) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	// Stop the agent process before deletion
	if h.ProcessManager != nil {
		if err := h.ProcessManager.Stop(r.Context(), kitchen, agentName); err != nil {
			log.Warn().Err(err).Str("agent", agentName).Msg("Failed to stop agent process during delete")
		}
	}

	if err := h.Store.DeleteAgent(r.Context(), kitchen, agentName); err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	log.Info().Str("agent", agentName).Str("kitchen", kitchen).Msg("Agent retired")
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "agent": agentName})
}

// BakeAgent bakes an agent. A PUT that lands while the bake is being prepared makes the bake's write
// stale; the bake is then prepared again on the agent as it is now, so its configuration includes the
// edit (or the edit waits in the draft for the next bake) and neither is lost.
func (h *Handlers) BakeAgent(w http.ResponseWriter, r *http.Request) {
	retryOnStale(w, r, h.bakeAgent)
}

func (h *Handlers) bakeAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	cat, ok := h.refCatalog(w, r.Context(), kitchen)
	if !ok {
		return
	}
	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if refuseRetired(w, agent) {
		return
	}

	var req struct {
		Version     string `json:"version"`
		Environment string `json:"environment"`
	}
	// ISS-023 fix: check decode error instead of silently discarding it.
	// Allow io.EOF since the request body is optional for BakeAgent.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.Version != "" && !models.ValidVersion(req.Version) {
		respondError(w, http.StatusBadRequest, "version must be MAJOR.MINOR.PATCH, digits only (for example 1.2.0)")
		return
	}
	// A bake takes up the edits made since the last one: what runs is what was baked (ADR-0043).
	agent.ApplyDraft()

	// Everything the agent names must exist in this kitchen, said once and in full (pkg/refcheck); the
	// resolver below stays as the second line for what only resolving can tell.
	if !h.checkAgentBake(w, cat, agent) {
		return
	}

	// ── Validate & resolve ALL ingredients via the Resolver ──────
	// This replaces the old manual checks with full ingredient resolution:
	// model, tools, prompts, data, embeddings, vectorstores, retrievers.

	// If agent has a top-level ModelProvider but no model ingredient, synthesize one
	if agent.ModelProvider != "" {
		hasModelIngredient := false
		for _, ing := range agent.Ingredients {
			if ing.Kind == models.IngredientModel {
				hasModelIngredient = true
				break
			}
		}
		if !hasModelIngredient {
			agent.Ingredients = append(agent.Ingredients, models.Ingredient{
				ID:       "auto-model",
				Name:     agent.ModelProvider,
				Kind:     models.IngredientModel,
				Required: true,
				Config: map[string]interface{}{
					"provider": agent.ModelProvider,
					"model":    agent.ModelName,
				},
			})
		}
	}

	resolved, err := h.Resolver.Resolve(r.Context(), agent)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "Agent cannot be baked — ingredient resolution failed",
			"details": err.Error(),
			"partial": resolved,
		})
		return
	}

	// Cache resolved config on the agent
	agent.ResolvedConfig = resolved
	agent.Status = models.AgentStatusBaking
	agent.UpdatedAt = time.Now().UTC()
	if req.Version != "" {
		// The caller named a version. The store accepts it only as a new version above the latest,
		// or the current one when the configuration is the one stored (models.DecideNamedVersion);
		// it never runs a changed configuration under a label that already exists.
		agent.Version = req.Version
		agent.VersionBump = models.VersionBumpNamed
	} else {
		// The store decides: the first bake keeps the registered version (0.1.0), a bake whose
		// configuration differs from the last one takes the next minor version and keeps the old
		// one, and a bake that changes nothing keeps its version.
		agent.VersionBump = models.VersionBumpBake
	}

	// ── Resolve target environment ──────────────────────────────────────────
	// If the kitchen has environments configured, the agent's A2A endpoint is
	// the env-scoped path so clients can call /env/{slug}/agents/{name}/a2a.
	// If no environments exist, fall back to the flat /agents/{name}/a2a path.
	targetEnvSlug := req.Environment
	envs, _ := h.Store.ListEnvironments(r.Context(), kitchen)
	if len(envs) > 0 {
		if targetEnvSlug == "" {
			// Default to lowest-order environment (first in ORDER BY env_order ASC)
			targetEnvSlug = envs[0].Slug
		} else {
			// Validate the requested env slug exists
			validEnv := false
			for _, e := range envs {
				if e.Slug == targetEnvSlug {
					validEnv = true
					break
				}
			}
			if !validEnv {
				respondError(w, http.StatusBadRequest,
					fmt.Sprintf("Environment '%s' does not exist in this kitchen. Available: %s",
						targetEnvSlug, joinEnvSlugs(envs)))
				return
			}
		}
		agent.A2AEndpoint = "/env/" + targetEnvSlug + "/agents/" + agentName + "/a2a"
	} else {
		// No environments configured — use flat endpoint (backward compat)
		agent.A2AEndpoint = "/agents/" + agentName + "/a2a"
		targetEnvSlug = ""
	}

	if err := h.Store.UpdateAgent(r.Context(), agent); err != nil {
		respondStoreError(w, err)
		return
	}

	// Re-fetch agent from store to get the version the store computed
	agent, _ = h.Store.GetAgent(r.Context(), kitchen, agentName)

	log.Info().
		Str("agent", agentName).
		Str("version", agent.Version).
		Str("environment", req.Environment).
		Msg("Agent baking started")

	// ISS-004 fix: Validate provider connectivity async, then mark ready or burnt.
	// Re-fetch the agent from the store inside the goroutine to avoid racing
	// on the same pointer that was used in the HTTP response. Capture only the
	// immutable identifiers (kitchen, agentName) — not the *Agent pointer.
	// Resolve the kitchen's K8sNamespace override (if configured) to inject into
	// the agent's process environment so the K8sExecutor uses the right namespace.
	var kitchenK8sNS string
	if kitchens, kErr := h.Store.ListKitchens(r.Context()); kErr == nil {
		for _, k := range kitchens {
			if k.Name == kitchen || k.ID == kitchen {
				kitchenK8sNS = k.K8sNamespace
				break
			}
		}
	}

	// The background step records its outcome through UpdateAgentLifecycle only (never the whole
	// agent), one at a time per agent and only for the newest bake (agent_bake_tail.go).
	agentKey, version := kitchen+"/"+agentName, agent.Version
	by := ""
	if id := pkgmw.GetIdentity(r.Context()); id != nil {
		by = id.Subject
	}
	gen := h.bakes.begin(agentKey)
	go h.bakes.run(agentKey, gen, func(superseded func() bool) {
		h.finishBake(kitchen, agentName, version, kitchenK8sNS, false, superseded, h.bakeDeployment(kitchen, agentName, version, targetEnvSlug, by))
	})

	respondJSON(w, http.StatusAccepted, map[string]string{
		"name":        agentName,
		"version":     agent.Version,
		"environment": targetEnvSlug,
		"status":      string(models.AgentStatusBaking),
		"agent_card":  agent.A2AEndpoint + "/.well-known/agent-card.json",
	})
}

// TestAgent sends a single test message through the agent's configured provider.
func (h *Handlers) TestAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	// ── Scoped key agent access check ───────────────────────
	if identity := pkgmw.GetIdentity(r.Context()); identity != nil && identity.Provider == "scoped-key" {
		keyID := identity.Claims["key_id"]
		scopedKey, err := h.Store.GetScopedKey(r.Context(), kitchen, keyID)
		if err != nil || !scopedKey.CanAccessAgent(agentName) {
			respondError(w, http.StatusForbidden,
				fmt.Sprintf("Scoped key does not have access to agent '%s'", agentName))
			return
		}
	}

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if agent.ModelProvider == "" && !agent.IsFrameworkNative() {
		respondError(w, http.StatusBadRequest, "Agent has no model provider configured")
		return
	}

	var req struct {
		Message         string `json:"message"`
		ThinkingEnabled bool   `json:"thinking_enabled,omitempty"`
		Tools           bool   `json:"tools,omitempty"` // when true, run full executor loop with tool calls
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		respondError(w, http.StatusBadRequest, "Request must include a non-empty 'message' field")
		return
	}

	// ── Input Guardrails ────────────────────────────────────
	gr, grOK := h.requireGuardrails(w, r, agent, agent.Guardrails)
	if !grOK {
		return
	}
	var tally guardTally // what the rules redacted or warned about, recorded on the trace
	if h.Guardrails != nil && len(gr) > 0 {
		eval, gErr := h.Guardrails.EvaluateInput(r.Context(), gr, req.Message)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Input guardrail evaluation error (test)")
		} else if !eval.Passed {
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Input blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		} else {
			req.Message = eval.Apply(req.Message)
			h.noteGuardrails(r.Context(), kitchen, agentName, "input", eval)
			tally.add(eval)
		}
	}

	// ── Tools mode: run full executor loop ──────────────────
	if req.Tools {
		if agent.Status != models.AgentStatusReady {
			respondError(w, http.StatusBadRequest,
				fmt.Sprintf("Agent '%s' is not ready (status: %s) — bake it first", agentName, agent.Status))
			return
		}

		// When thinking is enabled, extend request timeout
		if req.ThinkingEnabled {
			var cancel context.CancelFunc
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			r = r.WithContext(ctx)
			_ = ctx
		}

		var resolved *models.ResolvedIngredients
		if agent.ResolvedConfig != nil {
			resolved = agent.ResolvedConfig
		} else {
			resolved, err = h.Resolver.Resolve(r.Context(), agent)
			if err != nil {
				respondError(w, http.StatusBadRequest, "Ingredient resolution failed: "+err.Error())
				return
			}
		}

		runCtx, _ := withRunTrace(r.Context())
		r = r.WithContext(runCtx)
		response, trace, err := h.Executor.Execute(r.Context(), agent, req.Message, resolved, nil, req.ThinkingEnabled)
		if h.respondRunEnded(w, r, agent, req.Message, trace, err) {
			return
		}
		if err != nil {
			respondError(w, http.StatusBadGateway, "Execution failed: "+err.Error())
			return
		}

		// ── Output Guardrails ─────────────────────────────────
		// (A run that judged every reply itself has nothing left to judge: trace.OutputJudged.)
		if h.Guardrails != nil && len(gr) > 0 && !trace.OutputJudged {
			eval, gErr := h.Guardrails.EvaluateOutput(r.Context(), gr, response)
			if gErr != nil {
				log.Warn().Err(gErr).Str("agent", agentName).Msg("Output guardrail evaluation error (test+tools); the reply is withheld")
				h.emitGuardrailAudit(r.Context(), kitchen, agentName, "output", nil, gErr)
				respondError(w, http.StatusServiceUnavailable, "the reply could not be checked by the guardrails, so it was withheld")
				return
			} else if !eval.Passed {
				respondJSON(w, http.StatusForbidden, map[string]interface{}{
					"error":      "Output blocked by guardrails",
					"guardrails": eval.Results,
				})
				return
			} else {
				original := response
				response = eval.Apply(response)
				redactedTurn(trace, original, response)
				h.noteGuardrails(r.Context(), kitchen, agentName, "output", eval)
				tally.add(eval)
			}
		}

		// Record trace
		traceUsage := trace.Usage
		traceRecord := &models.Trace{
			ID:           trace.TraceID,
			AgentName:    agentName,
			AgentVersion: agent.Version,
			Kitchen:      kitchen,
			Status:       traceStatus(trace),
			DurationMs:   trace.TotalMs,
			TotalTokens:  trace.Usage.TotalTokens,
			CostUSD:      trace.Usage.EstimatedCost,
			InputText:    req.Message,
			OutputText:   response,
			Usage:        &traceUsage,
			Metadata: map[string]interface{}{
				"mode":  "managed",
				"turns": len(trace.Turns),
				"type":  "test",
				"tools": true,
			},
			CreatedAt: time.Now().UTC(),
		}
		tally.addRun(trace)
		withGuardrailMeta(traceRecord.Metadata, tally)
		h.Store.CreateTrace(r.Context(), traceRecord)

		// Persist executor turns as hierarchical spans (includes tool spans)
		h.persistExecutorSpans(r.Context(), trace)

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"agent":           agentName,
			"response":        response,
			"trace_id":        trace.TraceID,
			"turns":           len(trace.Turns),
			"usage":           trace.Usage,
			"latency_ms":      trace.TotalMs,
			"execution_trace": trace,
		})
		return
	}

	// Framework-native agents: proxy the test call to the running process
	if agent.IsFrameworkNative() {
		if agent.Status != models.AgentStatusReady {
			respondError(w, http.StatusBadRequest,
				fmt.Sprintf("Agent '%s' is not ready (status: %s) — bake it first to test framework-native agents", agentName, agent.Status))
			return
		}
		response, traceRecord, err := h.proxyToProcess(r.Context(), agent, req.Message, nil, nil, kitchen)
		if err != nil {
			respondError(w, http.StatusBadGateway, "Framework agent test failed: "+err.Error())
			return
		}
		if h.Guardrails != nil && len(gr) > 0 {
			if eval, gErr := h.Guardrails.EvaluateOutput(r.Context(), gr, response); gErr != nil {
				log.Warn().Err(gErr).Str("agent", agentName).Msg("Output guardrail evaluation error (test, framework-native)")
			} else if !eval.Passed {
				respondJSON(w, http.StatusForbidden, map[string]interface{}{
					"error":      "Output blocked by guardrails",
					"guardrails": eval.Results,
				})
				return
			} else {
				response = eval.Apply(response)
				h.noteGuardrails(r.Context(), kitchen, agentName, "output", eval)
			}
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"agent":      agentName,
			"response":   response,
			"provider":   string(agent.Runtime),
			"model":      agent.ModelName,
			"usage":      traceRecord.Usage,
			"latency_ms": traceRecord.DurationMs,
			"trace_id":   traceRecord.ID,
		})
		return
	}

	// When thinking is enabled, extend request timeout
	if req.ThinkingEnabled {
		var cancel context.CancelFunc
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		r = r.WithContext(ctx)
		_ = ctx
	}

	// Build messages — include system prompt from ingredients if present
	messages := []models.ChatMessage{}
	for _, ing := range agent.Ingredients {
		if ing.Kind == models.IngredientPrompt {
			if text, ok := ing.Config["text"].(string); ok && text != "" {
				messages = append(messages, models.ChatMessage{Role: "system", Content: text})
			}
		}
	}
	messages = append(messages, models.ChatMessage{Role: "user", Content: req.Message})

	// Route through the model router
	start := time.Now()
	routeReq := &models.RouteRequest{
		Messages:        messages,
		Model:           agent.ModelName,
		Strategy:        models.RoutingFallback,
		Kitchen:         kitchen,
		AgentRef:        agentName,
		ThinkingEnabled: req.ThinkingEnabled,
	}

	// Use ContextSkipTrace so the router does not create its own flat trace;
	// this handler records a richer trace below with input/output text.
	resp, err := h.Router.Route(router.ContextGuardrailsApplied(router.ContextSkipTrace(r.Context())), routeReq)
	duration := time.Since(start)
	if err != nil {
		if respondRefusal(w, err) {
			return
		}
		respondError(w, http.StatusBadGateway, "Model provider error: "+err.Error())
		return
	}

	// ── Output Guardrails ───────────────────────────────────
	if h.Guardrails != nil && len(gr) > 0 {
		eval, gErr := h.Guardrails.EvaluateOutput(r.Context(), gr, resp.Content)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Output guardrail evaluation error (test)")
		} else if !eval.Passed {
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Output blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		} else {
			resp.Content = eval.Apply(resp.Content)
			h.noteGuardrails(r.Context(), kitchen, agentName, "output", eval)
			tally.add(eval)
		}
	}

	// Record trace — enriched with input/output text and full token usage
	traceUsage := resp.Usage
	trace := &models.Trace{
		ID:           uuid.New().String(),
		AgentName:    agentName,
		AgentVersion: agent.Version,
		Kitchen:      kitchen,
		Status:       "completed",
		DurationMs:   duration.Milliseconds(),
		TotalTokens:  resp.Usage.TotalTokens,
		CostUSD:      resp.Usage.EstimatedCost,
		InputText:    req.Message,
		OutputText:   resp.Content,
		Usage:        &traceUsage,
		Metadata: map[string]interface{}{
			"provider":        resp.Provider,
			"model":           resp.Model,
			"type":            "test",
			"thinking_blocks": resp.ThinkingBlocks,
			"thinking_tokens": resp.Usage.ThinkingTokens,
		},
		CreatedAt: time.Now().UTC(),
	}
	withGuardrailMeta(trace.Metadata, tally)
	h.Store.CreateTrace(r.Context(), trace)
	h.emitThinkingAudit(r.Context(), kitchen, agentName, "test", req.ThinkingEnabled, resp.ThinkingBlocks, resp.Usage.ThinkingTokens)

	// Persist a single LLM span for the test call
	now := time.Now().UTC()
	llmStart := now.Add(-duration)
	reqJSON, _ := json.Marshal(messages)
	respJSON, _ := json.Marshal(map[string]interface{}{
		"content":         resp.Content,
		"thinking_blocks": resp.ThinkingBlocks,
	})
	llmSpan := models.Span{
		ID:         uuid.New().String(),
		TraceID:    trace.ID,
		Name:       fmt.Sprintf("llm.%s", resp.Model),
		Kind:       models.SpanKindLLM,
		Status:     "completed",
		StartTime:  llmStart,
		EndTime:    now,
		DurationMs: duration.Milliseconds(),
		Input:      reqJSON,
		Output:     respJSON,
		Usage:      &traceUsage,
		Model:      resp.Model,
		Provider:   resp.Provider,
	}
	h.Store.CreateSpan(r.Context(), &llmSpan)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"agent":           agentName,
		"response":        resp.Content,
		"provider":        resp.Provider,
		"model":           resp.Model,
		"usage":           resp.Usage,
		"latency_ms":      duration.Milliseconds(),
		"trace_id":        trace.ID,
		"thinking_blocks": resp.ThinkingBlocks,
	})
}

// RecookAgent edits a baked agent's configuration and re-bakes it.
// POST /api/v1/agents/{agentName}/recook
//
// Accepts the same fields as UpdateAgent (description, framework, model_provider,
// model_name, ingredients, skills, tags, max_turns) plus optionally "version".
// After applying edits, runs the full bake pipeline (resolve + provider test).
// Bumps the minor version automatically unless version is explicitly provided.
func (h *Handlers) RecookAgent(w http.ResponseWriter, r *http.Request) {
	retryOnStale(w, r, h.recookAgent)
}

func (h *Handlers) recookAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	cat, ok := h.refCatalog(w, r.Context(), kitchen)
	if !ok {
		return
	}
	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if refuseRetired(w, agent) {
		return
	}
	// Only allow re-cook on agents that have been baked at least once
	if agent.Status == models.AgentStatusDraft {
		respondError(w, http.StatusBadRequest,
			"Agent is still a draft — use bake, not re-cook")
		return
	}

	var req struct {
		models.AgentEdit
		Tags    map[string]string `json:"tags"`
		Version string            `json:"version"`
	}
	body, ok := readConfigBody(w, r)
	if !ok {
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}
	}
	if req.Version != "" && !models.ValidVersion(req.Version) {
		respondError(w, http.StatusBadRequest, "version must be MAJOR.MINOR.PATCH, digits only (for example 1.2.0)")
		return
	}
	var refused []string
	if req.Cleared, refused = models.NullFields(body); len(refused) > 0 {
		respondError(w, http.StatusBadRequest, strings.Join(refused, ", ")+" cannot be cleared: the agent cannot run without it. Send a new value instead")
		return
	}

	// ── Apply edits ──────────────────────────────────────────
	// The edits in the body join any draft already waiting, and the re-cook bakes the lot.
	req.ExecutionMode = "" // as in an edit: only a bot may change it, and a re-cook never has
	probe := agent.WithDraft()
	req.AgentEdit.Apply(probe)
	if status, err := h.checkBotAgent(probe); err != nil {
		respondError(w, status, err.Error())
		return
	}
	agent.Edit(req.AgentEdit)
	agent.ApplyDraft()
	if len(req.Tags) > 0 {
		agent.Tags = mergeTags(agent.Tags, req.Tags) // a copy: the map the store handed out is shared with its own
	}
	if !h.checkAgentBake(w, cat, agent) {
		return
	}

	// ── Resolve ingredients (same as BakeAgent) ──────────────
	if agent.ModelProvider != "" {
		hasModelIngredient := false
		for _, ing := range agent.Ingredients {
			if ing.Kind == models.IngredientModel {
				hasModelIngredient = true
				break
			}
		}
		if !hasModelIngredient {
			agent.Ingredients = append(agent.Ingredients, models.Ingredient{
				ID:       "auto-model",
				Name:     agent.ModelProvider,
				Kind:     models.IngredientModel,
				Required: true,
				Config: map[string]interface{}{
					"provider": agent.ModelProvider,
					"model":    agent.ModelName,
				},
			})
		}
	}

	resolved, err := h.Resolver.Resolve(r.Context(), agent)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "Agent cannot be re-cooked — ingredient resolution failed",
			"details": err.Error(),
			"partial": resolved,
		})
		return
	}

	// ── Update agent with new config + baking status ─────────
	agent.ResolvedConfig = resolved
	agent.Status = models.AgentStatusBaking
	agent.UpdatedAt = time.Now().UTC()
	if req.Version != "" {
		agent.Version = req.Version
		agent.VersionBump = models.VersionBumpNamed // see BakeAgent
	} else {
		agent.VersionBump = models.VersionBumpBake
	}

	if err := h.Store.UpdateAgent(r.Context(), agent); err != nil {
		respondStoreError(w, err)
		return
	}

	// Re-fetch to get the store-computed version
	agent, _ = h.Store.GetAgent(r.Context(), kitchen, agentName)

	log.Info().
		Str("agent", agentName).
		Str("version", agent.Version).
		Msg("Agent re-cook started")

	// ── Async provider test and process replacement (same step as BakeAgent) ──
	agentKey, version := kitchen+"/"+agentName, agent.Version
	gen := h.bakes.begin(agentKey)
	go h.bakes.run(agentKey, gen, func(superseded func() bool) {
		h.finishBake(kitchen, agentName, version, "", true, superseded, nil)
	})

	respondJSON(w, http.StatusAccepted, map[string]interface{}{
		"name":       agentName,
		"version":    agent.Version,
		"status":     string(models.AgentStatusBaking),
		"re_cooked":  true,
		"agent_card": "/agents/" + agentName + "/a2a/.well-known/agent-card.json",
	})
}

func (h *Handlers) CoolAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	// A retired agent stays retired: cooling it would have brought it back as a cooled agent.
	if refuseRetired(w, agent) {
		return
	}

	// Stop the agent process if running
	if h.ProcessManager != nil {
		if err := h.ProcessManager.Stop(r.Context(), kitchen, agentName); err != nil {
			log.Warn().Err(err).Str("agent", agentName).Msg("Failed to stop agent process during cool")
		}
	}

	// Only the status and the process change: the agent read above may be stale by now, and writing it
	// whole would put back whatever a bake or an edit changed in between.
	applied, err := h.Store.UpdateAgentLifecycle(r.Context(), kitchen, agentName, models.AgentLifecycleGuard{Not: models.AgentStatusRetired},
		models.AgentLifecycle{Status: models.AgentStatusCooled, ClearProcess: true})
	if err != nil {
		log.Warn().Err(err).Str("agent", agentName).Msg("The cooled status could not be recorded")
	} else if !applied {
		respondError(w, http.StatusConflict, retiredMessage(agentName))
		return
	}

	// Pro: scale the k8s deployment to 0 so the pod is drained.
	if h.WorkloadScaler != nil {
		if scaleErr := h.WorkloadScaler.ScaleWorkloadForAgent(r.Context(), kitchen, agentName, 0); scaleErr != nil {
			log.Warn().Err(scaleErr).Str("agent", agentName).Msg("Failed to scale workload to 0 during cool")
		} else {
			log.Info().Str("agent", agentName).Msg("Workload scaled to 0 (cooled)")
		}
	}

	log.Info().Str("agent", agentName).Msg("Agent cooled")
	respondJSON(w, http.StatusOK, map[string]string{
		"name":   agentName,
		"status": string(models.AgentStatusCooled),
	})
}

// RewarmAgent transitions a cooled agent back to ready.
// POST /api/v1/agents/{agentName}/rewarm
func (h *Handlers) RewarmAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if refuseRetired(w, agent) {
		return
	}
	if agent.Status != models.AgentStatusCooled {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("agent is '%s', only cooled agents can be rewarmed", agent.Status))
		return
	}

	// Guarded by the status read above: an agent that was baked or retired since is not rewarmed.
	applied, err := h.Store.UpdateAgentLifecycle(r.Context(), kitchen, agentName, models.AgentLifecycleGuard{Status: models.AgentStatusCooled},
		models.AgentLifecycle{Status: models.AgentStatusReady, DeleteTags: []string{"error"}})
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !applied {
		respondError(w, http.StatusConflict, "the agent is no longer cooled: read it again")
		return
	}

	// Pro: restore the k8s deployment replica count so the operator brings the pod back.
	if h.WorkloadScaler != nil {
		if scaleErr := h.WorkloadScaler.ScaleWorkloadForAgent(r.Context(), kitchen, agentName, 1); scaleErr != nil {
			log.Warn().Err(scaleErr).Str("agent", agentName).Msg("Failed to scale workload back up during rewarm")
		} else {
			log.Info().Str("agent", agentName).Msg("Workload scaled to 1 (rewarmed)")
		}
	}

	log.Info().Str("agent", agentName).Msg("Agent rewarmed")
	respondJSON(w, http.StatusOK, map[string]string{
		"name":   agentName,
		"status": string(models.AgentStatusReady),
	})
}

func (h *Handlers) ListAgentVersions(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")

	versions, err := h.Store.ListAgentVersions(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, models.RedactAgents(versions))
}

func (h *Handlers) GetAgentVersion(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")
	version := chi.URLParam(r, "version")

	agent, err := h.Store.GetAgentVersion(r.Context(), kitchen, agentName, version)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, agent.Redacted())
}

// ══════════════════════════════════════════════════════════════
// ── Recipe Handlers ──────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// archivedView says which recipes a list should show: the live ones (the default), all of them
// ("?archived=include"), or only the archived ones ("?archived=only"). "?include_archived=true" is
// the same as include.
func archivedView(r *http.Request) string {
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("archived")))
	if v == "" && strings.EqualFold(r.URL.Query().Get("include_archived"), "true") {
		v = "include"
	}
	return v
}

func (h *Handlers) ListRecipes(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	recipes, err := h.Store.ListRecipes(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	view := archivedView(r)
	shown := make([]models.Recipe, 0, len(recipes))
	for _, rec := range recipes {
		switch {
		case view == "include",
			view == "only" && rec.IsArchived(),
			view != "only" && !rec.IsArchived():
			shown = append(shown, rec)
		}
	}
	respondJSON(w, http.StatusOK, shown)
}

func (h *Handlers) CreateRecipe(w http.ResponseWriter, r *http.Request) {
	var req models.Recipe
	body, ok := readConfigBody(w, r)
	if !ok {
		return
	}
	bodyProblems, ok := decodeConfig(w, body, &req)
	if !ok {
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "Recipe 'name' is required")
		return
	}
	if !recipeNameRe.MatchString(req.Name) {
		respondError(w, http.StatusBadRequest, "Recipe 'name' must be 2-63 chars and use lowercase letters, numbers, '_' or '-'")
		return
	}

	kitchen := middleware.GetKitchen(r.Context())
	// An archived recipe still holds its name (its runs belong to it): say so rather than replace it.
	if existing, err := h.Store.GetRecipe(r.Context(), kitchen, req.Name); err == nil && existing.IsArchived() {
		respondError(w, http.StatusConflict, fmt.Sprintf("Recipe '%s' is archived: restore it, or choose another name", req.Name))
		return
	}
	req.ID = uuid.New().String()
	req.Kitchen = kitchen
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()
	req.ArchivedAt, req.ArchivedBy = nil, ""

	// Normalize client-facing "agent" field into AgentRef used by the engine.
	for i := range req.Steps {
		if req.Steps[i].AgentRef == "" && req.Steps[i].Agent != "" {
			req.Steps[i].AgentRef = req.Steps[i].Agent
		}
		if req.Steps[i].Agent == "" && req.Steps[i].AgentRef != "" {
			req.Steps[i].Agent = req.Steps[i].AgentRef
		}
	}

	if !h.checkRecipeWrite(w, r, kitchen, nil, &req, bodyProblems) {
		return
	}
	if err := h.checkSubRecipeRefs(r.Context(), kitchen, req.Name, req.Steps); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := checkStepSettings(req.Steps); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.Store.CreateRecipe(r.Context(), &req); err != nil {
		respondCreateError(w, err, "create the recipe")
		return
	}

	log.Info().Str("recipe", req.Name).Str("id", req.ID).Msg("Recipe created")
	respondJSON(w, http.StatusCreated, req)
}

func (h *Handlers) GetRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	kitchen := middleware.GetKitchen(r.Context())

	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, recipeName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, recipe)
}

func (h *Handlers) UpdateRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	kitchen := middleware.GetKitchen(r.Context())

	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, recipeName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if recipe.IsArchived() {
		respondError(w, http.StatusConflict, fmt.Sprintf("Recipe '%s' is archived: restore it to change it", recipeName))
		return
	}

	var req models.Recipe
	body, ok := readConfigBody(w, r)
	if !ok {
		return
	}
	// A recipe sent back whole carries the fields of the stored one; they are the recipe's own, so none is unknown.
	bodyProblems, ok := decodeConfig(w, body, &req)
	if !ok {
		return
	}

	before := *recipe
	if req.Description != "" {
		recipe.Description = req.Description
	}
	if len(req.Steps) > 0 {
		// Normalize client-facing "agent" field into AgentRef used by the engine.
		for i := range req.Steps {
			if req.Steps[i].AgentRef == "" && req.Steps[i].Agent != "" {
				req.Steps[i].AgentRef = req.Steps[i].Agent
			}
			if req.Steps[i].Agent == "" && req.Steps[i].AgentRef != "" {
				req.Steps[i].Agent = req.Steps[i].AgentRef
			}
		}
		after := before
		after.Steps = req.Steps
		if !h.checkRecipeWrite(w, r, kitchen, &before, &after, bodyProblems) {
			return
		}
		if err := h.checkSubRecipeRefs(r.Context(), kitchen, recipe.Name, req.Steps); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := checkStepSettings(req.Steps); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		recipe.Steps = req.Steps
	} else if refcheck.New(bodyProblems) != nil {
		respondInvalidConfig(w, "the recipe", bodyProblems)
		return
	}
	recipe.UpdatedAt = time.Now().UTC()

	if err := h.Store.UpdateRecipe(r.Context(), recipe); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, recipe)
}

// DeleteRecipe archives a recipe, the way deleting an agent retires it: the recipe and every run it
// has stay in the store (a run is a record of what models did, and must remain auditable), but the
// recipe disappears from lists and can no longer be run or edited. RestoreRecipe undoes it.
func (h *Handlers) DeleteRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	kitchen := middleware.GetKitchen(r.Context())

	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, recipeName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if !recipe.IsArchived() {
		now := time.Now().UTC()
		recipe.ArchivedAt = &now
		recipe.ArchivedBy = callerSubject(r)
		recipe.UpdatedAt = now
		if err := h.Store.UpdateRecipe(r.Context(), recipe); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Info().Str("recipe", recipeName).Str("by", recipe.ArchivedBy).Msg("Recipe archived")
	}
	w.WriteHeader(http.StatusNoContent)
}

// RestoreRecipe brings an archived recipe back. POST /api/v1/recipes/{recipeName}/restore
func (h *Handlers) RestoreRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	kitchen := middleware.GetKitchen(r.Context())

	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, recipeName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if recipe.IsArchived() {
		recipe.ArchivedAt, recipe.ArchivedBy = nil, ""
		recipe.UpdatedAt = time.Now().UTC()
		if err := h.Store.UpdateRecipe(r.Context(), recipe); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Info().Str("recipe", recipeName).Msg("Recipe restored")
	}
	respondJSON(w, http.StatusOK, recipe)
}

// callerSubject is who made the request, for the record of who archived something.
func callerSubject(r *http.Request) string {
	if id := pkgmw.GetIdentity(r.Context()); id != nil {
		return id.Subject
	}
	return ""
}

// recipeExecContext is the context a recipe run starts under: the caller's key (so the run's agent
// calls are made as them) and identity (for the record of who started it).
func recipeExecContext(r *http.Request) context.Context {
	execCtx := r.Context()
	if triggerKey := extractAPIKeyFromRequest(r); triggerKey != "" {
		execCtx = workflow.WithExecutionAPIKey(execCtx, triggerKey)
	}
	if id := pkgmw.GetIdentity(r.Context()); id != nil && id.Subject != "" {
		execCtx = workflow.WithTriggeredBy(execCtx, id.Subject)
	}
	// The request is there for the synchronous start of the run, where a decision that needs the
	// caller's signature (skipping an approval gate) is taken. The run itself does not use it.
	return contracts.WithRequest(execCtx, r)
}

// RerunRecipeRun starts a recipe again with the input (and environment) of an earlier run: "run again".
// The new run records which run it repeats. It uses the recipe as it is now, not as it was then.
// POST /api/v1/recipes/{recipeName}/runs/{runId}/rerun  body (optional): {"input": {...}, "environment": "..."}
func (h *Handlers) RerunRecipeRun(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	runID := chi.URLParam(r, "runId")
	kitchen := middleware.GetKitchen(r.Context())

	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, recipeName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if recipe.IsArchived() {
		respondError(w, http.StatusConflict, fmt.Sprintf("Recipe '%s' is archived: restore it to run it", recipeName))
		return
	}
	prev, err := h.runInKitchen(r, runID)
	if err != nil || prev.RecipeID != recipe.ID {
		respondError(w, http.StatusNotFound, fmt.Sprintf("Run '%s' not found for recipe '%s'", runID, recipeName))
		return
	}

	var req struct {
		Input       map[string]interface{} `json:"input"`
		Environment *string                `json:"environment"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	input := prev.Input
	if req.Input != nil {
		input = req.Input
	}
	env := prev.Environment
	if req.Environment != nil {
		env = *req.Environment
	}

	executor := h.RecipeExecutor
	if executor == nil {
		executor = h.Workflow
	}
	newID, err := executor.ExecuteRecipe(workflow.WithRerunOf(recipeExecContext(r), prev.ID), recipe, kitchen, input, env)
	if err != nil {
		if strings.HasPrefix(err.Error(), "permission_denied") {
			respondError(w, http.StatusForbidden, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	log.Info().Str("recipe", recipeName).Str("run_id", newID).Str("rerun_of", prev.ID).Msg("Recipe run repeated")
	respondJSON(w, http.StatusAccepted, map[string]string{
		"recipe": recipeName, "status": "running", "run_id": newID, "rerun_of": prev.ID,
		"poll": "/api/v1/recipes/" + recipeName + "/runs/" + newID,
	})
}

func (h *Handlers) BakeRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	kitchen := middleware.GetKitchen(r.Context())

	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, recipeName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if recipe.IsArchived() {
		respondError(w, http.StatusConflict, fmt.Sprintf("Recipe '%s' is archived: restore it to run it", recipeName))
		return
	}

	// Parse request body: { "input": {...}, "environment": "staging" }
	var req struct {
		Input       map[string]interface{} `json:"input"`
		Environment string                 `json:"environment"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	// What the steps name must still exist, and the environment asked for must be the kitchen's.
	if !h.checkRecipeRun(w, r, kitchen, recipe, req.Environment) {
		return
	}

	// Resolve the executor: Pro sets h.RecipeExecutor for role-gated env execution;
	// OSS falls back directly to the workflow engine.
	executor := h.RecipeExecutor
	if executor == nil {
		executor = h.Workflow
	}

	runID, err := executor.ExecuteRecipe(recipeExecContext(r), recipe, kitchen, req.Input, req.Environment)
	if err != nil {
		// (This compared the first 16 characters of the message with the 17-character word, so a refusal
		// such as "permission_denied: environment requires approval" was answered 500, not 403.)
		if strings.HasPrefix(err.Error(), "permission_denied") {
			respondError(w, http.StatusForbidden, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	log.Info().
		Str("recipe", recipeName).
		Str("run_id", runID).
		Str("environment", req.Environment).
		Int("steps", len(recipe.Steps)).
		Msg("Recipe execution started")

	resp := map[string]string{
		"recipe": recipeName,
		"status": "running",
		"run_id": runID,
		"poll":   "/api/v1/recipes/" + recipeName + "/runs/" + runID,
	}
	if req.Environment != "" {
		resp["environment"] = req.Environment
	}
	respondJSON(w, http.StatusAccepted, resp)
}

func extractAPIKeyFromRequest(r *http.Request) string {
	if authHeader := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	}
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return key
	}
	if key := strings.TrimSpace(r.URL.Query().Get("api_key")); key != "" {
		return key
	}
	return ""
}

func (h *Handlers) RecipeHistory(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	kitchen := middleware.GetKitchen(r.Context())

	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, recipeName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	runs, err := h.Store.ListRecipeRuns(r.Context(), recipe.ID, 50)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []models.RecipeRun{}
	}
	respondJSON(w, http.StatusOK, runs)
}

// GetRecipeRun returns the status and results of a specific recipe run.
func (h *Handlers) GetRecipeRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")

	run, err := h.runInKitchen(r, runID)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	resp := map[string]interface{}{
		"run": run,
	}
	if run.Status == models.RecipeRunPaused {
		resp["pending_gates"] = h.Workflow.GetPendingGates(run.ID)
	}

	respondJSON(w, http.StatusOK, resp)
}

// ReleaseWait ends a wait step early.
// POST /api/v1/recipes/{recipeName}/runs/{runId}/steps/{stepName}/release
func (h *Handlers) ReleaseWait(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	stepName := chi.URLParam(r, "stepName")
	kitchen := middleware.GetKitchen(r.Context())
	// The run must be this kitchen's whether or not the decision is signed: the engine's release map is
	// keyed by run id alone, and an id is not a secret (it is in traces and the audit trail).
	if _, _, err := h.waitingStep(r.Context(), kitchen, runID, stepName); err != nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("Step '%s' of run '%s' is not waiting", stepName, runID))
		return
	}
	sig, ok := h.signDecision(w, r, contracts.GateWaitRelease, runID+":"+stepName, contracts.MeaningApproved, nil)
	if !ok {
		return
	}
	released := h.Workflow.ReleaseWait(kitchen, runID, stepName)
	var applyErr error
	if !released {
		applyErr = fmt.Errorf("step is not waiting")
	}
	h.decisionApplied(r, sig, applyErr)
	if !released {
		respondError(w, http.StatusNotFound, fmt.Sprintf("Step '%s' of run '%s' is not waiting", stepName, runID))
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"run_id": runID, "step": stepName, "released": true})
}

// endLostRun marks an unfinished run that no process holds as canceled. It reports whether it did.
func (h *Handlers) endLostRun(r *http.Request, runID string) bool {
	run, err := h.Store.GetRecipeRun(r.Context(), runID)
	if err != nil || run == nil {
		return false
	}
	if kitchen := middleware.GetKitchen(r.Context()); run.Kitchen != "" && run.Kitchen != kitchen {
		return false
	}
	switch run.Status {
	case models.RecipeRunSubmitted, models.RecipeRunRunning, models.RecipeRunPaused:
	default:
		return false
	}
	now := time.Now().UTC()
	run.Status, run.CompletedAt = models.RecipeRunCanceled, &now
	if !run.StartedAt.IsZero() {
		run.DurationMs = now.Sub(run.StartedAt).Milliseconds()
	}
	run.Error = "canceled by a person; no process was holding this run (it was most likely lost when the server restarted)"
	for i := range run.StepResults {
		if run.StepResults[i].Status == "running" || run.StepResults[i].Status == "waiting" {
			run.StepResults[i].Status = "canceled"
		}
	}
	if err := h.Store.UpdateRecipeRun(r.Context(), run); err != nil {
		return false
	}
	return true
}

// CancelRecipeRun cancels a running recipe execution.
func (h *Handlers) CancelRecipeRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")

	// The engine keys its live runs by id alone, so the run's kitchen is checked here, before anything is cancelled.
	if _, err := h.runInKitchen(r, runID); err != nil {
		respondError(w, http.StatusNotFound, "Run not found or already completed: "+runID)
		return
	}
	if ok := h.Workflow.CancelRun(runID); !ok {
		// No process holds this run. If the store still shows it as unfinished, the process that held it is
		// gone (a restart): the person asked for it to end, so it ends in the record instead of staying
		// "waiting" for good. A run another replica is still executing would also be marked; cancelling is
		// the person's explicit act.
		if !h.endLostRun(r, runID) {
			respondError(w, http.StatusNotFound, "Run not found or already completed: "+runID)
			return
		}
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"run_id": runID,
		"status": "canceled",
	})
}

// ApproveGate approves or rejects a human gate in a recipe run.
func (h *Handlers) ApproveGate(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	stepName := chi.URLParam(r, "stepName")

	var req struct {
		Approved bool `json:"approved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if _, err := h.runInKitchen(r, runID); err != nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("No pending gate '%s' for run '%s'", stepName, runID))
		return
	}
	if h.DecisionSigner != nil {
		// This route records no approver and checks no constraint. With signatures on, it is the
		// same decision as the route that does, so it takes the same path.
		h.approveSignedGate(w, r, runID, stepName, req.Approved, "", "")
		return
	}

	if ok := h.Workflow.ApproveGate(runID, stepName, req.Approved); !ok {
		respondError(w, http.StatusNotFound, fmt.Sprintf("No pending gate '%s' for run '%s'", stepName, runID))
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"run_id":   runID,
		"step":     stepName,
		"approved": req.Approved,
	})
}

// ══════════════════════════════════════════════════════════════
// ── Model Router Handlers ────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// builtinProviderTemplates are hardcoded so they work even when the model
// catalogue is unreachable.  Users pick a template, paste their API key,
// and everything else is pre-filled.
var builtinProviderTemplates = []models.ProviderTemplate{
	{
		Kind:            "openai",
		DisplayName:     "OpenAI",
		Description:     "GPT-4o, GPT-4.1, o3/o4-mini and other OpenAI models",
		DefaultEndpoint: "https://api.openai.com/v1",
		DefaultModels:   []string{"gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o", "gpt-4o-mini", "o3", "o4-mini"},
		RequiredConfig:  []string{"api_key"},
		HelpURL:         "https://platform.openai.com/api-keys",
	},
	{
		Kind:            "azure-openai",
		DisplayName:     "Azure OpenAI",
		Description:     "OpenAI models hosted on Azure with your own deployments",
		DefaultEndpoint: "https://{resource}.openai.azure.com",
		DefaultModels:   []string{"gpt-4o", "gpt-4o-mini"},
		RequiredConfig:  []string{"api_key", "resource_name", "deployment_name", "api_version"},
		HelpURL:         "https://learn.microsoft.com/azure/ai-services/openai/",
	},
	{
		Kind:            "anthropic",
		DisplayName:     "Anthropic",
		Description:     "Claude 4 Opus, Sonnet, and Haiku models",
		DefaultEndpoint: "https://api.anthropic.com/v1",
		DefaultModels:   []string{"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"},
		RequiredConfig:  []string{"api_key"},
		HelpURL:         "https://console.anthropic.com/settings/keys",
	},
	{
		Kind:            "gemini",
		DisplayName:     "Google Gemini",
		Description:     "Gemini 2.5 Pro/Flash and 2.0 Flash models via Google AI Studio",
		DefaultEndpoint: "https://generativelanguage.googleapis.com/v1beta",
		DefaultModels:   []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash", "gemini-2.0-flash-lite"},
		RequiredConfig:  []string{"api_key"},
		HelpURL:         "https://aistudio.google.com/apikey",
	},
	{
		Kind:            "ollama",
		DisplayName:     "Ollama (Local)",
		Description:     "Run open-source models locally — Llama 4, Qwen, Gemma, etc.",
		DefaultEndpoint: "http://localhost:11434",
		DefaultModels:   []string{"llama3.1", "qwen2.5", "gemma2"},
		RequiredConfig:  []string{},
		HelpURL:         "https://ollama.com/library",
	},
	{
		Kind:            "litellm",
		DisplayName:     "LiteLLM Proxy",
		Description:     "Unified proxy to 100+ LLM providers via LiteLLM",
		DefaultEndpoint: "http://localhost:4000",
		DefaultModels:   []string{},
		RequiredConfig:  []string{"api_key"},
		HelpURL:         "https://docs.litellm.ai/docs/",
	},
	{
		Kind:            "openrouter",
		DisplayName:     "OpenRouter",
		Description:     "Access 200+ models (GPT-5, Claude, Llama, Gemini, Mistral, and more) via a single OpenAI-compatible endpoint",
		DefaultEndpoint: "https://openrouter.ai/api/v1",
		DefaultModels:   []string{"openai/gpt-4.1", "anthropic/claude-opus-4", "google/gemini-2.5-pro", "meta-llama/llama-4-maverick", "mistralai/mistral-large", "deepseek/deepseek-r2"},
		RequiredConfig:  []string{"api_key"},
		HelpURL:         "https://openrouter.ai/keys",
	},
}

// ListProviderTemplates returns hardcoded provider templates so the
// dashboard can pre-fill the "Add Provider" form.
func (h *Handlers) ListProviderTemplates(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, builtinProviderTemplates)
}

func (h *Handlers) ListProviders(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	providers, err := h.Store.ListProviders(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if providers == nil {
		providers = []models.ModelProvider{}
	}
	// Mask API keys in response
	masked := make([]models.ModelProvider, len(providers))
	for i, p := range providers {
		cp := p
		masked[i] = *maskProviderKeys(&cp)
		masked[i].Modalities = h.providerModalities(&p)
	}
	respondJSON(w, http.StatusOK, masked)
}

func (h *Handlers) CreateProvider(w http.ResponseWriter, r *http.Request) {
	var req models.ModelProvider
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := h.Router.CheckModalities(&req); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	req.ID = uuid.New().String()
	req.Kitchen = middleware.GetKitchen(r.Context())
	req.CreatedAt = time.Now().UTC()

	if err := h.checkExternalRefs(r, nil, &req); err != nil {
		respondError(w, http.StatusForbidden, err.Error())
		return
	}
	if err := h.checkVaultRef(r.Context(), req.Kitchen, req.SecretRef); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A key pasted into the form goes to the vault (when there is one) and only a reference is stored.
	sealed, err := h.sealProviderKeys(r.Context(), &req)
	if err != nil {
		h.dropProviderKeys(r.Context(), &req, sealed...)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := h.Store.CreateProvider(r.Context(), &req); err != nil {
		h.dropProviderKeys(r.Context(), &req, sealed...)
		respondCreateError(w, err, "create the provider")
		return
	}

	log.Info().Str("provider", req.Name).Str("kind", req.Kind).Msg("Model provider registered")
	created := maskProviderKeys(&req) // never echo the API key back
	created.Modalities = h.providerModalities(&req)
	respondJSON(w, http.StatusCreated, created)
}

func (h *Handlers) GetProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "providerName")
	kitchen := middleware.GetKitchen(r.Context())
	provider, err := h.Store.GetProvider(r.Context(), kitchen, name)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	masked := maskProviderKeys(provider)
	masked.Modalities = h.providerModalities(provider)
	respondJSON(w, http.StatusOK, masked)
}

// providerModalities lists what an agent on the provider can use: the union
// over the provider's models, since image input depends on the model.
func (h *Handlers) providerModalities(p *models.ModelProvider) []string {
	if h.Router == nil {
		return nil
	}
	names := p.Models
	if len(names) == 0 {
		names = []string{""}
	}
	have := map[string]bool{}
	for _, model := range names {
		for _, m := range h.Router.Modalities(p, model) {
			have[m] = true
		}
	}
	var out []string
	for _, m := range models.AllModalities {
		if have[m] {
			out = append(out, m)
		}
	}
	return out
}

func (h *Handlers) DeleteProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "providerName")
	kitchen := middleware.GetKitchen(r.Context())
	existing, _ := h.Store.GetProvider(r.Context(), kitchen, name)
	if err := h.Store.DeleteProvider(r.Context(), kitchen, name); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The provider's own keys go with it. A credential it only pointed at is left alone.
	if existing != nil {
		h.dropProviderKeys(r.Context(), existing, ownedKeyNames(existing)...)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) UpdateProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "providerName")
	kitchen := middleware.GetKitchen(r.Context())

	provider, err := h.Store.GetProvider(r.Context(), kitchen, name)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	var req models.ModelProvider
	if err := json.Unmarshal(body, &req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	// is_default is a plain bool on the model, so "false" and "not sent" look
	// the same there; only change it when the caller actually sent it.
	var sent struct {
		IsDefault        *bool   `json:"is_default"`
		TLSSkipVerify    *bool   `json:"tls_skip_verify"`
		RotationStrategy *string `json:"rotation_strategy"`
	}
	_ = json.Unmarshal(body, &sent)
	keysBefore := providerKeySignature(provider)
	oldOwned := ownedKeyNames(provider) // before a pool in the request replaces the stored one
	heldSecretRef, heldPool := provider.SecretRef, append([]models.APIKeyEntry(nil), provider.APIKeys...)

	if req.Kind != "" {
		provider.Kind = req.Kind
	}
	if !h.mayRepoint(r, provider, &req) {
		respondJSON(w, http.StatusForbidden, map[string]string{
			"error":   "provider_endpoint_pinned",
			"message": "This provider's credential is pinned to its current host. Enter a new credential in the same request, or ask someone who may manage providers to change the endpoint.",
		})
		return
	}
	if req.Endpoint != "" {
		provider.Endpoint = req.Endpoint
	}
	if len(req.Models) > 0 {
		provider.Models = req.Models
	}
	// Check the update itself before merging: merging drops emptied entries,
	// which would hide a typo like an unknown modality with no settings.
	if err := models.ValidateModalities(req.Config); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Config != nil {
		// Merge config — don't overwrite existing keys that aren't in the update. A key sent back
		// still masked (a client that read the provider and wrote it back) keeps its stored value.
		if provider.Config == nil {
			provider.Config = map[string]interface{}{}
		}
		for k, v := range models.RestoreRedactedConfig(req.Config, provider.Config) {
			if k == "modalities" {
				provider.Config[k] = models.MergeModalities(provider.Config[k], v)
				continue
			}
			provider.Config[k] = v
		}
	}
	if sent.IsDefault != nil {
		provider.IsDefault = *sent.IsDefault
	}
	if sent.TLSSkipVerify != nil {
		provider.TLSSkipVerify = *sent.TLSSkipVerify
	}
	if sent.RotationStrategy != nil {
		if !validRotationStrategy(*sent.RotationStrategy) {
			respondError(w, http.StatusBadRequest, "rotation_strategy must be round-robin, random or weighted")
			return
		}
		provider.RotationStrategy = *sent.RotationStrategy
	}
	// A key pool sent in full replaces the stored one; entries read back masked keep their stored key.
	if req.APIKeys != nil {
		pool, err := mergePoolUpdate(req.APIKeys, provider.APIKeys)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		provider.APIKeys = pool
	}
	// What the request would leave the provider pointing at is checked against what it pointed at before.
	if req.SecretRef != "" || req.APIKeys != nil {
		candidate := *provider
		if req.SecretRef != "" {
			candidate.SecretRef = req.SecretRef
		}
		if err := h.checkExternalRefs(r, &models.ModelProvider{SecretRef: heldSecretRef, APIKeys: heldPool}, &candidate); err != nil {
			respondError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	// Point the provider at an external secret (k8s:// or env://) instead of keeping a key in the
	// database. Sending nothing leaves it as it is.
	if req.SecretRef != "" {
		if err := h.checkVaultRef(r.Context(), kitchen, req.SecretRef); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		provider.SecretRef = req.SecretRef
		if strings.HasPrefix(req.SecretRef, secretref.VaultScheme) {
			delete(provider.Config, "api_key") // the reference wins; a stale key is not left behind
		}
	}
	// A new key goes to the vault (when there is one); so does one this provider still holds in plain
	// text from before, the first time it is saved.
	sealedAll, sealErr := h.sealProviderKeys(r.Context(), provider)
	// A credential the provider already owned was rotated in place and stays whatever happens next;
	// only the ones made just now are cleaned up if this save fails.
	sealed := without(sealedAll, oldOwned)
	if sealErr != nil {
		h.dropProviderKeys(r.Context(), provider, sealed...)
		respondError(w, http.StatusBadRequest, sealErr.Error())
		return
	}
	if err := h.Router.CheckModalities(provider); err != nil {
		h.dropProviderKeys(r.Context(), provider, sealed...)
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Update CA bundle if provided. Sending an empty string keeps the existing
	// bundle. Sending a non-empty PEM string replaces it (or sets it for the
	// first time). The cached custom HTTP client is invalidated so the new
	// bundle is picked up immediately on the next provider call.
	if req.CABundle != "" {
		provider.CABundle = req.CABundle
		h.Router.InvalidateProviderClient(provider.Name)
	}

	// Capture old model list before saving so we can detect removed models.
	oldModels := make(map[string]bool, len(provider.Models))
	for _, m := range provider.Models {
		oldModels[m] = true
	}

	if err := h.Store.UpdateProvider(r.Context(), provider); err != nil {
		h.dropProviderKeys(r.Context(), provider, sealed...)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Pointing the provider away from a credential it made leaves that credential unused: remove it.
	kept := map[string]bool{}
	for _, n := range ownedKeyNames(provider) {
		kept[n] = true
	}
	for _, n := range oldOwned {
		if !kept[n] {
			h.dropProviderKeys(r.Context(), provider, n)
		}
	}
	// A new key value was written (including a rotation in place, which leaves the reference as it
	// was), or the keys the provider reads were changed: processes holding the old one need telling.
	if h.KeyRotation != nil && (len(sealedAll) > 0 || providerKeySignature(provider) != keysBefore) {
		h.KeyRotation.ProviderKeyChanged(context.WithoutCancel(r.Context()), kitchen, name)
	}

	// If the model list shrank, burn any ready/active agents whose model was removed.
	burntCount := 0
	if len(req.Models) > 0 {
		newModels := make(map[string]bool, len(req.Models))
		for _, m := range req.Models {
			newModels[m] = true
		}
		if allAgents, listErr := h.Store.ListAgents(r.Context(), kitchen); listErr == nil {
			for i := range allAgents {
				ag := &allAgents[i]
				if ag.ModelProvider != name {
					continue
				}
				if ag.Status != models.AgentStatusReady && ag.Status != models.AgentStatusBaking {
					continue
				}
				if ag.ModelName != "" && !newModels[ag.ModelName] {
					// Guarded by the status and version read above, and only the status is written: an
					// agent baked or edited since is not burnt with a stale copy of itself.
					applied, updateErr := h.Store.UpdateAgentLifecycle(r.Context(), kitchen, ag.Name,
						models.AgentLifecycleGuard{Status: ag.Status, Version: ag.Version}, models.AgentLifecycle{Status: models.AgentStatusBurnt})
					if updateErr != nil {
						log.Warn().Err(updateErr).Str("agent", ag.Name).Msg("Failed to burn agent after provider update")
					} else if applied {
						burntCount++
						log.Info().Str("provider", name).Str("agent", ag.Name).Str("model", ag.ModelName).Msg("Agent burnt — model removed from provider")
					}
				}
			}
		}
	}

	log.Info().Str("provider", name).Int("agents_burnt", burntCount).Msg("Model provider updated")
	updated := maskProviderKeys(provider)
	updated.Modalities = h.providerModalities(provider)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"provider":     updated,
		"agents_burnt": burntCount,
	})
}

func (h *Handlers) RouteModel(w http.ResponseWriter, r *http.Request) {
	req, ok := h.rawRouteRequest(w, r)
	if !ok {
		return
	}

	resp, err := h.Router.Route(r.Context(), req)
	if err != nil {
		if respondRefusal(w, err) {
			return
		}
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

// RouteModelStream routes a model request with Server-Sent Events streaming.
// POST /api/v1/models/route/stream
func (h *Handlers) RouteModelStream(w http.ResponseWriter, r *http.Request) {
	req, ok := h.rawRouteRequest(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "Streaming not supported")
		return
	}

	// The stream is committed (status 200, headers) when its first frame is written, so a call the gate,
	// a rate limit or a guardrail refuses before any model answers can still be a 402, 429 or 403.
	started := false
	start := func() {
		if started {
			return
		}
		started = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
	}

	err := h.Router.RouteStream(r.Context(), req, func(chunk *models.StreamChunk) error {
		start()
		data, _ := json.Marshal(chunk)
		_, writeErr := fmt.Fprintf(w, "data: %s\n\n", data)
		if writeErr != nil {
			return writeErr
		}
		flusher.Flush()
		return nil
	})

	if err != nil {
		if !started && respondRefusal(w, err) {
			return
		}
		start()
		errChunk, _ := json.Marshal(models.StreamChunk{Error: err.Error(), Done: true})
		fmt.Fprintf(w, "data: %s\n\n", errChunk)
		flusher.Flush()
		return
	}
	start()
}

func (h *Handlers) GetCostSummary(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	summary := h.Router.GetCostSummary(kitchen)
	respondJSON(w, http.StatusOK, summary)
}

// TestProvider performs a real credential-validating test against a single provider.
// POST /api/v1/models/providers/{providerName}/test
func (h *Handlers) TestProvider(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "providerName")
	if providerName == "" {
		respondError(w, http.StatusBadRequest, "provider name is required")
		return
	}

	provider, err := h.Store.GetProvider(r.Context(), middleware.GetKitchen(r.Context()), providerName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, fmt.Sprintf("provider %q not found", providerName))
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	log.Info().Str("provider", providerName).Str("kind", provider.Kind).Msg("Testing provider")

	result := h.Router.TestProvider(r.Context(), provider)
	if result.Refused != nil && respondRefusal(w, result.Refused) {
		return // the test was not made: a budget or a rate limit said no (402, 429), the provider was not asked
	}

	// Cache the test result on the provider
	now := time.Now().UTC()
	provider.LastTestedAt = &now
	provider.LastTestHealthy = &result.Healthy
	provider.LastTestError = result.Error
	provider.LastTestLatency = result.LatencyMs

	if err := h.Store.UpdateProvider(r.Context(), provider); err != nil {
		log.Warn().Err(err).Str("provider", providerName).Msg("Failed to cache test result on provider")
	}

	respondJSON(w, http.StatusOK, result)
}

// ══════════════════════════════════════════════════════════════
// ── MCP Gateway Handlers ─────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) MCPEndpoint(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	var req models.MCPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models.MCPResponse{
			Jsonrpc: "2.0",
			Error: &models.MCPError{
				Code:    -32700,
				Message: "Parse error",
				Data:    err.Error(),
			},
			ID: nil,
		})
		return
	}

	log.Info().Str("method", req.Method).Str("kitchen", kitchen).Msg("MCP request received")

	// A tool call made here has no agent run around it: the workspace guardrails judge its arguments
	// and the call is audited under the caller (mcp_gateway.go).
	var tool string
	callReq := &req
	var resp *models.MCPResponse
	if req.Method == "tools/call" {
		var params models.MCPToolCallParams
		if json.Unmarshal(req.Params, &params) == nil {
			tool = params.Name
			callReq, resp = h.guardMCPCall(r.Context(), kitchen, &req, &params)
		}
	}
	if resp == nil {
		resp = h.MCPGateway.HandleJSONRPC(r.Context(), kitchen, callReq)
	}
	if tool != "" {
		h.auditMCPCall(r.Context(), kitchen, tool, mcpOutcome(resp))
	}
	if resp == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handlers) MCPSSEEndpoint(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "SSE not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := h.MCPGateway.Subscribe(kitchen)
	defer h.MCPGateway.Unsubscribe(kitchen, ch)

	fmt.Fprintf(w, "event: connected\ndata: {\"kitchen\":\"%s\"}\n\n", kitchen)
	flusher.Flush()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(msg)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(data))
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}

func (h *Handlers) ListMCPTools(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	tools, err := h.Store.ListTools(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tools == nil {
		tools = []models.MCPTool{}
	}
	// A tool's authentication is never returned; see pkg/toolauth.
	respondJSON(w, http.StatusOK, toolauth.RedactTools(tools))
}

func (h *Handlers) RegisterMCPTool(w http.ResponseWriter, r *http.Request) {
	var body toolBody
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err != nil || body.decode(raw) != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	req := body.MCPTool

	// Validate required fields
	if strings.TrimSpace(req.Name) == "" {
		respondError(w, http.StatusBadRequest, "Tool 'name' is required")
		return
	}
	if !validRegisteredTransport(req.Transport) {
		respondError(w, http.StatusBadRequest, "Invalid transport: must be 'http', 'sse', or 'stdio'")
		return
	}

	kitchen := middleware.GetKitchen(r.Context())
	req.ID = uuid.New().String()
	req.Kitchen = kitchen
	req.Enabled = body.enabledOnCreate() // a tool registered with enabled=false is registered switched off
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()

	if req.Transport == "" {
		req.Transport = "http"
	}
	if len(req.Capabilities) == 0 {
		req.Capabilities = []string{"tool"}
	}
	if err := h.checkToolEndpoint(r.Context(), kitchen, req.Transport, req.Endpoint); err != nil {
		respondError(w, http.StatusBadRequest, "endpoint: "+err.Error())
		return
	}
	auth, sealed, err := h.sealToolAuth(r.Context(), kitchen, req.Name, restoreMaskedAuth(nil, req.AuthConfig))
	if err != nil {
		respondCreateError(w, err, "register the tool")
		return
	}
	req.AuthConfig = auth

	if err := h.Store.CreateTool(r.Context(), &req); err != nil {
		h.dropToolCredential(r.Context(), kitchen, sealed)
		respondCreateError(w, err, "register the tool")
		return
	}

	log.Info().Str("tool", req.Name).Str("transport", req.Transport).Str("kitchen", kitchen).Msg("MCP tool registered")
	respondJSON(w, http.StatusCreated, toolauth.RedactTool(req))
}

// BulkRegisterMCPTools registers multiple MCP tools in a single request.
// POST /api/v1/tools/bulk
// Body: { "tools": [ { "name": "...", ... }, ... ] }
func (h *Handlers) BulkRegisterMCPTools(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tools []toolBody `json:"tools"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if len(req.Tools) == 0 {
		respondError(w, http.StatusBadRequest, "No tools provided")
		return
	}

	kitchen := middleware.GetKitchen(r.Context())
	now := time.Now().UTC()

	// Every tool is checked before any is created, so a bad one does not leave the others half registered.
	tools := make([]models.MCPTool, len(req.Tools))
	for i := range req.Tools {
		tool := req.Tools[i].MCPTool
		if tool.Name == "" {
			respondError(w, http.StatusBadRequest, fmt.Sprintf("Tool at index %d missing 'name'", i))
			return
		}
		if tool.Transport == models.MCPTransportNative {
			respondError(w, http.StatusBadRequest, fmt.Sprintf("Tool '%s': a native tool is created only by the tool pack that runs it", tool.Name))
			return
		}
		tool.ID = uuid.New().String()
		tool.Kitchen = kitchen
		tool.Enabled = req.Tools[i].enabledOnCreate()
		tool.CreatedAt = now
		tool.UpdatedAt = now
		if tool.Transport == "" {
			tool.Transport = "http"
		}
		if len(tool.Capabilities) == 0 {
			tool.Capabilities = []string{"tool"}
		}
		if err := h.checkToolEndpoint(r.Context(), kitchen, tool.Transport, tool.Endpoint); err != nil {
			respondError(w, http.StatusBadRequest, fmt.Sprintf("Tool '%s' endpoint: %s", tool.Name, err.Error()))
			return
		}
		tools[i] = tool
	}

	created := make([]models.MCPTool, 0, len(tools))
	for i := range tools {
		tool := &tools[i]
		auth, sealed, err := h.sealToolAuth(r.Context(), kitchen, tool.Name, restoreMaskedAuth(nil, tool.AuthConfig))
		if err != nil {
			respondCreateError(w, err, "register the tool '"+tool.Name+"'")
			return
		}
		tool.AuthConfig = auth
		if err := h.Store.CreateTool(r.Context(), tool); err != nil {
			h.dropToolCredential(r.Context(), kitchen, sealed)
			respondCreateError(w, err, "register the tool '"+tool.Name+"'")
			return
		}
		created = append(created, *tool)
	}

	log.Info().Int("count", len(created)).Str("kitchen", kitchen).Msg("Bulk MCP tools registered")
	respondJSON(w, http.StatusCreated, map[string]interface{}{
		"created": len(created),
		"tools":   toolauth.RedactTools(created),
	})
}

func (h *Handlers) GetMCPTool(w http.ResponseWriter, r *http.Request) {
	toolName := chi.URLParam(r, "toolName")
	kitchen := middleware.GetKitchen(r.Context())

	tool, err := h.Store.GetTool(r.Context(), kitchen, toolName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, toolauth.RedactTool(*tool))
}

// validRegisteredTransport says whether a tool may be registered with this transport; empty
// means the default. The native transport is not one: a native tool is run by the control plane itself
// and only the edition that runs it (tool packs) creates one.
func validRegisteredTransport(t string) bool {
	return t == "" || t == "http" || t == "sse" || t == "stdio"
}

// refuseManagedTool answers 403 for a tool an edition manages (transport native: a tool pack's tool) and
// reports that it did. Such a tool's endpoint, transport and schema are what the edition's governance
// (its egress policy, SQL validation, pinned base URL, approvals) is built on, so they are changed only
// through that edition's own API; if the generic tools API could rewrite them, a role with ordinary tool
// permissions could point a governed tool at any endpoint through the plain HTTP path.
func refuseManagedTool(w http.ResponseWriter, tool *models.MCPTool) bool {
	if tool.Transport != models.MCPTransportNative {
		return false
	}
	respondJSON(w, http.StatusForbidden, map[string]string{
		"error":   "tool_managed",
		"message": "This tool belongs to a tool pack and is changed only through the tool pack (its upgrade, secrets and tool switches), not through /tools.",
	})
	return true
}

func (h *Handlers) DeleteMCPTool(w http.ResponseWriter, r *http.Request) {
	toolName := chi.URLParam(r, "toolName")
	kitchen := middleware.GetKitchen(r.Context())

	if tool, err := h.Store.GetTool(r.Context(), kitchen, toolName); err == nil && refuseManagedTool(w, tool) {
		return
	}
	if err := h.Store.DeleteTool(r.Context(), kitchen, toolName); err != nil {
		respondCreateError(w, err, "delete the tool")
		return
	}
	// The credential this code sealed the tool's secret into goes with it.
	if h.SecretResolver != nil && h.keyVault() != nil {
		_ = h.Store.DeleteKitchenCredential(r.Context(), kitchen, toolCredentialName(toolName))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) UpdateMCPTool(w http.ResponseWriter, r *http.Request) {
	toolName := chi.URLParam(r, "toolName")
	kitchen := middleware.GetKitchen(r.Context())

	tool, err := h.Store.GetTool(r.Context(), kitchen, toolName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if refuseManagedTool(w, tool) {
		return
	}

	var body toolBody
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err != nil || body.decode(raw) != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	req := body.MCPTool

	if req.Transport == models.MCPTransportNative {
		respondError(w, http.StatusBadRequest, "Invalid transport: a native tool is created only by the tool pack that runs it")
		return
	}
	if req.Description != "" {
		tool.Description = req.Description
	}
	if req.Endpoint != "" {
		tool.Endpoint = req.Endpoint
	}
	if req.Transport != "" {
		tool.Transport = req.Transport
	}
	if req.Schema != nil {
		tool.Schema = req.Schema
	}
	var sealed string
	if req.AuthConfig != nil {
		// What GET returned (secrets as ****) written back keeps the secret that is stored.
		auth, created, err := h.sealToolAuth(r.Context(), kitchen, toolName, restoreMaskedAuth(tool.AuthConfig, req.AuthConfig))
		if err != nil {
			respondCreateError(w, err, "update the tool")
			return
		}
		tool.AuthConfig, sealed = auth, created
	}
	if len(req.Capabilities) > 0 {
		tool.Capabilities = req.Capabilities
	}
	// enabled is applied when the body says it (false is meaningful), not defaulted to false by its absence.
	if body.EnabledSet != nil {
		tool.Enabled = *body.EnabledSet
	}
	tool.UpdatedAt = time.Now().UTC()
	if err := h.checkToolEndpoint(r.Context(), kitchen, tool.Transport, tool.Endpoint); err != nil {
		h.dropToolCredential(r.Context(), kitchen, sealed)
		respondError(w, http.StatusBadRequest, "endpoint: "+err.Error())
		return
	}

	if err := h.Store.UpdateTool(r.Context(), tool); err != nil {
		h.dropToolCredential(r.Context(), kitchen, sealed)
		respondCreateError(w, err, "update the tool")
		return
	}

	log.Info().Str("tool", toolName).Str("kitchen", kitchen).Msg("MCP tool updated")
	respondJSON(w, http.StatusOK, toolauth.RedactTool(*tool))
}

// ══════════════════════════════════════════════════════════════
// ── Trace Handlers ───────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) GetTrace(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	traceID := chi.URLParam(r, "traceId")

	// If ?spans=true, return trace with its spans attached for waterfall visualization
	includeSpans := r.URL.Query().Get("spans") == "true"
	var trace *models.Trace
	var err error
	if includeSpans {
		trace, err = h.Store.GetTraceWithSpans(r.Context(), traceID)
	} else {
		trace, err = h.Store.GetTrace(r.Context(), traceID)
	}
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if trace.Kitchen != "" && trace.Kitchen != kitchen {
		respondError(w, http.StatusNotFound, "trace not found")
		return
	}
	if scores, err := h.Store.ListTraceScores(r.Context(), traceID); err == nil {
		trace.Scores = scores
	}
	respondJSON(w, http.StatusOK, h.traceOut(r, trace))
}

// ListSpans returns all spans for a given trace, ordered by start_time.
// GET /api/v1/traces/{traceId}/spans
func (h *Handlers) ListSpans(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	traceID := chi.URLParam(r, "traceId")

	// Verify trace exists
	trace, err := h.Store.GetTrace(r.Context(), traceID)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if trace.Kitchen != "" && trace.Kitchen != kitchen {
		respondError(w, http.StatusNotFound, "trace not found")
		return
	}

	spans, err := h.Store.ListSpansByTrace(r.Context(), traceID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if spans == nil {
		spans = []models.Span{}
	}
	respondJSON(w, http.StatusOK, h.spansOut(r, spans))
}

// GetSpan returns a single span by ID.
// GET /api/v1/spans/{spanId}
func (h *Handlers) GetSpan(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	spanID := chi.URLParam(r, "spanId")
	span, err := h.Store.GetSpan(r.Context(), spanID)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	trace, err := h.Store.GetTrace(r.Context(), span.TraceID)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if trace.Kitchen != "" && trace.Kitchen != kitchen {
		respondError(w, http.StatusNotFound, "span not found")
		return
	}
	respondJSON(w, http.StatusOK, h.spanOut(r, span))
}

// ══════════════════════════════════════════════════════════════
// ── Kitchen Handlers ─────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) ListKitchens(w http.ResponseWriter, r *http.Request) {
	kitchens, err := h.Store.ListKitchens(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if kitchens == nil {
		kitchens = []models.Kitchen{}
	}
	respondJSON(w, http.StatusOK, kitchens)
}

func (h *Handlers) CreateKitchen(w http.ResponseWriter, r *http.Request) {
	var req models.Kitchen
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	req.ID = uuid.New().String()
	req.CreatedAt = time.Now().UTC()

	if err := h.Store.CreateKitchen(r.Context(), &req); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("kitchen", req.Name).Str("id", req.ID).Msg("Kitchen created")
	respondJSON(w, http.StatusCreated, req)
}

func (h *Handlers) GetKitchen(w http.ResponseWriter, r *http.Request) {
	kitchenID := chi.URLParam(r, "kitchenId")
	kitchen, err := h.Store.GetKitchen(r.Context(), kitchenID)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, kitchen)
}

func (h *Handlers) DeleteKitchen(w http.ResponseWriter, r *http.Request) {
	kitchenID := chi.URLParam(r, "kitchenId")

	// Prevent deleting the default kitchen
	if kitchenID == "default" {
		respondError(w, http.StatusForbidden, "Cannot delete the default kitchen")
		return
	}

	if err := h.Store.DeleteKitchen(r.Context(), kitchenID); err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	log.Info().Str("kitchen", kitchenID).Msg("Kitchen deleted")
	w.WriteHeader(http.StatusNoContent)
}

// ══════════════════════════════════════════════════════════════
// ── A2A Gateway Handlers ─────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) A2AEndpoint(w http.ResponseWriter, r *http.Request) {
	limitA2ABody(w, r)
	var rpcReq struct {
		Jsonrpc string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
		ID      interface{}     `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32700,
				"message": "Parse error",
				"data":    err.Error(),
			},
			"id": nil,
		})
		return
	}

	log.Info().Str("method", rpcReq.Method).Msg("A2A JSON-RPC request received")

	switch rpcReq.Method {
	case "tasks/send":
		h.handleA2ATaskSend(w, r, rpcReq.Params, rpcReq.ID)
	case "tasks/get":
		h.handleA2ATaskGet(w, r, rpcReq.Params, rpcReq.ID)
	case "tasks/cancel":
		h.handleA2ATaskCancel(w, r, rpcReq.Params, rpcReq.ID)
	default:
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32601,
				"message": "Method not found",
				"data":    "Method '" + rpcReq.Method + "' is not yet implemented in the gateway",
			},
			"id": rpcReq.ID,
		})
	}
}

func (h *Handlers) handleA2ATaskSend(w http.ResponseWriter, r *http.Request, params json.RawMessage, rpcID interface{}) {
	var taskReq struct {
		ID      string `json:"id"`
		Message struct {
			Role  string `json:"role"`
			Parts []struct {
				Type string          `json:"type"`
				Kind string          `json:"kind"` // later A2A revisions name the part type "kind"
				Text string          `json:"text"`
				File *a2aFilePart    `json:"file,omitempty"`
				Data json.RawMessage `json:"data,omitempty"`
			} `json:"parts"`
		} `json:"message"`
		ProviderConfig struct {
			Name          string `json:"name"`
			Kind          string `json:"kind"`
			CABundle      string `json:"ca_bundle"`
			TLSSkipVerify bool   `json:"tls_skip_verify"`
		} `json:"provider_config"`
		// Optional: specify which agent to invoke
		Metadata struct {
			AgentName string `json:"agent_name"`
		} `json:"metadata"`
		// configuration.blocking=false asks for the task back at once, not its answer.
		Configuration struct {
			Blocking *bool `json:"blocking"`
			// VoiceOutput asks for the reply spoken as well: it comes back as an audio FilePart
			// artifact (cascaded TTS on the agent's own provider). Voice picks the TTS voice.
			VoiceOutput bool   `json:"voice_output"`
			Voice       string `json:"voice"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(params, &taskReq); err != nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error":   map[string]interface{}{"code": -32602, "message": "Invalid params"},
			"id":      rpcID,
		})
		return
	}

	kitchen := middleware.GetKitchen(r.Context())

	// Only the platform chooses how a provider is connected to (see providerOverrideAllowed).
	if strings.TrimSpace(taskReq.ProviderConfig.Name) != "" && !providerOverrideAllowed(r) {
		log.Warn().Str("provider", taskReq.ProviderConfig.Name).Msg("A2A tasks/send: ignored a provider TLS override from a caller that is not the platform")
		taskReq.ProviderConfig.Name, taskReq.ProviderConfig.CABundle, taskReq.ProviderConfig.TLSSkipVerify = "", "", false
	}

	// Extract text, file, and data parts. Text goes to the prompt; files become
	// media content parts; structured data is sent as its JSON text so it is
	// never silently dropped.
	var userMessage string
	var textParts []string // the text parts in order, assembled by a2aJoinText as the pod assembles them
	var userParts []models.ContentPart
	var fileGuardText string // what the file parts say to a guardrail (a2a_parts.go)
	for _, part := range taskReq.Message.Parts {
		ptype := part.Type
		if ptype == "" {
			ptype = part.Kind
		}
		switch ptype {
		case "text", "":
			textParts = append(textParts, part.Text)
		case "file":
			if part.File == nil || (part.File.Bytes == "" && part.File.URI == "" && part.File.BlobRef == "") {
				a2aRPCError(w, rpcID, -32602, "A file part needs file.bytes (base64), file.uri or file.blob_ref (from POST /api/v1/blobs)", nil)
				return
			}
			if part.File.MimeType == "" {
				a2aRPCError(w, rpcID, -32602, "A file part needs file.mimeType", nil)
				return
			}
			userParts = append(userParts, a2aFileToContentPart(part.File))
			fileGuardText = joinNonEmpty([]string{fileGuardText, fileText(map[string]interface{}{
				"name": part.File.Name, "uri": part.File.URI, "mimeType": part.File.MimeType, "bytes": part.File.Bytes})})
		case "data":
			if len(part.Data) > 0 {
				textParts = append(textParts, "\n"+string(part.Data))
			}
		default:
			// A part the gateway does not understand is refused, not dropped: the agent
			// would otherwise answer a different question than the one asked.
			a2aRPCError(w, rpcID, -32602, "Unsupported message part type \""+ptype+"\"; use text, file or data", nil)
			return
		}
	}

	if len(textParts) > maxA2ATextParts {
		a2aRPCError(w, rpcID, -32602, "A message may have at most 256 text parts", nil)
		return
	}
	userMessage = a2aJoinText(textParts)

	taskID := taskReq.ID
	if taskID == "" {
		taskID = uuid.New().String()
	}

	if strings.TrimSpace(taskReq.ProviderConfig.Name) != "" {
		log.Info().
			Str("task_id", taskID).
			Str("provider", taskReq.ProviderConfig.Name).
			Str("kind", taskReq.ProviderConfig.Kind).
			Bool("has_ca_bundle", taskReq.ProviderConfig.CABundle != "").
			Int("ca_bundle_len", len(taskReq.ProviderConfig.CABundle)).
			Bool("tls_skip_verify", taskReq.ProviderConfig.TLSSkipVerify).
			Msg("A2A tasks/send received provider TLS configuration")
	}

	// If an agent is specified, try to invoke it
	agentName := taskReq.Metadata.AgentName
	// Fall back to the X-AgentOven-Agent header (set by proxyA2ARequest) so
	// agent pods running the control plane binary can identify themselves.
	if agentName == "" {
		agentName = strings.TrimSpace(r.Header.Get("X-AgentOven-Agent"))
	}
	// Final fallback: if this binary is running as an agent pod, use its own name.
	if agentName == "" {
		agentName = strings.TrimSpace(os.Getenv("AGENT_NAME"))
	}
	targetKitchen := kitchen
	// Cross-kitchen A2A: "otherkitchen/agentname" format (ADR-0014 / ADR-0007)
	if idx := strings.Index(agentName, "/"); idx > 0 {
		targetKitchen = agentName[:idx]
		agentName = agentName[idx+1:]
	}
	// Cross-kitchen authorization check (ADR-0016): when the target kitchen differs
	// from the caller's kitchen, verify an active CrossKitchenGrant exists.
	if targetKitchen != kitchen {
		allowed, checkErr := h.Store.CheckCrossKitchenAccess(r.Context(), kitchen, targetKitchen, agentName)
		if checkErr != nil || !allowed {
			w.Header().Set("Content-Type", "application/a2a+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"error": map[string]interface{}{
					"code":    -32004,
					"message": "Cross-kitchen access denied — no active grant",
					"data":    targetKitchen + "/" + agentName,
				},
				"id": rpcID,
			})
			return
		}
	}
	if agentName != "" && h.Executor != nil {
		agent, err := h.Store.GetAgent(r.Context(), targetKitchen, agentName)
		if err != nil {
			w.Header().Set("Content-Type", "application/a2a+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"error": map[string]interface{}{
					"code":    -32001,
					"message": "Agent not found",
					"data":    agentName,
				},
				"id": rpcID,
			})
			return
		}

		if agent.Status != models.AgentStatusReady {
			w.Header().Set("Content-Type", "application/a2a+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"error": map[string]interface{}{
					"code":    -32002,
					"message": "Agent not ready",
					"data":    string(agent.Status),
				},
				"id": rpcID,
			})
			return
		}

		// Use the config pinned at bake time, as invoke does, and resolve live only for an
		// agent that was never baked.
		resolved := agent.ResolvedConfig
		if resolved == nil {
			resolved, err = h.Resolver.Resolve(r.Context(), agent)
		}
		if err != nil {
			w.Header().Set("Content-Type", "application/a2a+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"error": map[string]interface{}{
					"code":    -32003,
					"message": "Ingredient resolution failed",
					"data":    err.Error(),
				},
				"id": rpcID,
			})
			return
		}

		// Speech parts are transcribed by the agent's own provider, so this waits
		// until the agent is known.
		if len(userParts) > 0 {
			transcript, rest, err := h.transcribeAudioParts(r.Context(), agent, userParts)
			if err != nil {
				w.Header().Set("Content-Type", "application/a2a+json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"error":   map[string]interface{}{"code": -32602, "message": err.Error()},
					"id":      rpcID,
				})
				return
			}
			if transcript != "" {
				userMessage = strings.TrimSpace(userMessage + " " + transcript)
			}
			userParts = rest
		}
		if msg, ok := h.checkBlobRefs(r, kitchen, userParts); !ok {
			a2aRPCError(w, rpcID, -32602, msg, nil)
			return
		}
		var speaker audio.Speaker
		if taskReq.Configuration.VoiceOutput {
			if taskReq.Configuration.Blocking != nil && !*taskReq.Configuration.Blocking {
				a2aRPCError(w, rpcID, -32602, "voice_output is only available on a blocking task", nil)
				return
			}
			// Ask for the speech engine now, so an agent that cannot speak is refused before it runs.
			eng, engErr := h.speechEngineFor(r.Context(), agent)
			if engErr != nil {
				a2aRPCError(w, rpcID, -32602, engErr.Error(), nil)
				return
			}
			speaker = eng
		}

		// Guardrails: the workspace rules on top of the agent's own, on what comes in and
		// (below) on what goes out — the same whether the agent runs in-process or in a
		// separately deployed process. If they cannot be established the agent is not run.
		gr, grErr := h.guardrailsFor(r.Context(), agent, agent.Guardrails)
		if grErr != nil {
			log.Error().Err(grErr).Str("agent", agentName).Msg("refusing A2A task: workspace guardrails could not be applied")
			h.emitGuardrailAudit(r.Context(), agent.Kitchen, agentName, "policy", nil, grErr)
			a2aRPCError(w, rpcID, -32004, "The guardrail policy for this agent could not be applied, so the agent was not run", nil)
			return
		}
		var tally guardTally // what the rules redacted or warned about, recorded on the trace
		if h.Guardrails != nil && len(gr) > 0 && fileGuardText != "" {
			// A file's name, location and (for a text file) content reach the agent as well. They are judged on
			// their own and cannot be redacted in place, so a rule that would change them refuses the message.
			if eval, gErr := h.Guardrails.EvaluateInput(r.Context(), gr, fileGuardText); gErr != nil {
				log.Warn().Err(gErr).Str("agent", agentName).Msg("Input guardrail evaluation error (A2A file parts)")
				h.emitGuardrailAudit(r.Context(), agent.Kitchen, agentName, "input", nil, gErr)
			} else if !eval.Passed || eval.Redacted > 0 {
				h.emitGuardrailAudit(r.Context(), agent.Kitchen, agentName, "input", eval, nil)
				a2aRPCError(w, rpcID, -32001, "Input blocked by guardrails", eval.Results)
				return
			}
		}
		if eval := h.firstBlockedPart(r.Context(), gr, textParts); eval != nil {
			h.emitGuardrailAudit(r.Context(), agent.Kitchen, agentName, "input", eval, nil)
			a2aRPCError(w, rpcID, -32001, "Input blocked by guardrails", eval.Results)
			return
		}
		if h.Guardrails != nil && len(gr) > 0 {
			if eval, gErr := h.Guardrails.EvaluateInput(r.Context(), gr, userMessage); gErr != nil {
				log.Warn().Err(gErr).Str("agent", agentName).Msg("Input guardrail evaluation error (A2A task)")
				h.emitGuardrailAudit(r.Context(), agent.Kitchen, agentName, "input", nil, gErr)
			} else if !eval.Passed {
				h.emitGuardrailAudit(r.Context(), agent.Kitchen, agentName, "input", eval, nil)
				a2aRPCError(w, rpcID, -32001, "Input blocked by guardrails", eval.Results)
				return
			} else {
				userMessage = eval.Apply(userMessage)
				h.noteGuardrails(r.Context(), agent.Kitchen, agentName, "input", eval)
				tally.add(eval)
			}
		}

		if taskReq.Configuration.Blocking != nil && !*taskReq.Configuration.Blocking {
			h.a2aSendAsync(w, r, rpcID, agent, resolved, gr, userMessage, userParts, kitchen)
			return
		}

		// Create initial trace immediately so tasks/get can find it
		initialTrace := &models.Trace{
			ID:           taskID,
			AgentName:    agentName,
			AgentVersion: agent.Version,
			Kitchen:      kitchen,
			Status:       "working",
			InputText:    userMessage,
			Metadata: map[string]interface{}{
				"source":  "a2a",
				"task_id": taskID,
			},
			CreatedAt: time.Now().UTC(),
		}
		h.Store.CreateTrace(r.Context(), initialTrace)

		// Execute agent synchronously — return full completed result in one response.
		// This avoids a separate tasks/get poll cycle and ensures tokens/output are
		// always propagated back to the caller (engine, dashboard, SDK, etc.).
		execCtx := r.Context()
		var providerTLS *router.ProviderTLSOverride
		if strings.TrimSpace(taskReq.ProviderConfig.Name) != "" {
			override := router.ProviderTLSOverride{
				ProviderName:  taskReq.ProviderConfig.Name,
				CABundle:      taskReq.ProviderConfig.CABundle,
				TLSSkipVerify: taskReq.ProviderConfig.TLSSkipVerify,
			}
			providerTLS = &override
			execCtx = router.WithProviderTLSOverride(execCtx, override)
			log.Info().
				Str("task_id", taskID).
				Str("provider", taskReq.ProviderConfig.Name).
				Msg("A2A execution context set with provider TLS override")
		}

		var response string
		var status string
		var failure string // why a failed task failed, so the caller is not left with a bare "failed"
		var mediaRefused bool
		var totalTokens int64
		var costUSD float64
		var usage *models.TokenUsage
		var execTrace *executor.ExecutionTrace

		if agent.IsFrameworkNative() {
			// Framework-native: proxy to the running process. It is sent text only, so a
			// file part is refused rather than dropped.
			if len(userParts) > 0 {
				a2aRPCError(w, rpcID, -32602, "This agent runs its own framework and takes text only; send files to an agent that runs on AgentOven's own loop", nil)
				return
			}
			resp, traceRecord, proxyErr := h.proxyToProcess(execCtx, agent, userMessage, nil, providerTLS, kitchen)
			if proxyErr != nil {
				status = "failed"
				failure = proxyErr.Error()
				log.Warn().Err(proxyErr).Str("agent", agentName).Str("task_id", taskID).Msg("A2A framework-native execution failed")
			} else {
				response = resp
				status = "completed"
				if traceRecord.Usage != nil {
					totalTokens = traceRecord.Usage.TotalTokens
					costUSD = traceRecord.CostUSD
					u := *traceRecord.Usage
					usage = &u
				}
			}
		} else {
			userMsg := models.ChatMessage{Role: "user", Content: userMessage, ContentParts: userParts}
			execCtx, _ = withRunTrace(execCtx)
			resp, et, execErr := h.Executor.ExecuteMessage(execCtx, agent, userMsg, resolved, nil, false)
			response = resp
			execTrace = et
			if execErr != nil {
				status = "failed"
				failure = execErr.Error()
				log.Warn().Err(execErr).Str("agent", agentName).Str("task_id", taskID).Msg("A2A agent execution failed")
				mediaRefused = router.IsMediaError(execErr)
			} else {
				status = "completed"
				if execTrace != nil {
					costUSD = execTrace.Usage.EstimatedCost
					totalTokens = execTrace.Usage.TotalTokens
					traceUsage := execTrace.Usage
					usage = &traceUsage
				}
			}
		}

		var outputBlocked *models.GuardrailEvaluation
		if status == "completed" && response != "" && h.Guardrails != nil && len(gr) > 0 && (execTrace == nil || !execTrace.OutputJudged) {
			if eval, gErr := h.Guardrails.EvaluateOutput(r.Context(), gr, response); gErr != nil {
				log.Warn().Err(gErr).Str("agent", agentName).Msg("Output guardrail evaluation error (A2A task); the reply is withheld")
				h.emitGuardrailAudit(r.Context(), agent.Kitchen, agentName, "output", nil, gErr)
				status = "blocked"
				response = "" // a reply that could not be judged is never stored or returned
			} else if !eval.Passed {
				h.emitGuardrailAudit(r.Context(), agent.Kitchen, agentName, "output", eval, nil)
				outputBlocked = eval
				status = "blocked"
				response = "" // never stored or returned
			} else {
				original := response
				response = eval.Apply(response)
				redactedTurn(execTrace, original, response)
				h.noteGuardrails(r.Context(), agent.Kitchen, agentName, "output", eval)
				tally.add(eval)
			}
		}

		trace := &models.Trace{
			ID:           taskID,
			AgentName:    agentName,
			AgentVersion: agent.Version,
			Kitchen:      kitchen,
			Status:       status,
			TotalTokens:  totalTokens,
			CostUSD:      costUSD,
			InputText:    userMessage,
			OutputText:   response,
			Usage:        usage,
			Metadata: map[string]interface{}{
				"source":  "a2a",
				"task_id": taskID,
			},
			CreatedAt: initialTrace.CreatedAt,
		}
		if failure != "" {
			trace.Metadata["error"] = failure
		}
		tally.addRun(execTrace)
		withGuardrailMeta(trace.Metadata, tally)
		if execTrace != nil {
			thinkingBlocks, thinkingTokens := summarizeThinking(execTrace)
			if len(thinkingBlocks) > 0 {
				trace.Metadata["thinking_blocks"] = thinkingBlocks
			}
			if thinkingTokens > 0 {
				trace.Metadata["thinking_tokens"] = thinkingTokens
			}
		}
		h.Store.CreateTrace(execCtx, trace)

		// Persist executor spans for waterfall visualization
		if execTrace != nil {
			h.persistExecutorSpans(execCtx, execTrace)
		}

		if outputBlocked != nil {
			a2aRPCError(w, rpcID, -32001, "Output blocked by guardrails", outputBlocked.Results)
			return
		}
		if mediaRefused {
			// The caller sent something the model cannot take: an invalid request, not a failed
			// run, and the reason is the answer.
			a2aRPCError(w, rpcID, -32602, failure, nil)
			return
		}

		// Build result with artifacts and usage so callers get the full response
		// in one round-trip — no poll cycle needed.
		taskMeta := map[string]interface{}{
			"agent_name": agentName,
			"kitchen":    kitchen,
		}
		// Embed the executor trace so the caller (engine / pro control plane) can
		// persist spans directly into Postgres rather than relying on the pod's
		// in-memory store (which is not visible to the caller).
		if execTrace != nil {
			if traceJSON, err := json.Marshal(execTrace); err == nil {
				taskMeta["exec_trace"] = json.RawMessage(traceJSON)
			}
		}
		taskStatus := map[string]interface{}{"state": status}
		if status == "failed" && failure != "" {
			taskStatus["message"] = map[string]interface{}{
				"role":  "agent",
				"parts": []map[string]string{{"type": "text", "text": failure}},
			}
		}
		taskResult := map[string]interface{}{
			"id":       taskID,
			"status":   taskStatus,
			"metadata": taskMeta,
		}
		if status == "completed" && response != "" {
			parts := []map[string]interface{}{{"type": "text", "text": response}}
			if speaker != nil {
				// The reply spoken, as a FilePart: the same cascaded TTS /invoke uses.
				speech, mime, sErr := speaker.Speak(r.Context(), response, taskReq.Configuration.Voice)
				if sErr != nil {
					a2aRPCError(w, rpcID, -32603, "could not synthesize speech: "+sErr.Error(), nil)
					return
				}
				parts = append(parts, map[string]interface{}{
					"type": "file", "kind": "file",
					"file": map[string]interface{}{
						"name": "reply" + audioExt(mime), "mimeType": mime,
						"bytes": base64.StdEncoding.EncodeToString(speech),
					},
				})
			}
			taskResult["artifacts"] = []map[string]interface{}{{"parts": parts}}
		}
		if totalTokens > 0 || costUSD > 0 {
			taskResult["usage"] = map[string]interface{}{
				"total_tokens": totalTokens,
				"cost_usd":     costUSD,
			}
		}

		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"result":  taskResult,
			"id":      rpcID,
		})
		return
	}

	// No specific agent — record trace and return submitted
	trace := &models.Trace{
		ID:        taskID,
		AgentName: "a2a-gateway",
		Kitchen:   kitchen,
		Status:    "submitted",
		InputText: userMessage,
		Metadata: map[string]interface{}{
			"source": "a2a",
		},
		CreatedAt: time.Now().UTC(),
	}
	h.Store.CreateTrace(r.Context(), trace)

	w.Header().Set("Content-Type", "application/a2a+json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"result": map[string]interface{}{
			"id":     taskID,
			"status": map[string]string{"state": "submitted"},
		},
		"id": rpcID,
	})
}

func (h *Handlers) handleA2ATaskGet(w http.ResponseWriter, r *http.Request, params json.RawMessage, rpcID interface{}) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &req); err != nil || req.ID == "" {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32602,
				"message": "Invalid params",
				"data":    "task id is required",
			},
			"id": rpcID,
		})
		return
	}

	// A background task is read from the task manager (which checks the kitchen).
	if h.a2aTaskFromManager(w, r, req.ID, rpcID) {
		return
	}

	// Look up actual task state from the trace store. A task is only visible to the kitchen
	// it was created in: a task ID is not a credential.
	trace, err := h.Store.GetTrace(r.Context(), req.ID)
	if err == nil && trace != nil && trace.Kitchen != middleware.GetKitchen(r.Context()) {
		trace = nil
	}
	if err != nil || trace == nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32001,
				"message": "Task not found",
				"data":    req.ID,
			},
			"id": rpcID,
		})
		return
	}

	// Map trace status to A2A task state
	state := trace.Status
	switch state {
	case "completed", "failed", "submitted", "working", "canceled":
		// valid A2A states, pass through
	case "error":
		state = "failed"
	default:
		state = "working"
	}

	result := map[string]interface{}{
		"id":     trace.ID,
		"status": map[string]string{"state": state},
	}
	if trace.AgentName != "" {
		result["metadata"] = map[string]string{
			"agent_name": trace.AgentName,
			"kitchen":    trace.Kitchen,
		}
	}
	if state == "completed" && trace.OutputText != "" {
		result["artifacts"] = []map[string]interface{}{
			{
				"parts": []map[string]string{
					{"type": "text", "text": trace.OutputText},
				},
			},
		}
	}
	if trace.TotalTokens > 0 || trace.CostUSD > 0 {
		result["usage"] = map[string]interface{}{
			"total_tokens": trace.TotalTokens,
			"cost_usd":     trace.CostUSD,
		}
	}

	w.Header().Set("Content-Type", "application/a2a+json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"result":  result,
		"id":      rpcID,
	})
}

func (h *Handlers) ServeAgentCard(w http.ResponseWriter, r *http.Request) {
	card := map[string]interface{}{
		"name":        "AgentOven Gateway",
		"description": "AgentOven control plane — multi-agent orchestration gateway",
		"version":     "0.2.0",
		"provider": map[string]string{
			"organization": "AgentOven",
			"url":          "https://agentoven.dev",
		},
		"supportedInterfaces": []map[string]string{
			{
				"url":             r.Host + "/a2a",
				"protocolBinding": "jsonrpc-http",
				"protocolVersion": "1.0",
			},
		},
		"capabilities": map[string]bool{
			"streaming":         true,
			"pushNotifications": true,
		},
	}
	w.Header().Set("Content-Type", "application/a2a+json")
	json.NewEncoder(w).Encode(card)
}

func (h *Handlers) A2AAgentEndpoint(w http.ResponseWriter, r *http.Request) {
	limitA2ABody(w, r)
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())
	envSlug := strings.TrimSpace(r.Header.Get("X-AO-Environment"))

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32001,
				"message": "Agent not found",
				"data":    "Agent '" + agentName + "' is not registered",
			},
			"id": nil,
		})
		return
	}

	if agent.Status != models.AgentStatusReady {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32002,
				"message": "Agent not ready",
				"data":    "Agent '" + agentName + "' status is " + string(agent.Status),
			},
			"id": nil,
		})
		return
	}

	// If the caller provided an environment context, prefer the env-scoped
	// backend resolution path. Recipe runs already forward X-AO-Environment,
	// and env-backed agents may not have any usable global backend at all.
	if envSlug != "" {
		if h.AgentEnvStore != nil {
			ae, err := h.AgentEnvStore.GetAgentEnvironment(r.Context(), kitchen, agentName, envSlug)
			if err == nil {
				if ae.Status != models.AgentEnvStatusReady {
					w.Header().Set("Content-Type", "application/a2a+json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0",
						"error": map[string]interface{}{
							"code":    -32002,
							"message": "Environment not ready",
							"data":    fmt.Sprintf("Agent '%s' in environment '%s' is %s", agentName, envSlug, ae.Status),
						},
						"id": nil,
					})
					return
				}

				log.Info().Str("agent", agentName).Str("env", envSlug).
					Str("backend", sanitizeURLForLog(ae.BackendEndpoint)).
					Msg("Proxying env-scoped A2A request via stable gateway")

				h.emitEnvA2AAudit(r.Context(), kitchen, agentName, envSlug)
				h.proxyA2AGuarded(w, r, ae.BackendEndpoint, agent, h.resolveEnvGuardrails(agent.Guardrails, ae), envSlug)
				return
			}
		}

		if agent.Status != models.AgentStatusReady {
			w.Header().Set("Content-Type", "application/a2a+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"error": map[string]interface{}{
					"code":    -32002,
					"message": "Agent not ready",
					"data":    "Agent '" + agentName + "' status is " + string(agent.Status),
				},
				"id": nil,
			})
			return
		}

		backendURL, backendReason := h.resolveEnvBackendEndpoint(r.Context(), agent, envSlug)
		if backendURL == "" {
			w.Header().Set("Content-Type", "application/a2a+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"error": map[string]interface{}{
					"code":    -32003,
					"message": "No backend available",
					"data": map[string]interface{}{
						"message":        "Agent has no backend for environment '" + envSlug + "'",
						"backend_reason": backendReason,
						"agent":          agentName,
						"environment":    envSlug,
					},
				},
				"id": nil,
			})
			return
		}

		log.Info().
			Str("agent", agentName).
			Str("env", envSlug).
			Str("backend", sanitizeURLForLog(backendURL)).
			Str("reason", backendReason).
			Msg("Proxying env-scoped A2A request via stable gateway")

		h.proxyA2AGuarded(w, r, backendURL, agent, agent.Guardrails, envSlug)
		return
	}

	// ── Resolve the backend endpoint ─────────────────────────
	// ADR-0007: The control plane is the A2A gateway. Clients always call
	// /agents/{name}/a2a (stable URL). The control plane proxies to:
	//   - Managed agents: ProcessInfo.Endpoint (local/docker/k8s subprocess)
	//   - External agents: BackendEndpoint (user-provided URL)
	// This ensures auth, RBAC, observability, and rate-limiting for every call.
	backendURL, backendReason := h.liveBackend(r.Context(), agent)
	if backendURL == "" {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32003,
				"message": "No backend available",
				"data": map[string]interface{}{
					"message":        "Agent has no running process or backend endpoint configured",
					"backend_reason": backendReason,
					"agent":          agentName,
					"mode":           string(agent.Mode),
					"status":         string(agent.Status),
				},
			},
			"id": nil,
		})
		return
	}

	log.Info().
		Str("agent", agentName).
		Str("backend", sanitizeURLForLog(backendURL)).
		Str("mode", string(agent.Mode)).
		Str("reason", backendReason).
		Msg("Proxying A2A request to agent backend")

	// ── Proxy the request ───────────────────────────────────
	h.proxyA2AGuarded(w, r, backendURL, agent, agent.Guardrails, "")
}

// ResolveBackendEndpoint determines where to proxy A2A calls for an agent.
// For managed agents, it returns the process endpoint (subprocess/docker/k8s).
// For external agents, it returns the user-provided backend URL.
func (h *Handlers) ResolveBackendEndpoint(agent *models.Agent) (string, string) {
	// K8s-deployed agents: operator writes backend_endpoint when workload Running (ADR-0014)
	if agent.BackendEndpoint != "" {
		return agent.BackendEndpoint, "agent_backend_endpoint"
	}

	// Managed agents: use the spawned process endpoint
	if agent.IsManaged() {
		if agent.Process != nil && agent.Process.Status == models.ProcessRunning {
			if agent.Process.Endpoint != "" {
				return agent.Process.Endpoint, "managed_process_endpoint"
			}
		}

		// Fallback for k8s mode during endpoint propagation lag.
		if agent.ExecutionMode == models.ExecModeK8s ||
			(agent.Process != nil && agent.Process.Mode == models.ExecModeK8s) {
			namespace := os.Getenv("AGENTOVEN_K8S_NAMESPACE")
			if namespace == "" {
				namespace = "agentoven"
			}
			svcName := fmt.Sprintf("agent-%s-%s-svc", agent.Kitchen, agent.Name)
			return fmt.Sprintf("http://%s.%s.svc.cluster.local:9000", svcName, namespace), "k8s_service_dns_fallback"
		}

		return "", "managed_process_not_running"
	}

	// External agents: use the configured backend endpoint
	if agent.Mode == models.AgentModeExternal {
		if agent.A2AEndpoint != "" {
			return strings.TrimRight(agent.A2AEndpoint, "/"), "external_a2a_endpoint"
		}
		return "", "external_backend_endpoint_missing"
	}

	return "", "unsupported_agent_mode"
}

// A2AAgentEnvEndpoint handles POST /env/{envSlug}/agents/{agentName}/a2a.
// It resolves the backend endpoint scoped to the given environment's active
// K8s workload, falling back to the agent's global backend if no env workload exists.
func (h *Handlers) A2AAgentEnvEndpoint(w http.ResponseWriter, r *http.Request) {
	limitA2ABody(w, r)
	agentName := chi.URLParam(r, "agentName")
	envSlug := chi.URLParam(r, "envSlug")
	kitchen := middleware.GetKitchen(r.Context())

	a2aError := func(code int, msg, data string) {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error":   map[string]interface{}{"code": code, "message": msg, "data": data},
			"id":      nil,
		})
	}

	// ── Scoped key access check (agent + environment) ────────────────────
	if identity := pkgmw.GetIdentity(r.Context()); identity != nil && identity.Provider == "scoped-key" {
		keyID := identity.Claims["key_id"]
		scopedKey, err := h.Store.GetScopedKey(r.Context(), kitchen, keyID)
		if err != nil || !scopedKey.CanAccessAgent(agentName) {
			a2aError(-32001, "Forbidden", fmt.Sprintf("Scoped key does not have access to agent '%s'", agentName))
			return
		}
		if !scopedKey.CanAccessEnvironment(envSlug) {
			a2aError(-32001, "Forbidden", fmt.Sprintf("Scoped key does not have access to environment '%s'", envSlug))
			return
		}
	}

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		a2aError(-32001, "Agent not found", "Agent '"+agentName+"' is not registered")
		return
	}

	// ── Pro path: use AgentEnvironment record if available ───────────────
	if h.AgentEnvStore != nil {
		ae, err := h.AgentEnvStore.GetAgentEnvironment(r.Context(), kitchen, agentName, envSlug)
		if err != nil {
			a2aError(-32002, "Environment not found", fmt.Sprintf("Agent '%s' has no deployment in environment '%s'", agentName, envSlug))
			return
		}
		if ae.Status != models.AgentEnvStatusReady {
			a2aError(-32002, "Environment not ready", fmt.Sprintf("Agent '%s' in environment '%s' is %s", agentName, envSlug, ae.Status))
			return
		}

		log.Info().Str("agent", agentName).Str("env", envSlug).
			Str("backend", sanitizeURLForLog(ae.BackendEndpoint)).
			Msg("Proxying env-scoped A2A request (Pro)")

		// Emit audit event with environment field
		h.emitEnvA2AAudit(r.Context(), kitchen, agentName, envSlug)

		// Guardrails resolve per ae.GuardrailPolicy, with the workspace rules applied on top.
		h.proxyA2AGuarded(w, r, ae.BackendEndpoint, agent, h.resolveEnvGuardrails(agent.Guardrails, ae), envSlug)
		return
	}

	// ── OSS fallback path ─────────────────────────────────────────────────
	if agent.Status != models.AgentStatusReady {
		a2aError(-32002, "Agent not ready", "Agent '"+agentName+"' status is "+string(agent.Status))
		return
	}

	// Resolve env-specific backend: look for the A2A endpoint registered
	// for this environment slug on the agent record, then fall back to global.
	backendURL, backendReason := h.resolveEnvBackendEndpoint(r.Context(), agent, envSlug)
	if backendURL == "" {
		a2aError(-32003, "No backend available", "Agent has no backend for environment '"+envSlug+"'")
		return
	}

	log.Info().
		Str("agent", agentName).
		Str("env", envSlug).
		Str("backend", sanitizeURLForLog(backendURL)).
		Str("reason", backendReason).
		Msg("Proxying env-scoped A2A request to agent backend")

	h.proxyA2AGuarded(w, r, backendURL, agent, agent.Guardrails, envSlug)
}

// resolveEnvBackendEndpoint finds the backend URL for an agent in a specific
// environment. Checks agent.EnvEndpoints[envSlug] first (set by the operator
// when a K8s workload reaches Running in that env), then falls back to the
// global ResolveBackendEndpoint.
func (h *Handlers) resolveEnvBackendEndpoint(ctx context.Context, agent *models.Agent, envSlug string) (string, string) {
	// Env-specific endpoint map populated by the operator per-env workload
	if agent.EnvEndpoints != nil {
		if ep, ok := agent.EnvEndpoints[envSlug]; ok && ep != "" {
			return ep, "env_backend_endpoint"
		}
	}
	// Fall back to global backend (handles non-env baked agents gracefully)
	url, reason := h.liveBackend(ctx, agent)
	if url != "" {
		return url, "global_fallback_" + reason
	}
	return "", "no_backend_for_env_" + envSlug
}

func sanitizeURLForLog(raw string) string {
	if raw == "" {
		return raw
	}
	if i := strings.Index(raw, "?"); i >= 0 {
		return raw[:i] + "?<redacted>"
	}
	return raw
}

// resolveEnvGuardrails returns the effective guardrail list for an env-scoped
// request based on the AgentEnvironment.GuardrailPolicy field:
//   - "inherit"  → agent-level guardrails (default)
//   - "strict"   → agent-level + ae.RequiredGuardrails
//   - "relaxed"  → agent-level minus ae.DisabledGuardrails
//   - "disabled" → empty (no guardrails)
func (h *Handlers) resolveEnvGuardrails(agentGuardrails []models.Guardrail, ae *models.AgentEnvironment) []models.Guardrail {
	switch ae.GuardrailPolicy {
	case "disabled":
		return nil
	case "strict":
		merged := make([]models.Guardrail, len(agentGuardrails))
		copy(merged, agentGuardrails)
	outer:
		for _, reqID := range ae.RequiredGuardrails {
			for _, existing := range merged {
				if existing.ID == reqID {
					continue outer
				}
			}
			// Append a minimal synthetic guardrail for required IDs not already present.
			merged = append(merged, models.Guardrail{ID: reqID, Enabled: true})
		}
		return merged
	case "relaxed":
		disabled := make(map[string]bool, len(ae.DisabledGuardrails))
		for _, g := range ae.DisabledGuardrails {
			disabled[g] = true
		}
		var out []models.Guardrail
		for _, g := range agentGuardrails {
			if !disabled[g.ID] {
				out = append(out, g)
			}
		}
		return out
	default: // "inherit" or empty
		return agentGuardrails
	}
}

// auditActorFromIdentity resolves the acting identity for an audit event, returning the
// subject id, an email (only ever a real email address), a display name suitable for UI
// rendering, the identity provider, and a coarse actor type ("user", "service_account",
// "api_key"). Centralizing this avoids each call site re-deriving userEmail by falling back
// to DisplayName (which broke rendering for service-account names containing "@").
func auditActorFromIdentity(ctx context.Context) (userID, userEmail, displayName, provider, actorType string) {
	identity := pkgmw.GetIdentity(ctx)
	if identity == nil {
		return "", "", "", "", ""
	}
	userID = identity.Subject
	userEmail = identity.Email
	displayName = identity.DisplayName
	if displayName == "" {
		displayName = userEmail
	}
	provider = identity.Provider
	switch provider {
	case "service_account":
		actorType = "service_account"
	case "scoped-key", "apikey":
		actorType = "api_key"
	default:
		actorType = "user"
	}
	return userID, userEmail, displayName, provider, actorType
}

// emitGuardrailAuditEnv emits a guardrail audit event with environment context.
func (h *Handlers) emitGuardrailAuditEnv(ctx context.Context, kitchen, agentName, envSlug, stage string, eval *models.GuardrailEvaluation, guardrailErr error) {
	if h.Store == nil {
		return
	}
	userID, userEmail, displayName, provider, actorType := auditActorFromIdentity(ctx)
	action, status := guardrailOutcome(stage, eval)
	details := map[string]interface{}{"agent": agentName, "env": envSlug, "stage": stage, "provider": provider}
	if guardrailErr != nil {
		action = "guardrail." + stage + "_error"
		status = http.StatusInternalServerError
		details["error"] = guardrailErr.Error()
	}
	if eval != nil {
		details["results"] = eval.Results
		details["passed"] = eval.Passed
		details["redacted"], details["warned"] = eval.Redacted, eval.Warned
	}
	_ = h.Store.CreateAuditEvent(ctx, &models.AuditEvent{
		ID:                 uuid.New().String(),
		Timestamp:          time.Now().UTC(),
		UserID:             userID,
		UserEmail:          userEmail,
		ActorType:          actorType,
		ActorDisplayName:   displayName,
		Action:             action,
		Resource:           "guardrails",
		ResourceID:         agentName,
		Kitchen:            kitchen,
		Environment:        envSlug,
		Details:            details,
		ResponseStatus:     status,
		RegulationTags:     []string{"compliance", "guardrails"},
		DataClassification: "restricted",
	})
}

// emitEnvA2AAudit records an audit event for an env-scoped A2A proxy call.
func (h *Handlers) emitEnvA2AAudit(ctx context.Context, kitchen, agentName, envSlug string) {
	if h.Store == nil {
		return
	}
	userID, userEmail, displayName, _, actorType := auditActorFromIdentity(ctx)
	_ = h.Store.CreateAuditEvent(ctx, &models.AuditEvent{
		ID:                 uuid.New().String(),
		Timestamp:          time.Now().UTC(),
		UserID:             userID,
		UserEmail:          userEmail,
		ActorType:          actorType,
		ActorDisplayName:   displayName,
		Action:             "agent.a2a_env_proxy",
		Resource:           "agent",
		ResourceID:         agentName,
		Kitchen:            kitchen,
		Environment:        envSlug,
		Details:            map[string]interface{}{"agent": agentName, "env": envSlug},
		ResponseStatus:     http.StatusOK,
		RegulationTags:     []string{"access"},
		DataClassification: "internal",
	})
}

// backendHTTPClient is a shared HTTP client used for all agent-to-agent
// proxy calls. TLS verification is skipped by default because backend agents
// run as internal cluster workloads and typically use self-signed certificates.
// Set AGENTOVEN_BACKEND_TLS_VERIFY=true to opt back in to strict verification.
var backendHTTPClient = func() *http.Client {
	skipVerify := os.Getenv("AGENTOVEN_BACKEND_TLS_VERIFY") != "true"
	return &http.Client{
		Timeout: 5 * time.Minute,
		Transport: &http.Transport{
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: skipVerify}, // #nosec G402 — internal cluster traffic; opt-in strict via env var
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ResponseHeaderTimeout: 60 * time.Second,
		},
	}
}()

// proxyA2ARequest relays an HTTP request to a backend agent endpoint and
// streams the response back to the caller. This is the core of the
// control-plane-as-gateway pattern (ADR-0007).
// managed says the backend is a process this platform launched, which is the only kind that is
// given the pod token; an external agent's URL is somebody else's and gets nothing of ours.
func (h *Handlers) proxyA2ARequest(w http.ResponseWriter, r *http.Request, backendURL, agentName, kitchen string, managed bool) {
	// Read the original request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error":   map[string]interface{}{"code": -32700, "message": "Failed to read request body"},
			"id":      nil,
		})
		return
	}

	// A TLS override for the provider connection is the platform's to set; a pod must not be
	// told to skip verification because a caller asked.
	if !providerOverrideAllowed(r) {
		body, _ = stripProviderOverride(body)
	}

	// Use the backend URL as-is — it already contains the full path (e.g. /a2a).
	targetURL := strings.TrimRight(backendURL, "/")

	// Create the proxied request
	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error":   map[string]interface{}{"code": -32603, "message": "Failed to create proxy request: " + err.Error()},
			"id":      nil,
		})
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("X-AgentOven-Agent", agentName)
	if managed {
		h.signPodRequest(proxyReq, kitchen, agentName)
	}

	// Forward the request to the backend
	resp, err := backendHTTPClient.Do(proxyReq)
	if err != nil {
		log.Warn().Err(err).Str("agent", agentName).Str("backend", backendURL).Msg("Backend request failed")
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error":   map[string]interface{}{"code": -32603, "message": "Backend unreachable: " + err.Error()},
			"id":      nil,
		})
		return
	}
	defer resp.Body.Close()

	// Copy backend response headers and body back to the caller
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (h *Handlers) ServeAgentSpecificCard(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		card := map[string]interface{}{
			"name":        agentName,
			"description": "Agent managed by AgentOven (not found)",
			"supportedInterfaces": []map[string]string{
				{
					"url":             scheme + "://" + host + "/agents/" + agentName + "/a2a",
					"protocolBinding": "jsonrpc-http",
				},
			},
		}
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(card)
		return
	}

	skills := make([]map[string]interface{}, 0)
	for _, s := range agent.Skills {
		skills = append(skills, map[string]interface{}{
			"id":          s,
			"name":        s,
			"description": "Skill: " + s,
		})
	}

	card := map[string]interface{}{
		"name":        agent.Name,
		"description": agent.Description,
		"version":     agent.Version,
		"provider": map[string]string{
			"organization": "AgentOven",
			"url":          "https://agentoven.dev",
		},
		"supportedInterfaces": []map[string]string{
			{
				"url":             scheme + "://" + host + "/agents/" + agentName + "/a2a",
				"protocolBinding": "jsonrpc-http",
				"protocolVersion": "1.0",
			},
		},
		"capabilities": map[string]bool{
			"streaming":         true,
			"pushNotifications": true,
		},
		"skills": skills,
	}
	w.Header().Set("Content-Type", "application/a2a+json")
	json.NewEncoder(w).Encode(card)
}

// ══════════════════════════════════════════════════════════════
// ── Prompt Handlers ──────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) ListPrompts(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	prompts, err := h.Store.ListPrompts(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if prompts == nil {
		prompts = []models.Prompt{}
	}
	respondJSON(w, http.StatusOK, prompts)
}

func (h *Handlers) CreatePrompt(w http.ResponseWriter, r *http.Request) {
	var req models.Prompt
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Name == "" || req.Template == "" {
		respondError(w, http.StatusBadRequest, "name and template are required")
		return
	}

	kitchen := middleware.GetKitchen(r.Context())
	req.ID = uuid.New().String()
	req.Kitchen = kitchen
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()

	// Auto-extract variables from template
	req.Variables = resolver.ExtractVariables(req.Template)

	// Run prompt validation if auto-validate is enabled
	settings, _ := h.resolvedKitchenSettings(r.Context(), kitchen)
	var report *models.ValidationReport
	if settings != nil && settings.AutoValidate {
		var err error
		report, err = h.PromptValidator.Validate(r.Context(), &req, settings)
		if err != nil {
			log.Warn().Err(err).Str("prompt", req.Name).Msg("Validation failed")
		}
		// Block creation if there are validation errors
		if report != nil {
			for _, issue := range report.Issues {
				if issue.Severity == models.ValidationError {
					respondJSON(w, http.StatusBadRequest, map[string]interface{}{
						"error":  "Prompt validation failed",
						"report": report,
					})
					return
				}
			}
		}
	}

	if err := h.Store.CreatePrompt(r.Context(), &req); err != nil {
		respondCreateError(w, err, "create the prompt")
		return
	}

	log.Info().Str("prompt", req.Name).Int("version", req.Version).Str("kitchen", kitchen).Msg("Prompt created")

	response := map[string]interface{}{"prompt": req}
	if report != nil {
		response["validation"] = report
	}
	respondJSON(w, http.StatusCreated, response)
}

func (h *Handlers) GetPrompt(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())

	prompt, err := h.Store.GetPrompt(r.Context(), kitchen, promptName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, prompt)
}

func (h *Handlers) UpdatePrompt(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())

	existing, err := h.Store.GetPrompt(r.Context(), kitchen, promptName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	var req models.Prompt
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Template != "" {
		existing.Template = req.Template
		existing.Variables = resolver.ExtractVariables(req.Template)
	}
	if len(req.Tags) > 0 {
		existing.Tags = req.Tags
	}

	// Run prompt validation if auto-validate is enabled
	settings, _ := h.resolvedKitchenSettings(r.Context(), kitchen)
	var report *models.ValidationReport
	if settings != nil && settings.AutoValidate {
		var err error
		report, err = h.PromptValidator.Validate(r.Context(), existing, settings)
		if err != nil {
			log.Warn().Err(err).Str("prompt", promptName).Msg("Validation failed")
		}
		if report != nil {
			for _, issue := range report.Issues {
				if issue.Severity == models.ValidationError {
					respondJSON(w, http.StatusBadRequest, map[string]interface{}{
						"error":  "Prompt validation failed",
						"report": report,
					})
					return
				}
			}
		}
	}

	if err := h.Store.UpdatePrompt(r.Context(), existing); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Re-fetch to get the new version
	updated, _ := h.Store.GetPrompt(r.Context(), kitchen, promptName)
	if updated != nil {
		existing = updated
	}

	log.Info().Str("prompt", promptName).Int("version", existing.Version).Str("kitchen", kitchen).Msg("Prompt updated")

	response := map[string]interface{}{"prompt": existing}
	if report != nil {
		response["validation"] = report
	}
	respondJSON(w, http.StatusOK, response)
}

func (h *Handlers) DeletePrompt(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())

	if err := h.Store.DeletePrompt(r.Context(), kitchen, promptName); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("prompt", promptName).Str("kitchen", kitchen).Msg("Prompt deleted")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) ListPromptVersions(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())

	versions, err := h.Store.ListPromptVersions(r.Context(), kitchen, promptName)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if versions == nil {
		versions = []models.Prompt{}
	}
	respondJSON(w, http.StatusOK, versions)
}

func (h *Handlers) GetPromptVersion(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())
	versionStr := chi.URLParam(r, "version")

	version, err := strconv.Atoi(versionStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "version must be an integer")
		return
	}

	prompt, err := h.Store.GetPromptVersion(r.Context(), kitchen, promptName, version)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, prompt)
}

// ══════════════════════════════════════════════════════════════
// ── Agent Config Handler ─────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// GetAgentConfig returns the fully resolved ingredient configuration for an agent.
// This is the endpoint external agents call to fetch their runtime config.
// GET /api/v1/agents/{agentName}/config
func (h *Handlers) GetAgentConfig(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	resolved, err := h.Resolver.Resolve(r.Context(), agent)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "Ingredient resolution failed",
			"details": err.Error(),
			"partial": resolved.Redacted(),
		})
		return
	}

	// The provider key is the platform's to hold: callers see that one is set, not what it is.
	respondJSON(w, http.StatusOK, models.AgentConfig{
		Agent:       agent.Redacted(),
		Ingredients: *resolved.Redacted(),
	})
}

// GetAgentProcess returns the process status for a running agent.
// GET /api/v1/agents/{agentName}/process
func (h *Handlers) GetAgentProcess(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	if h.ProcessManager == nil {
		respondError(w, http.StatusServiceUnavailable, "Process manager not available")
		return
	}

	info, err := h.ProcessManager.Status(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"agent":   agentName,
			"status":  "no_process",
			"message": "No process tracked for this agent. Agent may be in external mode or not yet baked.",
		})
		return
	}

	respondJSON(w, http.StatusOK, info)
}

// ListProcesses returns all running agent processes.
// GET /api/v1/processes
func (h *Handlers) ListProcesses(w http.ResponseWriter, r *http.Request) {
	if h.ProcessManager == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	running := h.ProcessManager.ListRunning()
	if running == nil {
		running = []*models.ProcessInfo{}
	}
	respondJSON(w, http.StatusOK, running)
}

// StreamAgentLogs streams live agent process logs via Server-Sent Events.
// GET /api/v1/agents/{agentName}/logs
func (h *Handlers) StreamAgentLogs(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	if h.ProcessManager == nil {
		respondError(w, http.StatusServiceUnavailable, "process manager not available")
		return
	}

	logBuf := h.ProcessManager.GetLogBuffer(kitchen, agentName)

	if logBuf == nil {
		// K8s fallback: stream directly from pod logs when no local log buffer exists.
		entries, err := h.ProcessManager.GetK8sRecentLogs(r.Context(), kitchen, agentName, 200)
		if err != nil {
			respondError(w, http.StatusNotFound, fmt.Sprintf("no log source for agent '%s': %v", agentName, err))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		rc := http.NewResponseController(w)

		for _, entry := range entries {
			data, _ := json.Marshal(entry)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		if err := rc.Flush(); err != nil {
			respondError(w, http.StatusInternalServerError, "streaming not supported")
			return
		}

		ch, err := h.ProcessManager.SubscribeK8sLogs(r.Context(), kitchen, agentName, 100)
		if err != nil {
			return
		}
		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case entry, ok := <-ch:
				if !ok {
					return
				}
				data, _ := json.Marshal(entry)
				fmt.Fprintf(w, "data: %s\n\n", data)
				if err := rc.Flush(); err != nil {
					return
				}
			}
		}
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)

	// Send recent log history first
	recent := logBuf.Recent(200)
	for _, entry := range recent {
		data, _ := json.Marshal(entry)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	if err := rc.Flush(); err != nil {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Subscribe to live updates
	ch := logBuf.Subscribe()
	defer logBuf.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(entry)
			fmt.Fprintf(w, "data: %s\n\n", data)
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// GetAgentLogs returns the recent log history for an agent (non-streaming).
// GET /api/v1/agents/{agentName}/logs/recent
func (h *Handlers) GetAgentLogs(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	if h.ProcessManager == nil {
		respondError(w, http.StatusServiceUnavailable, "process manager not available")
		return
	}

	logBuf := h.ProcessManager.GetLogBuffer(kitchen, agentName)
	if logBuf == nil {
		entries, err := h.ProcessManager.GetK8sRecentLogs(r.Context(), kitchen, agentName, 500)
		if err != nil {
			respondError(w, http.StatusNotFound, fmt.Sprintf("no log source for agent '%s': %v", agentName, err))
			return
		}
		respondJSON(w, http.StatusOK, entries)
		return
	}

	entries := logBuf.Recent(500)
	if entries == nil {
		entries = []process.LogEntry{}
	}
	respondJSON(w, http.StatusOK, entries)
}

// ══════════════════════════════════════════════════════════════
// ── Framework-Native Proxy Helper ────────────────────────────
// ══════════════════════════════════════════════════════════════

// proxyInvokeResponse is the expected response shape from a framework-native
// agent's POST /invoke endpoint (as served by agentoven.runtime.AgentOvenServer).
type proxyInvokeResponse struct {
	Response string `json:"response"`
	Usage    struct {
		InputTokens    int64 `json:"input_tokens"`
		OutputTokens   int64 `json:"output_tokens"`
		TotalTokens    int64 `json:"total_tokens"`
		CachedTokens   int64 `json:"cached_tokens"`
		CacheCreation  int64 `json:"cache_creation"`
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"usage"`
	// Turns is what the runner did, call by call: the model, its usage, the reasoning it returned
	// and the tools it ran. Only the AgentOven runner sends it; a LangChain or other hosted
	// framework's process answers with the response and totals alone.
	Turns []proxyTurn `json:"turns,omitempty"`
}

type proxyTurn struct {
	Number    int    `json:"number"`
	Model     string `json:"model"`
	LatencyMs int64  `json:"latency_ms"`
	Usage     struct {
		InputTokens    int64 `json:"input_tokens"`
		OutputTokens   int64 `json:"output_tokens"`
		TotalTokens    int64 `json:"total_tokens"`
		CachedTokens   int64 `json:"cached_tokens"`
		CacheCreation  int64 `json:"cache_creation"`
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"usage"`
	Response       string `json:"response"`
	ThinkingBlocks []struct {
		Content    string `json:"content"`
		TokenCount int64  `json:"token_count"`
		Redacted   bool   `json:"redacted"`
	} `json:"thinking_blocks"`
	ToolCalls []struct {
		ID        string                 `json:"id"`
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	} `json:"tool_calls"`
	ToolResults []struct {
		ToolCallID string `json:"tool_call_id"`
		Name       string `json:"name"`
		Content    string `json:"content"`
		IsError    bool   `json:"is_error"`
	} `json:"tool_results"`
}

type thinkingKey struct{}

// withThinking marks a call to a hosted process as one that should have the model think.
func withThinking(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, thinkingKey{}, on)
}

func thinkingFrom(ctx context.Context) bool {
	on, _ := ctx.Value(thinkingKey{}).(bool)
	return on
}

// execTraceFromProxy turns the runner's per-turn record into the executor's trace shape, with every
// turn priced at the agent's provider's price for the model it used, so a hosted agent's trace has
// the same spans, thinking and cost as one run in-process.
func (h *Handlers) execTraceFromProxy(ctx context.Context, agent *models.Agent, traceID string, durationMs int64, resp *proxyInvokeResponse) *executor.ExecutionTrace {
	et := &executor.ExecutionTrace{TraceID: traceID, AgentName: agent.Name, Kitchen: agent.Kitchen, TotalMs: durationMs}
	for _, pt := range resp.Turns {
		model := pt.Model
		if model == "" {
			model = agent.ModelName
		}
		turn := executor.Turn{
			Number: pt.Number, Response: pt.Response, LatencyMs: pt.LatencyMs, Provider: agent.ModelProvider, Model: model,
			Usage: models.TokenUsage{
				InputTokens: pt.Usage.InputTokens, OutputTokens: pt.Usage.OutputTokens, TotalTokens: pt.Usage.TotalTokens,
				CachedTokens: pt.Usage.CachedTokens, CacheCreation: pt.Usage.CacheCreation, ThinkingTokens: pt.Usage.ThinkingTokens,
			},
		}
		if h.Router != nil {
			h.Router.Settle(ctx, agent.Kitchen, agent.ModelProvider, model, &turn.Usage)
		}
		for _, tb := range pt.ThinkingBlocks {
			turn.ThinkingBlocks = append(turn.ThinkingBlocks, models.ThinkingBlock{
				Content: tb.Content, TokenCount: tb.TokenCount, Redacted: tb.Redacted, Model: model, Provider: agent.ModelProvider,
			})
		}
		for _, tc := range pt.ToolCalls {
			turn.ToolCalls = append(turn.ToolCalls, executor.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		for _, tr := range pt.ToolResults {
			turn.ToolResults = append(turn.ToolResults, executor.ToolResult{ToolCallID: tr.ToolCallID, Name: tr.Name, Content: tr.Content, IsError: tr.IsError})
		}
		et.Turns = append(et.Turns, turn)
		u := &et.Usage
		u.InputTokens += turn.Usage.InputTokens
		u.OutputTokens += turn.Usage.OutputTokens
		u.TotalTokens += turn.Usage.TotalTokens
		u.CachedTokens += turn.Usage.CachedTokens
		u.CacheCreation += turn.Usage.CacheCreation
		u.ThinkingTokens += turn.Usage.ThinkingTokens
		u.EstimatedCost += turn.Usage.EstimatedCost
		u.CacheSavingsUSD += turn.Usage.CacheSavingsUSD
		u.PriceSource = turn.Usage.PriceSource
	}
	return et
}

// proxyToProcess forwards an invoke call to a framework-native agent's running
// process. It handles the full invoke lifecycle: OTel trace, HTTP call, trace
// storage. Guardrails are expected to have run before and after this call in
// the calling handler.
func (h *Handlers) proxyToProcess(
	ctx context.Context,
	agent *models.Agent,
	message string,
	variables map[string]string,
	providerTLS *router.ProviderTLSOverride,
	kitchen string,
) (string, *models.Trace, error) {
	if agent.Process == nil || agent.Process.Status != models.ProcessRunning {
		// Not recorded as running: bring it back if this control plane runs it.
		if err := h.ensureProcess(ctx, agent); err != nil {
			return "", nil, fmt.Errorf("agent '%s': %w", agent.Name, err)
		}
	}
	if agent.Process == nil || agent.Process.Status != models.ProcessRunning {
		return "", nil, fmt.Errorf("agent '%s' process is not running — bake it first", agent.Name)
	}

	traceID := uuid.New().String()
	if id := hostedRunFrom(ctx); id != "" {
		traceID = id // the run the process's model calls will be accounted to
	}
	start := time.Now()

	// Build the invoke request body
	body := map[string]interface{}{
		"message":  message,
		"trace_id": traceID,
	}
	if id := hostedRunFrom(ctx); id != "" {
		body["run_id"] = id
	}
	callTimeout := 30 * time.Second
	if thinkingFrom(ctx) {
		body["thinking_enabled"] = true
	}
	if agent.IsAgenticBot() {
		// A bot's run is tracked by the bot manager under this ID, whether or not the agent has a budget.
		if err := h.admitBotInvoke(ctx, agent, traceID, message); err != nil {
			return "", nil, err
		}
		body["run_id"] = traceID
		callTimeout = botInvokeTimeout()
	}
	if len(variables) > 0 {
		body["variables"] = variables
	}
	if sid := proxySessionFrom(ctx); sid != "" {
		// A hosted framework keeps its own memory, keyed on a thread ID.
		body["session_id"] = sid
		body["thread_id"] = sid
	}
	if providerTLS != nil && strings.TrimSpace(providerTLS.ProviderName) != "" {
		body["provider_config"] = map[string]interface{}{
			"name":            providerTLS.ProviderName,
			"ca_bundle":       providerTLS.CABundle,
			"tls_skip_verify": providerTLS.TLSSkipVerify,
		}
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal proxy request: %w", err)
	}

	invokeURL := strings.TrimRight(agent.Process.Endpoint, "/") + "/invoke"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, invokeURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", nil, fmt.Errorf("failed to build proxy request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	h.signPodRequest(httpReq, agent.Kitchen, agent.Name)

	client := &http.Client{Timeout: callTimeout}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("proxy request to agent process failed: %w", err)
	}
	defer httpResp.Body.Close()

	rawBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read proxy response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("agent process returned %d: %s", httpResp.StatusCode, string(rawBody))
	}

	var proxyResp proxyInvokeResponse
	if err := json.Unmarshal(rawBody, &proxyResp); err != nil {
		// If the response isn't the expected shape, treat the raw body as the response text.
		proxyResp.Response = strings.TrimSpace(string(rawBody))
	}

	durationMs := time.Since(start).Milliseconds()

	usage := models.TokenUsage{
		InputTokens:    proxyResp.Usage.InputTokens,
		OutputTokens:   proxyResp.Usage.OutputTokens,
		TotalTokens:    proxyResp.Usage.TotalTokens,
		CachedTokens:   proxyResp.Usage.CachedTokens,
		CacheCreation:  proxyResp.Usage.CacheCreation,
		ThinkingTokens: proxyResp.Usage.ThinkingTokens,
	}
	// A runner that reports its turns gets them priced, shown as spans and its thinking kept. One
	// that does not (a hosted framework) is priced from its totals alone.
	var execTrace *executor.ExecutionTrace
	if len(proxyResp.Turns) > 0 {
		execTrace = h.execTraceFromProxy(ctx, agent, traceID, durationMs, &proxyResp)
		usage = execTrace.Usage
	} else if h.Router != nil && agent.ModelName != "" && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
		h.Router.Settle(ctx, agent.Kitchen, agent.ModelProvider, agent.ModelName, &usage)
	}

	traceRecord := &models.Trace{
		ID:           traceID,
		AgentName:    agent.Name,
		AgentVersion: agent.Version,
		Kitchen:      kitchen,
		Status:       "completed",
		DurationMs:   durationMs,
		TotalTokens:  usage.TotalTokens,
		CostUSD:      usage.EstimatedCost,
		InputText:    message,
		OutputText:   h.scrubForStorage(ctx, agent, models.GuardrailStageOutput, proxyResp.Response),
		Usage:        &usage,
		Metadata: map[string]interface{}{
			"mode":    "managed",
			"runtime": string(agent.Runtime),
			"type":    "invoke",
		},
		CreatedAt: time.Now().UTC(),
	}
	if execTrace != nil {
		blocks, _ := summarizeThinking(execTrace)
		traceRecord.ThinkingBlocks = blocks
	}
	h.Store.CreateTrace(ctx, traceRecord)
	if execTrace != nil {
		h.persistExecutorSpans(ctx, execTrace)
	}

	return proxyResp.Response, traceRecord, nil
}

// proxyToProcessStream proxies an invoke call to a running agent pod's
// POST /invoke/stream endpoint and relays the SSE stream to the client.
func (h *Handlers) proxyToProcessStream(
	ctx context.Context,
	w http.ResponseWriter,
	agent *models.Agent,
	message string,
	variables map[string]string,
) error {
	if agent.Process == nil || agent.Process.Status != models.ProcessRunning {
		return fmt.Errorf("agent '%s' process is not running — bake it first", agent.Name)
	}

	traceID := uuid.New().String()
	if id := hostedRunFrom(ctx); id != "" {
		traceID = id
	}
	streamURL := strings.TrimRight(agent.Process.Endpoint, "/") + "/invoke/stream"

	body := map[string]interface{}{
		"message":  message,
		"trace_id": traceID,
	}
	if id := hostedRunFrom(ctx); id != "" {
		body["run_id"] = id
	}
	if agent.IsAgenticBot() {
		if err := h.admitBotInvoke(ctx, agent, traceID, message); err != nil {
			return err
		}
		body["run_id"] = traceID
	}
	if len(variables) > 0 {
		body["variables"] = variables
	}
	if sid := proxySessionFrom(ctx); sid != "" {
		body["session_id"] = sid
		body["thread_id"] = sid
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal stream request: %w", err)
	}

	podReq, err := http.NewRequestWithContext(ctx, http.MethodPost, streamURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to build stream request: %w", err)
	}
	podReq.Header.Set("Content-Type", "application/json")
	h.signPodRequest(podReq, agent.Kitchen, agent.Name)

	// Use a long timeout — stream can run for the full agentic loop duration.
	client := &http.Client{Timeout: 10 * time.Minute}
	podResp, err := client.Do(podReq)
	if err != nil {
		return fmt.Errorf("stream request to agent pod failed: %w", err)
	}
	defer podResp.Body.Close()

	if podResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(podResp.Body)
		return fmt.Errorf("agent pod returned %d: %s", podResp.StatusCode, string(raw))
	}

	// Set SSE headers and relay the stream line by line.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)
	scanner := bufio.NewScanner(podResp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		if line == "" && canFlush {
			flusher.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error relaying agent pod stream: %w", err)
	}
	return nil
}

// ══════════════════════════════════════════════════════════════
// ── Managed Agent Invoke Handler ─────────────────────────────
// ══════════════════════════════════════════════════════════════

// InvokeAgent executes a managed-mode agent's agentic loop.
// POST /api/v1/agents/{agentName}/invoke
func (h *Handlers) InvokeAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	// ── Scoped key agent access check ───────────────────────
	if identity := pkgmw.GetIdentity(r.Context()); identity != nil && identity.Provider == "scoped-key" {
		keyID := identity.Claims["key_id"]
		scopedKey, err := h.Store.GetScopedKey(r.Context(), kitchen, keyID)
		if err != nil || !scopedKey.CanAccessAgent(agentName) {
			respondError(w, http.StatusForbidden,
				fmt.Sprintf("Scoped key does not have access to agent '%s'", agentName))
			return
		}
	}

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if !agent.IsManaged() {
		respondError(w, http.StatusBadRequest,
			fmt.Sprintf("Agent '%s' is in '%s' mode — use the A2A endpoint for external agents", agentName, agent.Mode))
		return
	}

	// Parse request body early so guardrails can evaluate before status check
	var req struct {
		Message         string            `json:"message"`
		Variables       map[string]string `json:"variables,omitempty"`        // prompt template variables
		ThinkingEnabled bool              `json:"thinking_enabled,omitempty"` // enable extended thinking
		SessionID       string            `json:"session_id,omitempty"`       // continue a prior conversation — any agent, not just agentic
		Audio           *audioInput       `json:"audio,omitempty"`            // spoken input, transcribed before the turn
		VoiceOutput     bool              `json:"voice_output,omitempty"`     // also return the reply as speech
		Voice           string            `json:"voice,omitempty"`            // TTS voice name, default "alloy"
		// ContentParts attach images, PDFs, video or audio to the message (see models.ContentPart).
		ContentParts []models.ContentPart `json:"content_parts,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Message == "" && req.Audio == nil && len(req.ContentParts) == 0 {
		respondError(w, http.StatusBadRequest, "Request must include a non-empty 'message', 'audio' or 'content_parts' field")
		return
	}

	// Cascaded voice, step 1: speech in becomes text before anything else runs,
	// so guardrails and the agent see the same thing a typed message would give.
	var transcript string
	if req.Audio != nil {
		engine, err := h.speechEngineFor(r.Context(), agent)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		transcript, err = transcribeAudioInput(r.Context(), engine, req.Audio)
		if err != nil {
			if respondRefusal(w, err) {
				return
			}
			respondError(w, http.StatusBadRequest, "could not transcribe audio: "+err.Error())
			return
		}
		if req.Message == "" {
			req.Message = transcript
		} else {
			req.Message = req.Message + "\n" + transcript
		}
	}

	// Attached media: spoken parts become text like req.Audio does, the rest (images,
	// documents, video) goes to the model with the message.
	var mediaParts []models.ContentPart
	if len(req.ContentParts) > 0 {
		spoken, rest, err := h.transcribeAudioParts(r.Context(), agent, req.ContentParts)
		if err != nil {
			if respondRefusal(w, err) {
				return
			}
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		if spoken != "" {
			transcript = strings.TrimSpace(transcript + " " + spoken)
			req.Message = strings.TrimSpace(req.Message + "\n" + spoken)
		}
		mediaParts = rest
		if msg, ok := h.checkBlobRefs(r, kitchen, mediaParts); !ok {
			respondError(w, http.StatusNotFound, msg)
			return
		}
		if len(mediaParts) > 0 && agent.IsFrameworkNative() {
			respondError(w, http.StatusUnprocessableEntity, "this agent runs its own framework and takes text only; images, documents and other media can be sent to agents that run on AgentOven's own loop")
			return
		}
	}

	// ── Input Guardrails ────────────────────────────────────
	// Evaluate BEFORE status check — bad input should be rejected
	// regardless of agent state (security-first principle).
	gr, grOK := h.requireGuardrails(w, r, agent, agent.Guardrails)
	if !grOK {
		return
	}
	var tally guardTally // what the rules redacted or warned about, recorded on the trace
	if h.Guardrails != nil && len(gr) > 0 {
		eval, gErr := h.Guardrails.EvaluateInput(r.Context(), gr, req.Message)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Input guardrail evaluation error")
			h.emitGuardrailAudit(r.Context(), kitchen, agentName, "input", nil, gErr)
		} else if !eval.Passed {
			h.emitGuardrailAudit(r.Context(), kitchen, agentName, "input", eval, nil)
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Input blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		} else {
			req.Message = eval.Apply(req.Message)
			h.noteGuardrails(r.Context(), kitchen, agentName, "input", eval)
			tally.add(eval)
		}
	}

	if agent.Status != models.AgentStatusReady {
		respondError(w, http.StatusBadRequest,
			fmt.Sprintf("Agent '%s' is not ready (status: %s) — bake it first", agentName, agent.Status))
		return
	}

	// When thinking is enabled, extend the request timeout to avoid premature cancellation.
	// Thinking models (o-series, Claude extended thinking) can take 30-120s per turn.
	if req.ThinkingEnabled {
		var cancel context.CancelFunc
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		r = r.WithContext(ctx)
		_ = ctx // use new ctx
	}

	// Use cached resolved config from bake time, or re-resolve live
	var resolved *models.ResolvedIngredients
	if agent.ResolvedConfig != nil {
		resolved = agent.ResolvedConfig
	} else {
		resolved, err = h.Resolver.Resolve(r.Context(), agent)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Ingredient resolution failed: "+err.Error())
			return
		}
	}

	// Sanitize variable values before prompt rendering
	sanitizedVars := req.Variables
	if len(req.Variables) > 0 {
		settings, _ := h.resolvedKitchenSettings(r.Context(), kitchen)
		var issues []models.ValidationIssue
		sanitizedVars, issues, err = h.PromptValidator.SanitizeVariables(r.Context(), req.Variables, settings)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Variable sanitization failed: "+err.Error())
			return
		}
		for _, issue := range issues {
			if issue.Severity == models.ValidationError {
				respondJSON(w, http.StatusBadRequest, map[string]interface{}{
					"error":  "Variable injection detected",
					"issues": issues,
				})
				return
			}
		}
	}

	// Execute: if a pod is already running for this agent, proxy to it.
	// This works for all runtimes (agentoven-native and framework-native alike).
	// Framework-native agents without a running process are an error; agentoven-
	// native agents without a process fall back to the in-process Go executor.
	if agent.IsFrameworkNative() && (agent.Process == nil || agent.Process.Status != models.ProcessRunning) {
		// Its process is the only thing that can run it: bring it back rather than fail.
		if err := h.ensureProcess(r.Context(), agent); err != nil {
			respondError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	// A running pod is sent text only, so a message with media is answered by the in-process
	// executor instead (as A2A does for these agents).
	if agent.Process != nil && agent.Process.Status == models.ProcessRunning && len(mediaParts) == 0 {
		hostedCtx, endRun := h.beginHostedRun(withThinking(withCallerSession(r.Context(), req.SessionID), req.ThinkingEnabled), agent)
		response, traceRecord, err := h.proxyToProcess(hostedCtx, agent, req.Message, sanitizedVars, nil, kitchen)
		outcome := endRun()
		if outcome != nil && outcome.Stopped != nil {
			// The agent's model calls were refused at its budget: report that, with what it spent.
			h.respondHostedStopped(w, agentName, hostedRunFrom(hostedCtx), outcome)
			return
		}
		if err != nil {
			respondError(w, http.StatusBadGateway, "Agent pod execution failed: "+err.Error())
			return
		}
		if outcome != nil && traceRecord != nil {
			// The gateway saw every model call, so its count is the authoritative one.
			u := outcome.Usage
			traceRecord.Usage, traceRecord.TotalTokens, traceRecord.CostUSD = &u, u.TotalTokens, u.EstimatedCost
		}

		// ── Output Guardrails ─────────────────────────────────
		if h.Guardrails != nil && len(gr) > 0 {
			eval, gErr := h.Guardrails.EvaluateOutput(r.Context(), gr, response)
			if gErr != nil {
				log.Warn().Err(gErr).Str("agent", agentName).Msg("Output guardrail evaluation error")
			} else if !eval.Passed {
				respondJSON(w, http.StatusForbidden, map[string]interface{}{
					"error":      "Output blocked by guardrails",
					"guardrails": eval.Results,
				})
				return
			} else {
				response = eval.Apply(response)
				h.noteGuardrails(r.Context(), kitchen, agentName, "output", eval)
				tally.add(eval)
			}
		}
		h.recordGuardrailsOnTrace(r.Context(), traceRecord, response, tally)

		out := map[string]interface{}{
			"agent":      agentName,
			"response":   response,
			"trace_id":   traceRecord.ID,
			"turns":      1,
			"usage":      traceRecord.Usage,
			"latency_ms": traceRecord.DurationMs,
		}
		if !h.addSpeech(w, r.Context(), agent, out, response, transcript, req.VoiceOutput, req.Voice) {
			return
		}
		respondJSON(w, http.StatusOK, out)
		return
	}

	// Execute the agentic loop
	runCtx, _ := withRunTrace(r.Context())
	r = r.WithContext(runCtx)
	response, trace, err := h.Executor.ExecuteMessage(r.Context(), agent,
		models.ChatMessage{Role: "user", Content: req.Message, ContentParts: mediaParts}, resolved, sanitizedVars, req.ThinkingEnabled, req.SessionID)
	if h.respondRunEnded(w, r, agent, req.Message, trace, err) {
		return
	}
	if err != nil {
		if router.IsMediaError(err) {
			respondError(w, http.StatusUnprocessableEntity, "Execution failed: "+err.Error())
			return
		}
		respondError(w, http.StatusBadGateway, "Execution failed: "+err.Error())
		return
	}

	// ── Output Guardrails ───────────────────────────────────
	// (A run that judged every reply itself, the last one included, has nothing left to judge here.)
	if h.Guardrails != nil && len(gr) > 0 && !trace.OutputJudged {
		eval, gErr := h.Guardrails.EvaluateOutput(r.Context(), gr, response)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Output guardrail evaluation error; the reply is withheld")
			h.emitGuardrailAudit(r.Context(), kitchen, agentName, "output", nil, gErr)
			respondError(w, http.StatusServiceUnavailable, "the reply could not be checked by the guardrails, so it was withheld")
			return
		} else if !eval.Passed {
			h.emitGuardrailAudit(r.Context(), kitchen, agentName, "output", eval, nil)
			h.recordBlockedRun(r.Context(), agent, req.Message, trace)
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Output blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		} else {
			original := response
			response = eval.Apply(response)
			redactedTurn(trace, original, response)
			h.redactSessionReply(r.Context(), trace, original, response)
			h.noteGuardrails(r.Context(), kitchen, agentName, "output", eval)
			tally.add(eval)
		}
	}

	// Record trace in store — enriched with full token breakdown, input, and session
	traceUsage := trace.Usage
	traceRecord := &models.Trace{
		ID:           trace.TraceID,
		AgentName:    agentName,
		AgentVersion: agent.Version,
		Kitchen:      kitchen,
		Status:       traceStatus(trace),
		DurationMs:   trace.TotalMs,
		TotalTokens:  trace.Usage.TotalTokens,
		CostUSD:      trace.Usage.EstimatedCost,
		InputText:    req.Message,
		OutputText:   response,
		SessionID:    trace.SessionID,
		Usage:        &traceUsage,
		Metadata: map[string]interface{}{
			"mode":  "managed",
			"turns": len(trace.Turns),
			"type":  "invoke",
		},
		CreatedAt: time.Now().UTC(),
	}
	tally.addRun(trace)
	withGuardrailMeta(traceRecord.Metadata, tally)
	thinkingBlocks, thinkingTokens := summarizeThinking(trace)
	if len(thinkingBlocks) > 0 {
		traceRecord.Metadata["thinking_blocks"] = thinkingBlocks
	}
	if thinkingTokens > 0 {
		traceRecord.Metadata["thinking_tokens"] = thinkingTokens
	}
	h.Store.CreateTrace(r.Context(), traceRecord)
	h.emitThinkingAudit(r.Context(), kitchen, agentName, "invoke", req.ThinkingEnabled, thinkingBlocks, thinkingTokens)

	// Persist executor turns as hierarchical spans
	h.persistExecutorSpans(r.Context(), trace)

	out := map[string]interface{}{
		"agent":           agentName,
		"response":        response,
		"trace_id":        trace.TraceID,
		"session_id":      trace.SessionID, // pass back to continue this conversation on a later /invoke
		"turns":           len(trace.Turns),
		"usage":           trace.Usage,
		"latency_ms":      trace.TotalMs,
		"execution_trace": trace,
	}
	if !h.addSpeech(w, r.Context(), agent, out, response, transcript, req.VoiceOutput, req.Voice) {
		return
	}
	respondJSON(w, http.StatusOK, out)
}

// StreamInvokeAgent executes a managed agent's agentic loop over SSE: proxies
// to a running agent pod when one exists (identical to before), or — new —
// streams the in-process Go executor directly when it doesn't, the same
// fallback InvokeAgent already uses for its synchronous counterpart. Before
// this, an agent with no running process could never be streamed at all.
// POST /api/v1/agents/{agentName}/invoke/stream
func (h *Handlers) StreamInvokeAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	if identity := pkgmw.GetIdentity(r.Context()); identity != nil && identity.Provider == "scoped-key" {
		keyID := identity.Claims["key_id"]
		scopedKey, err := h.Store.GetScopedKey(r.Context(), kitchen, keyID)
		if err != nil || !scopedKey.CanAccessAgent(agentName) {
			respondError(w, http.StatusForbidden,
				fmt.Sprintf("Scoped key does not have access to agent '%s'", agentName))
			return
		}
	}

	var req struct {
		Message         string            `json:"message"`
		Variables       map[string]string `json:"variables,omitempty"`
		ThinkingEnabled bool              `json:"thinking_enabled,omitempty"`
		SessionID       string            `json:"session_id,omitempty"`
		// ContentParts attach images, PDFs, video or audio to the message.
		ContentParts []models.ContentPart `json:"content_parts,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Message == "" && len(req.ContentParts) == 0) {
		respondError(w, http.StatusBadRequest, "Request must include a non-empty 'message' field")
		return
	}

	// NOTE: this previously called GetAgent(ctx, agentName, kitchen) — the
	// store's signature is (ctx, kitchen, name), so the arguments were
	// swapped and this endpoint could not reliably resolve an agent outside
	// a single-kitchen deployment. Fixed here.
	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if !agent.IsManaged() {
		respondError(w, http.StatusBadRequest,
			fmt.Sprintf("Agent '%s' is in '%s' mode — use the A2A endpoint for external agents", agentName, agent.Mode))
		return
	}

	// Attached media: spoken parts become text, the rest goes to the model with the message.
	var mediaParts []models.ContentPart
	if len(req.ContentParts) > 0 {
		spoken, rest, err := h.transcribeAudioParts(r.Context(), agent, req.ContentParts)
		if err != nil {
			if respondRefusal(w, err) {
				return
			}
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		if spoken != "" {
			req.Message = strings.TrimSpace(req.Message + "\n" + spoken)
		}
		mediaParts = rest
		if msg, ok := h.checkBlobRefs(r, kitchen, mediaParts); !ok {
			respondError(w, http.StatusNotFound, msg)
			return
		}
		if len(mediaParts) > 0 && agent.IsFrameworkNative() {
			respondError(w, http.StatusUnprocessableEntity, "this agent runs its own framework and takes text only; images, documents and other media can be sent to agents that run on AgentOven's own loop")
			return
		}
	}

	gr, grOK := h.requireGuardrails(w, r, agent, agent.Guardrails)
	if !grOK {
		return
	}
	if h.Guardrails != nil && len(gr) > 0 {
		eval, gErr := h.Guardrails.EvaluateInput(r.Context(), gr, req.Message)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Input guardrail evaluation error")
			h.emitGuardrailAudit(r.Context(), kitchen, agentName, "input", nil, gErr)
		} else if !eval.Passed {
			h.emitGuardrailAudit(r.Context(), kitchen, agentName, "input", eval, nil)
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Input blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		} else {
			req.Message = eval.Apply(req.Message)
			h.noteGuardrails(r.Context(), kitchen, agentName, "input", eval)
		}
	}

	if agent.Status != models.AgentStatusReady {
		respondError(w, http.StatusBadRequest,
			fmt.Sprintf("Agent '%s' is not ready (status: %s) — bake it first", agentName, agent.Status))
		return
	}

	if agent.IsFrameworkNative() && (agent.Process == nil || agent.Process.Status != models.ProcessRunning) {
		if err := h.ensureProcess(r.Context(), agent); err != nil {
			respondError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	// A running pod is sent text only, so a message with media is streamed from the
	// in-process executor instead.
	if agent.Process != nil && agent.Process.Status == models.ProcessRunning && len(mediaParts) == 0 {
		sanitizedVars := req.Variables
		if sanitizedVars == nil {
			sanitizedVars = map[string]string{}
		}
		// Relays the process's stream as it arrives, or, when the agent has output
		// guardrails, holds it back until the whole reply has been checked.
		h.streamProcess(w, r, agent, gr, req.Message, sanitizedVars, req.SessionID)
		return
	}

	// No running pod — stream the in-process Go executor directly.
	var resolved *models.ResolvedIngredients
	if agent.ResolvedConfig != nil {
		resolved = agent.ResolvedConfig
	} else {
		resolved, err = h.Resolver.Resolve(r.Context(), agent)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Ingredient resolution failed: "+err.Error())
			return
		}
	}

	sanitizedVars := req.Variables
	if len(req.Variables) > 0 {
		settings, _ := h.resolvedKitchenSettings(r.Context(), kitchen)
		var issues []models.ValidationIssue
		sanitizedVars, issues, err = h.PromptValidator.SanitizeVariables(r.Context(), req.Variables, settings)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Variable sanitization failed: "+err.Error())
			return
		}
		for _, issue := range issues {
			if issue.Severity == models.ValidationError {
				respondJSON(w, http.StatusBadRequest, map[string]interface{}{
					"error":  "Variable injection detected",
					"issues": issues,
				})
				return
			}
		}
	}

	// With output guardrails the frames are held back and released only once the reply
	// has been checked (see stream_guard.go); without them they go out as they arrive.
	var held *heldEvents
	var flusher http.Flusher
	canFlush := false
	if h.holdBackNeeded(gr) {
		held = &heldEvents{}
	}
	// The stream is committed (status 200 and headers) with its first frame that says a model answered, not
	// before: a call the budget, a rate limit or a guardrail refuses before any model answers is still a
	// 402, 429 or 403, which is what an SDK reads (an error frame inside a 200 is retried like any outage).
	// The frames that come first (the start of the turn, an error, a pause at a budget) wait in `early` until then.
	committed := false
	var early []executor.Event
	emit := func(ev executor.Event) error {
		payload, merr := json.Marshal(ev)
		if merr != nil {
			return merr
		}
		if _, werr := fmt.Fprintf(w, "data: %s\n\n", payload); werr != nil {
			return werr
		}
		if canFlush {
			flusher.Flush()
		}
		return nil
	}
	commit := func() error {
		if committed {
			return nil
		}
		committed = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher, canFlush = w.(http.Flusher)
		for _, ev := range early {
			if err := emit(ev); err != nil {
				return err
			}
		}
		early = nil
		return nil
	}

	writeEvent := func(ev executor.Event) error {
		if held != nil {
			return held.add(ev)
		}
		if !committed && (ev.Type == executor.EventTurnStart || ev.Type == executor.EventError || ev.Type == executor.EventBudgetPaused) {
			early = append(early, ev)
			return nil
		}
		if err := commit(); err != nil {
			return err
		}
		return emit(ev)
	}

	runCtx, _ := withRunTrace(r.Context())
	r = r.WithContext(runCtx)
	response, trace, err := h.Executor.ExecuteStreamMessage(r.Context(), agent,
		models.ChatMessage{Role: "user", Content: req.Message, ContentParts: mediaParts}, resolved, sanitizedVars, req.ThinkingEnabled, writeEvent, req.SessionID)
	if err != nil && (held != nil || !committed) && h.respondRunEnded(w, r, agent, req.Message, trace, err) {
		return // refused before anything was sent: the caller got the status that says why (402, 429, 403, 503)
	}
	if !committed && held == nil {
		// Nothing but the start of a turn and an error came out: send them as the stream they were.
		if cerr := commit(); cerr != nil {
			return
		}
	}
	if held != nil && !h.releaseHeld(w, r, agent, gr, held, response) {
		h.recordBlockedRun(r.Context(), agent, req.Message, trace)
		return // blocked: the caller got a 403 and none of the reply
	}
	var pausedErr *executor.PausedError
	if err == executor.ErrPausedForApproval || errors.As(err, &pausedErr) {
		// The EventPaused / EventBudgetPaused frame already told the client what is
		// pending; there is no final response to persist until Resume runs.
		return
	}
	if err != nil {
		log.Warn().Err(err).Str("agent", agentName).Msg("Streaming execution failed")
		return // the failure already went out over the stream as an EventError frame
	}

	traceUsage := trace.Usage
	traceRecord := &models.Trace{
		ID:           trace.TraceID,
		AgentName:    agentName,
		AgentVersion: agent.Version,
		Kitchen:      kitchen,
		Status:       traceStatus(trace),
		DurationMs:   trace.TotalMs,
		TotalTokens:  trace.Usage.TotalTokens,
		CostUSD:      trace.Usage.EstimatedCost,
		InputText:    req.Message,
		OutputText:   response,
		SessionID:    trace.SessionID,
		Usage:        &traceUsage,
		Metadata: map[string]interface{}{
			"mode":  "managed",
			"turns": len(trace.Turns),
			"type":  "invoke_stream",
		},
		CreatedAt: time.Now().UTC(),
	}
	thinkingBlocks, thinkingTokens := summarizeThinking(trace)
	if len(thinkingBlocks) > 0 {
		traceRecord.Metadata["thinking_blocks"] = thinkingBlocks
	}
	if thinkingTokens > 0 {
		traceRecord.Metadata["thinking_tokens"] = thinkingTokens
	}
	h.Store.CreateTrace(r.Context(), traceRecord)
	h.emitThinkingAudit(r.Context(), kitchen, agentName, "invoke_stream", req.ThinkingEnabled, thinkingBlocks, thinkingTokens)
	h.persistExecutorSpans(r.Context(), trace)
}

// ResumeRun applies a human decision to a run paused on an approval-gated
// tool call (see Agent.ApprovalTools), or retries a run that was interrupted
// without ever pausing (a crashed process, a lost model call) from its last
// journaled turn. decision is required only for a genuinely paused run —
// resuming a plain interruption passes a nil decision since there is nothing
// to approve or deny.
// POST /api/v1/agents/{agentName}/runs/{traceID}/resume
func (h *Handlers) ResumeRun(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	traceID := chi.URLParam(r, "traceID")
	kitchen := middleware.GetKitchen(r.Context())

	if identity := pkgmw.GetIdentity(r.Context()); identity != nil && identity.Provider == "scoped-key" {
		keyID := identity.Claims["key_id"]
		scopedKey, err := h.Store.GetScopedKey(r.Context(), kitchen, keyID)
		if err != nil || !scopedKey.CanAccessAgent(agentName) {
			respondError(w, http.StatusForbidden,
				fmt.Sprintf("Scoped key does not have access to agent '%s'", agentName))
			return
		}
	}

	var body struct {
		Approved bool   `json:"approved"`
		Note     string `json:"note,omitempty"`
		// Extend raises the run's caps when it is paused at its budget. Approving a
		// budget pause without one is refused: a person has to say how much more.
		Extend *models.BudgetExtension `json:"extend,omitempty"`
	}
	var decision *executor.ResumeDecision
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		decision = &executor.ResumeDecision{Approved: body.Approved, Note: body.Note, Extend: body.Extend}
		if decision.Extend != nil {
			if !h.authorizeExtension(w, r, decision.Extend) {
				return
			}
			if id := pkgmw.GetIdentity(r.Context()); id != nil {
				decision.Extend.By = id.Subject
			}
		}
	}

	// A resumed run is an agent's output reaching a caller like any other, so the same
	// guardrails stand in front of it. They are established before anything runs.
	agent, aerr := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if aerr != nil || agent == nil {
		agent = &models.Agent{Name: agentName, Kitchen: kitchen}
	}
	gr, ok := h.requireGuardrails(w, r, agent, agent.Guardrails)
	if !ok {
		return
	}

	// A decision on a paused run is a signed gate (Pro ADR-0041).
	var sig *contracts.SignedDecision
	if h.DecisionSigner != nil && decision != nil {
		var ok bool
		if sig, ok = h.signRunDecision(w, r, kitchen, traceID, decision); !ok {
			return
		}
	}

	r = r.WithContext(models.WithTraceStamp(r.Context(), models.TraceStamp{TraceID: traceID}))
	response, trace, err := h.Executor.ResumeFor(r.Context(), kitchen, agentName, traceID, decision)
	h.decisionApplied(r, sig, decisionApplyError(err))
	if err != nil {
		switch {
		case h.respondRunEnded(w, r, agent, "", trace, err):
			// paused again (approval or budget), ended at its budget, or unpriced
		case strings.Contains(err.Error(), "no journal configured"):
			respondError(w, http.StatusServiceUnavailable, err.Error())
		case strings.Contains(err.Error(), "not found"):
			respondError(w, http.StatusNotFound, err.Error())
		case strings.Contains(err.Error(), "already finished"), strings.Contains(err.Error(), "ResumeDecision is required"),
			strings.Contains(err.Error(), "needs an extension"), strings.Contains(err.Error(), "already being resumed"):
			respondError(w, http.StatusBadRequest, err.Error())
		default:
			respondError(w, http.StatusBadGateway, "Resume failed: "+err.Error())
		}
		return
	}

	out, pass, eval := h.outputText(r.Context(), agent, gr, response)
	if !pass {
		respondOutputBlocked(w, eval)
		return
	}
	redactedTurn(trace, response, out)
	h.redactSessionReply(r.Context(), trace, response, out)
	response = out

	traceUsage := trace.Usage
	traceRecord := &models.Trace{
		ID:           trace.TraceID,
		AgentName:    agentName,
		AgentVersion: agent.Version,
		Kitchen:      kitchen,
		Status:       traceStatus(trace),
		DurationMs:   trace.TotalMs,
		TotalTokens:  trace.Usage.TotalTokens,
		CostUSD:      trace.Usage.EstimatedCost,
		OutputText:   response,
		SessionID:    trace.SessionID,
		Usage:        &traceUsage,
		Metadata: map[string]interface{}{
			"mode":  "managed",
			"turns": len(trace.Turns),
			"type":  "resume",
		},
		CreatedAt: time.Now().UTC(),
	}
	h.Store.CreateTrace(r.Context(), traceRecord)
	h.persistExecutorSpans(r.Context(), trace)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"agent":           agentName,
		"response":        response,
		"trace_id":        trace.TraceID,
		"session_id":      trace.SessionID,
		"status":          "completed",
		"turns":           len(trace.Turns),
		"usage":           trace.Usage,
		"latency_ms":      trace.TotalMs,
		"execution_trace": trace,
	})
}

// ListUnfinishedRuns reports runs that stopped without finishing — crashed,
// restarted, or paused for approval — in this kitchen, so a caller can decide
// which (if any) to resume with ResumeRun. Requires the executor to have a
// journal configured (the OSS default; see handlers.New), otherwise durable
// execution has nothing to report.
// GET /api/v1/agents/runs/unfinished
func (h *Handlers) ListUnfinishedRuns(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	runs, err := h.Executor.ListUnfinished(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"runs": runs})
}

// ══════════════════════════════════════════════════════════════
// ── Span Persistence Helpers ─────────────────────────────────
// ══════════════════════════════════════════════════════════════

// persistExecutorSpans converts an executor.ExecutionTrace into hierarchical Span records
// and persists them in the store. Creates:
//   - One root "agent" span covering the entire execution
//   - One "llm" span per turn (LLM call)
//   - One "tool" span per tool call within that turn
//
// This enables LangSmith-style waterfall visualization over the full execution tree.
func (h *Handlers) persistExecutorSpans(ctx context.Context, execTrace *executor.ExecutionTrace) {
	if execTrace == nil || len(execTrace.Turns) == 0 {
		return
	}

	traceID := execTrace.TraceID
	now := time.Now().UTC()
	execStart := now.Add(-time.Duration(execTrace.TotalMs) * time.Millisecond)

	// Root agent span
	rootSpanID := uuid.New().String()
	agentUsage := execTrace.Usage
	inputJSON, _ := json.Marshal(map[string]interface{}{
		"agent":   execTrace.AgentName,
		"kitchen": execTrace.Kitchen,
	})
	outputJSON, _ := json.Marshal(map[string]interface{}{
		"turns":        len(execTrace.Turns),
		"total_ms":     execTrace.TotalMs,
		"total_tokens": execTrace.Usage.TotalTokens,
	})

	spans := []models.Span{
		{
			ID:         rootSpanID,
			TraceID:    traceID,
			Name:       execTrace.AgentName,
			Kind:       models.SpanKindAgent,
			Status:     "completed",
			StartTime:  execStart,
			EndTime:    now,
			DurationMs: execTrace.TotalMs,
			Input:      inputJSON,
			Output:     outputJSON,
			Usage:      &agentUsage,
			Metadata: map[string]interface{}{
				"session_id": execTrace.SessionID,
			},
		},
	}

	// Track cumulative offset for turn timing
	turnOffset := execStart

	for _, turn := range execTrace.Turns {
		turnStart := turnOffset
		turnEnd := turnStart.Add(time.Duration(turn.LatencyMs) * time.Millisecond)
		turnOffset = turnEnd

		// LLM span for this turn
		llmSpanID := turn.SpanID
		if llmSpanID == "" {
			llmSpanID = uuid.New().String()
		}
		turnUsage := turn.Usage

		// Serialize the request messages as input
		reqJSON, _ := json.Marshal(turn.Request)
		respJSON, _ := json.Marshal(map[string]interface{}{
			"content":         turn.Response,
			"thinking_blocks": turn.ThinkingBlocks,
			"tool_calls":      turn.ToolCalls,
		})

		llmSpan := models.Span{
			ID:           llmSpanID,
			TraceID:      traceID,
			ParentSpanID: rootSpanID,
			Name:         fmt.Sprintf("llm.turn_%d", turn.Number),
			Kind:         models.SpanKindLLM,
			Status:       "completed",
			StartTime:    turnStart,
			EndTime:      turnEnd,
			DurationMs:   turn.LatencyMs,
			Input:        reqJSON,
			Output:       respJSON,
			Usage:        &turnUsage,
			Model:        turn.Model,
			Provider:     turn.Provider,
			Metadata: map[string]interface{}{
				"turn_number":     turn.Number,
				"thinking_blocks": len(turn.ThinkingBlocks),
				"thinking_tokens": turn.Usage.ThinkingTokens,
				"cached_tokens":   turn.Usage.CachedTokens,
				"price_source":    turn.Usage.PriceSource,
				"cache_hit":       turn.CacheHit,
			},
		}
		// Providers that failed before this one answered are events on the span.
		for _, a := range turn.Attempts {
			llmSpan.Events = append(llmSpan.Events, models.SpanEvent{
				Name: "fallback", Timestamp: turnStart,
				Attributes: map[string]interface{}{"provider": a.Provider, "model": a.Model, "error": a.Error, "latency_ms": a.LatencyMs},
			})
		}
		spans = append(spans, llmSpan)

		// Tool spans for each tool call in this turn
		if len(turn.ToolCalls) > 0 {
			// Distribute tool time evenly within the turn (approximation)
			toolDuration := turn.LatencyMs / int64(len(turn.ToolCalls)+1)
			toolStart := turnStart.Add(time.Duration(toolDuration) * time.Millisecond) // after LLM response

			for i, tc := range turn.ToolCalls {
				toolEnd := toolStart.Add(time.Duration(toolDuration) * time.Millisecond)

				tcInputJSON, _ := json.Marshal(tc.Arguments)
				var tcOutputJSON json.RawMessage
				var toolStatus string = "completed"
				var toolError string

				// Match tool result
				if i < len(turn.ToolResults) {
					tr := turn.ToolResults[i]
					tcOutputJSON, _ = json.Marshal(map[string]interface{}{
						"content":  tr.Content,
						"is_error": tr.IsError,
					})
					if tr.IsError {
						toolStatus = "failed"
						toolError = tr.Content
					}
				}

				toolSpan := models.Span{
					ID:           uuid.New().String(),
					TraceID:      traceID,
					ParentSpanID: llmSpanID,
					Name:         tc.Name,
					Kind:         models.SpanKindTool,
					Status:       toolStatus,
					StartTime:    toolStart,
					EndTime:      toolEnd,
					DurationMs:   toolDuration,
					Input:        tcInputJSON,
					Output:       tcOutputJSON,
					Error:        toolError,
					Metadata: map[string]interface{}{
						"tool_call_id": tc.ID,
					},
				}
				spans = append(spans, toolSpan)
				toolStart = toolEnd
			}
		}
	}

	// Batch persist all spans
	if err := h.Store.CreateSpans(ctx, spans); err != nil {
		log.Warn().Err(err).Str("trace_id", traceID).Int("span_count", len(spans)).Msg("Failed to persist executor spans")
	}
}

func summarizeThinking(execTrace *executor.ExecutionTrace) ([]models.ThinkingBlock, int64) {
	if execTrace == nil || len(execTrace.Turns) == 0 {
		return nil, 0
	}

	const maxBlocks = 16
	blocks := make([]models.ThinkingBlock, 0, maxBlocks)
	var thinkingTokens int64

	for _, turn := range execTrace.Turns {
		thinkingTokens += turn.Usage.ThinkingTokens
		for _, b := range turn.ThinkingBlocks {
			if len(blocks) >= maxBlocks {
				break
			}
			blocks = append(blocks, b)
		}
		if len(blocks) >= maxBlocks {
			break
		}
	}

	if len(blocks) == 0 {
		return nil, thinkingTokens
	}
	return blocks, thinkingTokens
}

func (h *Handlers) emitThinkingAudit(ctx context.Context, kitchen, agentName, source string, thinkingEnabled bool, blocks []models.ThinkingBlock, thinkingTokens int64) {
	if h.Store == nil {
		return
	}

	settings, err := h.Store.GetKitchenSettings(ctx, kitchen)
	if err != nil {
		return
	}
	if settings == nil || !settings.RequireThinkingAudit {
		return
	}

	userID, userEmail, displayName, _, actorType := auditActorFromIdentity(ctx)

	status := http.StatusOK
	action := "thinking.captured"
	if thinkingEnabled && len(blocks) == 0 {
		status = http.StatusConflict
		action = "thinking.missing"
	}

	details := map[string]interface{}{
		"agent":            agentName,
		"source":           source,
		"thinking_enabled": thinkingEnabled,
		"block_count":      len(blocks),
		"thinking_tokens":  thinkingTokens,
	}
	if len(blocks) > 0 {
		// Keep payload bounded for audit list queries while still preserving evidence.
		max := len(blocks)
		if max > 8 {
			max = 8
		}
		details["thinking_blocks"] = blocks[:max]
	}

	if err := h.Store.CreateAuditEvent(ctx, &models.AuditEvent{
		ID:                 uuid.New().String(),
		Timestamp:          time.Now().UTC(),
		UserID:             userID,
		UserEmail:          userEmail,
		ActorType:          actorType,
		ActorDisplayName:   displayName,
		Action:             action,
		Resource:           "thinking",
		ResourceID:         agentName,
		Kitchen:            kitchen,
		Details:            details,
		ResponseStatus:     status,
		RegulationTags:     []string{"compliance", "reasoning", "gxp"},
		DataClassification: "restricted",
	}); err != nil {
		log.Warn().Err(err).Str("agent", agentName).Str("action", action).Msg("failed to persist thinking audit event")
	}
}

// ══════════════════════════════════════════════════════════════
// ── Guardrails Handlers ──────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListGuardrailKinds returns the available guardrail types and their configuration schemas.
// GET /api/v1/guardrails/kinds
func (h *Handlers) ListGuardrailKinds(w http.ResponseWriter, r *http.Request) {
	kinds := []map[string]interface{}{
		{
			"kind":        "content_filter",
			"label":       "Content Filter",
			"description": "Block messages containing specific words or phrases",
			"config_schema": map[string]interface{}{
				"blocked_words":  "string[]  — list of blocked words/phrases",
				"case_sensitive": "boolean   — whether matching is case-sensitive (default: false)",
			},
		},
		{
			"kind":        "pii_detection",
			"label":       "PII Detection",
			"description": "Detect personally identifiable information and secrets, and block, redact, mask or warn",
			"config_schema": map[string]interface{}{
				"patterns":      "string[]  — detectors or groups to run (default: email, phone, ssn, credit_card); see detectors",
				"allowlist":     "string[]  — exact values never flagged",
				"allow_domains": "string[]  — email domains never flagged",
			},
		},
		{
			"kind":        "topic_restriction",
			"label":       "Topic Restriction",
			"description": "Restrict conversation to allowed topics or block specific topics",
			"config_schema": map[string]interface{}{
				"allowed_topics": "string[]  — if set, messages must contain at least one allowed topic",
				"blocked_topics": "string[]  — messages containing these topics are blocked",
			},
		},
		{
			"kind":        "max_length",
			"label":       "Max Length",
			"description": "Enforce character or word length limits",
			"config_schema": map[string]interface{}{
				"max_characters": "integer  — maximum character count",
				"max_words":      "integer  — maximum word count",
			},
		},
		{
			"kind":        "regex_filter",
			"label":       "Regex Filter",
			"description": "Match messages against a custom regex pattern",
			"config_schema": map[string]interface{}{
				"pattern":        "string   — regex pattern to match against (RE2, linear time)",
				"block_on_match": "boolean  — block when pattern matches (default: true); must be true to redact",
				"label":          "string   — placeholder name when redacting, [LABEL] (default: REDACTED)",
			},
		},
		{
			"kind":        "prompt_injection",
			"label":       "Prompt Injection",
			"description": "Detect common prompt injection and jailbreak attempts",
			"config_schema": map[string]interface{}{
				"sensitivity": "string  — detection sensitivity: high, medium, low (default: medium)",
			},
		},
		{
			"kind":        "llamaguard",
			"label":       "LlamaGuard",
			"description": "Ask a LlamaGuard-compatible model (any OpenAI-style chat endpoint) whether the text is safe; fails closed. The endpoint is reached through the egress guard: a private address needs an allowlist entry",
			"config_schema": map[string]interface{}{
				"endpoint":     "string   — base URL of the inference server (http or https, no credentials in the URL)",
				"model":        "string   — default llama-guard3:1b",
				"api_key":      "string   — optional bearer token, sent to the endpoint only",
				"timeout_secs": "number   — 1 to 30 (default 10)",
				"categories":   "string   — optional subset, e.g. S1,S10,S11",
				"fail_open":    "boolean  — pass the text when the endpoint gives no verdict (default: false)",
			},
		},
		{
			"kind":        "custom",
			"label":       "Custom",
			"description": "A signed https endpoint of the kitchen's own that decides allow, warn, redact, require_approval or block. Pro only, and only on a workspace rule: an agent's own custom rule names no endpoint and passes",
			"config_schema": map[string]interface{}{
				"endpoint":   "string   — https URL (Pro workspace rule)",
				"secret_ref": "string   — vault:// reference to the credential that signs the request",
				"timeout_ms": "number   — 100 to 10000 (default 3000)",
				"fail_open":  "boolean  — pass the text when the endpoint cannot be used (default: false)",
			},
		},
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"kinds":     kinds,
		"stages":    []string{"input", "output", "both", "retrieval", "all"},
		"actions":   []string{"block", "redact", "mask", "warn", "require_approval"},
		"detectors": grails.DetectorNames(),
	})
}

// ══════════════════════════════════════════════════════════════
// ── Kitchen Settings Handlers ────────────────────────────────
// ══════════════════════════════════════════════════════════════

// GetKitchenSettings returns the settings for the current kitchen.
// GET /api/v1/settings
func (h *Handlers) GetKitchenSettings(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	settings, err := h.Store.GetKitchenSettings(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, settings.Redacted())
}

// UpdateKitchenSettings updates the settings for the current kitchen.
// PUT /api/v1/settings
func (h *Handlers) UpdateKitchenSettings(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	var req models.KitchenSettings
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Merge with existing settings (preserve API key if not sent). Settings read from the API and written
	// back carry masks, which stand for what is stored.
	existing, _ := h.Store.GetKitchenSettings(r.Context(), kitchen)
	req = req.RestoreRedacted(existing)
	if existing != nil {
		if req.ValidationAPIKey == "" {
			req.ValidationAPIKey = existing.ValidationAPIKey
		}
		if req.ValidationProvider == "" {
			req.ValidationProvider = existing.ValidationProvider
		}
		if req.ValidationModel == "" {
			req.ValidationModel = existing.ValidationModel
		}
		if req.ValidationEndpoint == "" {
			req.ValidationEndpoint = existing.ValidationEndpoint
		}
	}

	req.KitchenID = kitchen
	req.UpdatedAt = time.Now().UTC()
	moved, sealErr := h.sealValidationKey(r.Context(), kitchen, &req)
	if sealErr != nil {
		respondError(w, http.StatusInternalServerError, sealErr.Error())
		return
	}

	if err := h.Store.UpsertKitchenSettings(r.Context(), &req); err != nil {
		if moved && (existing == nil || existing.ValidationAPIKey != req.ValidationAPIKey) {
			_ = h.keyVault().Delete(r.Context(), kitchen, validationKeyCredential)
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("kitchen", kitchen).Msg("Kitchen settings updated")

	respondJSON(w, http.StatusOK, req.Redacted())
}

// ══════════════════════════════════════════════════════════════
// ── Prompt Validation Handler ────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ValidatePrompt runs the prompt validator on a specific prompt.
// POST /api/v1/prompts/{promptName}/validate
func (h *Handlers) ValidatePrompt(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())

	prompt, err := h.Store.GetPrompt(r.Context(), kitchen, promptName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	settings, _ := h.resolvedKitchenSettings(r.Context(), kitchen)

	report, err := h.PromptValidator.Validate(r.Context(), prompt, settings)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Validation failed: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"report":  report,
		"edition": h.PromptValidator.Edition(),
	})
}

// ══════════════════════════════════════════════════════════════
// ── Approval Handlers ────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListApprovals returns approval records for the current kitchen.
func (h *Handlers) ListApprovals(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	status := r.URL.Query().Get("status")
	approvals, err := h.Store.ListApprovals(r.Context(), kitchen, status, 100)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if approvals == nil {
		approvals = []models.ApprovalRecord{}
	}
	respondJSON(w, http.StatusOK, h.approvalsOut(r, approvals))
}

// GetApproval returns a single approval record by gate key.
func (h *Handlers) GetApproval(w http.ResponseWriter, r *http.Request) {
	gateKey := chi.URLParam(r, "gateKey")
	record, err := h.Store.GetApproval(r.Context(), gateKey)
	if err != nil || !kitchenOwns(r, record.Kitchen) {
		respondError(w, http.StatusNotFound, "approval not found: "+gateKey)
		return
	}
	respondJSON(w, http.StatusOK, h.approvalOut(r, record))
}

// ApproveGateWithMetadata approves or rejects a gate with full approver identity.
func (h *Handlers) ApproveGateWithMetadata(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	stepName := chi.URLParam(r, "stepName")

	var req struct {
		Approved      bool   `json:"approved"`
		ApproverID    string `json:"approver_id,omitempty"`
		ApproverEmail string `json:"approver_email,omitempty"`
		Channel       string `json:"channel,omitempty"`
		Comments      string `json:"comments,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if _, err := h.runInKitchen(r, runID); err != nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("No pending gate '%s' for run '%s'", stepName, runID))
		return
	}
	// With signatures on, the approver is the verified signer; the fields in the body are ignored.
	if h.DecisionSigner != nil {
		h.approveSignedGate(w, r, runID, stepName, req.Approved, req.Channel, req.Comments)
		return
	}

	// Auto-populate approver identity from the authenticated caller when not explicitly provided.
	if id := pkgmw.GetIdentity(r.Context()); id != nil {
		if req.ApproverID == "" {
			req.ApproverID = id.Subject
		}
		if req.ApproverEmail == "" && id.Email != "" {
			req.ApproverEmail = id.Email
		}
	}

	// Extract OIDC issuer from the caller's token claims for same-tenant validation.
	approverIssuer := ""
	if id := pkgmw.GetIdentity(r.Context()); id != nil {
		approverIssuer = id.Claims["iss"]
	}

	ok, approveErr := h.Workflow.ApproveGateWithMetadata(runID, stepName, req.Approved, req.ApproverID, req.ApproverEmail, approverIssuer, req.Channel, req.Comments)
	if approveErr != nil {
		respondError(w, http.StatusForbidden, approveErr.Error())
		return
	}
	if !ok {
		respondError(w, http.StatusNotFound, fmt.Sprintf("No pending gate '%s' for run '%s'", stepName, runID))
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"run_id":   runID,
		"step":     stepName,
		"approved": req.Approved,
	})
}

// ══════════════════════════════════════════════════════════════
// ── Notification Channel Handlers ────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListChannels returns notification channels for the current kitchen.
func (h *Handlers) ListChannels(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	channels, err := h.Store.ListChannels(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, shownChannels(channels))
}

// GetChannel returns a single notification channel.
func (h *Handlers) GetChannel(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	name := chi.URLParam(r, "channelName")
	channel, err := h.Store.GetChannel(r.Context(), kitchen, name)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, shownChannel(*channel))
}

// CreateChannel creates a new notification channel.
func (h *Handlers) CreateChannel(w http.ResponseWriter, r *http.Request) {
	var ch models.NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if ch.SecretRef != "" {
		respondChannelBodyError(w, errServerSetRef)
		return
	}
	ch.Kitchen = middleware.GetKitchen(r.Context())
	if ch.Name == "" {
		respondError(w, http.StatusBadRequest, "Channel name is required")
		return
	}
	if ch.Kind == "" {
		respondError(w, http.StatusBadRequest, "Channel kind is required")
		return
	}
	if err := checkChannelURL(r.Context(), ch.Kitchen, ch.Kind, ch.URL); err != nil {
		respondError(w, http.StatusBadRequest, "url: "+err.Error())
		return
	}

	ch.ID = uuid.New().String()
	now := time.Now().UTC()
	ch.CreatedAt = now
	ch.UpdatedAt = now
	ch.Active = true

	if err := h.Store.CreateChannel(r.Context(), &ch); err != nil {
		respondCreateError(w, err, "create the channel")
		return
	}
	respondJSON(w, http.StatusCreated, shownChannel(ch))
}

// UpdateChannel updates an existing notification channel.
func (h *Handlers) UpdateChannel(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	name := chi.URLParam(r, "channelName")

	existing, err := h.Store.GetChannel(r.Context(), kitchen, name)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	update, secret, err := decodeChannelUpdate(r.Body)
	if err != nil {
		respondChannelBodyError(w, err)
		return
	}

	if err := checkChannelURL(r.Context(), kitchen, update.Kind, update.URL); err != nil {
		respondError(w, http.StatusBadRequest, "url: "+err.Error())
		return
	}
	existing.Kind = update.Kind
	existing.URL = update.URL
	switch secret {
	case secretReplace:
		existing.Secret = update.Secret
	case secretRemove:
		existing.Secret, existing.SecretRef = "", ""
	}
	existing.Config = update.Config
	existing.Events = update.Events
	existing.Active = update.Active
	existing.UpdatedAt = time.Now().UTC()

	if err := h.Store.UpdateChannel(r.Context(), existing); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, shownChannel(*existing))
}

// DeleteChannel removes a notification channel.
func (h *Handlers) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	name := chi.URLParam(r, "channelName")

	if err := h.Store.DeleteChannel(r.Context(), kitchen, name); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

// ══════════════════════════════════════════════════════════════
// ── Audit Event Handlers ─────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListAuditEvents returns audit events for the current kitchen.
func (h *Handlers) ListAuditEvents(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	q := r.URL.Query()
	filter := models.AuditFilter{
		Kitchen: kitchen,
		Limit:   100,
	}
	if v := q.Get("action"); v != "" {
		filter.Action = v
	}
	if v := q.Get("user_id"); v != "" {
		filter.UserID = v
	}
	// Accept both "resource" (canonical) and "agent" (legacy UI alias)
	if v := q.Get("resource"); v != "" {
		filter.Resource = v
	} else if v := q.Get("agent"); v != "" {
		filter.Resource = v
	}
	if v := q.Get("environment"); v != "" {
		filter.Environment = v
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			filter.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			filter.Offset = n
		}
	}

	events, err := h.Store.ListAuditEvents(r.Context(), filter)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []models.AuditEvent{}
	}

	// Backward-compatible payload for dashboard consumers still reading created_at.
	type auditEventView struct {
		models.AuditEvent
		CreatedAt time.Time `json:"created_at"`
	}
	view := make([]auditEventView, 0, len(events))
	for _, evt := range events {
		view = append(view, auditEventView{AuditEvent: evt, CreatedAt: evt.Timestamp})
	}

	respondJSON(w, http.StatusOK, view)
}

// CountAuditEvents returns the count of audit events matching the filter.
func (h *Handlers) CountAuditEvents(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	q := r.URL.Query()
	filter := models.AuditFilter{Kitchen: kitchen}
	if v := q.Get("action"); v != "" {
		filter.Action = v
	}
	if v := q.Get("user_id"); v != "" {
		filter.UserID = v
	}
	if v := q.Get("resource"); v != "" {
		filter.Resource = v
	} else if v := q.Get("agent"); v != "" {
		filter.Resource = v
	}
	if v := q.Get("environment"); v != "" {
		filter.Environment = v
	}

	count, err := h.Store.CountAuditEvents(r.Context(), filter)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]int64{"count": count})
}

// ── Helpers ──────────────────────────────────────────────────

// joinEnvSlugs returns a comma-separated list of environment slugs for error messages.
func joinEnvSlugs(envs []models.Environment) string {
	slugs := make([]string, len(envs))
	for i, e := range envs {
		slugs[i] = e.Slug
	}
	return strings.Join(slugs, ", ")
}

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	body, ok := checkedBody(data) // trace content only goes out through h.traceOut and its siblings
	if !ok {
		status, body = http.StatusInternalServerError, map[string]string{"error": "response withheld: it carried content without an access check"}
	}
	data = body
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}

// ══════════════════════════════════════════════════════════════
// ── Server Info Handler ──────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ServerInfoHandler returns the server edition, features, limits, and auth config.
// The CLI calls this once per session to discover what the server supports and
// to gate Pro-only commands at runtime.
func (h *Handlers) ServerInfoHandler(w http.ResponseWriter, r *http.Request) {
	if h.ServerInfo == nil {
		respondJSON(w, http.StatusOK, models.CommunityServerInfo("unknown"))
		return
	}
	respondJSON(w, http.StatusOK, h.ServerInfo)
}

// maskProviderKeys returns a copy of the provider that is safe to return to API consumers: its keys
// (config api_key, api_secret and other secret-looking settings, and every key in its rotation pool)
// are masked. See models.ModelProvider.Redacted.
func maskProviderKeys(p *models.ModelProvider) *models.ModelProvider {
	cp := p.Redacted()
	return &cp
}

// ══════════════════════════════════════════════════════════════
// ── Model Catalog Handlers (R8) ─────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListCatalog returns all known model capabilities from the catalog.
func (h *Handlers) ListCatalog(w http.ResponseWriter, r *http.Request) {
	if h.Catalog == nil {
		respondError(w, http.StatusServiceUnavailable, "model catalog not initialized")
		return
	}

	// Optional filter by provider kind
	providerKind := r.URL.Query().Get("provider")
	var caps []*models.ModelCapability
	if providerKind != "" {
		caps = h.Catalog.ListByProvider(providerKind)
	} else {
		caps = h.Catalog.ListAll()
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"models": caps,
		"count":  len(caps),
	})
}

// GetCatalogModel returns capability data for a specific model.
func (h *Handlers) GetCatalogModel(w http.ResponseWriter, r *http.Request) {
	if h.Catalog == nil {
		respondError(w, http.StatusServiceUnavailable, "model catalog not initialized")
		return
	}

	modelID := chi.URLParam(r, "modelID")
	if modelID == "" {
		respondError(w, http.StatusBadRequest, "modelID is required")
		return
	}

	// URL-decode the modelID (it may contain slashes encoded as %2F)
	cap := h.Catalog.LookupByID(modelID)
	if cap == nil {
		// Try with provider prefix
		providerKind := r.URL.Query().Get("provider")
		if providerKind != "" {
			cap = h.Catalog.Lookup(providerKind, modelID)
		}
	}
	if cap == nil {
		respondError(w, http.StatusNotFound, "model not found in catalog: "+modelID)
		return
	}

	respondJSON(w, http.StatusOK, cap)
}

// RefreshCatalog forces a refresh of the model catalog from LiteLLM.
func (h *Handlers) RefreshCatalog(w http.ResponseWriter, r *http.Request) {
	if h.Catalog == nil {
		respondError(w, http.StatusServiceUnavailable, "model catalog not initialized")
		return
	}

	go func() {
		ctx := context.Background()
		h.Catalog.Refresh(ctx)
	}()

	respondJSON(w, http.StatusAccepted, map[string]string{
		"status":  "refreshing",
		"message": "Catalog refresh started in background",
	})
}

// DiscoverModels triggers model discovery for a specific provider.
func (h *Handlers) DiscoverModels(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "providerName")

	// Look up the provider
	provider, err := h.Store.GetProvider(r.Context(), middleware.GetKitchen(r.Context()), providerName)
	if err != nil {
		respondError(w, http.StatusNotFound, "provider not found: "+providerName)
		return
	}

	// Call the discovery driver
	discovered, err := h.Router.DiscoverModelsForProvider(r.Context(), provider)
	if err != nil {
		respondError(w, http.StatusBadRequest, "discovery failed: "+err.Error())
		return
	}

	// Merge into catalog
	if h.Catalog != nil {
		h.Catalog.RegisterDiscovered(discovered)
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"provider":   providerName,
		"discovered": discovered,
		"count":      len(discovered),
	})
}

// ══════════════════════════════════════════════════════════════
// ── Session Handlers (R8) ────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// CreateSession starts a new multi-turn conversation session with an agent.
func (h *Handlers) CreateSession(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")

	if h.Sessions == nil {
		respondError(w, http.StatusServiceUnavailable, "session store not initialized")
		return
	}

	// Verify agent exists
	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusNotFound, "agent not found: "+agentName)
		return
	}
	if agent.Status != models.AgentStatusReady {
		respondError(w, http.StatusConflict, "agent must be in ready status to create sessions")
		return
	}

	// Parse optional config from body
	var req struct {
		Metadata map[string]interface{} `json:"metadata,omitempty"`
		MaxTurns int                    `json:"max_turns,omitempty"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req) // optional body
	}

	session := &models.Session{
		ID:        uuid.New().String(),
		Kitchen:   kitchen,
		AgentName: agentName,
		Status:    models.SessionActive,
		Messages:  []models.ChatMessage{},
		TurnCount: 0,
		MaxTurns:  req.MaxTurns,
		Metadata:  req.Metadata,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := h.Sessions.CreateSession(r.Context(), session); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Through sessionOut like every other session reply: a Session is a content type, and a bare one is
	// refused by respondJSON after the session was already created.
	respondJSON(w, http.StatusCreated, h.sessionOut(r, session))
}

// GetSession retrieves session details.
func (h *Handlers) GetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	if h.Sessions == nil {
		respondError(w, http.StatusServiceUnavailable, "session store not initialized")
		return
	}

	session, err := h.sessionInKitchen(r, sessionID, chi.URLParam(r, "agentName"))
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, h.sessionOut(r, session))
}

// ListSessions lists all sessions for an agent.
func (h *Handlers) ListSessions(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")

	if h.Sessions == nil {
		respondError(w, http.StatusServiceUnavailable, "session store not initialized")
		return
	}

	sessions, err := h.Sessions.ListSessions(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sessions == nil {
		sessions = []models.Session{}
	}

	respondJSON(w, http.StatusOK, h.sessionsOut(r, sessions))
}

// DeleteSession removes a session.
func (h *Handlers) DeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	if h.Sessions == nil {
		respondError(w, http.StatusServiceUnavailable, "session store not initialized")
		return
	}

	if _, err := h.sessionInKitchen(r, sessionID, chi.URLParam(r, "agentName")); err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := h.Sessions.DeleteSession(r.Context(), sessionID); err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// SendSessionMessage sends a user message to a session and gets the agent's response.
func (h *Handlers) SendSessionMessage(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")
	sessionID := chi.URLParam(r, "sessionID")

	if h.Sessions == nil {
		respondError(w, http.StatusServiceUnavailable, "session store not initialized")
		return
	}

	// Parse user message
	var req models.SessionMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" && len(req.ContentParts) == 0 {
		respondError(w, http.StatusBadRequest, "content is required")
		return
	}

	// Get existing session
	session, err := h.sessionInKitchen(r, sessionID, agentName)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	if session.Status != models.SessionActive {
		respondError(w, http.StatusConflict, "session is not active (status: "+string(session.Status)+")")
		return
	}
	if session.MaxTurns > 0 && session.TurnCount >= session.MaxTurns {
		respondError(w, http.StatusConflict, "session has reached max turns")
		return
	}

	// Verify agent
	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusNotFound, "agent not found: "+agentName)
		return
	}

	// Append user message to session history
	transcript, parts, err := h.transcribeAudioParts(r.Context(), agent, req.ContentParts)
	if err != nil {
		if respondRefusal(w, err) {
			return
		}
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	content := req.Content
	if transcript != "" {
		content = strings.TrimSpace(content + " " + transcript)
	}
	// Media bytes live in the blob store; the session keeps a reference, so a long
	// conversation about a PDF does not carry the PDF in every stored message.
	if msg, ok := h.checkBlobRefs(r, kitchen, parts); !ok {
		respondError(w, http.StatusNotFound, msg)
		return
	}
	userMsg := h.Router.ExternalizeMedia(r.Context(), kitchen, models.ChatMessage{
		Role:         "user",
		Content:      content,
		ContentParts: parts,
	})
	session.Messages = append(session.Messages, userMsg)

	// ── Input Guardrails (Session) ──────────────────────────
	gr, grOK := h.requireGuardrails(w, r, agent, agent.Guardrails)
	if !grOK {
		return
	}
	if h.Guardrails != nil && len(gr) > 0 {
		// content includes the transcript of any spoken parts, so speech is checked too.
		eval, gErr := h.Guardrails.EvaluateInput(r.Context(), gr, content)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Session input guardrail error")
			h.emitGuardrailAudit(r.Context(), kitchen, agentName, "input", nil, gErr)
		} else if !eval.Passed {
			// Remove the user message we just appended
			session.Messages = session.Messages[:len(session.Messages)-1]
			h.emitGuardrailAudit(r.Context(), kitchen, agentName, "input", eval, nil)
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Input blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		} else {
			if eval.Redacted > 0 {
				// The session is stored, so the message in it is the redacted one.
				content = eval.Apply(content)
				session.Messages[len(session.Messages)-1].Content = content
			}
			h.noteGuardrails(r.Context(), kitchen, agentName, "input", eval)
		}
	}

	// A hosted framework agent (LangChain, LangGraph, Deep Agents, ...) answers with its
	// own code. Never fall through to the raw model router for one.
	if agent.IsFrameworkNative() {
		if len(parts) > 0 {
			// The hosted process is sent text only. Dropping the attachment would answer a
			// different question than the one asked, so refuse it.
			session.Messages = session.Messages[:len(session.Messages)-1]
			respondError(w, http.StatusUnprocessableEntity, "this agent runs its own framework and takes text only; images, documents and other media can be sent to agents that run on AgentOven's own loop")
			return
		}
		h.sendNativeSessionMessage(w, r, agent, session, content, gr)
		return
	}

	// Route through model router with sliding context window
	startTime := time.Now()

	// ── Sliding Context Window ──────────────────────────────
	// Determine the context budget from agent config and model catalog.
	modelLimit := 0
	if h.Catalog != nil && agent.ModelName != "" {
		if cap := h.Catalog.LookupByID(agent.ModelName); cap != nil {
			modelLimit = cap.ContextWindow
		}
	}
	budget := ctxwindow.EffectiveBudget(agent.ContextBudget, modelLimit)

	// Estimate tokens for the full conversation
	allTokens := ctxwindow.EstimateTokensForMessages(session.Messages)
	summarised := false

	// If over budget, trim older messages and insert a text-based summary
	contextMessages := session.Messages
	if allTokens > budget {
		kept, toSummarize := ctxwindow.TrimToSlidingWindow(session.Messages, budget)
		if len(toSummarize) > 0 {
			summaryText := ctxwindow.FormatSummaryMessage(toSummarize)
			summaryMsg := models.ChatMessage{
				Role:    "system",
				Content: "[Summary of earlier conversation]\n" + summaryText,
			}
			// Insert summary after system messages, before recent messages
			contextMessages = make([]models.ChatMessage, 0, len(kept)+1)
			// Find where system messages end
			insertIdx := 0
			for i, m := range kept {
				if m.Role != "system" {
					insertIdx = i
					break
				}
				contextMessages = append(contextMessages, m)
				insertIdx = i + 1
			}
			contextMessages = append(contextMessages, summaryMsg)
			contextMessages = append(contextMessages, kept[insertIdx:]...)
			summarised = true

			log.Debug().
				Str("session", sessionID).
				Int("original_msgs", len(session.Messages)).
				Int("summarized", len(toSummarize)).
				Int("kept", len(kept)).
				Int("budget", budget).
				Int("estimated", allTokens).
				Msg("Session context trimmed via sliding window")
		}
	}

	// Recalculate used tokens after trimming
	usedTokens := ctxwindow.EstimateTokensForMessages(contextMessages)

	routeReq := &models.RouteRequest{
		Kitchen:       kitchen,
		Messages:      contextMessages,
		SessionID:     sessionID,
		AgentRef:      agentName,
		EnableCaching: true, // enable prompt caching for sessions by default
	}

	// Apply prompt-cache breakpoints (marks last system message)
	routeReq.Messages = ctxwindow.MarkCacheBreakpoints(routeReq.Messages)
	// Apply agent's model if configured
	if agent.ModelName != "" {
		routeReq.Model = agent.ModelName
	}

	// The input is judged above, with the agent's rules and the kitchen's, and the router does not judge it
	// again. The reply is judged by this handler's rules too, but inside the router, before it records the call
	// (replyJudge), so the trace and the capture it writes hold only what the caller may see.
	routeCtx := router.ContextReplyJudge(router.ContextGuardrailsApplied(r.Context()), h.replyJudge(kitchen, agentName, gr))

	// Use RouteWithBackup if agent has a backup provider configured
	var routeResp *models.RouteResponse
	if agent.BackupProvider != "" {
		routeResp, err = h.Router.RouteWithBackup(routeCtx, routeReq, agent.BackupProvider, agent.BackupModel)
	} else {
		routeResp, err = h.Router.Route(routeCtx, routeReq)
	}
	if err != nil {
		// The turn did not happen, so it must not stay in the history: a message the model
		// refused (an image sent to a text-only model) would otherwise fail every later turn.
		session.Messages = session.Messages[:len(session.Messages)-1]
		if router.IsMediaError(err) {
			respondError(w, http.StatusUnprocessableEntity, "model routing failed: "+err.Error())
			return
		}
		if respondRefusal(w, err) {
			return
		}
		respondError(w, http.StatusInternalServerError, "model routing failed: "+err.Error())
		return
	}
	latencyMs := time.Since(startTime).Milliseconds()

	// Append assistant response to session
	assistantMsg := models.ChatMessage{
		Role:    "assistant",
		Content: routeResp.Content,
	}
	session.Messages = append(session.Messages, assistantMsg)
	session.TurnCount++
	session.TotalTokens += routeResp.Usage.TotalTokens
	session.TotalCost += routeResp.Usage.EstimatedCost
	session.UpdatedAt = time.Now().UTC()

	// Check if max turns reached
	if session.MaxTurns > 0 && session.TurnCount >= session.MaxTurns {
		session.Status = models.SessionCompleted
	}

	// Update session in store
	if err := h.Sessions.UpdateSession(r.Context(), session); err != nil {
		log.Error().Err(err).Str("session", sessionID).Msg("Failed to update session")
	}

	// Compute context budget report for the response
	// Include output tokens in the used count for accurate reporting
	finalUsed := usedTokens + int(routeResp.Usage.OutputTokens)
	cacheHits := routeResp.Usage.CacheHits
	tokensSaved := int(routeResp.Usage.CachedTokens)
	budgetReport := ctxwindow.BuildReport(modelLimit, budget, finalUsed, summarised, cacheHits, tokensSaved)

	resp := models.SessionResponse{
		SessionID:     sessionID,
		TurnNumber:    session.TurnCount,
		Content:       routeResp.Content,
		FinishReason:  routeResp.FinishReason,
		Usage:         routeResp.Usage,
		ContextBudget: budgetReport,
		LatencyMs:     latencyMs,
		Status:        session.Status,
	}

	respondJSON(w, http.StatusOK, resp)
}

// StreamComplianceFailures streams guardrail-related compliance failures from audit events.
// GET /api/v1/compliance/failures/stream
func (h *Handlers) StreamComplianceFailures(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	inttelemetry.ComplianceStreamConnected(r.Context(), kitchen)
	defer inttelemetry.ComplianceStreamDisconnected(r.Context(), kitchen)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	seen := map[string]struct{}{}
	emit := func(events []models.AuditEvent) {
		for i := len(events) - 1; i >= 0; i-- {
			e := events[i]
			if !strings.HasPrefix(e.Action, "guardrail.") {
				continue
			}
			if _, ok := seen[e.ID]; ok {
				continue
			}
			seen[e.ID] = struct{}{}
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		flusher.Flush()
	}

	initial, err := h.Store.ListAuditEvents(r.Context(), models.AuditFilter{Kitchen: kitchen, Limit: 200})
	if err == nil {
		emit(initial)
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			events, err := h.Store.ListAuditEvents(r.Context(), models.AuditFilter{Kitchen: kitchen, Limit: 200})
			if err != nil {
				continue
			}
			emit(events)
		}
	}
}

func (h *Handlers) emitGuardrailAudit(ctx context.Context, kitchen, agentName, stage string, eval *models.GuardrailEvaluation, guardrailErr error) {
	if h.Store == nil {
		return
	}

	userID, userEmail, displayName, provider, actorType := auditActorFromIdentity(ctx)

	action, status := guardrailOutcome(stage, eval)
	details := map[string]interface{}{
		"agent":    agentName,
		"stage":    stage,
		"provider": provider,
	}

	if guardrailErr != nil {
		action = "guardrail." + stage + "_error"
		status = http.StatusInternalServerError
		details["error"] = guardrailErr.Error()
	}
	if eval != nil {
		details["results"] = eval.Results
		details["passed"] = eval.Passed
		details["redacted"], details["warned"] = eval.Redacted, eval.Warned
	}

	if err := h.Store.CreateAuditEvent(ctx, &models.AuditEvent{
		ID:                 uuid.New().String(),
		Timestamp:          time.Now().UTC(),
		UserID:             userID,
		UserEmail:          userEmail,
		ActorType:          actorType,
		ActorDisplayName:   displayName,
		Action:             action,
		Resource:           "guardrails",
		ResourceID:         agentName,
		Kitchen:            kitchen,
		Details:            details,
		ResponseStatus:     status,
		RegulationTags:     []string{"compliance", "guardrails"},
		DataClassification: "restricted",
	}); err != nil {
		log.Warn().Err(err).Str("agent", agentName).Str("action", action).Msg("failed to persist guardrail audit event")
		return
	}

	inttelemetry.RecordAuditEvent(ctx, kitchen, action, status)
	if strings.Contains(action, "_blocked") {
		inttelemetry.RecordGuardrailBlocked(ctx, kitchen, stage)
	}
}

// ══════════════════════════════════════════════════════════════
// ── Agent Card Handler (R8) ──────────────────────────────────
// ══════════════════════════════════════════════════════════════

// GetAgentCard returns an A2A-compatible agent card for a specific agent.
func (h *Handlers) GetAgentCard(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusNotFound, "agent not found: "+agentName)
		return
	}

	// Build agent card from agent metadata
	card := models.AgentCard{
		Name:        agent.Name,
		Description: agent.Description,
		Version:     agent.Version,
		Provider: models.AgentCardProvider{
			Organization: "AgentOven",
		},
	}

	// Build capabilities from agent's ingredients and status
	card.Capabilities = models.AgentCapabilities{
		Streaming: true,
		Sessions:  true,
	}

	// Determine supported input/output modes
	card.InputModes = []string{"text"}
	card.OutputModes = []string{"text"}

	// Check for tool use in ingredients
	for _, ing := range agent.Ingredients {
		if ing.Kind == "tool" {
			card.Capabilities.ToolCalling = true
			// Add as a skill
			card.Skills = append(card.Skills, models.AgentSkill{
				ID:          ing.Name,
				Name:        ing.Name,
				Description: "MCP tool: " + ing.Name,
			})
		}
	}

	// What the agent can take in and give out is what its provider offers.
	if h.Router != nil {
		if prov, model, err := h.agentProvider(r.Context(), agent); err == nil {
			card.Modalities = h.Router.Modalities(prov, model)
			for _, m := range card.Modalities {
				switch m {
				case models.ModalityImage:
					card.Capabilities.Vision = true
					card.InputModes = append(card.InputModes, m)
				case models.ModalityPDF, models.ModalityVideo:
					card.InputModes = append(card.InputModes, m)
				case models.ModalityAudio:
					card.InputModes = append(card.InputModes, m)
					card.OutputModes = append(card.OutputModes, m)
				}
			}
			if h.Catalog != nil {
				if cap := h.Catalog.Lookup(prov.Kind, model); cap != nil && cap.SupportsJSON {
					card.Capabilities.StructuredOutput = true
				}
			}
		}
	}

	// Set URL — always the stable control plane endpoint (ADR-0007).
	// Never expose subprocess/backend URLs in the agent card.
	card.URL = "/agents/" + agent.Name + "/a2a"

	respondJSON(w, http.StatusOK, card)
}

// ══════════════════════════════════════════════════════════════
// ── Discovery-Capable Providers Handler (R8) ────────────────
// ══════════════════════════════════════════════════════════════

// ListDiscoveryDrivers returns which provider kinds support model discovery.
func (h *Handlers) ListDiscoveryDrivers(w http.ResponseWriter, r *http.Request) {
	drivers := h.Router.ListDiscoveryCapableDrivers()
	var kinds []string
	for k := range drivers {
		kinds = append(kinds, k)
	}
	if kinds == nil {
		kinds = []string{}
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"discovery_capable": kinds,
	})
}

// ══════════════════════════════════════════════════════════════
// ── Scoped API Key Handlers ──────────────────────────────────
// ══════════════════════════════════════════════════════════════

// HandleCreateScopedKey creates a new scoped API key.
// The plain-text key is returned ONLY in this response — it cannot be retrieved again.
// POST /api/v1/keys
func (h *Handlers) HandleCreateScopedKey(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	var req struct {
		Label      string   `json:"label"`
		AgentNames []string `json:"agent_names"` // ["*"] for all agents
		MaxCalls   int      `json:"max_calls"`   // 0 = unlimited
		ExpiresIn  string   `json:"expires_in"`  // e.g. "24h", "7d", "" = never
		// OwnerEmail names the person the key belongs to. With an owner policy installed (Pro) it
		// is mandatory: left out it is the caller, and the person must be a member of the kitchen.
		OwnerEmail string `json:"owner_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Label == "" {
		respondError(w, http.StatusBadRequest, "label is required")
		return
	}
	var owner *contracts.CredentialOwner
	if h.CredentialOwners != nil {
		var err error
		if owner, err = h.CredentialOwners.ResolveOwner(r, kitchen, req.OwnerEmail, ""); err != nil {
			var ae *contracts.AuthError
			if errors.As(err, &ae) {
				respondJSON(w, ae.Status, map[string]string{"error": ae.Code, "message": ae.Message})
				return
			}
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if len(req.AgentNames) == 0 {
		respondError(w, http.StatusBadRequest, "agent_names is required (use [\"*\"] for all agents)")
		return
	}

	// Generate a cryptographically random key: ao_sk_<32 random bytes hex>
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to generate key")
		return
	}
	plainKey := fmt.Sprintf("ao_sk_%x", keyBytes)
	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(plainKey)))
	keyPrefix := plainKey[:12] // "ao_sk_" + first 6 hex chars

	// Parse expiration
	var expiresAt *time.Time
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			// Try day-based format: "7d" → 7 * 24h
			if strings.HasSuffix(req.ExpiresIn, "d") {
				days, err2 := strconv.Atoi(strings.TrimSuffix(req.ExpiresIn, "d"))
				if err2 != nil || days <= 0 {
					respondError(w, http.StatusBadRequest, "Invalid expires_in format (use e.g. '24h', '7d')")
					return
				}
				d = time.Duration(days) * 24 * time.Hour
			} else {
				respondError(w, http.StatusBadRequest, "Invalid expires_in format (use e.g. '24h', '7d')")
				return
			}
		}
		t := time.Now().UTC().Add(d)
		expiresAt = &t
	}

	// Determine creator from identity
	createdBy := "system"
	if identity := pkgmw.GetIdentity(r.Context()); identity != nil {
		createdBy = identity.Subject
	}

	key := &models.ScopedAPIKey{
		ID:         uuid.New().String(),
		KeyHash:    keyHash,
		KeyPrefix:  keyPrefix,
		Kitchen:    kitchen,
		AgentNames: req.AgentNames,
		Label:      req.Label,
		MaxCalls:   req.MaxCalls,
		ExpiresAt:  expiresAt,
		CreatedBy:  createdBy,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if owner != nil {
		key.OwnerUserID, key.OwnerEmail = owner.UserID, owner.Email
	}

	if err := h.Store.CreateScopedKey(r.Context(), key); err != nil {
		respondCreateError(w, err, "create the key")
		return
	}

	// Return the plain key ONCE — it cannot be retrieved again
	respondJSON(w, http.StatusCreated, map[string]interface{}{
		"id":          key.ID,
		"key":         plainKey, // only time it's exposed
		"key_prefix":  key.KeyPrefix,
		"label":       key.Label,
		"kitchen":     key.Kitchen,
		"agent_names": key.AgentNames,
		"max_calls":   key.MaxCalls,
		"expires_at":  key.ExpiresAt,
		"created_by":  key.CreatedBy,
		"owner_email": key.OwnerEmail,
		"created_at":  key.CreatedAt,
	})
}

// HandleListScopedKeys lists all scoped keys in the kitchen (hashes hidden).
// GET /api/v1/keys
func (h *Handlers) HandleListScopedKeys(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	keys, err := h.Store.ListScopedKeys(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, keys)
}

// HandleGetScopedKey returns a single scoped key by ID.
// GET /api/v1/keys/{keyID}
func (h *Handlers) HandleGetScopedKey(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	keyID := chi.URLParam(r, "keyID")

	key, err := h.Store.GetScopedKey(r.Context(), kitchen, keyID)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	respondJSON(w, http.StatusOK, key)
}

// HandleRevokeScopedKey revokes a scoped key (soft delete).
// POST /api/v1/keys/{keyID}/revoke
func (h *Handlers) HandleRevokeScopedKey(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	keyID := chi.URLParam(r, "keyID")

	if err := h.Store.RevokeScopedKey(r.Context(), kitchen, keyID); err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "revoked", "id": keyID})
}

// HandleGetScopedKeyUsage returns usage stats for a scoped key.
// GET /api/v1/keys/{keyID}/usage
func (h *Handlers) HandleGetScopedKeyUsage(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	keyID := chi.URLParam(r, "keyID")

	key, err := h.Store.GetScopedKey(r.Context(), kitchen, keyID)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	remaining := -1 // unlimited
	if key.MaxCalls > 0 {
		remaining = key.MaxCalls - key.CallCount
		if remaining < 0 {
			remaining = 0
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"id":              key.ID,
		"label":           key.Label,
		"call_count":      key.CallCount,
		"max_calls":       key.MaxCalls,
		"remaining_calls": remaining,
		"revoked":         key.Revoked,
		"expired":         key.IsExpired(),
		"quota_exceeded":  key.IsQuotaExceeded(),
	})
}

// HandleDeleteScopedKey permanently removes a scoped key.
// DELETE /api/v1/keys/{keyID}
func (h *Handlers) HandleDeleteScopedKey(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	keyID := chi.URLParam(r, "keyID")

	if err := h.Store.DeleteScopedKey(r.Context(), kitchen, keyID); err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// SetKitchenOwnerCheck installs the rule that decides whether the caller administers a given kitchen.
// Without one the caller's role decides (contracts.IsKitchenOwner and IsPlatformAdmin), which in the
// community edition means "admin". Pro installs a check that also requires a person to head the
// kitchen they name, so a Head Chef of one kitchen holds no authority in another (ADR-0040).
func (h *Handlers) SetKitchenOwnerCheck(fn func(r *http.Request, kitchen string) bool) {
	h.kitchenOwnerCheck = fn
}

// callerOwnsKitchen reports whether the authenticated caller may administer kitchen: a kitchen-owner
// role and, when a check is installed, that check. Nobody authenticated is refused.
func (h *Handlers) callerOwnsKitchen(r *http.Request, kitchen string) bool {
	id := pkgmw.GetIdentity(r.Context())
	if id == nil || !contracts.IsKitchenOwner(id.Role) {
		return false
	}
	return h.kitchenOwnerCheck == nil || h.kitchenOwnerCheck(r, kitchen)
}

// callerAdminsPlatformFor reports whether the caller is a platform administrator who may act on
// kitchen (a grant reaches a second kitchen, so it is the platform's decision).
func (h *Handlers) callerAdminsPlatformFor(r *http.Request, kitchen string) bool {
	id := pkgmw.GetIdentity(r.Context())
	if id == nil || !contracts.IsPlatformAdmin(id.Role) {
		return false
	}
	return h.kitchenOwnerCheck == nil || h.kitchenOwnerCheck(r, kitchen)
}

// AdminRetireAllDeployments stops every active agent process in the current kitchen
// and marks them cooled. Requires admin identity.
// DELETE /api/v1/admin/deployments
func (h *Handlers) AdminRetireAllDeployments(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	// Kitchen owners only (admin, or in Pro a head chef or restaurateur)
	if !h.callerOwnsKitchen(r, kitchen) {
		respondError(w, http.StatusForbidden, "kitchen owner role required")
		return
	}

	agents, err := h.Store.ListAgents(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	retired := 0
	for i := range agents {
		ag := &agents[i]
		switch ag.Status {
		case models.AgentStatusReady, models.AgentStatusBaking, models.AgentStatusBurnt:
			if h.ProcessManager != nil {
				if stopErr := h.ProcessManager.Stop(r.Context(), kitchen, ag.Name); stopErr != nil {
					log.Warn().Err(stopErr).Str("agent", ag.Name).Msg("Failed to stop agent process during retire-all")
				}
			}
			// Only the status and the process change, guarded by the status read above: the list was read
			// before the processes were stopped, and an edit or bake made since must not be put back.
			applied, updateErr := h.Store.UpdateAgentLifecycle(r.Context(), kitchen, ag.Name, models.AgentLifecycleGuard{Status: ag.Status},
				models.AgentLifecycle{Status: models.AgentStatusCooled, ClearProcess: true})
			if updateErr != nil {
				log.Warn().Err(updateErr).Str("agent", ag.Name).Msg("Failed to update agent status during retire-all")
				continue
			}
			if !applied {
				continue // it changed state meanwhile (a bake finished, someone cooled it): not retired by this request
			}
			retired++
			log.Info().Str("agent", ag.Name).Str("kitchen", kitchen).Msg("Agent retired (bulk)")
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{"retired": retired, "kitchen": kitchen})
}

// ── Cross-Kitchen Grant Handlers ─────────────────────────────────────────────

// HandleListCrossKitchenGrants lists all active grants for the current kitchen.
// GET /api/v1/admin/cross-kitchen-grants
func (h *Handlers) HandleListCrossKitchenGrants(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	if !h.callerOwnsKitchen(r, kitchen) {
		respondError(w, http.StatusForbidden, "kitchen owner role required")
		return
	}
	grants, err := h.Store.ListCrossKitchenGrants(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if grants == nil {
		grants = []models.CrossKitchenGrant{}
	}
	respondJSON(w, http.StatusOK, grants)
}

// HandleCreateCrossKitchenGrant creates a new grant.
// POST /api/v1/admin/cross-kitchen-grants
func (h *Handlers) HandleCreateCrossKitchenGrant(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	if !h.callerAdminsPlatformFor(r, kitchen) {
		respondError(w, http.StatusForbidden, "platform administrator role required")
		return
	}

	var req models.CrossKitchenGrant
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// The target kitchen of the grant is always the current kitchen; the source
	// is specified by the caller (which kitchen is being granted access).
	req.ID = uuid.New().String()
	req.TargetKitchen = kitchen
	req.CreatedAt = time.Now().UTC()
	if id := pkgmw.GetIdentity(r.Context()); id != nil {
		req.CreatedBy = id.Subject
	}
	if req.AllowedAgents == nil {
		req.AllowedAgents = []string{}
	}

	if err := h.Store.CreateCrossKitchenGrant(r.Context(), &req); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("id", req.ID).Str("source", req.SourceKitchen).Str("target", kitchen).Msg("Cross-kitchen grant created")
	respondJSON(w, http.StatusCreated, req)
}

// HandleRevokeCrossKitchenGrant revokes (soft-deletes) a grant.
// DELETE /api/v1/admin/cross-kitchen-grants/{grantId}
func (h *Handlers) HandleRevokeCrossKitchenGrant(w http.ResponseWriter, r *http.Request) {
	if !h.callerAdminsPlatformFor(r, middleware.GetKitchen(r.Context())) {
		respondError(w, http.StatusForbidden, "platform administrator role required")
		return
	}
	grantID := chi.URLParam(r, "grantId")
	if err := h.Store.RevokeCrossKitchenGrant(r.Context(), grantID); err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	log.Info().Str("id", grantID).Msg("Cross-kitchen grant revoked")
	respondJSON(w, http.StatusOK, map[string]string{"status": "revoked", "id": grantID})
}

// a2aFilePart is the A2A "file" part body: either inline base64 bytes or a URI.
type a2aFilePart struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType"`
	Bytes    string `json:"bytes,omitempty"` // base64
	URI      string `json:"uri,omitempty"`
	// BlobRef names a file uploaded with POST /api/v1/blobs: an AgentOven extension, so a large
	// file is sent once and referenced after that. Only the kitchen that uploaded it can use it.
	BlobRef string `json:"blob_ref,omitempty"`
}

func a2aFileToContentPart(f *a2aFilePart) models.ContentPart {
	media := &models.MediaRef{MimeType: f.MimeType, Name: f.Name}
	switch {
	case f.BlobRef != "":
		media.BlobRef = f.BlobRef
	case f.Bytes != "":
		media.Data = f.Bytes
	default:
		media.URL = f.URI
	}
	kind := "file"
	mime := strings.ToLower(f.MimeType)
	switch {
	case strings.HasPrefix(mime, "image/"):
		kind = "image"
	case strings.HasPrefix(mime, "audio/"):
		kind = "audio"
	case strings.HasPrefix(mime, "video/"):
		kind = "video"
	}
	return models.ContentPart{Type: kind, Media: media}
}

// EffectiveGuardrails returns the guardrails to enforce for an agent: its own list
// combined with the kitchen's workspace guardrails (global rules), per ADR-0013.
func (h *Handlers) EffectiveGuardrails(ctx context.Context, agent *models.Agent) ([]models.Guardrail, error) {
	return h.guardrailsFor(ctx, agent, agent.Guardrails)
}

// guardrailsFor returns the guardrails to enforce for the agent: own (its list, or that
// list after an environment policy) with the workspace guardrails applied on top. It
// fails closed: an error means the workspace guardrails could not be established and the
// agent must not run (guardrails.Effective).
func (h *Handlers) guardrailsFor(ctx context.Context, agent *models.Agent, own []models.Guardrail) ([]models.Guardrail, error) {
	return grails.Effective(ctx, h.GuardrailPolicy, agent.Kitchen, agent.Name, own)
}

// requireGuardrails resolves the guardrails to enforce or refuses the request. When the
// workspace guardrails cannot be established the agent is not run: the caller gets a 503
// with nothing about the rules, and the failure is audited.
func (h *Handlers) requireGuardrails(w http.ResponseWriter, r *http.Request, agent *models.Agent, own []models.Guardrail) ([]models.Guardrail, bool) {
	gr, err := h.guardrailsFor(r.Context(), agent, own)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.Name).Str("kitchen", agent.Kitchen).Msg("refusing request: workspace guardrails could not be applied")
		h.emitGuardrailAudit(r.Context(), agent.Kitchen, agent.Name, "policy", nil, err)
		respondError(w, http.StatusServiceUnavailable, "the guardrail policy for this agent could not be applied, so the agent was not run")
		return nil, false
	}
	return gr, true
}

// SetSubagentRuntime installs the runtime that lets agents spawn subagents they describe
// themselves (the task and tasks tools). Without one, agents can only delegate to registered
// agents. Pass nil to remove it.
func (h *Handlers) SetSubagentRuntime(rt contracts.SubagentRuntime) {
	if h.Executor != nil {
		h.Executor.SetSubagentRuntime(rt)
	}
}

// SetNativeToolRunner installs the runner for tools of transport "native" (a tool pack's tools).
// Without one such a tool fails with "this edition cannot run tool packs". Call it before serving.
func (h *Handlers) SetNativeToolRunner(r contracts.NativeToolRunner) {
	if h.MCPGateway != nil {
		h.MCPGateway.SetNativeToolRunner(r)
	}
}

// SetToolApprovalPolicy makes the tools the policy names wait for a person's approval in every
// agent's run, and makes the MCP endpoint refuse them (it has no way to ask). Call it before serving.
func (h *Handlers) SetToolApprovalPolicy(p contracts.ToolApprovalPolicy) {
	if h.MCPGateway != nil {
		h.MCPGateway.SetToolApprovalPolicy(p)
	}
	if h.Executor != nil {
		h.Executor.SetToolApprovalPolicy(p)
	}
}

// SetCredentialOwnerPolicy makes every new scoped key belong to a person the policy accepts.
func (h *Handlers) SetCredentialOwnerPolicy(p contracts.CredentialOwnerPolicy) {
	h.CredentialOwners = p
}

// SetGuardrailPolicy makes workspace guardrails apply to every request this handler
// serves and to every tool call the executor dispatches. Pass nil to turn them off.
func (h *Handlers) SetGuardrailPolicy(src contracts.WorkspaceGuardrailSource) {
	h.GuardrailPolicy = src
	if h.Executor != nil {
		h.Executor.SetGuardrailPolicy(src)
	}
}

// SetCustomGuardrails installs the evaluator that judges guardrails of kind custom (Pro: a signed
// webhook to an endpoint the kitchen configured). Without one a custom rule passes, as it always
// has in the community edition. It reports false when the guardrail service in use takes none.
func (h *Handlers) SetCustomGuardrails(ev contracts.CustomGuardrailEvaluator) bool {
	svc, ok := h.Guardrails.(interface {
		SetCustom(contracts.CustomGuardrailEvaluator)
	})
	if ok {
		svc.SetCustom(ev)
	}
	return ok
}

// withRunTrace chooses the trace ID of the run about to start and records it on the context. The
// executor uses it as the run's ID, and every audit event the run causes (a guardrail verdict, a
// budget pause, an approval) carries it, which is what lets a trace show its governance events.
func withRunTrace(ctx context.Context) (context.Context, string) {
	id := uuid.New().String()
	ctx = executor.WithTraceID(ctx, id)
	return models.WithTraceStamp(ctx, models.TraceStamp{TraceID: id}), id
}

// recordBlockedRun records a run whose reply an output guardrail withheld. The run happened (the model
// was called and paid for) and the guardrail's verdict is in the audit log under its trace ID, so the run
// has to exist as a trace for the two to be read together. Until this existed a blocked run left no trace.
//
// The community edition does not keep the withheld reply: the trace carries no output text and the last
// turn's response is blanked in its spans. A deployment that captures complete model calls (see
// models.LLMCallCapturer) holds the reply in its capture, behind whatever access it gives captures.
func (h *Handlers) recordBlockedRun(ctx context.Context, agent *models.Agent, input string, trace *executor.ExecutionTrace) {
	if h.Store == nil || trace == nil {
		return
	}
	usage := trace.Usage
	tr := &models.Trace{
		ID: trace.TraceID, AgentName: agent.Name, AgentVersion: agent.Version, Kitchen: agent.Kitchen,
		Status: "blocked", DurationMs: trace.TotalMs, TotalTokens: usage.TotalTokens, CostUSD: usage.EstimatedCost,
		InputText: input, SessionID: trace.SessionID, Usage: &usage,
		Metadata:  map[string]interface{}{"mode": "managed", "turns": len(trace.Turns), "type": "invoke", "blocked_by": "output_guardrail"},
		CreatedAt: time.Now().UTC(),
	}
	if err := h.Store.CreateTrace(ctx, tr); err != nil {
		log.Warn().Err(err).Str("trace_id", trace.TraceID).Msg("Failed to record the trace of a blocked run")
		return
	}
	withheld := *trace
	withheld.Turns = append([]executor.Turn(nil), trace.Turns...)
	if n := len(withheld.Turns); n > 0 {
		withheld.Turns[n-1].Response = ""
	}
	h.persistExecutorSpans(ctx, &withheld)
}
