package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// bakeSerializer makes the background step of a bake or re-cook run one at a time per agent, and only
// for the newest request. Several overlapping re-cooks of one agent used to start one hosted process
// each, and the older one could finish last and restore an older state. Now every request takes a
// generation number; the step takes the agent's lock, and a step whose generation is no longer the
// newest does nothing, because the newer request will do the same work with the newer configuration.
type bakeSerializer struct {
	mu     sync.Mutex
	gens   map[string]uint64
	locks  map[string]*bakeLock
	issued uint64
}

type bakeLock struct {
	mu      sync.Mutex
	pending int // steps begun and not finished, so the entry can be dropped when it is idle
}

// begin registers a new bake or re-cook of the agent and returns its generation. Exactly one run must
// follow each begin: the registration keeps the agent's entry alive until that step has finished, so
// an entry is dropped only when no step is pending.
func (b *bakeSerializer) begin(agentKey string) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gens == nil {
		b.gens, b.locks = map[string]uint64{}, map[string]*bakeLock{}
	}
	b.issued++
	b.gens[agentKey] = b.issued
	l := b.locks[agentKey]
	if l == nil {
		l = &bakeLock{}
		b.locks[agentKey] = l
	}
	l.pending++
	return b.issued
}

// run calls fn with the agent's lock held, unless a newer request has begun since gen. fn is told how
// to ask whether it has been superseded since, for the places where it waits.
func (b *bakeSerializer) run(agentKey string, gen uint64, fn func(superseded func() bool)) {
	b.mu.Lock()
	l := b.locks[agentKey]
	b.mu.Unlock()

	l.mu.Lock()
	defer func() {
		l.mu.Unlock()
		b.mu.Lock()
		if l.pending--; l.pending == 0 {
			delete(b.locks, agentKey)
			delete(b.gens, agentKey)
		}
		b.mu.Unlock()
	}()

	superseded := func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.gens[agentKey] != gen
	}
	if superseded() {
		return
	}
	fn(superseded)
}

// lifecycle applies a change to an agent's status, tags, backend and process under the guard, and logs
// when the agent has moved on and the change was dropped.
func (h *Handlers) lifecycle(kitchen, name string, guard models.AgentLifecycleGuard, change models.AgentLifecycle) bool {
	applied, err := h.Store.UpdateAgentLifecycle(context.Background(), kitchen, name, guard, change)
	switch {
	case err != nil:
		log.Warn().Err(err).Str("agent", name).Str("status", string(change.Status)).Msg("bake: cannot record the outcome")
	case !applied:
		log.Info().Str("agent", name).Str("wanted", string(change.Status)).Msg("bake: the agent changed since this bake began, outcome dropped")
	}
	return applied && err == nil
}

// burnt marks the agent burnt with the reason in its error tag.
func (h *Handlers) burnt(kitchen, name string, guard models.AgentLifecycleGuard, reason string, extra models.AgentLifecycle) {
	extra.Status = models.AgentStatusBurnt
	if extra.SetTags == nil {
		extra.SetTags = map[string]string{}
	}
	extra.SetTags["error"] = reason
	h.lifecycle(kitchen, name, guard, extra)
}

