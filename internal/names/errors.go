package names

import (
	"fmt"
	"time"
)

// Stable codes of refused requests, in Error.Code and Availability.Code. The
// UI translates by code; the English message is a fallback.
const (
	CodeInvalidRequest   = "invalid_request"
	CodeInvalidName      = "invalid_name"
	CodeInvalidServer    = "invalid_server"
	CodeInvalidPort      = "invalid_port"
	CodeInvalidChallenge = "invalid_challenge"
	CodeNameReserved     = "name_reserved"
	CodeNameTaken        = "name_taken"
	CodeNameHeld         = "name_held"
	CodeNameInUse        = "name_in_use"
	CodeNotClaimed       = "not_claimed"
	CodeNotYourName      = "not_your_name"
	CodeNameLapsed       = "name_lapsed"
	CodeLimitReached     = "limit_reached"
	CodeTooManyServers   = "too_many_servers"
	CodeTooManyTXT       = "too_many_challenges"
	CodeZoneFull         = "zone_full"
	CodeNotPublic        = "not_public_address"
	CodeMisconfigured    = "service_misconfigured"
	CodeRateLimited      = "rate_limited"
	CodeUnsigned         = "unsigned"
	CodeBadSignature     = "bad_signature"
	CodeClockSkew        = "clock_skew"
	CodeReplayed         = "replayed"
	CodeDNSUnavailable   = "dns_unavailable"
	CodeDNSPending       = "dns_pending"
	CodeNoName           = "no_name"
	CodeUnavailable      = "service_unavailable"
	CodeInternal         = "internal"
)

// Error is a request the names service (or this client, before sending)
// refused. Message is a full English sentence and Hint what to do next;
// Params carries the values a translated message needs.
type Error struct {
	Status     int
	Code       string
	Message    string
	Hint       string
	Params     map[string]any
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Hint != "" {
		return e.Message + " " + e.Hint
	}
	return e.Message
}

// ErrorBody is the JSON the service answers a refused request with.
type ErrorBody struct {
	Error  string         `json:"error"`
	Code   string         `json:"code"`
	Hint   string         `json:"hint,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

// Body is e as the service sends it.
func (e *Error) Body() ErrorBody {
	return ErrorBody{Error: e.Message, Code: e.Code, Hint: e.Hint, Params: e.Params}
}

func errorf(status int, code, hint, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...), Hint: hint}
}
