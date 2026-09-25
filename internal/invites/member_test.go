package invites

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	otherServer = "c7fpx9m2ab"
	thirdServer = "d4hk8s2wqz"
)

// existing is the project's servers in most tests.
var existing = []string{serverID, otherServer, thirdServer}

var (
	owner     = Account{UserID: 1, Name: "siya", InstallRole: InstallOwner}
	admin     = Account{UserID: 2, Name: "lena", InstallRole: InstallMember, ProjectRole: RoleAdmin, Servers: AllServers(), TwoFactor: true}
	moderator = Account{UserID: 3, Name: "tobi", InstallRole: InstallMember, ProjectRole: RoleModerator, Servers: AllServers()}
	viewer    = Account{UserID: 4, Name: "vic", InstallRole: InstallMember, ProjectRole: RoleViewer, Servers: AllServers()}
	outsider  = Account{UserID: 5, Name: "otto", InstallRole: InstallMember}
)

func with(a Account, f func(*Account)) Account {
	f(&a)
	return a
}

func TestCanGrant(t *testing.T) {
	scoped := with(admin, func(a *Account) { a.Servers = OnlyServers(serverID) })
	var hundred []string
	for range MaxScopeServers {
		hundred = append(hundred, newID())
	}
	for _, tc := range []struct {
		name    string
		inviter Account
		role    string
		servers Scope
		code    string
	}{
		{"owner invites an admin", owner, RoleAdmin, AllServers(), ""},
		{"owner invites a moderator for one server", owner, RoleModerator, OnlyServers(serverID), ""},
		{"owner invites a viewer for two servers", owner, RoleViewer, OnlyServers(serverID, otherServer), ""},
		{"owner invites for the most servers a list may name", owner, RoleViewer, OnlyServers(hundred...), ""},
		{"owner invites an owner", owner, InstallOwner, AllServers(), CodeRoleNotAllowed},
		{"admin invites a moderator", admin, RoleModerator, AllServers(), ""},
		{"admin invites a viewer for one server", admin, RoleViewer, OnlyServers(otherServer), ""},
		{"admin invites an admin", admin, RoleAdmin, AllServers(), CodeRoleNotAllowed},
		{"admin invites an owner", admin, InstallOwner, AllServers(), CodeRoleNotAllowed},
		{"admin without two-factor", with(admin, func(a *Account) { a.TwoFactor = false }), RoleModerator, AllServers(), CodeTwoFactorRequired},
		{"admin without two-factor invites an admin", with(admin, func(a *Account) { a.TwoFactor = false }), RoleAdmin, AllServers(), CodeRoleNotAllowed},
		{"admin of one server invites for it", scoped, RoleModerator, OnlyServers(serverID), ""},
		{"admin of one server invites for all", scoped, RoleModerator, AllServers(), CodeServersNotAllowed},
		{"admin of one server invites for another", scoped, RoleModerator, OnlyServers(otherServer), CodeServersNotAllowed},
		{"admin of one server invites for it and another", scoped, RoleViewer, OnlyServers(serverID, otherServer), CodeServersNotAllowed},
		{"admin with no readable servers", with(admin, func(a *Account) { a.Servers = Scope{} }), RoleViewer, OnlyServers(serverID), CodeServersNotAllowed},
		{"moderator invites a viewer", moderator, RoleViewer, AllServers(), CodeRoleNotAllowed},
		{"viewer invites a viewer", viewer, RoleViewer, AllServers(), CodeRoleNotAllowed},
		{"account outside the project", outsider, RoleViewer, AllServers(), CodeRoleNotAllowed},
		{"deleted account", Account{}, RoleViewer, AllServers(), CodeRoleNotAllowed},
		{"unknown install role", with(admin, func(a *Account) { a.InstallRole = "root" }), RoleViewer, AllServers(), CodeRoleNotAllowed},
		{"install member role as project role", owner, InstallMember, AllServers(), CodeRoleUnknown},
		{"made-up role", owner, "superuser", AllServers(), CodeRoleUnknown},
		{"role in capitals", owner, "Admin", AllServers(), CodeRoleUnknown},
		{"no role", owner, "", AllServers(), CodeRoleUnknown},
		{"no servers chosen", owner, RoleViewer, Scope{}, CodeBadOptions},
		{"all servers and some", owner, RoleViewer, Scope{All: true, Servers: []string{serverID}}, CodeBadOptions},
		{"a server twice", owner, RoleViewer, OnlyServers(serverID, otherServer, serverID), CodeBadOptions},
		{"a server id with a slash", owner, RoleViewer, OnlyServers("../../etc/"), CodeBadOptions},
		{"an empty server id", owner, RoleViewer, OnlyServers(""), CodeBadOptions},
		{"too many servers", owner, RoleViewer, OnlyServers(append(hundred, serverID)...), CodeBadOptions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CanGrant(tc.inviter, tc.role, tc.servers)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e := wantCode(t, err, tc.code)
			if tc.code == CodeBadOptions && e.Params["field"] != "servers" {
				t.Errorf("refusal %+v names another field", e)
			}
		})
	}
	if e := wantCode(t, CanGrant(owner, InstallOwner, AllServers()), CodeRoleNotAllowed); e.Msg != "Invites can't make someone the owner." {
		t.Errorf("message = %q", e.Msg)
	}
	if e := wantCode(t, CanGrant(admin, RoleAdmin, AllServers()), CodeRoleNotAllowed); e.Params["role"] != RoleAdmin || e.Status != 403 {
		t.Errorf("refusal %+v", e)
	}
	e := wantCode(t, CanGrant(with(admin, func(a *Account) { a.TwoFactor = false }), RoleViewer, AllServers()), CodeTwoFactorRequired)
	if e.Status != 403 || e.Msg != "Set up two-factor sign-in before you can use admin rights." || e.Hint != "Turn it on from the Account page." {
		t.Errorf("refusal %+v", e)
	}
	if e := wantCode(t, CanGrant(scoped, RoleViewer, AllServers()), CodeServersNotAllowed); e.Status != 403 || e.Msg != "You can only give access to servers you can use yourself." {
		t.Errorf("refusal %+v", e)
	}
}

