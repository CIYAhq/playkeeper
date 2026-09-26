package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrorKind is the stable, machine-readable reason a backup failed.
type ErrorKind string

const (
	// KindInsufficientSpace: refused before anything was paused. FreeBytes
	// and NeededBytes say how far off it is.
	KindInsufficientSpace ErrorKind = "insufficient_space"
	// KindConsoleUnavailable: a console command failed while the server was
	// running. Timeout is set when it got no reply in time.
	KindConsoleUnavailable ErrorKind = "console_unavailable"
	// KindServerStopped: the server stopped during the backup.
	KindServerStopped ErrorKind = "server_stopped"
	// KindSaveFailed: the server replied that it could not save (Reply).
	KindSaveFailed ErrorKind = "save_failed"
	// KindSaveTimeout: the server did not confirm the save within Timeout.
	KindSaveTimeout ErrorKind = "save_timeout"
	// KindUnexpectedReply: a reply Take does not know (Command, Reply), for
	// example from a plugin or mod that replaced the command. Backing up with
	// the server stopped avoids the console.
	KindUnexpectedReply ErrorKind = "unexpected_reply"
	// KindSavingResumed: something else turned saving back on during the
	// backup, so the copy may not be one moment's world.
	KindSavingResumed ErrorKind = "saving_resumed"
	// KindSavingPaused: saving could not be turned back on (SavingPaused).
	// Call ResumeSaving until it succeeds.
	KindSavingPaused ErrorKind = "saving_paused"
	// KindFileChanging: a file kept changing while it was copied (File).
	KindFileChanging ErrorKind = "file_changing"
	// KindRefused: a restore would refuse the world or File; Err is the
	// *RefusedError.
	KindRefused ErrorKind = "refused"
	// KindDiskFull: the disk filled up while writing.
	KindDiskFull ErrorKind = "disk_full"
	// KindCancelled: ctx was cancelled or ran out of time; Err is ctx's error.
	KindCancelled ErrorKind = "cancelled"
	// KindNoWorld: the server folder has no world to back up; Err is the
	// *NoWorldError.
	KindNoWorld ErrorKind = "no_world"
	// KindFailed: anything else.
	KindFailed ErrorKind = "failed"
)

// Error is the error Take returns. Msg and Hint are the plain-English
// sentences to show; Kind and the fields below carry the same facts for code
// and translations.
type Error struct {
	Kind ErrorKind
	Msg  string
	Hint string
	// SavingPaused means world saving may still be paused on the server.
	SavingPaused bool
	FreeBytes    int64
	NeededBytes  int64
	File         string
	Command      string
	// Reply is the server's reply, on one line and shortened.
	Reply   string
	Timeout time.Duration
	Err     error
}

func (e *Error) Error() string { return e.Msg }

func (e *Error) Unwrap() error { return e.Err }

const hintFreeSpace = "Delete old backups (after downloading any you want to keep) or free disk space, then try again."

func errMisuse(what string) *Error {
	return &Error{Kind: KindFailed, Msg: "The backup was started incorrectly: " + what + ".",
		Hint: "This is a bug in Playkeeper. Please report it."}
}

func errFailed(what string, err error) *Error {
	return &Error{Kind: KindFailed, Msg: what + ": " + err.Error() + ".",
		Hint: "Try again. If it keeps failing, check the disk for errors.", Err: err}
}

func errNoWorld(e *NoWorldError) *Error {
	return &Error{Kind: KindNoWorld, Err: e,
		Msg:  fmt.Sprintf("There is no world named %q in %s, so nothing was backed up.", e.Level, e.DataDir),
		Hint: "Start the server once so it makes its world, then back it up."}
}

func errInsufficientSpace(free, need int64) *Error {
	return &Error{Kind: KindInsufficientSpace, FreeBytes: free, NeededBytes: need,
		Msg:  fmt.Sprintf("Not enough disk space for a backup: %s free, about %s needed.", formatBytes(free), formatBytes(need)),
		Hint: hintFreeSpace}
}

func (r *run) errConsole(err error) *Error {
	e := &Error{Kind: KindConsoleUnavailable, Hint: "Check that the server is online, then try again.", Err: err}
	what := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		e.Timeout = r.o.Timeouts.Command
		what = "no reply within " + plainDuration(e.Timeout)
	}
	e.Msg = "Playkeeper could not reach the server console (" + what + "), so no backup was made."
	return e
}

