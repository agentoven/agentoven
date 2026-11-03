package contracts

import "testing"

func TestDigestOfKeepsLargeIntegersApartAndRefusesWhatItCannotEncode(t *testing.T) {
	a := DigestOf(map[string]interface{}{"n": int64(1) << 53})
	b := DigestOf(map[string]interface{}{"n": int64(1)<<53 + 1})
	if a == "" || a == b {
		t.Fatalf("2^53 and 2^53+1 must not share a digest: %q %q", a, b)
	}
	// Key order and nesting do not matter; a changed value does.
	x := DigestOf(map[string]interface{}{"b": []interface{}{1, "two", map[string]interface{}{"z": 1, "a": 2}}, "a": nil})
	y := DigestOf(map[string]interface{}{"a": nil, "b": []interface{}{1, "two", map[string]interface{}{"a": 2, "z": 1}}})
	if x == "" || x != y {
		t.Fatalf("canonical form: %q %q", x, y)
	}
	if DigestOf(map[string]interface{}{"a": 1.5}) == DigestOf(map[string]interface{}{"a": 1.6}) {
		t.Fatal("a changed number changes the digest")
	}
	// Two different records that cannot be encoded used to share the digest of the error text.
	if d := DigestOf(map[string]interface{}{"c": make(chan int)}); d != "" {
		t.Fatalf("an unencodable record has no digest, got %q", d)
	}
	if d := DigestOf(func() {}); d != "" {
		t.Fatalf("got %q", d)
	}
}
