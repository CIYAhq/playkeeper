package machinelink

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Codes of the errors and problems in this package, stable for translation.
const (
	CodeAddressInvalid     = "link_address_invalid"
	CodeFingerprintInvalid = "link_fingerprint_invalid"
	CodeKeyFile            = "link_key_file"
	CodeNameInvalid        = "machine_name_invalid"

	CodeJoinCodeMalformed = "join_code_malformed"
	CodeJoinCodeWrong     = "join_code_wrong"
	CodeJoinCodeExpired   = "join_code_expired"
	CodeJoinCodeUsed      = "join_code_used"
	CodeJoinRateLimited   = "join_rate_limited"

	CodeDashboardUnreachable = "dashboard_unreachable"
	CodeNotADashboard        = "not_a_dashboard"
	CodeDashboardKeyMismatch = "dashboard_key_mismatch"

	CodeMachineUnknown       = "machine_unknown"
	CodeMachineRemoved       = "machine_removed"
	CodeMachineAlreadyJoined = "machine_already_joined"

	CodeVersionUnsupported = "link_version_unsupported"
	CodeProtocol           = "link_protocol_error"
	CodeTooLarge           = "link_message_too_large"
	CodeTimeout            = "link_timeout"
	CodeDashboardError     = "link_dashboard_error"
	CodeDropped            = "link_dropped"
	CodeHeartbeatTimeout   = "link_heartbeat_timeout"

	CodeNotConnected    = "machine_not_connected"
	CodeRouteNotAllowed = "link_route_not_allowed"
	CodeActorRequired   = "link_actor_required"
	CodeAgentDown       = "agent_unavailable"
)

// Error is a failure a person may see. Code and Params are for the UI's
// translations; Error returns the English sentence, and Hint says what to
// do next. Err is the underlying error, for logs. None of them ever holds
// a join code or a private key.
type Error struct {
	Code   string
	Params map[string]string
	Msg    string
	Hint   string
	// RetryAfter is set when trying again later can work.
	RetryAfter time.Duration
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

func retryAfterOf(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) {
		return e.RetryAfter
	}
	return 0
}

// ErrCodeUsed is what Store.Pair returns when another machine used the
// join code first.
var ErrCodeUsed = errors.New("machinelink: join code already used")

// ErrNotFound is what a Store returns for a machine or join code it
// doesn't have.
var ErrNotFound = errors.New("machinelink: not found")

func errAddress(s, why string) *Error {
	return &Error{Code: CodeAddressInvalid, Params: map[string]string{"address": cleanText(s, 80)},
		Msg:  "The dashboard address " + strconv.Quote(cleanText(s, 80)) + " is not valid: " + why + ".",
		Hint: "Use the address you open the dashboard with, such as panel.example.com or 203.0.113.7:8443."}
}

func errJoinCodeMalformed() *Error {
	return &Error{Code: CodeJoinCodeMalformed,
		Msg:  "That is not a join code. Join codes are 8 letters and digits, like 7KQ2-M9XD.",
		Hint: "Copy the command again from the dashboard (Settings › Machines)."}
}

func errJoinCodeWrong() *Error {
	return &Error{Code: CodeJoinCodeWrong,
		Msg:  "The dashboard doesn't know that join code.",
		Hint: "Check the code, or make a new one in the dashboard (Settings › Machines › Connect a machine)."}
}

func errJoinCodeExpired() *Error {
	return &Error{Code: CodeJoinCodeExpired,
		Msg:  "That join code has expired. Join codes work for 30 minutes.",
		Hint: "Make a new one in the dashboard (Settings › Machines › Connect a machine)."}
}

func errJoinCodeUsed() *Error {
	return &Error{Code: CodeJoinCodeUsed,
		Msg:  "That join code has already been used. Each code connects one machine.",
		Hint: "Make a new one in the dashboard (Settings › Machines › Connect a machine)."}
}

func errJoinRateLimited(wait time.Duration) *Error {
	return &Error{Code: CodeJoinRateLimited, Params: map[string]string{"wait": humanDuration(wait), "seconds": seconds(wait)},
		Msg:        "The dashboard has seen too many wrong join codes, so it isn't accepting any for a while.",
		Hint:       "Try again in " + humanDuration(wait) + ".",
		RetryAfter: wait}
}

