package offsite

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Kind says what went wrong, for code and for the dashboard's translations.
type Kind string

const (
	KindInvalidConfig  Kind = "invalid_config"     // a setting is missing or malformed
	KindWrongKeys      Kind = "wrong_keys"         // unknown access key ID, or a secret that doesn't match it
	KindNoSuchBucket   Kind = "no_such_bucket"     // the bucket doesn't exist
	KindPermission     Kind = "permission_denied"  // the key or account may not do this, or the account is disabled
	KindClockSkew      Kind = "clock_skew"         // this machine's clock is too far off
	KindWrongRegion    Kind = "wrong_region"       // the bucket is in another region
	KindNetwork        Kind = "network"            // no connection, a timeout or a broken connection
	KindTLS            Kind = "tls"                // the endpoint's certificate could not be verified
	KindRedirect       Kind = "redirect"           // the service sent the request elsewhere; never followed
	KindRateLimited    Kind = "rate_limited"       // the service asked to slow down
	KindServiceError   Kind = "service_error"      // the service failed (HTTP 5xx)
	KindStorageFull    Kind = "storage_full"       // a quota or storage cap is reached, or the other machine's disk is full
	KindChecksum       Kind = "checksum_mismatch"  // the service received different bytes than were sent
	KindLocalChanged   Kind = "local_file_changed" // the backup or its encrypted copy doesn't match its checksum
	KindVerifyFailed   Kind = "verify_failed"      // the stored copy doesn't match, or is damaged
	KindNotFound       Kind = "not_found"          // the copy is not there
	KindConflict       Kind = "conflict"           // a different file already has the copy's name
	KindUploadGone     Kind = "upload_gone"        // the unfinished upload no longer exists
	KindLocked         Kind = "locked"             // old versions the bucket refuses to delete
	KindTooLarge       Kind = "too_large"          // larger than the service accepts
	KindCanceled       Kind = "canceled"           // the context was cancelled
	KindUnexpected     Kind = "unexpected"         // an answer Playkeeper doesn't understand
	KindKeyMismatch    Kind = "key_mismatch"       // none of the server's encryption keys opens the copy
	KindNotEnoughSpace Kind = "not_enough_space"   // this machine's disk has no room to encrypt or restore a copy
	KindHostKeyUnknown Kind = "host_key_unknown"   // the other machine's host key isn't confirmed yet
	KindHostKeyChanged Kind = "host_key_changed"   // the other machine presented another host key than the confirmed one
	KindLoginRefused   Kind = "login_refused"      // the other machine refused the user name, password or key
	KindNoSuchFolder   Kind = "no_such_folder"     // the folder doesn't exist on the other machine
)

// What an Error's Op can be.
const (
	opSetup    = "setup"
	opTest     = "test"
	opUpload   = "upload"
	opVerify   = "verify"
	opList     = "list"
	opDelete   = "delete"
	opDownload = "download"
	opAbort    = "abort"
)

// Error is what every operation returns when it fails. Msg is a full
// sentence and Hint says what to do about it; Kind and Params let the
// dashboard show its own translation. Neither ever contains a secret.
type Error struct {
	Kind Kind
	// Op is what was being done: setup, test, upload, verify, list,
	// delete, download or abort.
	Op     string
	Name   string        // the copy's file name, if there is one
	Status int           // the HTTP status, or 0 without a response
	Code   string        // the service's error code, such as NoSuchBucket
	Region string        // for KindWrongRegion: the bucket's region, if the service said
	Skew   time.Duration // for KindClockSkew: how far the service's clock is ahead (negative: behind)
	// Field is the setting at fault, if one is: endpoint, region, bucket,
	// accessKeyId, secretKey, host, port, user, folder, password,
	// privateKey, hostKey…
	Field string
	// HostKey is, for KindHostKeyUnknown and KindHostKeyChanged, the key
	// the other machine presented; Pinned is the confirmed key's
	// fingerprint for KindHostKeyChanged.
	HostKey *HostKey
	Pinned  string
	// Need and Free are, for KindNotEnoughSpace and a full disk on the
	// other machine, the bytes needed and free.
	Need, Free int64
	// User and Folder are, over SFTP, the user name and folder a refusal
	// is about.
	User, Folder string
	Msg          string
	Hint         string
	// Retry reports whether trying again later may work.
	Retry bool
	// Resume is set when an upload stopped part-way and can continue:
	// save it and pass it back as Upload.Resume, or give it to Abort.
	Resume *UploadState
	Err    error // the underlying error, if any

	svcMsg string        // the service's own message, lower-cased
	wait   time.Duration // Retry-After, when the service sent one
}

