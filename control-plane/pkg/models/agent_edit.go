package models

import (
	"bytes"
	"encoding/json"
)

// AgentEdit is what a person may change about an agent without baking it: every field a PUT or a
// re-cook accepts. A field left out (empty, or nil for the guardrails) means "unchanged"; an empty
// guardrail list removes them all. An optional field is cleared by sending JSON null for it (Cleared
// lists those); the fields an agent cannot run without cannot be cleared (see NullFields).
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

	// Cleared names the optional fields this edit sets to nothing, because the request carried JSON null for
	// them (owner decision A4, 2026-11-03). It is kept in the draft so that clearing a field of a baked agent
	// waits for the next bake like any other edit. A field is never both set and cleared: a later edit that
	// sets it takes it off the list (Merge).
	Cleared []string `json:"cleared,omitempty"`
}

// clearableFields are the optional fields an explicit JSON null clears. The lists that already clear with
// an empty list (skills, guardrails, approval_tools, subagents) are not here: a client that marshals a nil
// slice sends null for "no opinion", and reading that as "remove them all" would wipe a guardrail by accident.
var clearableFields = []string{
	"description", "framework", "backup_provider", "backup_model", "execution_mode", "max_turns", "bot",
	"ingredients", "context_budget", "reasoning_strategy", "summary_provider", "summary_model",
}

// requiredFields are the settings an agent cannot run without; null for one is refused rather than ignored,
// so that nobody believes the field was cleared.
var requiredFields = []string{"model_provider", "model_name", "mode", "behavior", "runtime"}

// NullFields reads which of the body's top-level fields are JSON null. cleared are the optional fields
// to clear; refused are the required ones, which cannot be.
func NullFields(body []byte) (cleared, refused []string) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return nil, nil
	}
	isNull := func(k string) bool {
		v, ok := raw[k]
		return ok && bytes.Equal(bytes.TrimSpace(v), []byte("null"))
	}
	for _, k := range clearableFields {
		if isNull(k) {
			cleared = append(cleared, k)
		}
	}
	for _, k := range requiredFields {
		if isNull(k) {
			refused = append(refused, k)
		}
	}
	return cleared, refused
}

// sets reports whether the edit gives the named field a value.
func (e AgentEdit) sets(name string) bool {
	switch name {
	case "description":
		return e.Description != ""
	case "framework":
		return e.Framework != ""
	case "backup_provider":
		return e.BackupProvider != ""
	case "backup_model":
		return e.BackupModel != ""
	case "execution_mode":
		return e.ExecutionMode != ""
	case "max_turns":
		return e.MaxTurns > 0
	case "bot":
		return e.Bot != nil
	case "ingredients":
		return len(e.Ingredients) > 0
	case "context_budget":
		return e.ContextBudget > 0
	case "reasoning_strategy":
		return e.ReasoningStrategy != ""
	case "summary_provider":
		return e.SummaryProvider != nil
	case "summary_model":
		return e.SummaryModel != nil
	}
	return false
}

// clearOnEdit takes the named field out of the edit, for a draft that is cleared after it set the field.
func (e *AgentEdit) clearOnEdit(name string) {
	switch name {
	case "description":
		e.Description = ""
	case "framework":
		e.Framework = ""
	case "backup_provider":
		e.BackupProvider = ""
	case "backup_model":
		e.BackupModel = ""
	case "execution_mode":
		e.ExecutionMode = ""
	case "max_turns":
		e.MaxTurns = 0
	case "bot":
		e.Bot = nil
	case "ingredients":
		e.Ingredients = nil
	case "context_budget":
		e.ContextBudget = 0
	case "reasoning_strategy":
		e.ReasoningStrategy = ""
	case "summary_provider":
		e.SummaryProvider = nil
	case "summary_model":
		e.SummaryModel = nil
	}
}

// clearOnAgent sets the named field of a to nothing.
func clearOnAgent(a *Agent, name string) {
	switch name {
	case "description":
		a.Description = ""
	case "framework":
		a.Framework = ""
	case "backup_provider":
		a.BackupProvider = ""
	case "backup_model":
		a.BackupModel = ""
	case "execution_mode":
		a.ExecutionMode = ""
	case "max_turns":
		a.MaxTurns = 0
	case "bot":
		a.Bot = nil
	case "ingredients":
		a.Ingredients = nil
	case "context_budget":
		a.ContextBudget = 0
	case "reasoning_strategy":
		a.ReasoningStrategy = ""
	case "summary_provider":
		a.SummaryProvider = ""
	case "summary_model":
		a.SummaryModel = ""
	}
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
		e.ApprovalTools == nil && e.Subagents == nil && len(e.Cleared) == 0
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
	// A field the later edit sets is no longer cleared; a field it clears is no longer set.
	kept := e.Cleared[:0:0]
	for _, name := range e.Cleared {
		if !o.sets(name) {
			kept = append(kept, name)
		}
	}
	e.Cleared = kept
	for _, name := range o.Cleared {
		e.clearOnEdit(name)
		if !contains(e.Cleared, name) {
			e.Cleared = append(e.Cleared, name)
		}
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
	for _, name := range e.Cleared {
		if !e.sets(name) { // an edit that sets a field and clears it is read as setting it
			clearOnAgent(a, name)
		}
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