func TestGrantableRoles(t *testing.T) {
	for _, tc := range []struct {
		inviter Account
		want    []string
	}{
		{owner, []string{RoleAdmin, RoleModerator, RoleViewer}},
		{admin, []string{RoleModerator, RoleViewer}},
		{with(admin, func(a *Account) { a.Servers = OnlyServers(serverID) }), []string{RoleModerator, RoleViewer}},
		{with(admin, func(a *Account) { a.TwoFactor = false }), nil},
		{moderator, nil},
		{Account{}, nil},
	} {
		if got := GrantableRoles(tc.inviter); !slices.Equal(got, tc.want) {
			t.Errorf("GrantableRoles(%+v) = %v, want %v", tc.inviter, got, tc.want)
		}
	}
}

func TestCanLetPlayersIn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account Account
		server  string
		ok      bool
	}{
		{"owner", owner, serverID, true},
		{"owner, no server", owner, "", false},
		{"owner, bad server id", owner, "../x", false},
		{"admin", admin, serverID, true},
		{"admin without two-factor", with(admin, func(a *Account) { a.TwoFactor = false }), serverID, true},
		{"admin of another server", with(admin, func(a *Account) { a.Servers = OnlyServers(otherServer) }), serverID, false},
		{"moderator", moderator, serverID, true},
		{"moderator of this server", with(moderator, func(a *Account) { a.Servers = OnlyServers(otherServer, serverID) }), serverID, true},
		{"moderator of another server", with(moderator, func(a *Account) { a.Servers = OnlyServers(otherServer) }), serverID, false},
		{"moderator with no readable servers", with(moderator, func(a *Account) { a.Servers = Scope{} }), serverID, false},
		{"viewer", viewer, serverID, false},
		{"account outside the project", outsider, serverID, false},
		{"deleted account", Account{}, serverID, false},
		{"unknown install role", with(moderator, func(a *Account) { a.InstallRole = "root" }), serverID, false},
		{"unknown project role", with(moderator, func(a *Account) { a.ProjectRole = "Moderator" }), serverID, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CanLetPlayersIn(tc.account, tc.server)
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if e := wantCode(t, err, CodePlayersNotAllowed); e.Status != 403 || e.Msg != "You can't let players into this server." {
				t.Errorf("refusal %+v", e)
			}
		})
	}
}

