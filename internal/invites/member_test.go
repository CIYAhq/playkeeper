package invites

import (
	"bytes"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	owner     = Inviter{UserID: 1, InstallRole: InstallOwner}
	admin     = Inviter{UserID: 2, InstallRole: InstallMember, ProjectRole: RoleAdmin}
	moderator = Inviter{UserID: 3, InstallRole: InstallMember, ProjectRole: RoleModerator}
	viewer    = Inviter{UserID: 4, InstallRole: InstallMember, ProjectRole: RoleViewer}
	outsider  = Inviter{UserID: 5, InstallRole: InstallMember}
)

func TestCanGrant(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inviter Inviter
		role    string
		code    string
	}{
		{"owner invites an admin", owner, RoleAdmin, ""},
		{"owner invites a moderator", owner, RoleModerator, ""},
		{"owner invites a viewer", owner, RoleViewer, ""},
		{"owner invites an owner", owner, InstallOwner, CodeRoleNotAllowed},
		{"admin invites a moderator", admin, RoleModerator, ""},
		{"admin invites a viewer", admin, RoleViewer, ""},
		{"admin invites an admin", admin, RoleAdmin, CodeRoleNotAllowed},
		{"admin invites an owner", admin, InstallOwner, CodeRoleNotAllowed},
		{"moderator invites a viewer", moderator, RoleViewer, CodeRoleNotAllowed},
		{"viewer invites a viewer", viewer, RoleViewer, CodeRoleNotAllowed},
		{"account outside the project", outsider, RoleViewer, CodeRoleNotAllowed},
		{"deleted account", Inviter{}, RoleViewer, CodeRoleNotAllowed},
		{"unknown install role", Inviter{UserID: 6, InstallRole: "root", ProjectRole: RoleAdmin}, RoleViewer, CodeRoleNotAllowed},
		{"install member role as project role", owner, InstallMember, CodeRoleUnknown},
		{"made-up role", owner, "superuser", CodeRoleUnknown},
		{"role in capitals", owner, "Admin", CodeRoleUnknown},
		{"no role", owner, "", CodeRoleUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CanGrant(tc.inviter, tc.role)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantCode(t, err, tc.code)
		})
	}
	if e := wantCode(t, CanGrant(owner, InstallOwner), CodeRoleNotAllowed); e.Msg != "Invites can't make someone the owner." {
		t.Errorf("message = %q", e.Msg)
	}
	if e := wantCode(t, CanGrant(admin, RoleAdmin), CodeRoleNotAllowed); e.Params["role"] != RoleAdmin || e.Status != 403 {
		t.Errorf("refusal %+v", e)
	}
}

func TestGrantableRoles(t *testing.T) {
	for _, tc := range []struct {
		inviter Inviter
		want    []string
	}{
		{owner, []string{RoleAdmin, RoleModerator, RoleViewer}},
		{admin, []string{RoleModerator, RoleViewer}},
		{moderator, nil},
		{Inviter{}, nil},
	} {
		if got := GrantableRoles(tc.inviter); !slices.Equal(got, tc.want) {
			t.Errorf("GrantableRoles(%+v) = %v, want %v", tc.inviter, got, tc.want)
		}
	}
}

