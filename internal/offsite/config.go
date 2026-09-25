// Package offsite copies backups off the server to S3-compatible storage
// (AWS S3, Backblaze B2, Cloudflare R2, Wasabi, Hetzner Object Storage,
// MinIO and others) and lists, checks, downloads and deletes those copies.
// It signs requests itself with AWS Signature Version 4 over net/http
// instead of using an SDK.
//
// The service checks every upload as it arrives: each request carries a
// signed SHA-256 of its body and a Content-MD5, plus an
// x-amz-checksum-sha256 where the service supports one. After an upload the
// copy's size and checksum are read back and compared. Large archives go up
// in parts, streamed from the file with bounded memory, and an interrupted
// upload can be resumed or is aborted cleanly.
//
// The secret key is never logged, printed, marshalled or put in an error.
// The only host contacted is the configured endpoint (or bucket.endpoint),
// over HTTPS, and redirects are refused.
package offsite

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Secret holds a secret access key. It prints, logs and marshals as
// "[hidden]", never as its value; Reveal is only for storing it.
type Secret struct{ v *string }

func NewSecret(s string) Secret {
	if s == "" {
		return Secret{}
	}
	return Secret{v: &s}
}

func (s Secret) IsSet() bool { return s.v != nil }

// Reveal returns the key, for the code that stores it.
func (s Secret) Reveal() string {
	if s.v == nil {
		return ""
	}
	return *s.v
}

func (s Secret) String() string {
	if s.v == nil {
		return ""
	}
	return "[hidden]"
}

// Format makes every fmt verb, including %#v and %x, print String.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }

func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Provider is a storage service the setup screen offers, with the values
// it usually needs. Endpoint may contain {region} or {account}.
type Provider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	PathStyle bool   `json:"pathStyle"`
	Hint      string `json:"hint"`
}

// Providers lists the services the setup screen offers.
func Providers() []Provider {
	return []Provider{
		{ID: "aws", Name: "Amazon S3", Endpoint: "https://s3.{region}.amazonaws.com",
			Hint: "Create an access key for an IAM user that may read, write, list and delete objects in the bucket."},
		{ID: "b2", Name: "Backblaze B2", Endpoint: "https://s3.{region}.backblazeb2.com",
			Hint: "Create an application key for the bucket. The bucket page shows the endpoint; its region is the part after \"s3.\", for example us-west-004."},
		{ID: "r2", Name: "Cloudflare R2", Endpoint: "https://{account}.r2.cloudflarestorage.com", Region: "auto", PathStyle: true,
			Hint: "Create an R2 API token with Object Read & Write for the bucket. The endpoint contains your account ID."},
		{ID: "wasabi", Name: "Wasabi", Endpoint: "https://s3.{region}.wasabisys.com",
			Hint: "Create an access key for a user whose policy allows the bucket."},
		{ID: "hetzner", Name: "Hetzner Object Storage", Endpoint: "https://{region}.your-objectstorage.com", Region: "fsn1",
			Hint: "Create S3 credentials in the Hetzner Cloud Console. The region is the bucket's location, for example fsn1, nbg1 or hel1."},
		{ID: "minio", Name: "MinIO", Region: "us-east-1", PathStyle: true,
			Hint: "Use the address of your MinIO server with https:// and an access key allowed to use the bucket."},
		{ID: "other", Name: "Other S3-compatible storage", Region: "us-east-1", PathStyle: true,
			Hint: "Use the endpoint, region and access key your provider gives for its S3-compatible API."},
	}
}

// Config is where one server's copies go. Prefix is the folder inside the
// bucket, for example "playkeeper/<server id>/" (a missing trailing slash is
// added); one bucket can hold several servers' copies under different
// prefixes.
type Config struct {
	Provider    string `json:"provider"`
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	AccessKeyID string `json:"accessKeyId"`
	SecretKey   Secret `json:"-"`
	// PathStyle puts the bucket in the path (https://endpoint/bucket/key)
	// instead of the host name (https://bucket.endpoint/key).
	PathStyle bool `json:"pathStyle"`
}

func invalid(field, msg, hint string) *Error {
	return &Error{Kind: KindInvalidConfig, Op: "setup", Field: field, Msg: msg, Hint: hint}
}

