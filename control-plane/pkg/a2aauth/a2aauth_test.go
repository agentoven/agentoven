package a2aauth_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/a2aauth"
)

func TestInternalKeyIsStableWithinAProcessAndNotGuessable(t *testing.T) {
	a2aauth.UseSecret(nil)
	a, b := a2aauth.InternalKey(), a2aauth.InternalKey()
	if a != b || len(a) < 64 {
		t.Fatalf("key = %q / %q", a, b)
	}
}

func TestInternalKeyFromASharedSecretAgreesAcrossReplicas(t *testing.T) {
	defer a2aauth.UseSecret(nil)
	a2aauth.UseSecret([]byte("shared-secret"))
	one := a2aauth.InternalKey()
	a2aauth.UseSecret([]byte("shared-secret")) // "another replica", same configuration
	if two := a2aauth.InternalKey(); one != two || len(one) < 64 {
		t.Fatalf("replicas with the same secret must agree: %q vs %q", one, two)
	}
	a2aauth.UseSecret([]byte("different"))
	if a2aauth.InternalKey() == one {
		t.Error("a different secret must give a different key")
	}
	if one == a2aauth.PodToken([]byte("shared-secret"), "", "") {
		t.Error("the internal key and a pod token must not be the same value")
	}
}

func TestEqualNeverMatchesEmptyValues(t *testing.T) {
	if a2aauth.Equal("", "") || a2aauth.Equal("x", "") || a2aauth.Equal("", "x") {
		t.Error("an empty value must never authenticate")
	}
	if !a2aauth.Equal("abc", "abc") || a2aauth.Equal("abc", "abd") || a2aauth.Equal("abc", "abcd") {
		t.Error("Equal is wrong")
	}
}

func TestPodTokenIsBoundToTheKitchenTheAgentAndTheSecret(t *testing.T) {
	s := []byte("secret-one")
	base := a2aauth.PodToken(s, "default", "alpha")
	if base == "" || base != a2aauth.PodToken(s, "default", "alpha") {
		t.Fatal("a token must be deterministic")
	}
	for name, other := range map[string]string{
		"other agent":   a2aauth.PodToken(s, "default", "beta"),
		"other kitchen": a2aauth.PodToken(s, "prod", "alpha"),
		"other secret":  a2aauth.PodToken([]byte("secret-two"), "default", "alpha"),
		// "ab"+"c" must not collide with "a"+"bc": the fields are separated.
		"split ambiguity": a2aauth.PodToken(s, "defaulta", "lpha"),
	} {
		if other == base {
			t.Errorf("%s produced the same token", name)
		}
	}
	if a2aauth.PodToken(nil, "default", "alpha") != "" {
		t.Error("no secret means no token, not a token derived from nothing")
	}
}

func TestSecretPrefersTheEnvironmentThenAPersistedKey(t *testing.T) {
	t.Setenv("AGENTOVEN_A2A_SECRET", "")
	t.Setenv("AGENTOVEN_SA_SECRET", "")
	dir := t.TempDir()

	first, err := a2aauth.Secret(dir)
	if err != nil || len(first) < 32 {
		t.Fatalf("%q %v", first, err)
	}
	again, _ := a2aauth.Secret(dir)
	if string(first) != string(again) {
		t.Error("a persisted secret must survive a restart, or pods started before it lose the control plane")
	}
	if st, err := os.Stat(filepath.Join(dir, "a2a.secret")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the key file must be private: %v %v", st, err)
	}

	t.Setenv("AGENTOVEN_SA_SECRET", "from-sa")
	if s, _ := a2aauth.Secret(dir); string(s) != "from-sa" {
		t.Errorf("SA secret should win over the file: %q", s)
	}
	t.Setenv("AGENTOVEN_A2A_SECRET", "explicit")
	if s, _ := a2aauth.Secret(dir); string(s) != "explicit" {
		t.Errorf("the explicit secret should win: %q", s)
	}
	t.Setenv("AGENTOVEN_A2A_SECRET", "")
	t.Setenv("AGENTOVEN_SA_SECRET", "")
	if _, err := a2aauth.Secret(""); err == nil {
		t.Error("with no env and no directory there is nowhere to keep a secret")
	}
}
