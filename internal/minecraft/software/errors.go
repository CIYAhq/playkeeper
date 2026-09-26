package software

// Kind is the stable, machine-readable reason behind an Error, for the
// dashboard and scripts. Msg and Hint say the same in plain words.
type Kind string

const (
	KindUnreachable    Kind = "upstream_unreachable"
	KindUpstreamStatus Kind = "upstream_status"
	KindRateLimited    Kind = "upstream_rate_limited"
	KindNotFound       Kind = "not_found"
	KindMalformed      Kind = "upstream_malformed"
	KindTooLarge       Kind = "too_large"
	KindHostNotAllowed Kind = "host_not_allowed"
	KindHashMismatch   Kind = "hash_mismatch"
	KindSizeMismatch   Kind = "size_mismatch"
	KindMissingFile    Kind = "missing_file"
	KindUnsafePath     Kind = "unsafe_path"
	KindUnsupported    Kind = "unsupported"
	KindNoVersions     Kind = "no_versions"
)

// Error explains a failure: Msg is what happened as full sentences, Hint is
// what the user can do about it, and Params carries the values (upstream,
// host, file, status, hashes) behind Msg under stable keys.
type Error struct {
	Kind   Kind
	Msg    string
	Hint   string
	Params map[string]string
	Err    error
}

func (e *Error) Error() string { return e.Msg }

func (e *Error) Unwrap() error { return e.Err }

// short keeps error messages readable: hashes are shown by their first 16
// hex digits, like the rest of Playkeeper does.
func short(h string) string {
	if len(h) > 16 {
		return h[:16]
	}
	return h
}