// Validate checks c before anything is sent. Its errors are *Error with
// Kind KindInvalidConfig and Field naming the setting.
func (c Config) Validate() error {
	u, err := url.Parse(c.Endpoint)
	switch {
	case c.Endpoint == "":
		return invalid("endpoint", "Enter the endpoint address of the storage service.", "It starts with https://, for example https://s3.eu-central-003.backblazeb2.com.")
	case err != nil || u.Host == "" || u.Opaque != "":
		return invalid("endpoint", "The endpoint is not a valid address.", "Use the form https://host, for example https://s3.eu-central-003.backblazeb2.com.")
	case u.Scheme != "https":
		return invalid("endpoint", "The endpoint must start with https://, so the keys and backups travel encrypted.", "")
	case u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return invalid("endpoint", "The endpoint must be only https:// and a host name, without a path, query or user name.", "Put the bucket in Bucket and any folder in Prefix.")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return invalid("endpoint", "The endpoint's port is not a number between 1 and 65535.", "")
		}
	}
	ip := net.ParseIP(u.Hostname())
	switch {
	case ip != nil && refusedIP(ip):
		return invalid("endpoint", "The endpoint is a link-local, multicast, unspecified or cloud metadata address, which is never a storage service.", "Use the storage service's host name.")
	case ip == nil && !validHost(u.Hostname()):
		return invalid("endpoint", "The endpoint's host name may only contain letters, digits, dots and hyphens.", "")
	case ip != nil && !c.PathStyle:
		return invalid("pathStyle", "An endpoint given as an IP address only works with path-style addressing.", "Turn on path-style addressing.")
	}
	if !validRegion(c.Region) {
		return invalid("region", "Enter the region: lowercase letters, digits and hyphens, for example eu-central-1 (auto for Cloudflare R2).", "")
	}
	if msg := bucketProblem(c.Bucket); msg != "" {
		return invalid("bucket", msg, "Bucket names are 3 to 63 lowercase letters, digits, dots and hyphens.")
	}
	if !c.PathStyle && strings.Contains(c.Bucket, ".") {
		return invalid("pathStyle", "A bucket name with dots only works with path-style addressing.", "Turn on path-style addressing, or use a bucket name without dots.")
	}
	if msg := prefixProblem(c.Prefix); msg != "" {
		return invalid("prefix", msg, "Use letters, digits, dots, hyphens, underscores and slashes, for example playkeeper/survival/.")
	}
	if c.AccessKeyID == "" || len(c.AccessKeyID) > 128 || !printable(c.AccessKeyID) {
		return invalid("accessKeyId", "Enter the access key ID exactly as the storage service shows it.", "")
	}
	if s := c.SecretKey.Reveal(); s == "" || len(s) > 256 || !printable(s) {
		return invalid("secretKey", "Enter the secret access key exactly as the storage service showed it.", "The secret is shown only once when the key is created; create a new key if you no longer have it.")
	}
	return nil
}

func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

func validHost(h string) bool {
	if h == "" || len(h) > 253 || h[0] == '.' || h[len(h)-1] == '.' || strings.Contains(h, "..") {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

func validRegion(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func bucketProblem(b string) string {
	if len(b) < 3 || len(b) > 63 {
		return "The bucket name must be 3 to 63 characters long."
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return "The bucket name may only contain lowercase letters, digits, dots and hyphens."
		}
	}
	first, last := b[0], b[len(b)-1]
	if first == '.' || first == '-' || last == '.' || last == '-' || strings.Contains(b, "..") {
		return "The bucket name must start and end with a letter or digit and not contain two dots in a row."
	}
	if net.ParseIP(b) != nil {
		return "The bucket name must not look like an IP address."
	}
	return ""
}

func prefixProblem(p string) string {
	if p == "" {
		return ""
	}
	if len(p) > 200 || !utf8.ValidString(p) {
		return "The prefix must be at most 200 characters."
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "//") {
		return "The prefix must not start with a slash or contain two slashes in a row."
	}
	for _, seg := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		if seg == "." || seg == ".." {
			return "The prefix must not contain . or .. as a folder name."
		}
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_' || c == '/') {
			return "The prefix may only contain letters, digits, dots, hyphens, underscores and slashes."
		}
	}
	return ""
}

// ValidName reports whether name can be a copy's file name: a backup
// archive name (letters, digits, hyphens, underscores and dots, ending in
// .tar.gz) without path separators, of at most 200 bytes.
func ValidName(name string) bool {
	if len(name) > 200 || !strings.HasSuffix(name, ".tar.gz") || len(name) == len(".tar.gz") || name[0] == '.' || strings.Contains(name, "..") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