func (e *Error) Error() string { return e.Msg }

func (e *Error) Unwrap() error { return e.Err }

// Params are the values Msg and Hint mention, for a translated message.
func (e *Error) Params() map[string]string {
	p := map[string]string{"op": e.Op}
	for k, v := range map[string]string{"name": e.Name, "code": e.Code, "region": e.Region, "field": e.Field, "user": e.User, "folder": e.Folder} {
		if v != "" {
			p[k] = v
		}
	}
	if e.Status != 0 {
		p["status"] = strconv.Itoa(e.Status)
	}
	if e.Skew != 0 {
		p["skew"] = humanDuration(e.Skew)
		p["direction"] = "behind"
		if e.Skew < 0 {
			p["direction"] = "ahead"
		}
	}
	if e.HostKey != nil {
		p["fingerprint"], p["keyType"] = e.HostKey.Fingerprint, e.HostKey.Type
	}
	if e.Pinned != "" {
		p["pinnedFingerprint"] = e.Pinned
	}
	if e.Need > 0 {
		p["need"], p["needBytes"] = humanBytes(e.Need), strconv.FormatInt(e.Need, 10)
		p["free"], p["freeBytes"] = humanBytes(e.Free), strconv.FormatInt(e.Free, 10)
	}
	return p
}

func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Kind: KindUnexpected, Msg: "Something unexpected went wrong.", Err: err}
}

func opName(op string) string {
	switch op {
	case opUpload:
		return "The upload"
	case opVerify:
		return "Checking the copy"
	case opList:
		return "Listing the copies"
	case opDelete:
		return "Deleting the copy"
	case opDownload:
		return "The download"
	case opTest:
		return "The connection test"
	case opAbort:
		return "Cleaning up the unfinished upload"
	}
	return "The request"
}

// placeOf is where a destination of the kind keeps copies, for messages:
// "in the bucket" or "on the other machine".
func placeOf(kind string) string {
	if kind == TypeSFTP {
		return "on the other machine"
	}
	return "in the bucket"
}

// permissionWhat is what request r needed permission to do.
func (c *s3Client) permissionWhat(r *http.Request) string {
	switch {
	case r == nil:
		return "use this bucket"
	case r.Method == http.MethodPut || r.Method == http.MethodPost:
		return "write files to this bucket"
	case r.Method == http.MethodDelete:
		return "delete files in this bucket"
	case r.URL.Path == c.url("", nil).Path:
		return "list the files in this bucket"
	}
	return "read files in this bucket"
}

const permissionHint = "Give the key permission to read, write, list and delete files in the bucket, or create a key that has it."

// s3Error is the <Error> document S3 sends with a failure.
type s3Error struct {
	XMLName    xml.Name
	Code       string
	Message    string
	Region     string
	ServerTime string
}

func parseS3Error(body []byte) (s3Error, bool) {
	var se s3Error
	if xml.Unmarshal(bytes.TrimLeft(body, " \t\r\n"), &se) != nil || se.XMLName.Local != "Error" {
		return s3Error{}, false
	}
	if len(se.Code) > 64 || strings.IndexFunc(se.Code, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.')
	}) >= 0 {
		se.Code = ""
	}
	return se, true
}

