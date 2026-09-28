package invites

import (
	"reflect"
	"testing"
)

var wave1 = Allowance{Servers: 1, MemoryMB: 4096}

func creatorInvite(t *testing.T) (Invite, string) {
	t.Helper()
	c, err := NewMember(MemberSpec{ProjectID: projectID, Role: RoleAdmin, Allowance: wave1, Label: "alex"}, owner, existing, t0)
	if err != nil {
		t.Fatal(err)
	}
	return c.Invite, codeOf(t, c)
}

// A creator invite gives Admin with no servers and an allowance, and only
// the owner can make one.
func TestNewCreatorInvite(t *testing.T) {
	inv, _ := creatorInvite(t)
	if inv.Role != RoleAdmin || inv.Servers.All || len(inv.Servers.Servers) != 0 || inv.Allowance != wave1 || inv.MaxUses != 1 || !inv.ExpiresAt.Equal(stamp(t0).Add(MemberLifetime)) {
		t.Fatalf("creator invite %+v", inv)
	}
	scoped := with(admin, func(a *Account) { a.Servers = OnlyServers(serverID) })
	creator := Account{UserID: 6, Name: "alex", InstallRole: InstallMember, ProjectRole: RoleAdmin, TwoFactor: true, Allowance: wave1}
	for _, tc := range []struct {
		name    string
		inviter Account
		spec    MemberSpec
		code    string
	}{
		{"an admin of every server", admin, MemberSpec{Role: RoleAdmin, Allowance: wave1}, CodeRoleNotAllowed},
		{"an admin of one server", scoped, MemberSpec{Role: RoleAdmin, Allowance: wave1}, CodeRoleNotAllowed},
		{"a creator", creator, MemberSpec{Role: RoleAdmin, Allowance: wave1}, CodeRoleNotAllowed},
		{"with a server", owner, MemberSpec{Role: RoleAdmin, Servers: OnlyServers(serverID), Allowance: wave1}, CodeBadOptions},
		{"with every server", owner, MemberSpec{Role: RoleAdmin, Servers: AllServers(), Allowance: wave1}, CodeBadOptions},
		{"as a moderator", owner, MemberSpec{Role: RoleModerator, Allowance: wave1}, CodeBadOptions},
		{"no servers allowed", owner, MemberSpec{Role: RoleAdmin, Allowance: Allowance{MemoryMB: 4096}}, CodeBadOptions},
		{"too many servers", owner, MemberSpec{Role: RoleAdmin, Allowance: Allowance{Servers: MaxAllowanceServers + 1, MemoryMB: 4096}}, CodeBadOptions},
		{"too little memory", owner, MemberSpec{Role: RoleAdmin, Allowance: Allowance{Servers: 1, MemoryMB: 512}}, CodeBadOptions},
		{"too much memory", owner, MemberSpec{Role: RoleAdmin, Allowance: Allowance{Servers: 1, MemoryMB: MaxAllowanceMemoryMB + 512}}, CodeBadOptions},
		{"memory off the half-gigabyte steps", owner, MemberSpec{Role: RoleAdmin, Allowance: Allowance{Servers: 1, MemoryMB: 4000}}, CodeBadOptions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.ProjectID = projectID
			_, err := NewMember(tc.spec, tc.inviter, existing, t0)
			wantCode(t, err, tc.code)
		})
	}
}

// Accepting makes an Admin with no servers and the allowance, however many
// servers the project has, and needs two-factor sign-in like any admin.
func TestAcceptCreatorInvite(t *testing.T) {
	inv, code := creatorInvite(t)
	req := MemberRequest{Code: code, Username: "alex", Password: "correct horse"}
	for _, servers := range [][]string{existing, nil} {
		got, err := AcceptMember(inv, req, owner, servers, t0)
		if err != nil {
			t.Fatal(err)
		}
		want := MemberGrant{InviteID: inv.ID, ProjectID: projectID, Role: RoleAdmin, InstallRole: InstallMember, Username: "alex",
			Allowance: wave1, Requires: []Requirement{twoFactorRequirement()}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("with servers %v: got %+v, want %+v", servers, got, want)
		}
	}
	page, err := PreviewMember(inv, code, owner, existing, t0)
	if err != nil || page.Allowance != wave1 || page.Role != RoleAdmin || page.Servers.All || len(page.Servers.Servers) != 0 {
		t.Fatalf("preview %+v, %v", page, err)
	}
	moderator := inv
	moderator.Role = RoleModerator
	used, _ := RecordUse(inv, t0)
	for _, tc := range []struct {
		name    string
		inv     Invite
		inviter Account
		code    string
	}{
		{"someone else passed as its creator", inv, admin, CodeNotWorking},
		{"its creator was deleted", inv, Account{}, CodeNotWorking},
		{"its role changed to one a creator can't have", moderator, owner, CodeNotWorking},
		{"already accepted", used, owner, CodeUsedUp},
		{"revoked", Revoke(inv, t0), owner, CodeNotWorking},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AcceptMember(tc.inv, req, tc.inviter, existing, t0)
			wantCode(t, err, tc.code)
		})
	}
}
