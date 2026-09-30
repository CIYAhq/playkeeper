package update

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultReleaseURL is where releases are published; its manifest is always
// the latest release's.
const DefaultReleaseURL = "https://github.com/CIYAhq/playkeeper/releases/latest/download"

// CheckURL is where an install checks for a new release: playkeeper.io's
// copy of the latest release's manifest and signature, which
// cmd/release-mirror keeps and nginx serves with an ETag and Last-Modified,
// so a check whose release didn't change is answered 304 Not Modified.
// Downloads still come from DefaultReleaseURL; authenticity comes from the
// signature either way.
const CheckURL = "https://playkeeper.io/releases/latest"

// CheckReleaseURL accepts https:// URLs, and plain http:// only for this
// machine (local test mirrors). Authenticity comes from the signature either
// way; HTTPS keeps what is downloaded private.
func CheckReleaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("release location %q is not a URL", raw)
	}
	switch u.Scheme {
	case "https":
		return u, nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return u, nil
		}
		return nil, fmt.Errorf("release location %s is not HTTPS (plain HTTP is only allowed for this machine)", raw)
	}
	return nil, fmt.Errorf("release location %s must be an https:// URL", raw)
}

// Source fetches releases from one location.
type Source struct {
	BaseURL string
	Client  *http.Client
}

func (s Source) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// Validators are what a release location said to tell its copy of a file
// apart, its ETag and Last-Modified. Sent back, they let it answer 304 Not
// Modified when the file hasn't changed, instead of sending it again.
type Validators struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
}

func (v Validators) empty() bool { return v.ETag == "" && v.LastModified == "" }

// StatusError is a release location's answer that was neither the file nor
// 304 Not Modified. RetryAfter is how long a 429 or 503 asked to be left
// alone (its Retry-After), or 0.
type StatusError struct {
	URL        string
	Code       int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s answered HTTP %d", e.URL, e.Code) }

// retryAfter reads a Retry-After header: seconds, or an HTTP date.
func retryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		return max(time.Duration(n)*time.Second, 0)
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}

// get fetches name, asking only whether it changed when since has
// validators: then a 304 is a nil body, with any validators it carried.
func (s Source) get(ctx context.Context, name string, limit int64, since Validators) (io.ReadCloser, Validators, error) {
	u, err := CheckReleaseURL(s.BaseURL)
	if err != nil {
		return nil, Validators{}, err
	}
	u = u.JoinPath(name)
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, Validators{}, err
	}
	req.Header.Set("User-Agent", "playkeeper-updater")
	if since.ETag != "" {
		req.Header.Set("If-None-Match", since.ETag)
	}
	if since.LastModified != "" {
		req.Header.Set("If-Modified-Since", since.LastModified)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, Validators{}, fmt.Errorf("could not reach %s: %w", u.Redacted(), err)
	}
	seen := Validators{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
	switch {
	case resp.StatusCode == http.StatusNotModified && !since.empty():
		resp.Body.Close()
		return nil, seen, nil
	case resp.StatusCode != http.StatusOK:
		resp.Body.Close()
		se := &StatusError{URL: u.Redacted(), Code: resp.StatusCode}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			se.RetryAfter = retryAfter(resp.Header, time.Now())
		}
		return nil, Validators{}, se
	case resp.ContentLength > limit:
		resp.Body.Close()
		return nil, Validators{}, fmt.Errorf("%s is larger than expected (%d bytes)", name, resp.ContentLength)
	}
	return resp.Body, seen, nil
}

// small reads a file of at most a megabyte, or nil when it didn't change
// since since (see get).
func (s Source) small(ctx context.Context, name string, since Validators) ([]byte, Validators, error) {
	const limit = 1 << 20
	body, seen, err := s.get(ctx, name, limit, since)
	if err != nil || body == nil {
		return nil, seen, err
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, Validators{}, err
	}
	if len(b) > limit {
		return nil, Validators{}, fmt.Errorf("%s is larger than expected", name)
	}
	return b, seen, nil
}

// Release is a verified manifest with the exact bytes that were signed.
type Release struct {
	Manifest  *Manifest
	Raw       []byte
	Signature []byte
}

