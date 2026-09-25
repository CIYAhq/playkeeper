package update

import (
	"archive/tar"
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
	"strings"
	"time"
)

// DefaultReleaseURL is where releases are published; its manifest is always
// the latest release's.
const DefaultReleaseURL = "https://github.com/CIYAhq/playkeeper/releases/latest/download"

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

func (s Source) get(ctx context.Context, name string, limit int64) (io.ReadCloser, error) {
	u, err := CheckReleaseURL(s.BaseURL)
	if err != nil {
		return nil, err
	}
	u = u.JoinPath(name)
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "playkeeper-updater")
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", u.Redacted(), err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered HTTP %d", u.Redacted(), resp.StatusCode)
	}
	if resp.ContentLength > limit {
		resp.Body.Close()
		return nil, fmt.Errorf("%s is larger than expected (%d bytes)", name, resp.ContentLength)
	}
	return resp.Body, nil
}

func (s Source) small(ctx context.Context, name string) ([]byte, error) {
	const limit = 1 << 20
	body, err := s.get(ctx, name, limit)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, fmt.Errorf("%s is larger than expected", name)
	}
	return b, nil
}

// Release is a verified manifest with the exact bytes that were signed.
type Release struct {
	Manifest  *Manifest
	Raw       []byte
	Signature []byte
}

// Latest downloads the manifest and its signature and verifies them.
func (s Source) Latest(ctx context.Context, keys []ed25519.PublicKey) (*Release, error) {
	if len(keys) == 0 {
		return nil, ErrNoTrustedKey
	}
	raw, err := s.small(ctx, ManifestFile)
	if err != nil {
		return nil, err
	}
	sig, err := s.small(ctx, SignatureFile)
	if err != nil {
		return nil, err
	}
	m, err := VerifyManifest(raw, sig, keys)
	if err != nil {
		return nil, err
	}
	return &Release{Manifest: m, Raw: raw, Signature: sig}, nil
}

// Download saves the release tarball to dst, refusing anything that does not
// match the signed size and SHA-256.
func (s Source) Download(ctx context.Context, a Asset, dst string) error {
	body, err := s.get(ctx, a.File, a.Size)
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
