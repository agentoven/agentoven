package refcheck_test

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/refcheck"
)

type fixture struct {
	Catalogs map[string]refcheck.Catalog `json:"catalogs"`
	Cases    []fixtureCase               `json:"cases"`
}

type fixtureCase struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Catalog string `json:"catalog"`
	Options struct {
		Bake        bool   `json:"bake"`
		Environment string `json:"environment"`
	} `json:"options"`
	Input      json.RawMessage `json:"input"`
	Value      string          `json:"value"`
	Candidates []string        `json:"candidates"`
	Expect     json.RawMessage `json:"expect"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile("../../../sdk/fixtures/refcheck-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// key identifies a problem the way the fixture does.
func key(p refcheck.Problem) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s", p.Path, p.Code, p.Severity, p.Kind, p.Value, p.Within)
}

// TestFixtureCases runs the cases the SDKs run, so the three implementations cannot drift apart.
func TestFixtureCases(t *testing.T) {
	f := loadFixture(t)
	for _, tc := range f.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Kind == "nearest" {
				var want []string
				_ = json.Unmarshal(tc.Expect, &want)
				got := refcheck.Nearest(tc.Value, tc.Candidates, 3)
				if !reflect.DeepEqual(append([]string{}, got...), append([]string{}, want...)) {
					t.Fatalf("Nearest(%q) = %v, want %v", tc.Value, got, want)
				}
				return
			}
			var want []refcheck.Problem
			var wantRaw []map[string]json.RawMessage
			if err := json.Unmarshal(tc.Expect, &want); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal(tc.Expect, &wantRaw)
			cat := f.Catalogs[tc.Catalog]
			opts := refcheck.Options{Bake: tc.Options.Bake, Environment: tc.Options.Environment}
			var got []refcheck.Problem
			switch tc.Kind {
			case "agent":
				var a models.Agent
				if err := json.Unmarshal(tc.Input, &a); err != nil {
					t.Fatal(err)
				}
				got = refcheck.CheckAgent(&a, &cat, opts)
			case "recipe":
				var r models.Recipe
				if err := json.Unmarshal(tc.Input, &r); err != nil {
					t.Fatal(err)
				}
				got = refcheck.CheckRecipe(&r, &cat, opts)
			default:
				t.Fatalf("unknown case kind %q", tc.Kind)
			}
			gotKeys, wantKeys := keys(got), keys(want)
			if !reflect.DeepEqual(gotKeys, wantKeys) {
				t.Fatalf("problems differ\n got:  %s\n want: %s\n messages:\n  %s", strings.Join(gotKeys, "\n       "), strings.Join(wantKeys, "\n       "), messages(got))
			}
			// Suggestions are compared where the case lists them.
			byKey := map[string]refcheck.Problem{}
			for _, p := range got {
				byKey[key(p)] = p
			}
			for i, w := range want {
				if _, listed := wantRaw[i]["suggestions"]; !listed {
					continue
				}
				g := byKey[key(w)]
				if !reflect.DeepEqual(append([]string{}, g.Suggestions...), append([]string{}, w.Suggestions...)) {
					t.Errorf("%s: suggestions %v, want %v", w.Path, g.Suggestions, w.Suggestions)
				}
			}
			for _, p := range got {
				if p.Message == "" {
					t.Errorf("%s has no message", p.Path)
				}
			}
		})
	}
}

func keys(ps []refcheck.Problem) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = key(p)
	}
	sort.Strings(out)
	return out
}

func messages(ps []refcheck.Problem) string {
	var m []string
	for _, p := range ps {
		m = append(m, p.Message)
	}
	return strings.Join(m, "\n  ")
}
