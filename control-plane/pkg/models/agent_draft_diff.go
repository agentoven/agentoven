package models

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
)

// Kinds of change in an AgentDraftDiff.
const (
	DraftChangeAdded   = "added"
	DraftChangeChanged = "changed"
	DraftChangeRemoved = "removed"
)

// DraftChange is one difference between the baked agent and the agent as the draft will leave it.
//
//   - A plain field is named by its JSON path ("max_turns", "bot.max_actions").
//   - An element of a list that has an identity is one change: Field is the list ("guardrails",
//     "ingredients"), Key names the element (a guardrail's id, an ingredient's "kind:name"), and for a
//     changed element Details lists the fields inside it that differ (paths relative to the element).
//   - A list of names (skills, approval_tools, subagents, a bot's allowed_domains) is one change per
//     name that was added or removed; Before or After is the name.
//
// Before and After are what a reader of GET /agents/{name} would see: a secret is masked (RedactConfig),
// so a changed secret is a "changed" whose two sides both read "****". Whether it changed is decided on
// the real values, never on the masks.
type DraftChange struct {
	Field   string        `json:"field"`
	Key     string        `json:"key,omitempty"`
	Kind    string        `json:"kind"`
	Before  interface{}   `json:"before"`
	After   interface{}   `json:"after"`
	Details []DraftChange `json:"details,omitempty"`
}

// AgentDraftDiff is the answer to "what would the next bake change". Changes is never nil.
type AgentDraftDiff struct {
	Agent    string `json:"agent"`
	HasDraft bool   `json:"has_draft"`
	// Baked is false for an agent nothing was baked for yet; its edits apply at once and it has no draft.
	Baked        bool   `json:"baked"`
	BakedVersion string `json:"baked_version,omitempty"`
	// ChangesBehaviour says whether the draft would move the behaviour fingerprint (BakedFingerprint), which
	// is what makes a bake write a new version. It judges the configuration fields only: a bake also
	// re-resolves prompts and models, which can change the fingerprint even when this is false.
	ChangesBehaviour bool          `json:"changes_behaviour"`
	Changes          []DraftChange `json:"changes"`
}

// DiffAgentDraft compares the agent as it is baked with the agent as it will be once the draft is baked
// (Agent.WithDraft). An agent without a draft has no changes.
func DiffAgentDraft(a *Agent) AgentDraftDiff {
	d := AgentDraftDiff{Agent: a.Name, HasDraft: a.Draft != nil, Baked: a.Baked(), Changes: []DraftChange{}}
	if d.Baked {
		d.BakedVersion = a.Version
	}
	if a.Draft == nil {
		return d
	}
	// Both sides as a bake would leave them: it gives the agent its model ingredient (EnsureModelIngredient), so an
	// edit that replaces the ingredient list does not show the model ingredient as removed.
	baked, next := *a, a.WithDraft()
	baked.EnsureModelIngredient()
	next.EnsureModelIngredient()
	d.ChangesBehaviour = BakedFingerprint(&baked) != BakedFingerprint(next)
	before, beforeShown := agentTree(&baked)
	after, afterShown := agentTree(next)
	w := differ{}
	w.walk("", node{before, beforeShown, true}, node{after, afterShown, true})
	if w.out != nil {
		d.Changes = w.out
	}
	return d
}

// agentTree is the agent as the generic JSON tree an API caller would get, and the same tree masked.
// Everything the draft can touch is in it, so a field added to AgentEdit is diffed without a change here.
// The resolved configuration is left out: a draft does not change it until it is baked.
func agentTree(a *Agent) (raw, shown interface{}) {
	c := *a
	c.Draft, c.ResolvedConfig = nil, nil
	b, err := json.Marshal(c)
	if err != nil {
		return map[string]interface{}{}, map[string]interface{}{}
	}
	var tree map[string]interface{}
	if json.Unmarshal(b, &tree) != nil {
		return map[string]interface{}{}, map[string]interface{}{}
	}
	return tree, RedactConfig(tree)
}

// node is one value of the tree twice: as it is (compared) and as a reader may see it (reported).
type node struct {
	raw, shown interface{}
	ok         bool // false: the value is absent
}

func child(m map[string]interface{}, shown interface{}, k string) node {
	v, ok := m[k]
	if !ok || isEmptyJSON(v) || v == "" { // an empty list or text and an absent one are the same value, as in the fingerprint
		return node{}
	}
	sm, _ := shown.(map[string]interface{})
	return node{v, sm[k], true}
}

type differ struct{ out []DraftChange }

