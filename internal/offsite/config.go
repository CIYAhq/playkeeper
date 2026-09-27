// Package offsite keeps copies of a server's backups somewhere else: on
// S3-compatible storage (AWS S3, Backblaze B2, Cloudflare R2, Wasabi,
// Hetzner Object Storage, MinIO and others) or on another machine over
// SFTP. It uploads, lists, checks, downloads and deletes those copies.
//
// Every copy is encrypted on this machine before it leaves, with age
// (https://age-encryption.org) and an X25519 key of the server's own, so
// the storage company or the other machine only ever holds ciphertext. A
// restore decrypts the copy with the server's keys and checks the
// archive's own SHA-256 against the backup record. The recovery key file
// the user downloads opens the copies with Playkeeper or the age tool if
// the machine is lost. Rotating the key keeps the old ones, so older
// copies still open.
//
// A copy is encrypted into a spool file first and sent from that file,
// with bounded memory: to S3 in parts, each checked by the service on
// arrival, and to SFTP in segments that are read back before the copy
// takes its name. An interrupted upload resumes from the same encrypted
// file, or is aborted cleanly. The checksums the storage keeps describe
// the encrypted copy.
//
// S3 requests are signed with AWS Signature Version 4 over net/http
// instead of an SDK, go only to the configured endpoint over HTTPS, and
// never follow redirects. SFTP runs over golang.org/x/crypto/ssh: the
// other machine's host key is pinned once the user has confirmed its
// fingerprint, a changed key is refused before anything is sent, and the
// login is a password or an ed25519 key Playkeeper generates.
//
// Secrets (the S3 secret key, the SFTP password and private key, and the
// encryption keys) are never logged, printed, marshalled or put in an
// error.
package offsite

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// Secret holds a secret: a secret access key, a password, a private key or
// an encryption key. It prints, logs and marshals as "[hidden]", never as
// its value; Reveal is only for storing and using it.
type Secret struct{ v *string }

func NewSecret(s string) Secret {
	if s == "" {
		return Secret{}
	}
	return Secret{v: &s}
}

func (s Secret) IsSet() bool { return s.v != nil }

// Reveal returns the secret, for the code that stores or uses it.
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

// What Config.Type can be.
const (
	TypeS3   = "s3"
	TypeSFTP = "sftp"
)

// Config is where one server's copies go: S3-compatible storage or another
// machine over SFTP, as Type says. Only the matching part is used.
type Config struct {
	Type string     `json:"type"`
	S3   S3Config   `json:"s3,omitzero"`
	SFTP SFTPConfig `json:"sftp,omitzero"`
}

// Validate checks the part of c that Type chooses. Its errors are *Error
// with Kind KindInvalidConfig and Field naming the setting.
func (c Config) Validate() error {
	switch c.Type {
	case TypeS3:
		return c.S3.Validate()
	case TypeSFTP:
		return c.SFTP.Validate()
	}
	return invalid("type", "Choose where the copies go: S3-compatible storage or another machine over SFTP.", "")
}

