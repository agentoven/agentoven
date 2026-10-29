package contracts

import "testing"

func TestAdminHelpersKnowTheProRoles(t *testing.T) {
	cases := []struct {
		role          string
		platform, own bool
	}{
		{"admin", true, true},
		{"restaurateur", true, true},
		{"head_chef", false, true},
		{"chef", false, false},
		{"viewer", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		if got := IsPlatformAdmin(c.role); got != c.platform {
			t.Errorf("IsPlatformAdmin(%q) = %v, want %v", c.role, got, c.platform)
		}
		if got := IsKitchenOwner(c.role); got != c.own {
			t.Errorf("IsKitchenOwner(%q) = %v, want %v", c.role, got, c.own)
		}
	}
}