func errUnreachable(a Address, err error) *Error {
	return &Error{Code: CodeDashboardUnreachable, Params: map[string]string{"address": a.String(), "port": strconv.Itoa(a.Port)},
		Msg:  "This machine can't reach the dashboard at " + a.String() + ".",
		Hint: fmt.Sprintf("Check the address, and that this machine can connect to port %d on it: a firewall may block it.", a.Port),
		Err:  err}
}

func errNotADashboard(a Address, err error) *Error {
	return &Error{Code: CodeNotADashboard, Params: map[string]string{"address": a.String()},
		Msg:  a.String() + " answered, but not as a Playkeeper dashboard that accepts machines.",
		Hint: "Check the address and port, and update Playkeeper on the dashboard. Machines must reach the dashboard directly, not through a proxy that handles HTTPS itself (such as Cloudflare's orange cloud).",
		Err:  err}
}

func errKeyMismatch(a Address, joined bool) *Error {
	e := &Error{Code: CodeDashboardKeyMismatch, Params: map[string]string{"address": a.String()}}
	if joined {
		e.Msg = "The dashboard at " + a.String() + " has a different key from when this machine joined it, so this machine won't connect."
		e.Hint = "If the dashboard was set up again, connect this machine again with a new command from it. Otherwise someone may be intercepting the connection."
		return e
	}
	e.Msg = "The machine at " + a.String() + " is not the dashboard this command was made for: its key fingerprint doesn't match."
	e.Hint = "Don't continue. Copy the command again from the dashboard; if it still fails, someone may be intercepting the connection."
	return e
}

func errMachineUnknown() *Error {
	return &Error{Code: CodeMachineUnknown,
		Msg:  "The dashboard doesn't know this machine.",
		Hint: "It may have been set up again or restored from an older backup. Run sudo playkeeper leave here, then connect again with a new command from the dashboard."}
}

func errMachineRemoved() *Error {
	return &Error{Code: CodeMachineRemoved,
		Msg:  "This machine was removed from the dashboard, and its key no longer works.",
		Hint: "To connect it again, run sudo playkeeper leave here, then a new command from the dashboard."}
}

func errAlreadyJoined(name string) *Error {
	return &Error{Code: CodeMachineAlreadyJoined, Params: map[string]string{"name": name},
		Msg:  "This machine is already connected to this dashboard, as " + name + ".",
		Hint: "Nothing to do. To connect it again from scratch, run sudo playkeeper leave first."}
}

func errVersionUnsupported(v int) *Error {
	return &Error{Code: CodeVersionUnsupported, Params: map[string]string{"version": strconv.Itoa(v)},
		Msg:  "This machine and the dashboard run versions of Playkeeper that can't talk to each other.",
		Hint: "Update Playkeeper on both machines to the same version."}
}

func errProtocol(err error) *Error {
	return &Error{Code: CodeProtocol,
		Msg:  "The other side of the machine link sent something Playkeeper doesn't understand.",
		Hint: "Make sure both machines run the same version of Playkeeper.",
		Err:  err}
}

func errTooLarge(name string, limit int64) *Error {
	return &Error{Code: CodeTooLarge, Params: map[string]string{"name": name, "limit": formatBytes(limit)},
		Msg:  name + " sent a reply larger than the dashboard accepts (" + formatBytes(limit) + ").",
		Hint: "Update Playkeeper on both machines. If it keeps happening, the other machine may be misbehaving."}
}

func errHelloTooLarge() *Error {
	return &Error{Code: CodeTooLarge, Params: map[string]string{"limit": formatBytes(maxFrame)},
		Msg:  "This machine sent the dashboard a greeting larger than it accepts (" + formatBytes(maxFrame) + ").",
		Hint: "Make sure both machines run the same version of Playkeeper."}
}

func errRedirect(name string) *Error {
	return &Error{Code: CodeProtocol, Params: map[string]string{"name": name},
		Msg:  name + " answered with a redirect, which the dashboard never follows for a machine.",
		Hint: "Update Playkeeper on it. If it keeps happening, the machine may be misbehaving."}
}

func errRemovedDuringRequest(id, name string) *Error {
	return &Error{Code: CodeMachineRemoved, Params: map[string]string{"machineId": id, "name": name},
		Msg: name + " was removed from the dashboard."}
}

