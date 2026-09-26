package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"unicode"

	"github.com/CIYAhq/playkeeper/internal/api"
)

const maxJSONBody = 64 << 10

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg, hint string) {
	writeJSON(w, status, api.Error{Error: msg, Code: code, Hint: hint})
}

// apiError is an error that maps to a specific HTTP response.
type apiError struct {
	Status int
	Code   string
	Msg    string
	Hint   string
	Op     *api.Operation
	Err    error
}

func (e *apiError) Error() string { return e.Msg }

func (e *apiError) Unwrap() error { return e.Err }

func errInvalid(format string, args ...any) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: fmt.Sprintf(format, args...)}
}

func errConflict(msg, hint string) *apiError {
	return &apiError{Status: http.StatusConflict, Code: api.CodeConflict, Msg: msg, Hint: hint}
}

func errNotCreated() *apiError {
	return &apiError{Status: http.StatusConflict, Code: api.CodeNotCreated, Msg: "No Minecraft server has been created yet.", Hint: "Finish the setup steps to create your server."}
}

func errNotFound(what string) *apiError {
	return &apiError{Status: http.StatusNotFound, Code: api.CodeNotFound, Msg: what + " not found."}
}

func writeError(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.Status, api.Error{Error: ae.Msg, Code: ae.Code, Hint: ae.Hint, Operation: ae.Op})
		return
	}
	writeErr(w, http.StatusInternalServerError, api.CodeInternal, err.Error(), "")
}

// decode reads a bounded JSON body and rejects unknown fields and trailing
// data, so callers cannot smuggle extra arguments into an operation.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errInvalid("Invalid request body: %v", err)
	}
	if dec.More() {
		return errInvalid("Invalid request body: unexpected trailing data")
	}
	return nil
}

// validActor bounds the free-text actor name recorded in the audit log.
func validActor(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errInvalid("actor is required")
	}
	if len(s) > 64 {
		return "", errInvalid("actor is too long")
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return "", errInvalid("actor contains unprintable characters")
		}
	}
	return s, nil
}

func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("agent handler panic", "panic", v, "stack", string(debug.Stack()))
				writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Internal error", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
