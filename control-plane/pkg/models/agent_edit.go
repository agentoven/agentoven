package models

// AgentEdit is what a person may change about an agent without baking it: every field a PUT or a
// re-cook accepts. A field left out (empty, or nil for the guardrails) means "unchanged"; an empty
// guardrail list removes them all.
//
// An agent that has never been baked has nothing running, so an edit applies to it directly. An agent
// that has been baked runs what was last baked: an edit is kept as Agent.Draft and the next bake or
// re-cook applies it, so the version a trace names is the configuration that served the call
// (ADR-0043). The draft is the same type as the edit, so keeping an edit is merging it.
type AgentEdit struct {
	Description    string        `json:"description,omitempty"`
	Framework      string        `json:"framework,omitempty"`
	ModelProvider  string        `json:"model_provider,omitempty"`
	ModelName      string        `json:"model_name,omitempty"`
	BackupProvider string        `json:"backup_provider,omitempty"`
	BackupModel    string        `json:"backup_model,omitempty"`
	Mode           AgentMode     `json:"mode,omitempty"`
	ExecutionMode  ExecutionMode `json:"execution_mode,omitempty"`
	MaxTurns       int           `json:"max_turns,omitempty"`
	Behavior       AgentBehavior `json:"behavior,omitempty"`
	Runtime        AgentRuntime  `json:"runtime,omitempty"`
	Bot            *BotConfig    `json:"bot,omitempty"`
	Ingredients    []Ingredient  `json:"ingredients,omitempty"`
	Skills         []string      `json:"skills"`     // nil: unchanged, empty: none (like the guardrails)
	Guardrails     []Guardrail   `json:"guardrails"` // nil: unchanged, empty: none (no omitempty, or the difference is lost)

	// The settings below are the ones a PUT silently dropped once the edit became a draft: a person who set
	// them in the dashboard saw them accepted and never saw them take effect.
	ContextBudget     int               `json:"context_budget,omitempty"`
	ReasoningStrategy ReasoningStrategy `json:"reasoning_strategy,omitempty"`
	// SummaryProvider and SummaryModel are the model that summarizes the agent's history. A pointer, so
	// that "" (use the primary model again) is told from "not part of this edit".
	SummaryProvider *string `json:"summary_provider,omitempty"`
	SummaryModel    *string `json:"summary_model,omitempty"`
	// ApprovalTools and Subagents follow the guardrails' rule: nil is unchanged, an empty list clears.
	ApprovalTools []string `json:"approval_tools"`
	Subagents     []string `json:"subagents"`
}

// IsEmpty reports whether the edit changes nothing.
func (e *AgentEdit) IsEmpty() bool {
	return e == nil || (*e).isZero()
}

func (e AgentEdit) isZero() bool {
	return e.Description == "" && e.Framework == "" && e.ModelProvider == "" && e.ModelName == "" &&
		e.BackupProvider == "" && e.BackupModel == "" && e.Mode == "" && e.ExecutionMode == "" &&
		e.MaxTurns <= 0 && e.Behavior == "" && e.Runtime == "" && e.Bot == nil &&
		len(e.Ingredients) == 0 && e.Skills == nil && e.Guardrails == nil &&
		e.ContextBudget <= 0 && e.ReasoningStrategy == "" && e.SummaryProvider == nil && e.SummaryModel == nil &&
		e.ApprovalTools == nil && e.Subagents == nil
}

// Merge lays o over e: what o sets wins, what it leaves out stays as it was. It is how a second edit
// joins the draft of the first.
func (e *AgentEdit) Merge(o AgentEdit) {
	if o.Description != "" {
		e.Description = o.Description
	}
	if o.Framework != "" {
		e.Framework = o.Framework
	}
	if o.ModelProvider != "" {
		e.ModelProvider = o.ModelProvider
	}
	if o.ModelName != "" {
		e.ModelName = o.ModelName
	}
	if o.BackupProvider != "" {
		e.BackupProvider = o.BackupProvider
	}
	if o.BackupModel != "" {
		e.BackupModel = o.BackupModel
	}
	if o.Mode != "" {
		e.Mode = o.Mode
	}
	if o.ExecutionMode != "" {
		e.ExecutionMode = o.ExecutionMode
	}
	if o.MaxTurns > 0 {
		e.MaxTurns = o.MaxTurns
	}
	if o.Behavior != "" {
		e.Behavior = o.Behavior
	}
	if o.Runtime != "" {
		e.Runtime = o.Runtime
	}
	if o.Bot != nil {
		e.Bot = o.Bot
	}
	if len(o.Ingredients) > 0 {
		e.Ingredients = o.Ingredients
	}
	if o.Skills != nil {
		e.Skills = o.Skills
	}
	if o.Guardrails != nil {
		e.Guardrails = o.Guardrails
	}
	if o.ContextBudget > 0 {
		e.ContextBudget = o.ContextBudget
	}
	if o.ReasoningStrategy != "" {
		e.ReasoningStrategy = o.ReasoningStrategy
	}
	if o.SummaryProvider != nil {
		e.SummaryProvider = o.SummaryProvider
	}
	if o.SummaryModel != nil {
		e.SummaryModel = o.SummaryModel
	}
	if o.ApprovalTools != nil {
		e.ApprovalTools = o.ApprovalTools
	}
	if o.Subagents != nil {
		e.Subagents = o.Subagents
	}
}

