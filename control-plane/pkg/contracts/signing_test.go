package contracts

import "testing"

func TestDigestDoesNotDependOnKeyOrder(t *testing.T) {
	a := DigestOf(map[string]interface{}{"b": 1, "a": []interface{}{"x", map[string]interface{}{"d": 2, "c": 3}}})
	b := DigestOf(map[string]interface{}{"a": []interface{}{"x", map[string]interface{}{"c": 3, "d": 2}}, "b": 1})
	if a != b || len(a) != 64 {
		t.Fatalf("digests differ: %s %s", a, b)
	}
	if a == DigestOf(map[string]interface{}{"a": []interface{}{"x", map[string]interface{}{"c": 3, "d": 2}}, "b": 2}) {
		t.Fatal("a changed value must change the digest")
	}
	type rec struct {
		Tool string `json:"tool"`
		Args string `json:"args"`
	}
	if DigestOf(rec{"t", "{}"}) != DigestOf(map[string]interface{}{"tool": "t", "args": "{}"}) {
		t.Fatal("a struct and the map of its JSON must digest the same")
	}
}
