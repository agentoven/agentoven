package contracts

import "testing"

func TestApproverConstraintsAdmits(t *testing.T) {
	cases := []struct {
		name  string
		c     *ApproverConstraints
		email string
		role  string
		want  bool
	}{
		{"nil is open", nil, "a@x.test", "viewer", true},
		{"empty is open", &ApproverConstraints{}, "a@x.test", "viewer", true},
		{"role listed", &ApproverConstraints{Roles: []string{"chef", "head_chef"}}, "a@x.test", "chef", true},
		{"role not listed", &ApproverConstraints{Roles: []string{"chef"}}, "a@x.test", "baker", false},
		{"roles unknown, edition cannot say", &ApproverConstraints{Roles: []string{"chef"}}, "a@x.test", "", true},
		{"person listed, role not", &ApproverConstraints{Emails: []string{"A@x.test"}, Roles: []string{"head_chef"}}, "a@x.test", "baker", true},
		{"role listed, person not", &ApproverConstraints{Emails: []string{"z@x.test"}, Roles: []string{"baker"}}, "a@x.test", "baker", true},
		{"neither", &ApproverConstraints{Emails: []string{"z@x.test"}, Roles: []string{"head_chef"}}, "a@x.test", "baker", false},
		{"retired admin means a head chef", &ApproverConstraints{Roles: []string{"admin"}}, "a@x.test", "head_chef", true},
		{"restaurateur where head chef is", &ApproverConstraints{Roles: []string{"head_chef"}}, "a@x.test", "restaurateur", true},
		{"head chef where only restaurateur is", &ApproverConstraints{Roles: []string{"restaurateur"}}, "a@x.test", "head_chef", false},
		{"domain holds", &ApproverConstraints{Domain: "x.test"}, "a@X.test", "chef", true},
		{"domain fails", &ApproverConstraints{Domain: "x.test"}, "a@y.test", "chef", false},
		{"listed role beats domain", &ApproverConstraints{Roles: []string{"chef"}, Domain: "x.test"}, "a@y.test", "chef", true},
		{"unlisted role must still match domain", &ApproverConstraints{Roles: []string{"chef"}, Domain: "x.test"}, "a@y.test", "baker", false},
		{"same tenant alone is left to the engine", &ApproverConstraints{SameTenant: true}, "a@x.test", "baker", true},
		{"unlisted with same tenant falls through to the engine", &ApproverConstraints{Roles: []string{"chef"}, SameTenant: true}, "a@x.test", "baker", true},
	}
	for _, c := range cases {
		if got, why := c.c.Admits(c.email, c.role); got != c.want {
			t.Errorf("%s: got %v (%s), want %v", c.name, got, why, c.want)
		}
	}
}
