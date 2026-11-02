package secretref

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeVault map[string]string

func (f fakeVault) Read(_ context.Context, kitchen, name string) (string, error) {
	if v, ok := f[kitchen+"/"+name]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}
func (f fakeVault) Write(_ context.Context, k, n, v, _ string) error { f[k+"/"+n] = v; return nil }
func (f fakeVault) Delete(_ context.Context, k, n string) error      { delete(f, k+"/"+n); return nil }

func TestVaultRefResolvesInTheKitchenOnTheContext(t *testing.T) {
	v := fakeVault{"a/key": "value-a", "b/key": "value-b"}
	r := New()
	r.SetVault(v)
	for kitchen, want := range map[string]string{"a": "value-a", "b": "value-b"} {
		got, err := r.Resolve(WithKitchen(context.Background(), kitchen), "vault://key")
		if err != nil || got != want {
			t.Errorf("kitchen %s: %q, %v", kitchen, got, err)
		}
	}
	if _, err := r.Resolve(WithKitchen(context.Background(), "c"), "vault://key"); err == nil {
		t.Error("a kitchen reached a credential that is not in it")
	}
}

func TestVaultRefIsNeverCached(t *testing.T) {
	v := fakeVault{"default/key": "one"}
	r := New()
	r.SetVault(v)
	ctx := context.Background()
	if got, _ := r.Resolve(ctx, "vault://key"); got != "one" {
		t.Fatal(got)
	}
	v["default/key"] = "two" // rotated
	if got, _ := r.Resolve(ctx, "vault://key"); got != "two" {
		t.Errorf("a rotated credential was served from a cache: %q", got)
	}
	delete(v, "default/key") // revoked
	if _, err := r.Resolve(ctx, "vault://key"); err == nil {
		t.Error("a deleted credential still resolved")
	}
}

func TestVaultRefWithoutAVaultOrAName(t *testing.T) {
	_, err := New().Resolve(context.Background(), "vault://key")
	if err == nil || !strings.Contains(err.Error(), "Pro") {
		t.Errorf("no vault: %v", err)
	}
	r := New()
	r.SetVault(fakeVault{})
	if _, err := r.Resolve(context.Background(), "vault://"); err == nil {
		t.Error("an empty name resolved")
	}
	if name, ok := VaultName(VaultRef("x/y")); !ok || name != "x/y" {
		t.Errorf("VaultName = %q %v", name, ok)
	}
	var nilRes *Resolver
	if nilRes.Vault() != nil {
		t.Error("nil resolver has no vault")
	}
}

func TestResolveForReadsTheNamedKitchenNotTheDefault(t *testing.T) {
	v := fakeVault{"default/key": "default-value", "acme/key": "acme-value"}
	r := New()
	r.SetVault(v)
	if got, err := r.ResolveFor(context.Background(), "acme", "vault://key"); err != nil || got != "acme-value" {
		t.Fatalf("acme: %q %v", got, err)
	}
	if _, err := r.ResolveFor(context.Background(), "other", "vault://key"); err == nil {
		t.Fatal("a kitchen without the credential fell back to another kitchen's")
	}
}