func TestRequiresTwoFactor(t *testing.T) {
	for _, tc := range []struct {
		install, project string
		want             bool
	}{
		{InstallMember, RoleAdmin, true},
		{InstallMember, RoleModerator, false},
		{InstallMember, RoleViewer, false},
		{InstallMember, "", false},
		{InstallOwner, RoleAdmin, false},
		{InstallOwner, "", false},
	} {
		if got := RequiresTwoFactor(tc.install, tc.project); got != tc.want {
			t.Errorf("RequiresTwoFactor(%q, %q) = %v", tc.install, tc.project, got)
		}
	}
}

func TestNewMember(t *testing.T) {
	c, err := NewMember(MemberSpec{ProjectID: projectID, Role: RoleModerator, Servers: AllServers(), Label: "Sam"}, admin, existing, t0)
	if err != nil {
		t.Fatal(err)
	}
	inv := c.Invite
	if inv.Kind != KindMember || inv.Role != RoleModerator || !inv.Servers.All || inv.MaxUses != 1 || inv.CreatedBy != admin.UserID || inv.ServerID != "" ||
		inv.ExpiresAt.Sub(inv.CreatedAt) != 7*24*time.Hour || c.Path != "/join/"+codeOf(t, c) || inv.CodeHash != HashCode(codeOf(t, c)) {
		t.Errorf("invite %#v, path %q", inv.Summarize(t0), c.Path)
	}
	if inv.Code != "" || inv.Path() != "" || inv.Summarize(t0).Path != "" {
		t.Error("a co-admin invite's code must be shown once, not stored")
	}
	leaks(t, codeOf(t, c), c, inv, inv.Summarize(t0))

	some, err := NewMember(MemberSpec{ProjectID: projectID, Role: RoleViewer, Servers: OnlyServers(serverID, otherServer)}, owner, existing, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got := some.Invite.Servers; got.All || !slices.Equal(got.Servers, []string{otherServer, serverID}) || got.String() != otherServer+","+serverID {
		t.Errorf("servers %+v, want both, in order", got)
	}
	b, _ := json.Marshal(some.Invite.Summarize(t0))
	if !strings.Contains(string(b), `"servers":{"servers":["`+otherServer+`","`+serverID+`"]}`) {
		t.Errorf("summary JSON %s", b)
	}

	for _, tc := range []struct {
		name     string
		spec     MemberSpec
		inviter  Account
		existing []string
		code     string
	}{
		{"admin invites an admin", MemberSpec{ProjectID: projectID, Role: RoleAdmin, Servers: AllServers()}, admin, existing, CodeRoleNotAllowed},
		{"owner invites an owner", MemberSpec{ProjectID: projectID, Role: InstallOwner, Servers: AllServers()}, owner, existing, CodeRoleNotAllowed},
		{"bad project", MemberSpec{ProjectID: "nope", Role: RoleViewer, Servers: AllServers()}, owner, existing, CodeBadOptions},
		{"no servers chosen", MemberSpec{ProjectID: projectID, Role: RoleViewer}, owner, existing, CodeBadOptions},
		{"a server of another project", MemberSpec{ProjectID: projectID, Role: RoleViewer, Servers: OnlyServers(serverID, "zzzzzzzzzz")}, owner, existing, CodeBadOptions},
		{"a project without servers", MemberSpec{ProjectID: projectID, Role: RoleViewer, Servers: OnlyServers(serverID)}, owner, nil, CodeBadOptions},
		{"admin of one server invites for all", MemberSpec{ProjectID: projectID, Role: RoleViewer, Servers: AllServers()},
			with(admin, func(a *Account) { a.Servers = OnlyServers(serverID) }), existing, CodeServersNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewMember(tc.spec, tc.inviter, tc.existing, t0)
			wantCode(t, err, tc.code)
		})
	}
	if _, err := NewMember(MemberSpec{ProjectID: projectID, Role: RoleViewer, Servers: AllServers()}, owner, nil, t0); err != nil {
		t.Errorf("all servers of a project without servers yet: %v", err)
	}
}

