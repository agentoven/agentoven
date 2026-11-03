package secretseal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type vault map[string]string

func (v vault) Read(_ context.Context, k, n string) (string, error) {
	if s, ok := v[k+"/"+n]; ok {
		return s, nil
	}
	return "", errors.New("not found")
}
func (v vault) Write(_ context.Context, k, n, s, _ string) error { v[k+"/"+n] = s; return nil }
func (v vault) Delete(_ context.Context, k, n string) error      { delete(v, k+"/"+n); return nil }

func cfg() map[string]interface{} {
	return map[string]interface{}{
		"api_key": "S1", "index": "idx", "url": "https://h.example/p?token=S2",
		"headers": map[string]interface{}{"X-Any": "S3"}, "nested": []interface{}{map[string]interface{}{"password": "S4"}},
		"secret_ref": "vault://mine", "empty": "", "masked": "ab****", "max_tokens": 100,
	}
}

func TestSealMovesEverySecretAndOnlyThose(t *testing.T) {
	v := vault{}
	out, written, err := Seal(context.Background(), v, "k", "agent", "scope", cfg())
	if err != nil || len(written) != 4 {
		t.Fatalf("written %v err %v", written, err)
	}
	for _, s := range []string{"S1", "S2", "S3", "S4"} {
		if strings.Contains(strings.ReplaceAll(strings.Join(Refs(out), " ")+toString(out), "vault://", ""), s) {
			t.Errorf("%s is still in the sealed config: %v", s, out)
		}
	}
	if out["index"] != "idx" || out["secret_ref"] != "vault://mine" || out["max_tokens"] != 100 || out["masked"] != "ab****" {
		t.Errorf("a value that is not a secret changed: %v", out)
	}
	again, w2, _ := Seal(context.Background(), v, "k", "agent", "scope", out)
	if len(w2) != 0 || len(Refs(again)) != 4 {
		t.Errorf("sealing again wrote %v", w2)
	}
	back, err := Unseal(context.Background(), v, "k", "agent", out)
	if err != nil || back["api_key"] != "S1" || back["url"] != "https://h.example/p?token=S2" {
		t.Errorf("unseal: %v %v", back, err)
	}
}

func toString(m map[string]interface{}) string {
	var b strings.Builder
	var walk func(n interface{})
	walk = func(n interface{}) {
		switch x := n.(type) {
		case map[string]interface{}:
			for _, c := range x {
				walk(c)
			}
		case []interface{}:
			for _, c := range x {
				walk(c)
			}
		case string:
			b.WriteString(x + " ")
		}
	}
	walk(m)
	return b.String()
}

// A credential name can be worked out by anyone, so a config only ever gets back its own owner's credentials,
// and one owner's name is never the start of another's.
func TestUnsealAndReapOnlyTouchTheOwnersCredentials(t *testing.T) {
	v := vault{}
	out, _, _ := Seal(context.Background(), v, "k", "a", "s", map[string]interface{}{"api_key": "SECRET"})
	if _, err := Unseal(context.Background(), v, "k", "a-b", out); err == nil {
		t.Error("another agent read a credential that is not its own")
	}
	if OwnedBy("a", Name("a-b", "s", ".api_key")) || OwnedBy("a-b", Name("a", "s", ".api_key")) {
		t.Error("an owner's prefix is the start of another's")
	}
	_ = Reap(context.Background(), v, "k", "a-b", Refs(out), nil)
	if len(v) != 1 {
		t.Error("another owner's Reap deleted a credential")
	}
	_ = Reap(context.Background(), v, "k", "a", Refs(out), nil)
	if len(v) != 0 {
		t.Error("the owner's Reap left the credential")
	}
	if _, err := Unseal(context.Background(), v, "k", "a", out); err == nil {
		t.Error("a deleted credential must be an error, not an empty secret")
	}
}
