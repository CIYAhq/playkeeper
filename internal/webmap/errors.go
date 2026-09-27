package webmap

import "errors"

// Kind is a stable, machine-readable name for a refusal or failure. The UI
// translates it with the error's Params.
type Kind string

const (
	KindUnsupported    Kind = "unsupported_server_type"
	KindUnknownType    Kind = "unknown_server_type"
	KindInvalid        Kind = "invalid_setting"
	KindFolderUnusable Kind = "folder_unusable"
	KindCommandFailed  Kind = "command_failed"
	KindNotRunning     Kind = "not_running"
	KindStarting       Kind = "starting"
	KindNotAnswering   Kind = "not_answering"
	KindTooSlow        Kind = "too_slow"
	KindBadAnswer      Kind = "bad_answer"
	KindNotDrawn       Kind = "not_drawn"
	KindNotFound       Kind = "not_found"
	KindMethod         Kind = "method_not_allowed"
)

// Error is something that stopped an operation: Msg and Hint in English,
// Kind and Params for translation.
type Error struct {
	Kind   Kind              `json:"kind"`
	Params map[string]string `json:"params,omitempty"`
	Msg    string            `json:"message"`
	Hint   string            `json:"hint,omitempty"`
	Err    error             `json:"-"`
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

func fail(k Kind, params map[string]string, msg, hint string) *Error {
	return &Error{Kind: k, Params: params, Msg: msg, Hint: hint}
}

func kv(pairs ...string) map[string]string {
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

// KindOf returns the Kind of an *Error in err's chain, or "".
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}
