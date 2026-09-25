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

// MemberRequest is what the accept page sends.
type MemberRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Format keeps the token and password out of anything printed with fmt.
func (r MemberRequest) Format(f fmt.State, verb rune) {
	fmt.Fprintf(f, "{Username:%q Token:[hidden] Password:[hidden]}", r.Username)
}

// LogValue keeps the token and password out of slog output.
func (r MemberRequest) LogValue() slog.Value {
	return slog.GroupValue(slog.String("username", r.Username))
}

// MemberGrant is the account to create for an accepted member invite:
// a users row with InstallRole and a project_members row with Role. The
// password is hashed by the panel as for any other account.
type MemberGrant struct {
	InviteID    string
	ProjectID   string
	Role        string
	InstallRole string
	Username    string
}

// PreviewMember checks a member invite for the accept page and returns
// what the page may show. inviter is the invite's creator as it stands
// now: an invite stops working when its creator can no longer give the
// role.
func PreviewMember(inv Invite, token string, inviter Inviter, now time.Time) (Public, error) {
	if err := checkMember(inv, token, inviter, now); err != nil {
		return Public{}, err
	}
	return inv.public(), nil
}

// AcceptMember checks a member invite and the account its holder chose.
// The caller then creates the account and counts the use (see RecordUse)
// in one transaction, refusing a username that is taken in any
// capitalisation with UsernameTaken.
func AcceptMember(inv Invite, req MemberRequest, inviter Inviter, now time.Time) (MemberGrant, error) {
	if err := checkMember(inv, req.Token, inviter, now); err != nil {
		return MemberGrant{}, err
	}
	if err := ValidUsername(req.Username); err != nil {
		return MemberGrant{}, err
	}
	if err := ValidPassword(req.Password, req.Username); err != nil {
		return MemberGrant{}, err
	}
	return MemberGrant{InviteID: inv.ID, ProjectID: inv.ProjectID, Role: inv.Role, InstallRole: InstallMember, Username: req.Username}, nil
}

func checkMember(inv Invite, token string, inviter Inviter, now time.Time) error {
	if err := Check(inv, token, KindMember, now); err != nil {
		return err
	}
	if inviter.UserID != inv.CreatedBy || CanGrant(inviter, inv.Role) != nil {
		return notWorking("the invite's creator can no longer give its role")
	}
	return nil
}
