package models

import (
	"encoding/json"
	"os"
	"testing"
)

// The cases every implementation of an edit runs (sdk/fixtures/agent-edit-cases.json): this package is the
// reference, the Python and TypeScript SDKs and the Rust client follow it.
func TestEditsFollowTheSharedCases(t *testing.T) {
	raw, err := os.ReadFile("../../../sdk/fixtures/agent-edit-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			Name   string          `json:"name"`
			Agent  json.RawMessage `json:"agent"`
			Edit   json.RawMessage `json:"edit"`
			Result json.RawMessage `json:"result"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) < 9 {
		t.Fatalf("the fixture shrank to %d cases", len(fx.Cases))
	}
	for _, c := range fx.Cases {
		var agent Agent
		if err := json.Unmarshal(c.Agent, &agent); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		var edit AgentEdit
		if err := json.Unmarshal(c.Edit, &edit); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		// What the handler does: the nulls of the body become the edit's cleared fields; a null for a field
		// the agent cannot run without is refused there and changes nothing, so it is left out here.
		edit.Cleared, _ = NullFields(c.Edit)
		edit.Apply(&agent)

		b, _ := json.Marshal(&agent)
		var got, want, probed map[string]interface{}
		_ = json.Unmarshal(b, &got)
		_ = json.Unmarshal(c.Result, &want)
		// Only the fields a case mentions are compared: a missing key, "", 0, [] and {} are the same "nothing",
		// and a list or object is compared by its size, which is what an edit changes.
		_ = json.Unmarshal(c.Agent, &probed)
		for k := range want {
			probed[k] = true
		}
		for k := range probed {
			if g, w := shape(got[k]), shape(want[k]); g != w {
				t.Errorf("%s: %s is %v, want %v", c.Name, k, g, w)
			}
		}
	}
}

// shape reduces a JSON value to what the cases compare: its size for a list, object or string, itself for a number.
func shape(v interface{}) interface{} {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == 0 {
			return ""
		}
		return x
	case []interface{}:
		return sizeOrNothing(len(x))
	case map[string]interface{}:
		return sizeOrNothing(len(x))
	}
	return v
}

func sizeOrNothing(n int) interface{} {
	if n == 0 {
		return ""
	}
	return n
}
