package contracts

import "strings"

// ApproverConstraints are the limits a recipe step declares on who may decide its gate
// (approver_emails, approver_roles, approver_domain, require_same_tenant). An edition that
// verifies the signer before the decision is applied (Pro's signing service) asks Admits so a
// refused person is refused before a signature is spent; the workflow engine applies the same
// rules when the decision lands, and a test pins the two to one table.
type ApproverConstraints struct {
	Emails []string `json:"emails,omitempty"`
	Roles  []string `json:"roles,omitempty"`
	Domain string   `json:"domain,omitempty"`
	// SameTenant is checked against the identity provider's issuer, which only the engine knows:
	// Admits never refuses on it alone.
	SameTenant bool `json:"same_tenant,omitempty"`
}

// Empty reports whether the step declares no constraint at all (an open gate).
func (c *ApproverConstraints) Empty() bool {
	return c == nil || (len(c.Emails) == 0 && len(c.Roles) == 0 && c.Domain == "" && !c.SameTenant)
}

// Admits reports whether a person with this e-mail and role in the gate's kitchen satisfies the
// step's constraints, and why not when they do not. A named person or a listed role qualifies
// outright, whatever the domain; when the step names people or roles and the approver is neither,
// the gate is closed to them unless a domain or tenant constraint can still admit them; a domain
// constraint must then hold. A role counts only when role is known.
func (c *ApproverConstraints) Admits(email, role string) (bool, string) {
	if c.Empty() {
		return true, ""
	}
	email = strings.ToLower(strings.TrimSpace(email))
	emailListed := false
	for _, a := range c.Emails {
		if strings.ToLower(strings.TrimSpace(a)) == email {
			emailListed = true
			break
		}
	}
	roleListed := role != "" && ApproverRoleListed(c.Roles, role)
	namesPeople := len(c.Emails) > 0 || (len(c.Roles) > 0 && role != "")
	if namesPeople {
		if emailListed || roleListed {
			return true, ""
		}
		if c.Domain == "" && !c.SameTenant {
			switch {
			case len(c.Emails) > 0 && len(c.Roles) > 0:
				return false, "neither on the approver list nor in an approver role (" + strings.Join(c.Roles, ", ") + ")"
			case len(c.Roles) > 0:
				return false, "role " + role + " is not an approver role for this step (" + strings.Join(c.Roles, ", ") + ")"
			}
			return false, "not in the approver allow-list"
		}
	}
	if c.Domain != "" {
		if !strings.HasSuffix(email, "@"+strings.ToLower(strings.TrimSpace(c.Domain))) {
			return false, "e-mail does not belong to domain " + c.Domain
		}
	}
	return true, ""
}

// ApproverRoleListed reports whether role is one of the roles a step names. The retired "admin" in a
// list means a Head Chef or a Restaurateur, and a Restaurateur is admitted wherever a Head Chef is.
func ApproverRoleListed(listed []string, role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	for _, l := range listed {
		l = strings.ToLower(strings.TrimSpace(l))
		switch {
		case l == role:
			return true
		case l == "admin" && (role == RoleHeadChef || role == RoleRestaurateur):
			return true
		case l == RoleHeadChef && role == RoleRestaurateur:
			return true
		}
	}
	return false
}
