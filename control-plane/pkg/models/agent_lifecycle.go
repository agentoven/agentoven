package models

// AgentLifecycle is what the background step of a bake or re-cook may change once the agent has been
// provider-tested and its process started: the status, the error tag, the backend endpoint and the
// process. It is deliberately not a whole Agent: the step runs seconds after the request that began
// the bake, and writing a whole agent back overwrote every edit made in between (guardrails, the
// draft, a newer resolved configuration, the version).
type AgentLifecycle struct {
	Status          AgentStatus
	SetTags         map[string]string
	DeleteTags      []string
	BackendEndpoint string       // empty: unchanged
	Process         *ProcessInfo // nil: unchanged
	ClearProcess    bool         // the process is gone (a cool); wins over Process
}

// AgentLifecycleGuard is the optimistic-concurrency condition of a lifecycle change: it applies only
// while the agent is still in this status at this version. A cool, a retire, a delete or a newer bake
// that moved the agent on makes the older step's outcome stale, and the store then changes nothing.
type AgentLifecycleGuard struct {
	Status  AgentStatus
	Version string
	// Not refuses the change while the agent is in this status: a cool that reads the agent a moment before a
	// delete must not bring the retired agent back as cooled (owner decision A8).
	Not AgentStatus
}

// Apply writes the change onto a, which is the stored agent. It is shared by the stores so they
// cannot disagree on what a lifecycle change touches.
func (c AgentLifecycle) Apply(a *Agent) {
	if c.Status != "" {
		a.Status = c.Status
	}
	if len(c.SetTags) > 0 || len(c.DeleteTags) > 0 {
		tags := make(map[string]string, len(a.Tags)+len(c.SetTags))
		for k, v := range a.Tags {
			tags[k] = v
		}
		for k, v := range c.SetTags {
			tags[k] = v
		}
		for _, k := range c.DeleteTags {
			delete(tags, k)
		}
		a.Tags = tags
	}
	if c.BackendEndpoint != "" {
		a.BackendEndpoint = c.BackendEndpoint
	}
	if c.ClearProcess {
		a.Process = nil
	} else if c.Process != nil {
		a.Process = c.Process
	}
}

// Matches reports whether the stored agent still satisfies the guard.
func (g AgentLifecycleGuard) Matches(a *Agent) bool {
	return (g.Status == "" || a.Status == g.Status) && (g.Version == "" || a.Version == g.Version) &&
		(g.Not == "" || a.Status != g.Not)
}
