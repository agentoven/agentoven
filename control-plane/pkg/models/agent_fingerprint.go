package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
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

// agentStateFields are the Agent fields that describe where the agent is or who made it, not what it
// says or does. They are the only fields BakedFingerprint leaves out. Every other field counts, so a
// field added to Agent later is part of the fingerprint until someone decides, here and with a reason,
// that it is state: the safe mistake is an extra version, never a behaviour change that keeps its label.
// TestEveryAgentFieldIsClassified fails when a field is in neither this table nor its behaviour list.
var agentStateFields = map[string]string{
	"id":            "identity",
	"name":          "identity",
	"kitchen":       "identity",
	"status":        "lifecycle state, changes on every bake, cool and burn",
	"version":       "the label the fingerprint decides",
	"order":         "display order",
	"created_at":    "bookkeeping",
	"updated_at":    "bookkeeping",
	"revision":      "counts writes to the stored row, for optimistic concurrency",
	"created_by":    "bookkeeping",
	"tags":          "labels and the last error; none is read by the executor",
	"a2a_endpoint":  "derived from the agent name and environment",
	"env_endpoints": "filled in by the operator when a workload starts",
	"process":       "the running process (a port that changes on every start)",
	"draft":         "edits that are not baked yet; by definition they are not what runs",
}

// externalOnlyFields count only for an agent AgentOven does not run: for a managed agent the backend
// endpoint is where its own process happened to start, for an external agent it is the agent.
var externalOnlyFields = map[string]bool{"backend_endpoint": true}

// BakedFingerprint hashes everything about an agent that decides what it says or does: the resolved
// configuration (prompt text, tools, model, data, knowledge), the model and its backup, turn and
// context limits, guardrails, skills, runtime and mode, the bot's allowlists and approvals, the
// description (the system prompt when there is no prompt ingredient), the framework, entrypoint and
// repository, approval tools, subagents, output schema, summary model and reasoning strategy. It
// leaves out only agentStateFields, so a status change or a re-bake that pulled the same
// configuration yields the same fingerprint. An empty list and an absent one are the same value.
func BakedFingerprint(a *Agent) string {
	if a == nil {
		return ""
	}
	v := reflect.ValueOf(*a)
	t := v.Type()
	shape := make(map[string]interface{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name == "-" || name == "" || t.Field(i).Tag.Get("json") == "" {
			continue // not serialised, so not part of the agent
		}
		if _, state := agentStateFields[name]; state {
			continue
		}
		if externalOnlyFields[name] && a.Mode != AgentModeExternal {
			continue
		}
		shape[name] = v.Field(i).Interface()
	}
	// An agent stored before runtimes and modes existed has neither, and has always been agentoven and
	// managed; the Postgres store also fills the runtime in on read. Same agent, same fingerprint.
	if shape["runtime"] == AgentRuntime("") {
		shape["runtime"] = RuntimeAgentOven
	}
	if shape["mode"] == AgentMode("") {
		shape["mode"] = AgentModeManaged
	}
	raw, err := json.Marshal(shape)
	if err != nil {
		return ""
	}
	var tree interface{}
	if json.Unmarshal(raw, &tree) != nil {
		return ""
	}
	norm, _ := json.Marshal(normalise(tree)) // map keys come out sorted
	sum := sha256.Sum256(norm)
	return hex.EncodeToString(sum[:])
}

// normalise drops what carries no information, so the same configuration read back from a store that
// keeps a nil list as an empty one (or the other way round) hashes the same: the time a tool was
// resolved, and any object member that is null or an empty list or object.
func normalise(x interface{}) interface{} {
	switch t := x.(type) {
	case map[string]interface{}:
		delete(t, "baked_at")
		for k, c := range t {
			c = normalise(c)
			if isEmptyJSON(c) {
				delete(t, k)
			} else {
				t[k] = c
			}
		}
		return t
	case []interface{}:
		for i, c := range t {
			t[i] = normalise(c)
		}
		return t
	}
	return x
}

func isEmptyJSON(x interface{}) bool {
	switch t := x.(type) {
	case nil:
		return true
	case map[string]interface{}:
		return len(t) == 0
	case []interface{}:
		return len(t) == 0
	}
	return false // a 0, "" or false is a value: a temperature of 0 is not "unset"
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

// VersionBumpNamed asks the store to give the bake the version the caller named, if the rules of
// DecideNamedVersion allow it. A bake never takes a name from the caller silently: ADR-0043 says a
// version marks baked behaviour, so naming a version that holds something else is refused.
const VersionBumpNamed = "named"

// VersionError is a refused version name. The API answers it with 400 (a malformed name) or 409 (a
// name that is taken or behind the latest version).
type VersionError struct {
	Conflict bool
	Reason   string
}

func (e *VersionError) Error() string { return e.Reason }

var versionRE = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)

// ValidVersion reports whether v is a plain MAJOR.MINOR.PATCH version: digits only, no sign, no
// leading zeros, no suffix and no white space. A version is stored, listed, used in URLs and compared
// as numbers, so anything else (a path, a newline, a 5000-character string) is refused where the
// name is taken.
func ValidVersion(v string) bool { return versionRE.MatchString(v) }

func versionLess(a, b string) bool {
	am, an, ap := ParseSemver(a)
	bm, bn, bp := ParseSemver(b)
	if am != bm {
		return am < bm
	}
	if an != bn {
		return an < bn
	}
	return ap < bp
}

// DecideNamedVersion applies the VersionBumpNamed rule. current is the version the agent is at,
// stored the versions that have a snapshot, latest the snapshot of current and next the agent being
// baked. A named version that does not exist must be greater than every stored one and gets a new
// snapshot; naming the current version is fine when the configuration is the one stored (or when
// nothing was baked yet, which fills the registered version in); naming any other existing version is
// refused, because it would run this configuration under a label that holds another.
func DecideNamedVersion(requested, current string, stored []string, latest, next *Agent) (isNew bool, err error) {
	if !ValidVersion(requested) {
		return false, &VersionError{Reason: "version must be MAJOR.MINOR.PATCH, digits only (for example 1.2.0)"}
	}
	unbaked := latest == nil || latest.ResolvedConfig == nil
	if requested == current {
		if unbaked || BakedFingerprint(latest) == BakedFingerprint(next) {
			return false, nil
		}
		return false, &VersionError{Conflict: true, Reason: "version " + requested + " already holds a different configuration; leave the version out and the bake names it"}
	}
	top := current
	for _, v := range stored {
		if v == requested {
			return false, &VersionError{Conflict: true, Reason: "version " + requested + " already exists and cannot be run with another configuration"}
		}
		if versionLess(top, v) {
			top = v
		}
	}
	if !versionLess(top, requested) {
		return false, &VersionError{Conflict: true, Reason: "version must be greater than the latest version, " + top}
	}
	return true, nil
}
