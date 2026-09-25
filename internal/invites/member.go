package invites

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ValidUsername applies the panel's rules for usernames, with the same
// messages. It mirrors validUsername in internal/panel/auth.go and must
// change with it.
func ValidUsername(u string) error {
	if l := utf8.RuneCountInString(u); l < 3 || l > 32 {
		return usernameInvalid("length", "Username must be 3–32 characters.")
	}
	for _, r := range u {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.') {
			return usernameInvalid("characters", "Username may contain letters, numbers, dot, dash and underscore.")
		}
	}
	return nil
}

// ValidPassword applies the panel's rules for passwords, with the same
// messages. It mirrors validPassword in internal/panel/auth.go and must
// change with it.
func ValidPassword(pw, username string) error {
	if utf8.RuneCountInString(pw) < 10 {
		return passwordInvalid("too_short", "Password must be at least 10 characters.")
	}
	if len(pw) > 256 {
		return passwordInvalid("too_long", "Password must be at most 256 bytes.")
	}
	if strings.EqualFold(pw, username) {
		return passwordInvalid("same_as_username", "Password must differ from the username.")
	}
	return nil
}

// MemberRequest is what the join page sends to create an account.
type MemberRequest struct {
	Code     string `json:"code"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Format keeps the code and password out of anything printed with fmt.
func (r MemberRequest) Format(f fmt.State, verb rune) {
	fmt.Fprintf(f, "{Username:%q Code:[hidden] Password:[hidden]}", r.Username)
}

// LogValue keeps the code and password out of slog output.
func (r MemberRequest) LogValue() slog.Value {
	return slog.GroupValue(slog.String("username", r.Username))
}

// MemberGrant is the account to create for an accepted member invite:
// a users row with InstallRole and a project_members row with Role and
// Servers. The password is hashed by the panel as for any other account.
type MemberGrant struct {
	InviteID    string
	ProjectID   string
	Role        string
	Servers     Scope
	InstallRole string
	Username    string
	// Requires lists what the new member must do before they can use the
	// role. The panel enforces it (see RequiresTwoFactor).
	Requires []Requirement
}

// MemberPage is what the join page shows for a member invite that works.
type MemberPage struct {
	Kind      Kind          `json:"kind"`
	Inviter   string        `json:"inviter"`
	Role      string        `json:"role"`
	Servers   Scope         `json:"servers"`
	ExpiresAt time.Time     `json:"expiresAt"`
	Requires  []Requirement `json:"requires,omitempty"`
}

// PreviewMember checks a member invite for the join page and returns what
// the page may show. inviter is the invite's creator as it stands now, and
// existing lists the project's servers now: the invite stops working when
// its creator can no longer give its role or servers, and servers deleted
// since it was made drop out of it.
func PreviewMember(inv Invite, code string, inviter Account, existing []string, now time.Time) (MemberPage, error) {
	servers, err := checkMember(inv, code, inviter, existing, now)
	if err != nil {
		return MemberPage{}, err
	}
	return MemberPage{Kind: KindMember, Inviter: inviter.Name, Role: inv.Role, Servers: servers, ExpiresAt: inv.ExpiresAt,
		Requires: requirements(inv.Role)}, nil
}

// AcceptMember checks a member invite and the account its holder chose.
// The caller then creates the account and counts the use (see RecordUse)
// in one transaction, refusing a username that is taken in any
// capitalisation with UsernameTaken.
func AcceptMember(inv Invite, req MemberRequest, inviter Account, existing []string, now time.Time) (MemberGrant, error) {
	servers, err := checkMember(inv, req.Code, inviter, existing, now)
	if err != nil {
		return MemberGrant{}, err
	}
	if err := ValidUsername(req.Username); err != nil {
		return MemberGrant{}, err
	}
	if err := ValidPassword(req.Password, req.Username); err != nil {
		return MemberGrant{}, err
	}
	return MemberGrant{InviteID: inv.ID, ProjectID: inv.ProjectID, Role: inv.Role, Servers: servers, InstallRole: InstallMember,
		Username: req.Username, Requires: requirements(inv.Role)}, nil
}

func requirements(role string) []Requirement {
	if RequiresTwoFactor(InstallMember, role) {
		return []Requirement{twoFactorRequirement()}
	}
	return nil
}

// checkMember returns the servers the invite still gives. A creator who
// can no longer give the invite reads like a revoked link, ahead of expiry
// and use.
func checkMember(inv Invite, code string, inviter Account, existing []string, now time.Time) (Scope, error) {
	err := Check(inv, code, KindMember, now)
	if CodeOf(err) == CodeNotWorking {
		return Scope{}, err
	}
	servers, ok := inv.Servers.narrow(existing)
	switch {
	case !ok:
		return Scope{}, notWorking("none of the invite's servers exist any more")
	case inviter.UserID != inv.CreatedBy || CanGrant(inviter, inv.Role, servers) != nil:
		return Scope{}, notWorking("the invite's creator can no longer give its role or servers")
	case err != nil:
		return Scope{}, err
	}
	return servers, nil
}