// The rules and messages are the panel's (internal/panel/auth.go).
func TestValidUsername(t *testing.T) {
	for _, u := range []string{"abc", "Alice", "bob.smith", "a-b_c", "Élodie", "Ωμέγα", "用户名", strings.Repeat("x", 32), "123"} {
		if err := ValidUsername(u); err != nil {
			t.Errorf("ValidUsername(%q) = %v", u, err)
		}
	}
	for _, tc := range []struct{ u, rule string }{
		{"", "length"},
		{"ab", "length"},
		{strings.Repeat("x", 33), "length"},
		{strings.Repeat("é", 33), "length"},
		{"has space", "characters"},
		{"a/b", "characters"},
		{"alice@example", "characters"},
		{"wink😉", "characters"},
		{"tab\there", "characters"},
	} {
		e := wantCode(t, ValidUsername(tc.u), CodeUsername)
		want := map[string]string{"length": "Username must be 3–32 characters.", "characters": "Username may contain letters, numbers, dot, dash and underscore."}[tc.rule]
		if e.Params["rule"] != tc.rule || e.Msg != want {
			t.Errorf("ValidUsername(%q) = %q (%s), want %q", tc.u, e.Msg, e.Params["rule"], want)
		}
	}
}

func TestValidPassword(t *testing.T) {
	for _, tc := range []struct{ pw, user, rule string }{
		{"correct horse", "alice", ""},
		{strings.Repeat("x", 10), "alice", ""},
		{strings.Repeat("x", 256), "alice", ""},
		{"", "alice", "too_short"},
		{strings.Repeat("x", 9), "alice", "too_short"},
		{strings.Repeat("é", 9), "alice", "too_short"},
		{strings.Repeat("x", 257), "alice", "too_long"},
		{strings.Repeat("é", 129), "alice", "too_long"},
		{"AliceSmith1", "alicesmith1", "same_as_username"},
	} {
		err := ValidPassword(tc.pw, tc.user)
		if tc.rule == "" {
			if err != nil {
				t.Errorf("ValidPassword(%d bytes) = %v", len(tc.pw), err)
			}
			continue
		}
		e := wantCode(t, err, CodePassword)
		want := map[string]string{
			"too_short":        "Password must be at least 10 characters.",
			"too_long":         "Password must be at most 256 bytes.",
			"same_as_username": "Password must differ from the username.",
		}[tc.rule]
		if e.Params["rule"] != tc.rule || e.Msg != want {
			t.Errorf("ValidPassword(%d bytes) = %q (%s), want %q", len(tc.pw), e.Msg, e.Params["rule"], want)
		}
	}
}

func memberInvite(t *testing.T, inviter Account, role string, servers Scope) (Invite, string) {
	t.Helper()
	c, err := NewMember(MemberSpec{ProjectID: projectID, Role: role, Servers: servers}, inviter, existing, t0)
	if err != nil {
		t.Fatal(err)
	}
	return c.Invite, codeOf(t, c)
}

