package addons

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
)

// Kind is a stable, machine-readable name for a refusal, warning or manual
// step. The UI translates it with the notice's Params.
type Kind string

const (
	KindInvalid           Kind = "invalid_request"
	KindUnknownServerType Kind = "unknown_server_type"
	KindNoAddons          Kind = "no_addons"
	KindSourceUnsupported Kind = "source_unsupported"
	KindFolderUnusable    Kind = "folder_unusable"
	KindNotFound          Kind = "not_found"
	KindNotAddon          Kind = "not_an_addon"
	KindNotManaged        Kind = "not_managed"
	KindClientOnly        Kind = "client_only"
	KindNoVersion         Kind = "no_compatible_version"
	KindOnlyPrerelease    Kind = "only_prerelease"
	KindPrerelease        Kind = "prerelease"
	KindExternal          Kind = "external_download"
	KindDepExternal       Kind = "dependency_external"
	KindDepUnlisted       Kind = "dependency_unlisted"
	KindDepMissing        Kind = "dependency_unavailable"
	KindDepClientOnly     Kind = "dependency_client_only"
	KindConflict          Kind = "conflict"
	KindAlreadyInstalled  Kind = "already_installed"
	KindDuplicate         Kind = "duplicate"
	KindFileExists        Kind = "file_exists"
	KindModified          Kind = "modified"
	KindNeededBy          Kind = "needed_by"
	KindBadFileName       Kind = "bad_file_name"
	KindHostNotAllowed    Kind = "host_not_allowed"
	KindNotHTTPS          Kind = "not_https"
	KindRedirectRefused   Kind = "redirect_refused"
	KindTooLarge          Kind = "too_large"
	KindSizeMismatch      Kind = "size_mismatch"
	KindHashMismatch      Kind = "hash_mismatch"
	KindNoHash            Kind = "no_hash"
	KindRateLimited       Kind = "rate_limited"
	KindUnreachable       Kind = "unreachable"
	KindUpstream          Kind = "upstream_error"
	KindPlanChanged       Kind = "plan_changed"
	KindUpToDate          Kind = "up_to_date"
	KindIconRefused       Kind = "icon_refused"
)

// Notice is something to tell the user: Msg and Hint in English, Kind and
// Params for translation.
type Notice struct {
	Kind   Kind              `json:"kind"`
	Params map[string]string `json:"params,omitempty"`
	Msg    string            `json:"message"`
	Hint   string            `json:"hint,omitempty"`
}

// Error is a Notice that stopped an operation.
type Error struct {
	Notice
	Err error `json:"-"`
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

func notice(k Kind, params map[string]string, msg, hint string) Notice {
	return Notice{Kind: k, Params: params, Msg: msg, Hint: hint}
}

func fail(k Kind, params map[string]string, msg, hint string) *Error {
	return &Error{Notice: notice(k, params, msg, hint)}
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

// upstream explains an error from a source's API.
func upstream(src Source, err error) error {
	var e *Error
	if err == nil || errors.As(err, &e) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	name := src.Name()
	var (
		rl *fetch.RateLimitError
		se *fetch.StatusError
		ne *fetch.NetError
		tl *fetch.TooLargeError
	)
	switch {
	case errors.As(err, &rl):
		secs := strconv.Itoa(int(math.Ceil(rl.RetryAfter.Seconds())))
		return &Error{Notice: notice(KindRateLimited, kv("source", name, "seconds", secs),
			name+" asked Playkeeper to slow down.", "Try again in "+secs+" seconds."), Err: err}
	case errors.As(err, &se):
		msg := fmt.Sprintf("%s answered with an error (HTTP %d).", name, se.Status)
		if se.Detail != "" {
			msg = fmt.Sprintf("%s answered with an error (HTTP %d): %s.", name, se.Status, se.Detail)
		}
		return &Error{Notice: notice(KindUpstream, kv("source", name, "status", strconv.Itoa(se.Status)), msg,
			"Try again in a few minutes. If it keeps happening, "+name+" may be having problems."), Err: err}
	case errors.As(err, &ne):
		return &Error{Notice: notice(KindUnreachable, kv("source", name), "Playkeeper could not reach "+name+".",
			"Check that this machine can reach the internet, then try again."), Err: err}
	case errors.As(err, &tl):
		return &Error{Notice: notice(KindUpstream, kv("source", name, "status", "200"), name+" sent a larger answer than Playkeeper accepts.",
			"Try a narrower search."), Err: err}
	}
	return &Error{Notice: notice(KindUpstream, kv("source", name, "status", ""), name+" could not be used: "+err.Error()+".",
		"Try again in a few minutes."), Err: err}
}

// downloadError explains why a verified download of step s failed. Nothing
// has touched the server's folder when this is returned.
func downloadError(s Step, err error, max int64) error {
	var (
		he *fetch.HashError
		ze *fetch.SizeError
		tl *fetch.TooLargeError
		ho *fetch.HostError
		re *fetch.RedirectError
	)
	name, file, src := s.Name, s.FileName, s.Source.Name()
	tampered := "This can mean a damaged download or a tampered copy. Nothing was installed; try again later."
	switch {
	case errors.As(err, &he):
		return &Error{Notice: notice(KindHashMismatch, kv("name", name, "file", file, "source", src),
			fmt.Sprintf("The download of %s does not match the %s hash %s publishes, so Playkeeper did not install it.", file, he.Algo, src), tampered), Err: err}
	case errors.As(err, &ze):
		return &Error{Notice: notice(KindSizeMismatch, kv("name", name, "file", file, "source", src),
			fmt.Sprintf("The download of %s is not the size %s lists, so Playkeeper did not install it.", file, src), tampered), Err: err}
	case errors.As(err, &tl):
		return tooLarge(s, max)
	case errors.As(err, &re):
		return &Error{Notice: notice(KindRedirectRefused, kv("name", name, "file", file, "host", re.To),
			fmt.Sprintf("The download of %s was redirected to %s, which is not one of %s's own file hosts.", file, re.To, src),
			"Playkeeper only downloads add-ons from Modrinth's and Hangar's file hosts. Nothing was installed."), Err: err}
	case errors.As(err, &ho):
		return hostNotAllowed(s)
	}
	return upstream(s.Source, err)
}

func tooLarge(s Step, max int64) *Error {
	return fail(KindTooLarge, kv("name", s.Name, "file", s.FileName, "limit", fetch.Size(max)),
		fmt.Sprintf("%s is larger than the %s Playkeeper accepts for one add-on file.", s.FileName, fetch.Size(max)),
		"Install it by hand if you trust it. Nothing was installed.")
}

// hostNotAllowed refuses the address of step s's file.
func hostNotAllowed(s Step) *Error {
	host, scheme := "an address Playkeeper cannot check", ""
	if u, err := url.Parse(s.url); err == nil && u.Host != "" {
		host, scheme = printable(u.Hostname()), u.Scheme
	}
	params := kv("name", s.Name, "file", s.FileName, "host", host)
	hint := "Playkeeper only downloads add-ons over HTTPS from Modrinth's and Hangar's file hosts. Nothing was installed."
	if scheme != "" && scheme != "https" {
		return fail(KindNotHTTPS, params, fmt.Sprintf("%s would be downloaded from %s without HTTPS.", s.FileName, host), hint)
	}
	return fail(KindHostNotAllowed, params,
		fmt.Sprintf("%s would be downloaded from %s, which is not one of %s's own file hosts.", s.FileName, host, s.Source.Name()), hint)
}