func (w *differ) add(c DraftChange) { w.out = append(w.out, c) }

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func (w *differ) walk(path string, b, a node) {
	// A list that appears or disappears is told element by element, like one that changes: the elements
	// are what a reader wants to see go.
	if _, isList := a.raw.([]interface{}); isList && !b.ok {
		b = node{[]interface{}{}, []interface{}{}, true}
	} else if _, isList := b.raw.([]interface{}); isList && !a.ok {
		a = node{[]interface{}{}, []interface{}{}, true}
	}
	switch {
	case !b.ok && !a.ok:
		return
	case !b.ok:
		w.add(DraftChange{Field: path, Kind: DraftChangeAdded, After: a.shown})
		return
	case !a.ok:
		w.add(DraftChange{Field: path, Kind: DraftChangeRemoved, Before: b.shown})
		return
	}
	switch bv := b.raw.(type) {
	case map[string]interface{}:
		if av, ok := a.raw.(map[string]interface{}); ok {
			for _, k := range unionKeys(bv, av) {
				w.walk(join(path, k), child(bv, b.shown, k), child(av, a.shown, k))
			}
			return
		}
	case []interface{}:
		if av, ok := a.raw.([]interface{}); ok {
			w.walkList(path, bv, b.shown, av, a.shown)
			return
		}
	}
	if !reflect.DeepEqual(b.raw, a.raw) {
		w.add(DraftChange{Field: path, Kind: DraftChangeChanged, Before: b.shown, After: a.shown})
	}
}

func unionKeys(a, b map[string]interface{}) []string {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, dup := a[k]; !dup {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// elementKey names a list element that has an identity, or "" for a list without one. A guardrail is its
// id (the rule's kind and stage when it has none, the way RestoreRedactedGuardrails matches them); an
// ingredient is its kind and name.
func elementKey(path string, el interface{}) string {
	m, _ := el.(map[string]interface{})
	str := func(k string) string { s, _ := m[k].(string); return s }
	switch path {
	case "guardrails":
		if id := str("id"); id != "" {
			return id
		}
		return str("kind") + "/" + str("stage")
	case "ingredients":
		return str("kind") + ":" + str("name")
	}
	return ""
}

func (w *differ) walkList(path string, b []interface{}, bShown interface{}, a []interface{}, aShown interface{}) {
	bs, _ := bShown.([]interface{})
	as, _ := aShown.([]interface{})
	shownAt := func(s []interface{}, i int) interface{} {
		if i < len(s) {
			return s[i]
		}
		return nil
	}
	if path == "guardrails" || path == "ingredients" {
		w.walkKeyed(path, b, bs, a, as)
		return
	}
	if allStrings(b) && allStrings(a) {
		inA, inB := map[string]bool{}, map[string]bool{}
		for _, v := range a {
			inA[v.(string)] = true
		}
		for i, v := range b {
			inB[v.(string)] = true
			if !inA[v.(string)] {
				w.add(DraftChange{Field: path, Kind: DraftChangeRemoved, Before: shownAt(bs, i)})
			}
		}
		for i, v := range a {
			if !inB[v.(string)] {
				w.add(DraftChange{Field: path, Kind: DraftChangeAdded, After: shownAt(as, i)})
			}
		}
		return
	}
	switch { // a list without an identity is one value
	case reflect.DeepEqual(b, a):
	case len(b) == 0:
		w.add(DraftChange{Field: path, Kind: DraftChangeAdded, After: aShown})
	case len(a) == 0:
		w.add(DraftChange{Field: path, Kind: DraftChangeRemoved, Before: bShown})
	default:
		w.add(DraftChange{Field: path, Kind: DraftChangeChanged, Before: bShown, After: aShown})
	}
}

func allStrings(l []interface{}) bool {
	for _, v := range l {
		if _, ok := v.(string); !ok {
			return false
		}
	}
	return true
}

// keyed pairs the elements of a list by their key. Two elements with the same key (a stored list can hold
// them) are told apart by their position among the equals, so none is lost.
func keyed(path string, l []interface{}, shown []interface{}) (keys []string, byKey map[string]node) {
	byKey = make(map[string]node, len(l))
	seen := map[string]int{}
	for i, el := range l {
		k := elementKey(path, el)
		seen[k]++
		if seen[k] > 1 {
			k = k + "#" + strconv.Itoa(seen[k])
		}
		var s interface{}
		if i < len(shown) {
			s = shown[i]
		}
		keys = append(keys, k)
		byKey[k] = node{el, s, true}
	}
	return keys, byKey
}

func (w *differ) walkKeyed(path string, b, bs, a, as []interface{}) {
	bKeys, bBy := keyed(path, b, bs)
	aKeys, aBy := keyed(path, a, as)
	for _, k := range bKeys {
		bn := bBy[k]
		an, kept := aBy[k]
		switch {
		case !kept:
			w.add(DraftChange{Field: path, Key: k, Kind: DraftChangeRemoved, Before: bn.shown})
		case !reflect.DeepEqual(bn.raw, an.raw):
			inner := differ{}
			inner.walk("", bn, an)
			w.add(DraftChange{Field: path, Key: k, Kind: DraftChangeChanged, Before: bn.shown, After: an.shown, Details: inner.out})
		}
	}
	for _, k := range aKeys {
		if _, was := bBy[k]; !was {
			w.add(DraftChange{Field: path, Key: k, Kind: DraftChangeAdded, After: aBy[k].shown})
		}
	}
}
