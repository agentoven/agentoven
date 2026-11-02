package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// VersionBumpBake asks the store to decide the version from what was baked. A bake or a re-cook
// carries it. The store compares the agent with its latest stored version:
//   - that version was never baked (an agent that has only been registered): the bake fills it in
//     and the version stays, so the first bake of a new agent is 0.1.0;
//   - the baked configuration is different: the minor version goes up and the old version is kept;
//   - nothing that shapes behaviour is different: the version stays, and no version is written.
//
// An edit asks for nothing. Promoting a version between environments moves a pointer to a version
// that already exists and never writes one.
const VersionBumpBake = "bake"

// BakedFingerprint hashes everything about an agent that decides what it says or does: the
// resolved configuration (prompt text, tools, model, data, knowledge), the model and its backup,
// turn and context limits, guardrails, skills, runtime and mode. It leaves out what only describes
// the agent's state (status, timestamps, tags, endpoints, the time a tool was resolved), so a
// status change or a re-bake that pulled the same configuration yields the same fingerprint.
func BakedFingerprint(a *Agent) string {
	if a == nil {
		return ""
	}
	shape := struct {
		Resolved      *ResolvedIngredients `json:"resolved"`
		Ingredients   []Ingredient         `json:"ingredients"`
		Provider      string               `json:"provider"`
		Model         string               `json:"model"`
		BackupProv    string               `json:"backup_provider"`
		BackupModel   string               `json:"backup_model"`
		MaxTurns      int                  `json:"max_turns"`
		ContextBudget int                  `json:"context_budget"`
		Guardrails    []Guardrail          `json:"guardrails"`
		Skills        []string             `json:"skills"`
		Runtime       AgentRuntime         `json:"runtime"`
		Mode          AgentMode            `json:"mode"`
		Behavior      string               `json:"behavior"`
		Budget        *RunBudget           `json:"budget"`
	}{
		a.ResolvedConfig, a.Ingredients, a.ModelProvider, a.ModelName, a.BackupProvider, a.BackupModel,
		a.MaxTurns, a.ContextBudget, a.Guardrails, a.Skills, a.Runtime, a.Mode, string(a.Behavior), a.Budget,
	}
	raw, err := json.Marshal(shape)
	if err != nil {
		return ""
	}
	var v interface{}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	stripBakedAt(v)
	norm, _ := json.Marshal(v) // map keys come out sorted
	sum := sha256.Sum256(norm)
	return hex.EncodeToString(sum[:])
}

func stripBakedAt(x interface{}) {
	switch t := x.(type) {
	case map[string]interface{}:
		delete(t, "baked_at")
		for _, c := range t {
			stripBakedAt(c)
		}
	case []interface{}:
		for _, c := range t {
			stripBakedAt(c)
		}
	}
}

// DecideBakeVersion applies the VersionBumpBake rule. latest is the stored snapshot of the
// agent's current version. It returns the version the bake should carry and whether that is a new
// version (a new snapshot) rather than the first fill-in or no change at all.
func DecideBakeVersion(current string, latest, next *Agent) (version string, isNew bool, changed bool) {
	if latest == nil || latest.ResolvedConfig == nil {
		return current, false, true // first bake: the registered version is baked, nothing is bumped
	}
	if BakedFingerprint(latest) == BakedFingerprint(next) {
		return current, false, false
	}
	return BumpMinor(current), true, true
}
