package accounts

import "testing"

// A user name starting with "-" must be refused before any tool is run.
func TestRejectOptionLikeNames(t *testing.T) {
	bad := "-oops"
	checks := map[string]error{
		"Create":         Create(bad, "Full Name", "password123", nil, false),
		"SetPassword":    SetPassword(bad, "password123"),
		"Update":         Update(bad, "Full Name", false),
		"SetEnabled":     SetEnabled(bad, true),
		"SetEnabledOff":  SetEnabled(bad, false),
		"Delete":         Delete(bad, false),
		"SetUserGroups":  SetUserGroups(bad, nil),
		"CreateGroup":    CreateGroup(bad),
		"DeleteGroup":    DeleteGroup(bad),
		"GroupMembers":   SetGroupMembers("users", []string{bad}),
		"GroupMembersGr": SetGroupMembers(bad, nil),
	}
	for name, err := range checks {
		if err == nil {
			t.Errorf("%s accepted an option-like name", name)
		}
	}
}