func errTimeout(name string, d time.Duration) *Error {
	return &Error{Code: CodeTimeout, Params: map[string]string{"name": name, "wait": humanDuration(d), "seconds": seconds(d)},
		Msg:  name + " didn't answer within " + humanDuration(d) + ".",
		Hint: "Try again in a moment. If it keeps happening, the connection to it may be very slow."}
}

func errDashboard(err error) *Error {
	return &Error{Code: CodeDashboardError,
		Msg:        "The dashboard had a problem reading its list of machines.",
		Hint:       "Try again in a minute. If it keeps happening, check the dashboard's logs.",
		RetryAfter: time.Minute,
		Err:        err}
}

func errNotConnected(id, name string) *Error {
	return &Error{Code: CodeNotConnected, Params: map[string]string{"machineId": id, "name": name},
		Msg:  name + " is not connected to the dashboard right now.",
		Hint: "Check that it is on and online. It connects again on its own as soon as it can."}
}

func errDropped(id, name string, err error) *Error {
	return &Error{Code: CodeNotConnected, Params: map[string]string{"machineId": id, "name": name},
		Msg:  "The connection to " + name + " dropped before it answered.",
		Hint: "Try again in a moment.",
		Err:  err}
}

func errRouteNotAllowed(method, path string) *Error {
	return &Error{Code: CodeRouteNotAllowed, Params: map[string]string{"method": method, "path": cleanText(path, 200)},
		Msg: "The dashboard can't send that request to another machine."}
}

func errActorRequired(name string) *Error {
	return &Error{Code: CodeActorRequired, Params: map[string]string{"name": name},
		Msg: "Requests that change something on " + name + " must say who made them, in at most 64 printable characters."}
}

func errHeartbeat(d time.Duration) *Error {
	return &Error{Code: CodeHeartbeatTimeout, Params: map[string]string{"wait": humanDuration(d)},
		Msg:  "The dashboard stopped checking in for " + humanDuration(d) + ", so this machine dropped the connection.",
		Hint: "It connects again on its own. If this keeps happening, check the network between the two machines."}
}

func errLinkDropped(err error) *Error {
	return &Error{Code: CodeDropped,
		Msg:  "The connection to the dashboard dropped.",
		Hint: "This machine connects again on its own.",
		Err:  err}
}

// wire converts an error to send to the other side.
func (e *Error) wire() *wireError {
	return &wireError{Code: e.Code, Params: e.Params, Message: e.Msg, Hint: e.Hint, RetryAfterMs: e.RetryAfter.Milliseconds()}
}

// err converts an error received from the dashboard. It ends up in a
// terminal and in logs, so control characters are dropped and lengths
// bounded.
func (w *wireError) err() *Error {
	e := &Error{Code: cleanCode(w.Code), Msg: cleanText(w.Message, 400), Hint: cleanText(w.Hint, 400)}
	if e.Code == "" {
		e.Code = CodeProtocol
	}
	if e.Msg == "" {
		e.Msg = "The dashboard refused the connection."
	}
	if n := len(w.Params); n > 0 && n <= 8 {
		e.Params = make(map[string]string, n)
		for k, v := range w.Params {
			if k = cleanCode(k); k != "" {
				e.Params[k] = cleanText(v, 200)
			}
		}
	}
	if w.RetryAfterMs > 0 {
		e.RetryAfter = min(time.Duration(w.RetryAfterMs)*time.Millisecond, 24*time.Hour)
	}
	return e
}

func cleanCode(s string) string {
	if len(s) == 0 || len(s) > 64 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.') {
			return ""
		}
	}
	return s
}

// cleanText drops control characters (so text can't move a terminal's
// cursor or fake log lines) and cuts it to max runes.
func cleanText(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			continue
		}
		if n == max {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

func humanDuration(d time.Duration) string {
	switch {
	case d < 2*time.Second:
		return "1 second"
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + " seconds"
	case d < 2*time.Minute:
		return "1 minute"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " minutes"
	case d < 2*time.Hour:
		return "1 hour"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " hours"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + " days"
	}
}

func seconds(d time.Duration) string {
	return strconv.FormatInt(int64((d+time.Second-1)/time.Second), 10)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + " MiB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + " KiB"
	default:
		return strconv.FormatInt(n, 10) + " bytes"
	}
}