// Cached is a location's latest release as it served it last: the manifest
// and signature byte for byte, and each file's validators.
type Cached struct {
	Manifest      []byte     `json:"manifest"`
	Signature     []byte     `json:"signature"`
	ManifestSeen  Validators `json:"manifestSeen"`
	SignatureSeen Validators `json:"signatureSeen"`
}

// Latest downloads the manifest and its signature and verifies them.
func (s Source) Latest(ctx context.Context, keys []ed25519.PublicKey) (*Release, error) {
	rel, _, err := s.LatestSince(ctx, keys, nil)
	return rel, err
}

// LatestSince is Latest for a location fetched from before, as prev. It asks
// for the signature only if it changed since prev, and for the manifest only
// when the signature did: a release's signature covers its manifest's exact
// bytes, so the same signature means the same manifest. A release that
// didn't change costs one 304, and the same signature sent again (by a
// location without validators) no more than the signature. Either way the
// release is verified as Latest's is, prev's files included, and comes with
// what to pass as prev next time.
func (s Source) LatestSince(ctx context.Context, keys []ed25519.PublicKey, prev *Cached) (*Release, *Cached, error) {
	if len(keys) == 0 {
		return nil, nil, ErrNoTrustedKey
	}
	have := prev != nil && len(prev.Manifest) > 0 && len(prev.Signature) > 0
	var since Validators
	if have {
		since = prev.SignatureSeen
	}
	sig, sigSeen, err := s.small(ctx, SignatureFile, since)
	if err != nil {
		return nil, nil, err
	}
	next := &Cached{Signature: sig, SignatureSeen: sigSeen}
	if sig == nil || (have && bytes.Equal(sig, prev.Signature)) {
		next.Signature, next.Manifest, next.ManifestSeen = prev.Signature, prev.Manifest, prev.ManifestSeen
		if sig == nil {
			next.SignatureSeen = Validators{ETag: cmp.Or(sigSeen.ETag, prev.SignatureSeen.ETag), LastModified: cmp.Or(sigSeen.LastModified, prev.SignatureSeen.LastModified)}
		}
	} else if next.Manifest, next.ManifestSeen, err = s.small(ctx, ManifestFile, Validators{}); err != nil {
		return nil, nil, err
	}
	m, err := VerifyManifest(next.Manifest, next.Signature, keys)
	if err != nil {
		return nil, nil, err
	}
	return &Release{Manifest: m, Raw: next.Manifest, Signature: next.Signature}, next, nil
}

// Download saves the release tarball to dst, refusing anything that does not
// match the signed size and SHA-256.
func (s Source) Download(ctx context.Context, a Asset, dst string) error {
	body, _, err := s.get(ctx, a.File, a.Size, Validators{})
	if err != nil {
		return err
	}
	defer body.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, a.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return fmt.Errorf("downloading %s failed: %w", a.File, err)
	}
	if n != a.Size {
		os.Remove(dst)
		return fmt.Errorf("%s is %d bytes, the signed manifest says %d", a.File, n, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		os.Remove(dst)
		return fmt.Errorf("%s does not match the signed SHA-256 (got %s, want %s)", a.File, got[:16], a.SHA256[:16])
	}
	return nil
}

// ExtractBinary copies the playkeeper binary out of a downloaded tarball to
// dst (mode 0755). Only that one regular file is read, and it must match the
// signed SHA-256.
func ExtractBinary(tarball string, a Asset, dst string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s is not a gzip file: %w", filepath.Base(tarball), err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s does not contain %s", filepath.Base(tarball), a.Binary)
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", filepath.Base(tarball), err)
		}
		if hdr.Name != a.Binary {
			continue
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Size <= 0 || hdr.Size > MaxTarballBytes*4 {
			return fmt.Errorf("%s in %s is not a regular file", a.Binary, filepath.Base(tarball))
		}
		return writeVerified(tr, hdr.Size, dst, a.BinarySHA256)
	}
}

func writeVerified(r io.Reader, size int64, dst, want string) error {
	tmp := dst + ".partial"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), io.LimitReader(r, size))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			err = fmt.Errorf("the playkeeper binary does not match the signed SHA-256 (got %s, want %s)", got[:16], want[:16])
		}
	}
	if err == nil {
		err = os.Chmod(tmp, 0o755)
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// FileSHA256 hashes a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