// clean makes a service's message safe to show: printable ASCII, single
// spaces, bounded, and never the secret.
func (c *s3Client) clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r > '~' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if secret := c.cfg.SecretKey.Reveal(); secret != "" {
		s = strings.ReplaceAll(s, secret, "[hidden]")
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// responseError turns a failed response to r into an Error. sent is when
// the request was signed, to tell how far off the clock is.
func (c *s3Client) responseError(op, name string, r *http.Request, status int, h http.Header, body []byte, sent time.Time) *Error {
	se, _ := parseS3Error(body)
	e := &Error{Op: op, Name: name, Status: status, Code: se.Code, svcMsg: strings.ToLower(se.Message)}
	region := se.Region
	if region == "" {
		region = h.Get("X-Amz-Bucket-Region")
	}
	if !validRegion(region) {
		region = ""
	}
	code := se.Code
	switch {
	case status == http.StatusInsufficientStorage || code == "XMinioStorageFull" || code == "QuotaExceeded" ||
		strings.Contains(e.svcMsg, "cap exceeded") || strings.Contains(e.svcMsg, "quota"):
		e.Kind = KindStorageFull
		e.Msg = "The storage account is full: its quota or storage cap is reached."
		e.Hint = "Raise the cap or quota at the storage service, or keep fewer copies off the server."
	case code == "RequestTimeTooSkewed":
		c.skewError(e, se.ServerTime, h, sent)
	case code == "InvalidAccessKeyId" || code == "InvalidToken" || code == "ExpiredToken":
		e.Kind, e.Field = KindWrongKeys, "accessKeyId"
		e.Msg = "The storage service doesn't recognise the access key ID."
		e.Hint = "Check the access key ID, and that the endpoint is the one for the bucket's region. Create a new key if it was deleted."
	case code == "SignatureDoesNotMatch":
		e.Kind, e.Field = KindWrongKeys, "secretKey"
		e.Msg = "The secret access key doesn't match the access key ID."
		e.Hint = "Enter the secret key again exactly as the storage service showed it, or create a new key."
	case code == "AuthorizationHeaderMalformed" || code == "PermanentRedirect" || code == "IllegalLocationConstraintException" ||
		region != "" && region != c.cfg.Region && (status == http.StatusMovedPermanently || status == http.StatusTemporaryRedirect || status == http.StatusBadRequest):
		e.Kind, e.Field, e.Region = KindWrongRegion, "region", region
		if region != "" && region != c.cfg.Region {
			e.Msg = fmt.Sprintf("The bucket is in region %s, not %s.", region, c.cfg.Region)
			e.Hint = fmt.Sprintf("Change the region to %s. Some providers also use a different endpoint per region.", region)
		} else {
			e.Msg = fmt.Sprintf("The storage service says region %s is wrong for this bucket.", c.cfg.Region)
			e.Hint = "Check the bucket's region and endpoint on its page at the storage service."
		}
	case code == "NoSuchBucket":
		e.Kind, e.Field = KindNoSuchBucket, "bucket"
		e.Msg = fmt.Sprintf("There is no bucket named %s at this storage service.", c.cfg.Bucket)
		e.Hint = "Check the bucket name and the endpoint, or create the bucket at the storage service first."
	case code == "InvalidBucketName":
		e.Kind, e.Field = KindInvalidConfig, "bucket"
		e.Msg = "The storage service says the bucket name is not valid."
		e.Hint = "Check the bucket name on its page at the storage service."
	case code == "NoSuchUpload" || code == "InvalidPart" || code == "InvalidPartOrder":
		e.Kind = KindUploadGone
		e.Msg = "The storage service no longer has the unfinished upload's parts."
		e.Hint = "Copy the backup again; it starts from the beginning."
	case code == "NoSuchKey" || status == http.StatusNotFound && code == "":
		e.Kind = KindNotFound
		e.Msg = "The copy is not in the bucket."
		if name != "" {
			e.Msg = fmt.Sprintf("The copy %s is not in the bucket.", name)
		}
		e.Hint = "It may have been deleted at the storage service."
	case code == "BadDigest" || code == "InvalidDigest" || code == "XAmzContentSHA256Mismatch" || code == "XAmzContentChecksumMismatch":
		e.Kind, e.Retry = KindChecksum, true
		e.Msg = "The storage service received different data than was sent, so it refused it."
		e.Hint = "The connection may be unreliable. Try again."
	case code == "EntityTooLarge":
		e.Kind = KindTooLarge
		e.Msg = "The storage service refused the upload because it is too large."
		e.Hint = "Check the provider's maximum file size."
	case code == "AccountProblem" || code == "AllAccessDisabled":
		e.Kind = KindPermission
		e.Msg = "The storage service has disabled access to this account or bucket."
		e.Hint = "Check the account's billing and status at the storage service."
	case code == "AccessDenied" || status == http.StatusForbidden || status == http.StatusUnauthorized:
		e.Kind = KindPermission
		e.Msg = "The access key isn't allowed to " + c.permissionWhat(r) + "."
		e.Hint = permissionHint
		if code == "" {
			e.Msg = "The storage service refused access: the keys are wrong, or the access key isn't allowed to " + c.permissionWhat(r) + "."
			e.Hint = "Check the access key ID and secret. " + permissionHint
		}
	case code == "SlowDown" || code == "Throttling" || code == "ThrottlingException" || code == "TooManyRequests" ||
		code == "RequestLimitExceeded" || code == "ServiceUnavailable" || status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
		e.Kind, e.Retry = KindRateLimited, true
		e.Msg = "The storage service asked Playkeeper to slow down."
		e.Hint = "Try again in a few minutes."
	case code == "RequestTimeout":
		e.Kind, e.Retry = KindNetwork, true
		e.Msg = "The storage service stopped waiting for data because the connection was too slow."
		e.Hint = "Check this machine's internet connection, then try again."
	case code == "InternalError" || code == "OperationAborted" || status >= 500:
		e.Kind, e.Retry = KindServiceError, true
		e.Msg = "The storage service had a problem."
		if status >= 300 {
			e.Msg = fmt.Sprintf("The storage service had a problem (HTTP %d).", status)
		}
		e.Hint = "Try again later. If it keeps happening, check the provider's status page."
	case status >= 300 && status < 400:
		e.Kind, e.Field = KindRedirect, "endpoint"
		e.Msg = "The storage service tried to send the request to another address, which Playkeeper doesn't follow."
		e.Hint = "Check the endpoint, region and path-style setting against the provider's instructions."
	default:
		e.Kind = KindUnexpected
		e.Msg = "The storage service refused the request"
		if status >= 300 {
			e.Msg += fmt.Sprintf(" with HTTP %d", status)
		}
		if d := strings.TrimRight(c.clean(strings.Trim(se.Code+": "+se.Message, ": ")), "."); d != "" {
			e.Msg += ": " + d
		}
		e.Msg += "."
		e.Hint = "Check the storage settings against the provider's instructions. If it keeps happening, look up the error code in the provider's documentation."
	}
	return e
}

func (c *s3Client) skewError(e *Error, serverTime string, h http.Header, sent time.Time) {
	e.Kind = KindClockSkew
	server, err := time.Parse(time.RFC3339, serverTime)
	if err != nil {
		server, _ = http.ParseTime(h.Get("Date"))
	}
	if !server.IsZero() && !sent.IsZero() {
		e.Skew = server.Sub(sent).Round(time.Second)
	}
	switch {
	case e.Skew > 0:
		e.Msg = fmt.Sprintf("This machine's clock is %s behind the storage service's, so the service refused the request.", humanDuration(e.Skew))
	case e.Skew < 0:
		e.Msg = fmt.Sprintf("This machine's clock is %s ahead of the storage service's, so the service refused the request.", humanDuration(e.Skew))
	default:
		e.Msg = "This machine's clock is too far off the storage service's, so the service refused the request."
	}
	e.Hint = "Turn on automatic time sync on this machine (sudo timedatectl set-ntp true), then try again."
}

// checksumUnsupported reports whether e is a service refusing the
// x-amz-checksum headers rather than the request.
func checksumUnsupported(e *Error) bool {
	if e.Status != http.StatusBadRequest && e.Status != http.StatusNotImplemented {
		return false
	}
	switch e.Code {
	case "NotImplemented":
		return true
	case "InvalidArgument", "InvalidRequest", "UnsupportedHeader", "":
		return strings.Contains(e.svcMsg, "checksum")
	}
	return false
}

var errStalled = errors.New("no data moved for too long")

type refusedAddrError struct{ ip string }

func (e *refusedAddrError) Error() string { return "refusing to connect to " + e.ip }

// transportError turns a failure without a response into an Error.
func (c *s3Client) transportError(op, name string, err error) *Error {
	e := &Error{Op: op, Name: name, Err: err}
	var (
		dnsErr   *net.DNSError
		refused  *refusedAddrError
		verify   *tls.CertificateVerificationError
		unknown  x509.UnknownAuthorityError
		hostname x509.HostnameError
		badCert  x509.CertificateInvalidError
		record   tls.RecordHeaderError
		netErr   net.Error
	)
	switch {
	case errors.Is(err, context.Canceled):
		e.Kind = KindCanceled
		e.Msg = opName(op) + " was cancelled before it finished."
	case errors.Is(err, errStalled):
		e.Kind, e.Retry = KindNetwork, true
		e.Msg = fmt.Sprintf("The connection to the storage service stalled: no data moved for %s.", humanDuration(c.stall))
		e.Hint = "Check this machine's internet connection, then try again."
	case errors.As(err, &refused):
		e.Kind, e.Field = KindInvalidConfig, "endpoint"
		e.Msg = fmt.Sprintf("The endpoint's host name points to %s, a link-local, multicast, unspecified or cloud metadata address Playkeeper never connects to.", refused.ip)
		e.Hint = "Check the endpoint address."
	case errors.As(err, &dnsErr):
		e.Kind, e.Field = KindNetwork, "endpoint"
		if dnsErr.IsNotFound {
			e.Msg = fmt.Sprintf("The host name %s could not be found.", dnsErr.Name)
			e.Hint = "Check the endpoint address."
			if !c.cfg.PathStyle {
				e.Hint = "Check the endpoint address, or turn on path-style addressing if the provider doesn't give each bucket its own host name."
			}
		} else {
			e.Retry = true
			e.Msg = "This machine couldn't look up the storage service's address (DNS)."
			e.Hint = "Check this machine's DNS settings and internet connection."
		}
	case errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &badCert):
		e.Kind, e.Field = KindTLS, "endpoint"
		e.Msg = "The storage service's certificate could not be verified, so nothing was sent."
		e.Hint = "Check the endpoint address. A self-hosted service needs a certificate from a public authority, such as Let's Encrypt."
	case errors.As(err, &record) || errors.Is(err, http.ErrSchemeMismatch):
		e.Kind, e.Field = KindTLS, "endpoint"
		e.Msg = "The endpoint didn't answer with HTTPS."
		e.Hint = "Check the endpoint's address and port; the service must accept HTTPS there."
	case errors.Is(err, syscall.ECONNREFUSED):
		e.Kind, e.Field = KindNetwork, "endpoint"
		e.Msg = "Nothing accepted the connection at the endpoint (connection refused)."
		e.Hint = "Check the endpoint's address and port, and that the service is running."
	case errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH):
		e.Kind, e.Retry = KindNetwork, true
		e.Msg = "This machine can't reach the storage service's network."
		e.Hint = "Check this machine's internet connection and firewall."
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		e.Kind, e.Retry = KindNetwork, true
		e.Msg = "The storage service didn't answer in time."
		e.Hint = "Check this machine's internet connection, then try again."
	default:
		e.Kind, e.Retry = KindNetwork, true
		e.Msg = "The connection to the storage service broke."
		e.Hint = "Check this machine's internet connection, then try again."
	}
	return e
}

func unexpected(op, name, msg string) *Error {
	return &Error{Kind: KindUnexpected, Op: op, Name: name, Msg: msg}
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	plural := func(n int64, unit string) string {
		if n == 1 {
			return "1 " + unit
		}
		return strconv.FormatInt(n, 10) + " " + unit + "s"
	}
	switch {
	case d < 2*time.Minute:
		return plural(int64(d.Round(time.Second)/time.Second), "second")
	case d < 2*time.Hour:
		return plural(int64(d.Round(time.Minute)/time.Minute), "minute")
	case d < 48*time.Hour:
		return plural(int64(d.Round(time.Hour)/time.Hour), "hour")
	}
	return plural(int64(d.Round(24*time.Hour)/(24*time.Hour)), "day")
}

// humanBytes writes n in decimal units, as the dashboard shows sizes.
func humanBytes(n int64) string {
	switch {
	case n >= 999_950_000_000:
		return fmt.Sprintf("%.1f TB", float64(n)/1e12)
	case n >= 999_950_000:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 999_500:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.0f kB", float64(n)/1e3)
	}
	return strconv.FormatInt(n, 10) + " bytes"
}
