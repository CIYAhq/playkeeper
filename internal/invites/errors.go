package invites

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
)

// Codes of the refusals in this package, stable for translation.
const (
	CodeNotWorking     = "invite_not_working"
	CodeExpired        = "invite_expired"
	CodeUsedUp         = "invite_used_up"
	CodeBadOptions     = "invite_options_invalid"
	CodeRoleUnknown    = "invite_role_unknown"
	CodeRoleNotAllowed = "invite_role_not_allowed"
	CodeRateLimited    = "rate_limited"
	CodePlayerName     = "player_name_invalid"
	CodePlayerUnknown  = "player_not_found"
	CodePlayerDemo     = "player_demo"
	CodePlayerLegacy   = "player_legacy"
	CodeMojangBusy     = "mojang_busy"
	CodeMojangDown     = "mojang_unavailable"
	CodeUsername       = "username_invalid"
	CodeUsernameTaken  = "username_taken"
	CodePassword       = "password_invalid"
)

// Error is a refusal a person may see. Code and Params are for the UI's
// translations; Error returns the English sentence, and Hint says what to
// do next. Reason and Err are for logs only: a public page never shows
// them, so they may say what the page must not (that a link was revoked,
// say). Neither ever contains a token or password.
type Error struct {
	Code   string
	Params map[string]string
	Msg    string
	Hint   string
	// Status is the HTTP status to answer with.
	Status int
	// RetryAfter is set when trying again later can work.
	RetryAfter time.Duration
	Reason     string
	Err        error
}

func (e *Error) Error() string { return e.Msg }

func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the code of an *Error in err's chain, or "".
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// NotFound is the refusal for a token that matches no stored invite. It
// reads exactly like a revoked link or one of the wrong kind.
func NotFound() *Error { return notWorking("no invite has this token") }

// UsernameTaken is the refusal when the username chosen on the accept page
// already belongs to an account.
func UsernameTaken() *Error {
	return &Error{Code: CodeUsernameTaken, Status: http.StatusConflict,
		Msg: "That username is taken.", Hint: "Choose another one."}
}

func notWorking(reason string) *Error {
	return &Error{Code: CodeNotWorking, Status: http.StatusNotFound,
		Msg:    "This invite link doesn't work any more.",
		Hint:   "Ask the person who sent it for a new link.",
		Reason: reason}
}

func expired() *Error {
	return &Error{Code: CodeExpired, Status: http.StatusGone,
		Msg: "This invite link has expired.", Hint: "Ask the person who sent it for a new link."}
}

func usedUp(kind Kind) *Error {
	if kind == KindMember {
		return &Error{Code: CodeUsedUp, Status: http.StatusGone,
			Msg: "This invite has already been used.", Hint: "If you accepted it, sign in with the username and password you chose."}
	}
	return &Error{Code: CodeUsedUp, Status: http.StatusGone,
		Msg: "This invite link has been used as many times as it allows.", Hint: "Ask the person who sent it for a new link."}
}

func badOptions(field, msg string, params ...string) *Error {
	p := map[string]string{"field": field}
	for i := 0; i+1 < len(params); i += 2 {
		p[params[i]] = params[i+1]
	}
	return &Error{Code: CodeBadOptions, Status: http.StatusBadRequest, Params: p, Msg: msg}
}

func roleUnknown() *Error {
	return &Error{Code: CodeRoleUnknown, Status: http.StatusBadRequest,
		Msg: "That is not a project role.", Hint: "Choose admin, moderator or viewer."}
}

func roleNotAllowed(role string) *Error {
	if role == InstallOwner {
		return &Error{Code: CodeRoleNotAllowed, Status: http.StatusForbidden, Params: map[string]string{"role": role},
			Msg: "Invites can't make someone the owner.", Hint: "Each Playkeeper has one owner: the account that set it up."}
	}
	return &Error{Code: CodeRoleNotAllowed, Status: http.StatusForbidden, Params: map[string]string{"role": role},
		Msg: fmt.Sprintf("You can't give the %s role.", role), Hint: "Ask the owner of this Playkeeper to send the invite."}
}

func rateLimited(scope string, wait time.Duration) *Error {
	msg := "Too many tries from your network."
	if scope == "invite" {
		msg = "This invite link has had too many tries."
	}
	return &Error{Code: CodeRateLimited, Status: http.StatusTooManyRequests, RetryAfter: wait,
		Params: map[string]string{"scope": scope, "seconds": seconds(wait)},
		Msg:    msg, Hint: "Wait a few minutes, then try again."}
}

func playerName() *Error {
	return &Error{Code: CodePlayerName, Status: http.StatusBadRequest,
		Msg: "Minecraft usernames are 3–16 letters, numbers or underscores.", Hint: "Use the name shown in the Minecraft Launcher."}
}

func playerNotFound(name string) *Error {
	return &Error{Code: CodePlayerUnknown, Status: http.StatusUnprocessableEntity, Params: map[string]string{"name": name},
		Msg:  fmt.Sprintf("No Minecraft: Java Edition account is called %s.", name),
		Hint: "Check the spelling. Minecraft on phones and consoles (Bedrock Edition) can't join this server."}
}

func playerDemo(name string) *Error {
	return &Error{Code: CodePlayerDemo, Status: http.StatusUnprocessableEntity, Params: map[string]string{"name": name},
		Msg:  fmt.Sprintf("%s hasn't bought Minecraft: Java Edition, so it can't join servers.", name),
		Hint: "Buy Java Edition for this account, then open the link again."}
}

func playerLegacy(name string) *Error {
	return &Error{Code: CodePlayerLegacy, Status: http.StatusUnprocessableEntity, Params: map[string]string{"name": name},
		Msg:  fmt.Sprintf("%s is an old Mojang account that was never moved to a Microsoft account, so it can't sign in any more.", name),
		Hint: "Use the account you play Minecraft with now."}
}

func mojangBusy(wait time.Duration, cause error) *Error {
	hint := "Try again in a minute."
	if wait > time.Minute {
		hint = "Try again in a few minutes."
	}
	return &Error{Code: CodeMojangBusy, Status: http.StatusServiceUnavailable, RetryAfter: wait,
		Params: map[string]string{"seconds": seconds(wait)},
		Msg:    "Minecraft's account service is busy right now.", Hint: hint,
		Reason: cause.Error(), Err: cause}
}

func mojangDown(cause error) *Error {
	return &Error{Code: CodeMojangDown, Status: http.StatusBadGateway,
		Msg: "Playkeeper couldn't check that name with Minecraft's account service.", Hint: "Try again in a few minutes.",
		Reason: cause.Error(), Err: cause}
}

func usernameInvalid(rule, msg string) *Error {
	return &Error{Code: CodeUsername, Status: http.StatusBadRequest, Params: map[string]string{"rule": rule}, Msg: msg}
}

func passwordInvalid(rule, msg string) *Error {
	return &Error{Code: CodePassword, Status: http.StatusBadRequest, Params: map[string]string{"rule": rule}, Msg: msg}
}

func seconds(d time.Duration) string {
	return strconv.Itoa(int(math.Ceil(d.Seconds())))
}