// finishBake is the background step of a bake (recook false) or a re-cook (recook true): it tests the
// provider, starts the agent's process (a re-cook replaces it) and moves the agent from baking to
// ready or burnt. It records the outcome through UpdateAgentLifecycle only, so what a person edited
// while it ran is never written over, and it does nothing if the agent was cooled, retired, deleted
// or baked again in the meantime.
func (h *Handlers) finishBake(kitchen, agentName, version, k8sNamespace string, recook bool, superseded func() bool) {
	time.Sleep(1 * time.Second) // let the response that started the bake go out first
	if superseded() {
		return
	}
	ctx := context.Background()
	guard := models.AgentLifecycleGuard{Status: models.AgentStatusBaking, Version: version}

	fresh, err := h.Store.GetAgent(ctx, kitchen, agentName)
	if err != nil {
		log.Warn().Err(err).Str("agent", agentName).Msg("bake: cannot re-fetch agent")
		return
	}
	if !guard.Matches(fresh) {
		log.Info().Str("agent", agentName).Str("status", string(fresh.Status)).Msg("bake: agent no longer baking, aborting")
		return
	}

	// Test the provider with a real credential-validating call
	if fresh.ModelProvider != "" {
		if provider, err := h.Store.GetProvider(ctx, kitchen, fresh.ModelProvider); err == nil {
			// A test the budget or a rate limit stopped from being made says nothing about the provider: the
			// bake does not fail because the kitchen is out of budget (the agent is refused when it is called).
			if result := h.Router.TestProvider(ctx, provider); result.Refused != nil {
				log.Warn().Err(result.Refused).Str("agent", agentName).Msg("bake: the provider test was refused by a budget or rate limit; the credential was not validated")
			} else if !result.Healthy {
				h.burnt(kitchen, agentName, guard, fmt.Sprintf("Provider '%s' test failed: %s", fresh.ModelProvider, result.Error), models.AgentLifecycle{})
				log.Warn().Str("agent", agentName).Msg("Agent burnt: provider test failed")
				return
			}
		}
	}

	ready := models.AgentLifecycle{Status: models.AgentStatusReady, DeleteTags: []string{"error"}}
	if k8sNamespace != "" {
		// Propagate the kitchen's K8s namespace into the agent's tags so the K8sExecutor can place the workload.
		if _, set := fresh.Tags["k8s_namespace"]; !set {
			fresh.Tags = mergeTags(fresh.Tags, map[string]string{"k8s_namespace": k8sNamespace}) // a copy: the stored map is shared
			ready.SetTags = map[string]string{"k8s_namespace": k8sNamespace}
		}
	}
	withNamespace := func(c models.AgentLifecycle) models.AgentLifecycle {
		c.SetTags = mergeTags(c.SetTags, ready.SetTags)
		return c
	}

	current := guard // what the agent is expected to still be when the process step reports
	startProcess := func() (models.AgentLifecycle, bool) {
		if recook {
			if err := h.ProcessManager.Stop(ctx, kitchen, agentName); err != nil {
				log.Warn().Err(err).Str("agent", agentName).Msg("re-cook: stop old process (non-fatal)")
			}
		}
		procInfo, err := h.ProcessManager.Start(ctx, fresh)
		if err != nil {
			reason := "Agent process failed to start: " + err.Error()
			if recook {
				reason = "Process restart after recook failed: " + err.Error()
			}
			h.burnt(kitchen, agentName, current, reason, withNamespace(models.AgentLifecycle{}))
			log.Warn().Err(err).Str("agent", agentName).Msg("Agent burnt: process start failed")
			return models.AgentLifecycle{}, false
		}
		// The control plane proxies /agents/{name}/a2a to the process endpoint, so clients keep the
		// stable URL (ADR-0007). The endpoint is stored as well because the Pro store does not keep
		// ProcessInfo, and later A2A lookups must not depend on the in-memory field.
		change := models.AgentLifecycle{Process: procInfo, BackendEndpoint: procInfo.Endpoint}
		log.Info().Str("agent", agentName).Str("backend", procInfo.Endpoint).Int("port", procInfo.Port).
			Str("mode", string(procInfo.Mode)).Msg("Agent process spawned: control plane will proxy A2A calls")
		fresh.Process = procInfo
		if procInfo.Endpoint != "" {
			fresh.BackendEndpoint = procInfo.Endpoint
		}
		return change, true
	}
	runsProcess := h.ProcessManager != nil && fresh.Mode != models.AgentModeExternal

	if !recook {
		if runsProcess {
			change, ok := startProcess()
			if !ok {
				return
			}
			ready.Process, ready.BackendEndpoint = change.Process, change.BackendEndpoint
		}
		// A managed agent must not be marked ready unless the gateway can resolve some backend for A2A
		// traffic, or recipe steps see a false-ready agent and fail later with "No backend available".
		if fresh.Mode != models.AgentModeExternal {
			if backendURL, reason := h.ResolveBackendEndpoint(fresh); backendURL == "" {
				h.burnt(kitchen, agentName, guard, "Agent has no backend after bake: "+reason, withNamespace(models.AgentLifecycle{}))
				log.Warn().Str("agent", agentName).Str("reason", reason).Msg("Agent burnt: no backend after bake")
				return
			}
		}
		if h.lifecycle(kitchen, agentName, guard, ready) {
			log.Info().Str("agent", agentName).Msg("Agent is ready")
		}
		return
	}

	// A re-cook marks the agent ready first, then replaces its process so the pod or process runs the
	// current configuration; a failed restart burns it again.
	if !h.lifecycle(kitchen, agentName, guard, ready) {
		return
	}
	log.Info().Str("agent", agentName).Msg("Agent re-cooked and ready")
	current = models.AgentLifecycleGuard{Status: models.AgentStatusReady, Version: version}
	if runsProcess {
		change, ok := startProcess()
		if !ok {
			return
		}
		h.lifecycle(kitchen, agentName, current, change)
		log.Info().Str("agent", agentName).Str("backend", change.BackendEndpoint).Msg("Agent process restarted after recook")
	}
	// Pro hook: bump k8s_workload.updated_at (and image if configured) so the operator emits a new
	// pod-template annotation, Kubernetes rolls the pods and they pull the current image.
	if h.WorkloadRestarter != nil {
		if err := h.WorkloadRestarter.TouchWorkloadForAgent(ctx, kitchen, agentName, h.K8sAgentImage); err != nil {
			log.Warn().Err(err).Str("agent", agentName).Msg("Failed to touch workload for recook restart")
		}
	}
}

func mergeTags(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// respondStoreError answers a failed agent write: a refused version name is the caller's error (400 for
// a malformed one, 409 for one that is taken or behind the latest), anything else is the server's.
func respondStoreError(w http.ResponseWriter, err error) {
	var ve *models.VersionError
	stale, isStale := asStale(err)
	switch {
	case isStale:
		respondStale(w, stale)
	case errors.As(err, &ve) && ve.Conflict:
		respondError(w, http.StatusConflict, ve.Reason)
	case errors.As(err, &ve):
		respondError(w, http.StatusBadRequest, ve.Reason)
	default:
		respondError(w, http.StatusInternalServerError, err.Error())
	}
}