func TestNewMember(t *testing.T) {
	c, err := NewMember(MemberSpec{ProjectID: projectID, Role: RoleModerator, Label: "Sam"}, admin, t0)
	if err != nil {
		t.Fatal(err)
	}
	inv := c.Invite
	if inv.Kind != KindMember || inv.Role != RoleModerator || inv.MaxUses != 1 || inv.CreatedBy != admin.UserID || inv.ServerID != "" ||
		inv.ExpiresAt.Sub(inv.CreatedAt) != DefaultMemberTTL || c.Path != "/join/"+codeOf(t, c) || inv.CodeHash != HashCode(codeOf(t, c)) {
		t.Errorf("invite %#v, path %q", inv.Summarize(t0), c.Path)
	}
	if inv.Code != "" || inv.Path() != "" || inv.Summarize(t0).Path != "" {
		t.Error("a co-admin invite's code must be shown once, not stored")
	}
	leaks(t, codeOf(t, c), c, inv, inv.Summarize(t0))

	_, err = NewMember(MemberSpec{ProjectID: projectID, Role: RoleAdmin}, admin, t0)
	wantCode(t, err, CodeRoleNotAllowed)
	_, err = NewMember(MemberSpec{ProjectID: projectID, Role: InstallOwner}, owner, t0)
	wantCode(t, err, CodeRoleNotAllowed)
	_, err = NewMember(MemberSpec{ProjectID: projectID, Role: RoleViewer, TTL: MaxMemberTTL + time.Second}, owner, t0)
	if e := wantCode(t, err, CodeBadOptions); e.Params["field"] != "expiry" || e.Params["maxDays"] != "7" {
		t.Errorf("refusal %+v", e)
	}
	if _, err := NewMember(MemberSpec{ProjectID: projectID, Role: RoleViewer, TTL: MaxMemberTTL}, owner, t0); err != nil {
		t.Error(err)
	}
	_, err = NewMember(MemberSpec{ProjectID: "nope", Role: RoleViewer}, owner, t0)
	wantCode(t, err, CodeBadOptions)
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

func memberInvite(t *testing.T, inviter Inviter, role string) (Invite, string) {
	t.Helper()
	c, err := NewMember(MemberSpec{ProjectID: projectID, Role: role}, inviter, t0)
	if err != nil {
		t.Fatal(err)
	}
	return c.Invite, codeOf(t, c)
}

func TestAcceptMember(t *testing.T) {
	inv, code := memberInvite(t, admin, RoleModerator)
	req := MemberRequest{Code: code, Username: "sam", Password: "correct horse"}
	got, err := AcceptMember(inv, req, admin, t0)
	if err != nil {
		t.Fatal(err)
	}
	want := MemberGrant{InviteID: inv.ID, ProjectID: projectID, Role: RoleModerator, InstallRole: InstallMember, Username: "sam"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	used, _ := RecordUse(inv, t0)
	player, playerCode := newPlayer(t, PlayerSpec{})
	with := func(f func(*MemberRequest)) MemberRequest {
		r := req
		f(&r)
		return r
	}
	for _, tc := range []struct {
		name    string
		inv     Invite
		req     MemberRequest
		inviter Inviter
		now     time.Time
		code    string
	}{
		{"wrong code", inv, with(func(r *MemberRequest) { r.Code = NewCode() }), admin, t0, CodeNotWorking},
		{"wrong code and bad username", inv, with(func(r *MemberRequest) { r.Code = "x"; r.Username = "no" }), admin, t0, CodeNotWorking},
		{"friend invite used to make an account", player, with(func(r *MemberRequest) { r.Code = playerCode }), Inviter{UserID: 1, InstallRole: InstallOwner}, t0, CodeNotWorking},
		{"expired", inv, req, admin, inv.ExpiresAt, CodeExpired},
		{"already accepted", used, req, admin, t0, CodeUsedUp},
		{"revoked", Revoke(inv, t0), req, admin, t0, CodeNotWorking},
		{"creator is now a viewer", inv, req, Inviter{UserID: 2, InstallRole: InstallMember, ProjectRole: RoleViewer}, t0, CodeNotWorking},
		{"creator left the project", inv, req, Inviter{UserID: 2, InstallRole: InstallMember}, t0, CodeNotWorking},
		{"creator was deleted", inv, req, Inviter{}, t0, CodeNotWorking},
		{"someone else passed as creator", inv, req, owner, t0, CodeNotWorking},
		{"username too short", inv, with(func(r *MemberRequest) { r.Username = "sa" }), admin, t0, CodeUsername},
		{"username with a space", inv, with(func(r *MemberRequest) { r.Username = "sam smith" }), admin, t0, CodeUsername},
		{"password too short", inv, with(func(r *MemberRequest) { r.Password = "hunter2" }), admin, t0, CodePassword},
		{"password is the username", inv, with(func(r *MemberRequest) { r.Username = "samwise1234"; r.Password = "SAMWISE1234" }), admin, t0, CodePassword},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AcceptMember(tc.inv, tc.req, tc.inviter, tc.now)
			e := wantCode(t, err, tc.code)
			if tc.code == CodeNotWorking && e.Msg != NotFound().Msg {
				t.Errorf("message %q gives away more than an unknown link would", e.Msg)
			}
		})
	}
}

func TestAcceptMemberAfterPromotion(t *testing.T) {
	inv, code := memberInvite(t, admin, RoleViewer)
	promoted := Inviter{UserID: admin.UserID, InstallRole: InstallOwner}
	if _, err := AcceptMember(inv, MemberRequest{Code: code, Username: "sam", Password: "correct horse"}, promoted, t0); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewMember(t *testing.T) {
	inv, code := memberInvite(t, owner, RoleAdmin)
	p, err := PreviewMember(inv, code, owner, t0)
	if err != nil {
		t.Fatal(err)
	}
	if p != (Public{Kind: KindMember, Role: RoleAdmin, ExpiresAt: inv.ExpiresAt}) {
		t.Errorf("preview %+v", p)
	}
	_, err = PreviewMember(inv, code, Inviter{}, t0)
	wantCode(t, err, CodeNotWorking)
	_, err = PreviewMember(inv, NewCode(), owner, t0)
	wantCode(t, err, CodeNotWorking)
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
