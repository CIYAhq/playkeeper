package gamefiles

import (
	"errors"
	"io/fs"
	"strconv"
)

// Kind is a stable, machine-readable name for a refusal. The UI translates it
// with the error's Params.
type Kind string

const (
	KindLink      Kind = "link"
	KindSpecial   Kind = "special_file"
	KindNotFile   Kind = "not_a_file"
	KindNotFolder Kind = "not_a_folder"
	KindTooLarge  Kind = "too_large"
	KindTooMany   Kind = "too_many_entries"
	KindChanged   Kind = "changed"
	KindBadName   Kind = "bad_name"
)

// Error is a refusal: Msg and Hint in English, Kind and Params for
// translation. Params always has "path", the path in the data directory
// that was refused; "limit" (bytes or entries) and "type" (named_pipe,
// socket, device or special) go with the kinds they describe.
type Error struct {
	Kind   Kind
	Params map[string]string
	Msg    string
	Hint   string
}

func (e *Error) Error() string { return e.Msg }

// KindOf returns the Kind of an *Error in err's chain, or "".
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

const (
	hintPlanted = "If you did not make it, a plugin or mod may have."
	hintMove    = "Move it out of the way, then try again."
)

func refuse(k Kind, p, msg, hint string, params ...string) error {
	m := map[string]string{"path": p}
	for i := 0; i+1 < len(params); i += 2 {
		m[params[i]] = params[i+1]
	}
	return &Error{Kind: k, Params: m, Msg: msg, Hint: hint}
}

// where names p at the start of a sentence.
func where(p string) string {
	if p == "." {
		return "The server's folder"
	}
	return p + " in the server's files"
}

func linkError(p string) error {
	return refuse(KindLink, p, where(p)+" is a link, which Playkeeper does not follow.",
		"Delete it, or replace it with the file or folder it points to, then try again. "+hintPlanted)
}

func specialError(p string, mode fs.FileMode) error {
	what, typ := "a special file", "special"
	switch {
	case mode&fs.ModeNamedPipe != 0:
		what, typ = "a named pipe", "named_pipe"
	case mode&fs.ModeSocket != 0:
		what, typ = "a socket", "socket"
	case mode&fs.ModeDevice != 0:
		what, typ = "a device", "device"
	}
	return refuse(KindSpecial, p, where(p)+" is not a normal file (it is "+what+").",
		"Delete it, then try again. "+hintPlanted, "type", typ)
}

func notFileError(p string) error {
	return refuse(KindNotFile, p, where(p)+" is a folder, not a file.", hintMove)
}

func notFolderError(p string) error {
	return refuse(KindNotFolder, p, where(p)+" is not a folder.", hintMove)
}

func tooLargeError(p string, limit int64) error {
	return refuse(KindTooLarge, p, where(p)+" is larger than "+sizeText(limit)+", the most Playkeeper reads.",
		"Delete it or make it smaller, then try again.", "limit", strconv.FormatInt(limit, 10))
}

func tooManyError(p string, limit int) error {
	return refuse(KindTooMany, p, where(p)+" has more than "+strconv.Itoa(limit)+" entries, the most Playkeeper lists.",
		"Delete what the server does not need from it, then try again.", "limit", strconv.Itoa(limit))
}

func changedError(p string) error {
	return refuse(KindChanged, p, where(p)+" changed while Playkeeper was using it.", "Try again.")
}

func badNameError(p string) error {
	return refuse(KindBadName, p, strconv.Quote(p)+" is not a path inside the server's files.", "")
}

// sizeText writes a limit the way the dashboard does, like "64 KB".
func sizeText(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + " MB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + " KB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}
