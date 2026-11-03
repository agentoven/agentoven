package secretref

import (
	"context"
	"strings"
	"testing"
)

func TestAKitchenCanOnlyNameItsOwnVaultCredentials(t *testing.T) {
	t.Setenv("LEAKME_SECRET", "hunter2-platform-secret")
	r := New()
	r.SetVault(fakeVault{"k/mailbox": "pw"})

	for _, ref := range []string{
		"env://LEAKME_SECRET", "env://DATABASE_URL", "ENV://LEAKME_SECRET", " env://LEAKME_SECRET", "env:// LEAKME_SECRET",
		"k8s://ns/secret/key", "file:///etc/passwd", "/etc/passwd", "vault://", "vault:// mailbox", "vault://../x", "vault://a b",
		"vault://" + strings.Repeat("a", 300), "vault://mailbox\n", "plain-literal-secret", "",
		"vault://env://LEAKME_SECRET",
	} {
		if name, err := KitchenVaultName(ref); err == nil {
			t.Errorf("KitchenVaultName(%q) = %q, want an error", ref, name)
		}
		v, err := r.ResolveFor(context.Background(), "k", ref)
		if err == nil || strings.Contains(v, "hunter2") {
			t.Errorf("ResolveFor(%q) = %q, %v: a kitchen's reference must not reach the process environment", ref, v, err)
		}
		if err != nil && (strings.Contains(err.Error(), "LEAKME") || strings.Contains(err.Error(), "plain-literal") || strings.Contains(err.Error(), "DATABASE_URL")) {
			t.Errorf("the refusal must not echo the reference (it may be a pasted secret): %v", err)
		}
	}
	if name, err := KitchenVaultName("vault://mailbox"); err != nil || name != "mailbox" {
		t.Fatalf("%q %v", name, err)
	}
	if v, err := r.ResolveFor(context.Background(), "k", "vault://mailbox"); err != nil || v != "pw" {
		t.Errorf("a vault reference still resolves in its kitchen: %q %v", v, err)
	}
	// The operator's own path (a platform provider key) keeps every scheme.
	if v, err := r.Resolve(context.Background(), "env://LEAKME_SECRET"); err != nil || v != "hunter2-platform-secret" {
		t.Errorf("Resolve is the operator's path and keeps env://: %q %v", v, err)
	}
}