func TestAcceptMember(t *testing.T) {
	inv, code := memberInvite(t, admin, RoleModerator, AllServers())
	req := MemberRequest{Code: code, Username: "sam", Password: "correct horse"}
	got, err := AcceptMember(inv, req, admin, existing, t0)
	if err != nil {
		t.Fatal(err)
	}
	want := MemberGrant{InviteID: inv.ID, ProjectID: projectID, Role: RoleModerator, Servers: AllServers(), InstallRole: InstallMember, Username: "sam"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	used, _ := RecordUse(inv, t0)
	player, playerCode := newPlayer(t, PlayerSpec{})
	withReq := func(f func(*MemberRequest)) MemberRequest {
		r := req
		f(&r)
		return r
	}
	for _, tc := range []struct {
		name    string
		inv     Invite
		req     MemberRequest
		inviter Account
		now     time.Time
		code    string
	}{
		{"wrong code", inv, withReq(func(r *MemberRequest) { r.Code = NewCode() }), admin, t0, CodeNotWorking},
		{"wrong code and bad username", inv, withReq(func(r *MemberRequest) { r.Code = "x"; r.Username = "no" }), admin, t0, CodeNotWorking},
		{"friend invite used to make an account", player, withReq(func(r *MemberRequest) { r.Code = playerCode }), owner, t0, CodeNotWorking},
		{"expired", inv, req, admin, inv.ExpiresAt, CodeExpired},
		{"already accepted", used, req, admin, t0, CodeUsedUp},
		{"revoked", Revoke(inv, t0), req, admin, t0, CodeNotWorking},
		{"creator is now a viewer", inv, req, with(admin, func(a *Account) { a.ProjectRole = RoleViewer }), t0, CodeNotWorking},
		{"creator left the project", inv, req, with(admin, func(a *Account) { a.ProjectRole = "" }), t0, CodeNotWorking},
		{"creator turned two-factor off", inv, req, with(admin, func(a *Account) { a.TwoFactor = false }), t0, CodeNotWorking},
		{"creator now has only one server", inv, req, with(admin, func(a *Account) { a.Servers = OnlyServers(serverID) }), t0, CodeNotWorking},
		{"creator was deleted", inv, req, Account{}, t0, CodeNotWorking},
		{"creator was deleted after it expired", inv, req, Account{}, inv.ExpiresAt, CodeNotWorking},
		{"creator was deleted after it was used", used, req, Account{}, t0, CodeNotWorking},
		{"someone else passed as creator", inv, req, owner, t0, CodeNotWorking},
		{"username too short", inv, withReq(func(r *MemberRequest) { r.Username = "sa" }), admin, t0, CodeUsername},
		{"username with a space", inv, withReq(func(r *MemberRequest) { r.Username = "sam smith" }), admin, t0, CodeUsername},
		{"password too short", inv, withReq(func(r *MemberRequest) { r.Password = "hunter2" }), admin, t0, CodePassword},
		{"password is the username", inv, withReq(func(r *MemberRequest) { r.Username = "samwise1234"; r.Password = "SAMWISE1234" }), admin, t0, CodePassword},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AcceptMember(tc.inv, tc.req, tc.inviter, existing, tc.now)
			e := wantCode(t, err, tc.code)
			if tc.code == CodeNotWorking && e.Msg != NotFound().Msg {
				t.Errorf("message %q gives away more than an unknown link would", e.Msg)
			}
		})
	}
}