// Apply writes the edit onto a, field by field, leaving alone what the edit leaves out. Ingredients
// that come back still masked (a client that read the agent and wrote it back) keep the stored secret.
func (e AgentEdit) Apply(a *Agent) {
	if e.Description != "" {
		a.Description = e.Description
	}
	if e.Framework != "" {
		a.Framework = e.Framework
	}
	if e.ModelProvider != "" {
		a.ModelProvider = e.ModelProvider
	}
	if e.ModelName != "" {
		a.ModelName = e.ModelName
	}
	if e.BackupProvider != "" {
		a.BackupProvider = e.BackupProvider
	}
	if e.BackupModel != "" {
		a.BackupModel = e.BackupModel
	}
	if e.Mode != "" {
		a.Mode = e.Mode
	}
	if e.ExecutionMode != "" {
		a.ExecutionMode = e.ExecutionMode
	}
	if e.MaxTurns > 0 {
		a.MaxTurns = e.MaxTurns
	}
	if e.Behavior != "" {
		a.Behavior = e.Behavior
	}
	if e.Runtime != "" {
		a.Runtime = e.Runtime
	}
	if e.Bot != nil {
		a.Bot = e.Bot
	}
	if len(e.Ingredients) > 0 {
		a.Ingredients = RestoreRedactedIngredients(e.Ingredients, a.Ingredients)
	}
	if e.Skills != nil {
		a.Skills = e.Skills
	}
	if e.Guardrails != nil {
		a.Guardrails = RestoreRedactedGuardrails(e.Guardrails, a.Guardrails)
	}
	if e.ContextBudget > 0 {
		a.ContextBudget = e.ContextBudget
	}
	if e.ReasoningStrategy != "" {
		a.ReasoningStrategy = e.ReasoningStrategy
	}
	if e.SummaryProvider != nil {
		a.SummaryProvider = *e.SummaryProvider
	}
	if e.SummaryModel != nil {
		a.SummaryModel = *e.SummaryModel
	}
	if e.ApprovalTools != nil {
		a.ApprovalTools = e.ApprovalTools
	}
	if e.Subagents != nil {
		a.Subagents = e.Subagents
	}
}

// Baked reports whether something has been baked for the agent, so that what runs is the baked
// configuration and not the live fields of a draft.
func (a *Agent) Baked() bool { return a.ResolvedConfig != nil }

// Edit takes an edit the way ADR-0043 says: applied at once to an agent that has never been baked
// (nothing runs yet), kept in the draft for one that has. It returns true when the edit was kept.
func (a *Agent) Edit(e AgentEdit) (kept bool) {
	if !a.Baked() {
		e.Apply(a)
		return false
	}
	if a.Draft == nil {
		a.Draft = &AgentEdit{}
	}
	// A masked ingredient the client sent back is restored against the draft's own copy when it has
	// one, else the baked one, so the draft never holds a mask.
	if len(e.Ingredients) > 0 {
		base := a.Ingredients
		if len(a.Draft.Ingredients) > 0 {
			base = a.Draft.Ingredients
		}
		e.Ingredients = RestoreRedactedIngredients(e.Ingredients, base)
	}
	if e.Guardrails != nil {
		base := a.Guardrails
		if a.Draft.Guardrails != nil {
			base = a.Draft.Guardrails
		}
		e.Guardrails = RestoreRedactedGuardrails(e.Guardrails, base)
	}
	a.Draft.Merge(e)
	if a.Draft.IsEmpty() {
		a.Draft = nil
	}
	return true
}

// ApplyDraft makes the draft the agent's configuration, as a bake or re-cook does before it resolves
// the ingredients. The draft is spent.
func (a *Agent) ApplyDraft() {
	if a.Draft != nil {
		a.Draft.Apply(a)
		a.Draft = nil
	}
}

// WithDraft returns a copy of the agent as it will be once the draft is baked, for checks that
// must judge the configuration a person is building and not the one that runs.
func (a *Agent) WithDraft() *Agent {
	c := *a
	c.Draft = nil
	if a.Draft != nil {
		a.Draft.Apply(&c)
	}
	return &c
}