// S3Config is a bucket at an S3-compatible storage service. Prefix is the
// folder inside the bucket, for example "playkeeper/<server id>/" (a
// missing trailing slash is added); one bucket can hold several servers'
// copies under different prefixes.
type S3Config struct {
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
func (c S3Config) Validate() error {
	u, err := url.Parse(c.Endpoint)
	switch {
	case c.Endpoint == "":
		return invalid("endpoint", "Enter the endpoint address of the storage service.", "It starts with https://, for example https://s3.eu-central-003.backblazeb2.com.")
	case err != nil || u.Host == "" || u.Opaque != "":
		return invalid("endpoint", "The endpoint is not a valid address.", "Use the form https://host, for example https://s3.eu-central-003.backblazeb2.com.")
	case u.Scheme != "https":
		return invalid("endpoint", "The endpoint must start with https://, so the keys travel encrypted.", "")
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

// SFTPConfig is another machine that keeps the copies, reached over SFTP
// (file transfer over SSH). Exactly one of Password and PrivateKey is set.
// HostKey is empty until the user has confirmed the fingerprint the
// connection test shows; it then holds that key, and a machine presenting
// any other key is refused.
type SFTPConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"` // 22 if 0
	User string `json:"user"`
	// Folder is where the copies go, and must already exist: an absolute
	// path, or one relative to the user's home folder. Several servers can
	// share it, since copies carry their archive's name.
	Folder     string `json:"folder"`
	Password   Secret `json:"-"`
	PrivateKey Secret `json:"-"` // an OpenSSH private key, as NewSSHKey makes
	// HostKey is the confirmed host key as known_hosts has it, without the
	// host name: for example "ssh-ed25519 AAAAC3Nz…".
	HostKey string `json:"hostKey"`
}

// Validate checks c before anything is sent. Its errors are *Error with
// Kind KindInvalidConfig and Field naming the setting.
func (c SFTPConfig) Validate() error {
	ip := net.ParseIP(c.Host)
	switch {
	case c.Host == "":
		return invalid("host", "Enter the other machine's address: its host name or IP address.", "")
	case ip != nil && refusedIP(ip):
		return invalid("host", "The address is a link-local, multicast, unspecified or cloud metadata address, which is never another machine's.", "Use the other machine's host name or IP address.")
	case ip == nil && !validHost(c.Host):
		return invalid("host", "The host name may only contain letters, digits, dots and hyphens.", "Enter only the machine's name or IP address, without a user name, port or folder.")
	case c.Port < 0 || c.Port > 65535:
		return invalid("port", "The port must be a number between 1 and 65535.", "SSH usually listens on port 22.")
	case c.User == "" || len(c.User) > 64 || !printable(c.User):
		return invalid("user", "Enter the user name to sign in with on the other machine.", "")
	}
	if msg := folderProblem(c.Folder); msg != "" {
		return invalid("folder", msg, "For example /srv/backups/playkeeper, or backups/playkeeper for a folder in the user's home folder.")
	}
	pw, key := c.Password.Reveal(), c.PrivateKey.Reveal()
	switch {
	case pw == "" && key == "":
		return invalid("password", "Enter the password, or sign in with a key Playkeeper makes.", "")
	case pw != "" && key != "":
		return invalid("privateKey", "Sign in with either the password or the key, not both.", "")
	case pw != "" && (len(pw) > 1024 || !utf8.ValidString(pw) || strings.ContainsFunc(pw, unicode.IsControl)):
		return invalid("password", "The password can't contain line breaks or other control characters.", "")
	case key != "":
		var missing *ssh.PassphraseMissingError
		if _, err := ssh.ParsePrivateKey([]byte(key)); errors.As(err, &missing) {
			return invalid("privateKey", "The private key is protected by a passphrase, which Playkeeper can't type.", "Sign in with a key Playkeeper makes.")
		} else if err != nil || len(key) > 16<<10 {
			return invalid("privateKey", "The private key is not an OpenSSH private key Playkeeper can read.", "Sign in with a key Playkeeper makes.")
		}
	}
	if c.HostKey != "" {
		if _, err := parseHostKey(c.HostKey); err != nil {
			return invalid("hostKey", "The confirmed host key is not a host key Playkeeper can check.", "Run the connection test again and confirm the fingerprint it shows.")
		}
	}
	return nil
}

func folderProblem(f string) string {
	switch {
	case f == "":
		return "Enter the folder on the other machine where the copies go."
	case len(f) > 1024 || !utf8.ValidString(f) || strings.ContainsFunc(f, unicode.IsControl) || strings.Contains(f, `\`):
		return "The folder must be a path of at most 1024 characters, with forward slashes and without control characters."
	case strings.HasPrefix(f, "~"):
		return "The folder can't start with ~. For a folder in the user's home folder, leave out ~/."
	case path.Clean(f) == "/":
		return "Choose a folder for the copies, not the root folder."
	case strings.Contains(strings.TrimSuffix(f, "/"), "//"):
		return "The folder must not contain two slashes in a row."
	}
	for _, seg := range strings.Split(f, "/") {
		if seg == "." || seg == ".." {
			return "The folder must not contain . or .. as a folder name."
		}
	}
	return ""
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

// ValidName reports whether name can be a backup archive's file name that
// Playkeeper copies: letters, digits, hyphens, underscores and dots, ending
// in .tar.gz, without path separators, of at most 200 bytes.
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

// CopyName is the file name of an archive's copy: the archive's name with
// .age added, because the copy is encrypted.
func CopyName(archive string) string { return archive + ".age" }

// archiveOf returns the archive name of the copy name, and whether name is
// a copy's name at all.
func archiveOf(name string) (string, bool) {
	a, ok := strings.CutSuffix(name, ".age")
	return a, ok && ValidName(a)
}

func validCopyName(name string) bool {
	_, ok := archiveOf(name)
	return ok
}