func TestAcceptMemberAsAdmin(t *testing.T) {
	inv, code := memberInvite(t, owner, RoleAdmin, AllServers())
	got, err := AcceptMember(inv, MemberRequest{Code: code, Username: "lena", Password: "correct horse"}, owner, existing, t0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Requirement{{Code: "two_factor_required", Text: "Set up two-factor sign-in before you can use admin rights.", Hint: "Turn it on from the Account page."}}
	if got.Role != RoleAdmin || !reflect.DeepEqual(got.Requires, want) {
		t.Errorf("grant %+v", got)
	}
	for _, role := range []string{RoleModerator, RoleViewer} {
		inv, code := memberInvite(t, owner, role, AllServers())
		if g, err := AcceptMember(inv, MemberRequest{Code: code, Username: "sam", Password: "correct horse"}, owner, existing, t0); err != nil || g.Requires != nil {
			t.Errorf("%s: %+v, %v", role, g, err)
		}
	}
}

func TestAcceptMemberServers(t *testing.T) {
	inv, code := memberInvite(t, owner, RoleViewer, OnlyServers(serverID, otherServer))
	req := MemberRequest{Code: code, Username: "sam", Password: "correct horse"}
	for _, tc := range []struct {
		name     string
		existing []string
		want     []string
	}{
		{"both still there", existing, []string{otherServer, serverID}},
		{"one deleted since", []string{serverID, thirdServer}, []string{serverID}},
		{"order doesn't matter", []string{thirdServer, otherServer, serverID}, []string{otherServer, serverID}},
	} {
		got, err := AcceptMember(inv, req, owner, tc.existing, t0)
		if err != nil || got.Servers.All || !slices.Equal(got.Servers.Servers, tc.want) {
			t.Errorf("%s: servers %+v, %v", tc.name, got.Servers, err)
		}
	}
	for name, left := range map[string][]string{"both deleted": {thirdServer}, "no servers left": nil} {
		_, err := AcceptMember(inv, req, owner, left, t0)
		if e := wantCode(t, err, CodeNotWorking); e.Reason != "none of the invite's servers exist any more" {
			t.Errorf("%s: reason %q", name, e.Reason)
		}
	}

	scoped := with(admin, func(a *Account) { a.Servers = OnlyServers(serverID, otherServer) })
	inv, code = memberInvite(t, scoped, RoleViewer, OnlyServers(serverID))
	req.Code = code
	if _, err := AcceptMember(inv, req, scoped, existing, t0); err != nil {
		t.Fatal(err)
	}
	moved := with(scoped, func(a *Account) { a.Servers = OnlyServers(otherServer) })
	_, err := AcceptMember(inv, req, moved, existing, t0)
	wantCode(t, err, CodeNotWorking)

	all, allCode := memberInvite(t, owner, RoleModerator, AllServers())
	got, err := AcceptMember(all, MemberRequest{Code: allCode, Username: "sam", Password: "correct horse"}, owner, nil, t0)
	if err != nil || !got.Servers.All {
		t.Errorf("all servers of a project that has none now: %+v, %v", got.Servers, err)
	}
}

func TestAcceptMemberAfterPromotion(t *testing.T) {
	inv, code := memberInvite(t, admin, RoleViewer, AllServers())
	promoted := Account{UserID: admin.UserID, InstallRole: InstallOwner}
	if _, err := AcceptMember(inv, MemberRequest{Code: code, Username: "sam", Password: "correct horse"}, promoted, existing, t0); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewMember(t *testing.T) {
	c, err := NewMember(MemberSpec{ProjectID: projectID, Role: RoleAdmin, Servers: AllServers(), Label: "Secret plans"}, owner, existing, t0)
	if err != nil {
		t.Fatal(err)
	}
	inv, code := c.Invite, codeOf(t, c)
	p, err := PreviewMember(inv, code, owner, existing, t0)
	if err != nil {
		t.Fatal(err)
	}
	want := MemberPage{Kind: KindMember, Inviter: "siya", Role: RoleAdmin, Servers: AllServers(), ExpiresAt: inv.ExpiresAt, Requires: []Requirement{twoFactorRequirement()}}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("preview %+v", p)
	}
	b, _ := json.Marshal(p)
	for _, s := range []string{"Secret plans", "createdBy", "uses", inv.ID, code, inv.CodeHash} {
		if strings.Contains(string(b), s) {
			t.Errorf("preview %s shows %q", b, s)
		}
	}
	if !strings.Contains(string(b), `"inviter":"siya","role":"admin","servers":{"all":true}`) || !strings.Contains(string(b), `"requires":[{"code":"two_factor_required"`) {
		t.Errorf("preview JSON %s", b)
	}
	_, err = PreviewMember(inv, code, Account{}, existing, t0)
	wantCode(t, err, CodeNotWorking)
	_, err = PreviewMember(inv, NewCode(), owner, existing, t0)
	wantCode(t, err, CodeNotWorking)
	_, err = PreviewMember(inv, code, owner, existing, inv.ExpiresAt)
	wantCode(t, err, CodeExpired)

	some, someCode := memberInvite(t, owner, RoleModerator, OnlyServers(serverID, otherServer))
	p, err = PreviewMember(some, someCode, owner, []string{serverID}, t0)
	if err != nil || !slices.Equal(p.Servers.Servers, []string{serverID}) || p.Requires != nil {
		t.Errorf("preview %+v, %v", p, err)
	}
}

func TestMemberRequestHidesSecrets(t *testing.T) {
	req := MemberRequest{Code: NewCode(), Username: "sam", Password: "correct horse battery"}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("accept", "req", req)
	slog.New(slog.NewTextHandler(&logged, nil)).Info("accept", "req", &req)
	for _, s := range []string{fmt.Sprint(req), fmt.Sprintf("%+v", req), fmt.Sprintf("%#v", req), fmt.Sprintf("%v", &req), logged.String()} {
		if strings.Contains(s, req.Code) || strings.Contains(s, req.Password) || !strings.Contains(s, "sam") {
			t.Errorf("printed as %q", s)
		}
	}
}

func TestUsernameTaken(t *testing.T) {
	e := UsernameTaken()
	if e.Code != CodeUsernameTaken || e.Status != 409 || e.Msg != "That username is taken." {
		t.Errorf("%+v", e)
	}
}