func errServerStopped(cause error) *Error {
	return &Error{Kind: KindServerStopped, Msg: "The server stopped while the backup was being made, so no backup was saved.",
		Hint: "Start the server again, then retry the backup. If it keeps stopping, check the Console for errors.", Err: cause}
}

func errSaveFailed(reply string) *Error {
	return &Error{Kind: KindSaveFailed, Reply: shortReply(reply),
		Msg:  "The server reported that it could not save the world, so no backup was made.",
		Hint: "Check that the disk has free space, then try again. The Console may show why saving failed."}
}

func (r *run) errSaveTimeout() *Error {
	d := r.o.Timeouts.Flush
	return &Error{Kind: KindSaveTimeout, Timeout: d,
		Msg:  fmt.Sprintf("The server did not confirm that the world was saved within %s, so no backup was made.", plainDuration(d)),
		Hint: "The server may be overloaded or its disk slow. Try again later, or back up with the server stopped."}
}

func errUnexpectedReply(cmd, reply string) *Error {
	short := shortReply(reply)
	msg := fmt.Sprintf("The server gave an unexpected reply to %q: %q. No backup was made.", cmd, short)
	if short == "" {
		msg = fmt.Sprintf("The server gave no reply to %q, so no backup was made.", cmd)
	}
	return &Error{Kind: KindUnexpectedReply, Command: cmd, Reply: short, Msg: msg,
		Hint: "A plugin or mod may have changed this command. Back up with the server stopped instead."}
}

func errSavingResumed() *Error {
	return &Error{Kind: KindSavingResumed,
		Msg:  "World saving was turned back on during the backup (by a restart, a plugin or a console command), so the copy might not match a single moment. No backup was saved.",
		Hint: "Try again. If this keeps happening, check whether a plugin runs save-on."}
}

// errStillPaused is the error when saving could not be turned back on. cause
// is why the backup failed, or nil if it had otherwise worked.
func (r *run) errStillPaused(cause, last error) *Error {
	msg := "World saving is paused on the server and Playkeeper could not turn it back on."
	if !r.offOK {
		msg = "World saving may be paused on the server: Playkeeper sent save-off and could not confirm that saving is back on."
	}
	var c *Error
	if errors.As(cause, &c) {
		msg += " " + c.Msg
	} else {
		msg += " No backup was saved."
	}
	return &Error{Kind: KindSavingPaused, SavingPaused: true, Msg: msg,
		Hint: "Open the Console and run save-on, or restart the server. Until then, progress could be lost if the server stops unexpectedly.",
		Err:  errors.Join(cause, last)}
}

func errFileChanging(rel string) *Error {
	return &Error{Kind: KindFileChanging, File: rel,
		Msg:  fmt.Sprintf("%s kept changing while it was being copied, so no backup was saved.", shortQuote(rel)),
		Hint: "A plugin or mod keeps writing to this file. Try again, or back up with the server stopped."}
}

func (r *run) errRefused(e *RefusedError) *Error {
	hint := fmt.Sprintf("Rename or remove that file in %s, then try again.", r.o.DataDir)
	if e.File == "" {
		hint = fmt.Sprintf("Remove files the world does not need from %s, then try again.", r.o.DataDir)
	}
	msg := e.Error()
	return &Error{Kind: KindRefused, File: e.File, Msg: strings.ToUpper(msg[:1]) + msg[1:] + ".", Hint: hint, Err: e}
}

func errDiskFull(err error) *Error {
	return &Error{Kind: KindDiskFull, Msg: "The disk filled up while the backup was being written, so no backup was saved.",
		Hint: hintFreeSpace, Err: err}
}

func errCancelled(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: KindCancelled, Msg: "The backup took too long and was stopped, so no backup was saved.",
			Hint: "Try again when the server is less busy.", Err: err}
	}
	return &Error{Kind: KindCancelled, Msg: "The backup was cancelled, so no backup was saved.", Err: err}
}

// shortReply bounds a server reply for messages: one line, at most 200 bytes.
func shortReply(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "") + "…"
	}
	return s
}

// plainDuration writes d as the UI says it: "45 seconds", "5 minutes".
func plainDuration(d time.Duration) string {
	switch {
	case d >= time.Minute && d%time.Minute == 0:
		return plural(int64(d/time.Minute), "minute")
	case d >= time.Second:
		return plural(int64(d.Round(time.Second)/time.Second), "second")
	default:
		return d.String()
	}
}

func plural(n int64, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// formatBytes writes n in binary units, as the agent's messages do.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
