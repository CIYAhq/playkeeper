package certs

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// maxPEMSize bounds a certificate or key file.
const maxPEMSize = 64 << 10

const noFollow = syscall.O_NOFOLLOW

// Certificate describes a saved certificate. It maps onto a row of the
// caller's certificates table.
type Certificate struct {
	Names     []string  `json:"names"`
	File      string    `json:"file"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	// RenewAt is when to get the next certificate; see RenewAt.
	RenewAt time.Time `json:"renewAt"`
	// Issuer names the issuing CA, such as "Let's Encrypt R12".
	Issuer string `json:"issuer"`
	Serial string `json:"serial"`
	// SHA256 is the fingerprint of the certificate, in the format the panel
	// shows for its self-signed certificate.
	SHA256 string `json:"sha256"`
}

func certificateInfo(file string, leaf *x509.Certificate) Certificate {
	issuer := strings.Join(leaf.Issuer.Organization, " ")
	if cn := leaf.Issuer.CommonName; cn != "" && !strings.Contains(cn, issuer) {
		issuer = strings.TrimSpace(issuer + " " + cn)
	} else if cn != "" {
		issuer = cn
	}
	names := make([]string, 0, len(leaf.DNSNames))
	for _, n := range leaf.DNSNames {
		names = append(names, strings.ToLower(n))
	}
	return Certificate{
		Names:     names,
		File:      file,
		NotBefore: leaf.NotBefore,
		NotAfter:  leaf.NotAfter,
		RenewAt:   RenewAt(leaf.NotBefore, leaf.NotAfter),
		Issuer:    issuer,
		Serial:    hex.EncodeToString(leaf.SerialNumber.Bytes()),
		SHA256:    fingerprint(leaf.Raw),
	}
}

// ReadCertificate describes a certificate file saved by Issue.
func ReadCertificate(file string) (*Certificate, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := readRegular(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	cert, err := parseBundle(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	info := certificateInfo(file, cert.Leaf)
	return &info, nil
}

// readRegular reads a regular file of at most maxPEMSize bytes.
func readRegular(f *os.File) ([]byte, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if st.Size() > maxPEMSize {
		return nil, fmt.Errorf("larger than %d bytes", maxPEMSize)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPEMSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxPEMSize {
		return nil, fmt.Errorf("larger than %d bytes", maxPEMSize)
	}
	return b, nil
}

// parseBundle loads a certificate chain and its private key from one PEM
// file, checking that they belong together.
func parseBundle(b []byte) (*tls.Certificate, error) {
	cert, err := tls.X509KeyPair(b, b)
	if err != nil {
		return nil, err
	}
	if cert.Leaf == nil {
		if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, err
		}
	}
	return &cert, nil
}

// encodeBundle is the certificate chain followed by the private key, the
// layout parseBundle reads. One file keeps the pair consistent on renewal.
func encodeBundle(chain [][]byte, key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, c := range chain {
		if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: c}); err != nil {
			return nil, err
		}
	}
	if err := pem.Encode(&buf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// openDir opens dir, creating it when missing. It is opened inside its
// parent, so a symbolic link planted in place of dir cannot lead outside the
// parent; the parent's own path must not be writable by less trusted users.
func openDir(dir string, owner *Owner) (*os.Root, error) {
	dir = filepath.Clean(dir)
	parent, base := filepath.Split(dir)
	if base == "" || base == "." || base == ".." {
		return nil, fmt.Errorf("%q is not a directory to save certificates in", dir)
	}
	if parent == "" {
		parent = "."
	}
	pr, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer pr.Close()
	mode := os.FileMode(0o700)
	if owner != nil {
		mode = 0o750
	}
	if err := pr.Mkdir(base, mode); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	r, err := pr.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	if owner != nil {
		if err := r.Chown(".", owner.UID, owner.GID); err != nil {
			r.Close()
			return nil, err
		}
		if err := r.Chmod(".", mode); err != nil {
			r.Close()
			return nil, err
		}
	}
	return r, nil
}

// writeFile replaces name in r with data through a temporary file and a
// rename, so readers see the old or the new file and never a partial one.
func writeFile(r *os.Root, name string, data []byte, mode os.FileMode, owner *Owner) (err error) {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := "." + name + ".tmp-" + hex.EncodeToString(suffix[:])
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			r.Remove(tmp)
		}
	}()
	if owner != nil {
		if err := f.Chown(owner.UID, owner.GID); err != nil {
			return err
		}
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := r.Rename(tmp, name); err != nil {
		return err
	}
	if d, err := r.Open("."); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// orderSuffix follows the certificate's first name in the name of the file
// an order is kept in.
const orderSuffix = ".order"

// keptOrder is the file an order is kept in from just before it is
// finalized until its certificate is saved, so that an attempt that fails
// in between leaves the next one the certificate of that order: asking for
// another would count against the certificate authority's limits.
type keptOrder struct {
	dir  *os.Root
	file string
}

// orderFile is what a kept order's file holds.
type orderFile struct {
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
	// Key is the private key the certificate is asked for, in SEC 1 form.
	Key []byte `json:"key"`
}

// keep saves the order at url, which the certificate authority considers
// invalid after expires, with the key the certificate is asked for.
func (k *keptOrder) keep(url string, expires time.Time, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	b, err := json.Marshal(orderFile{URL: url, Expires: expires, Key: der})
	if err != nil {
		return err
	}
	return writeFile(k.dir, k.file, b, 0o600, nil)
}

// load reads the kept order; the error is fs.ErrNotExist when there is none.
func (k *keptOrder) load() (orderFile, *ecdsa.PrivateKey, error) {
	var saved orderFile
	f, err := k.dir.OpenFile(k.file, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return saved, nil, err
	}
	defer f.Close()
	b, err := readRegular(f)
	if err != nil {
		return saved, nil, err
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		return saved, nil, err
	}
	if saved.URL == "" {
		return saved, nil, errors.New("no order URL")
	}
	key, err := x509.ParseECPrivateKey(saved.Key)
	return saved, key, err
}

// drop deletes the kept order. Failing to is harmless: the next attempt
// drops an order that can give no certificate, and fetches the one already
// saved again from an order that can.
func (k *keptOrder) drop() {
	k.dir.Remove(k.file)
}

// Forget deletes what Issue saves in dir for a certificate whose first name
// is name: the certificate, and the order kept until it is saved.
func Forget(dir, name string) error {
	n, err := NormalizeName(name)
	if err != nil {
		return err
	}
	var errs []error
	for _, file := range []string{n + ".pem", n + orderSuffix} {
		if err := os.Remove(filepath.Join(dir, file)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// loadAccountKey reads the ACME account key from path, creating a new ECDSA
// P-256 key (mode 0600) when the file does not exist yet.
func loadAccountKey(path string) (*ecdsa.PrivateKey, error) {
	if path == "" {
		return nil, newProblem(nil, CodeConfig, map[string]string{"kind": "no_account_key"})
	}
	dir, base := filepath.Split(filepath.Clean(path))
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, newProblem(err, CodeSaveFailed, map[string]string{"kind": "account_key"})
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, newProblem(err, CodeSaveFailed, map[string]string{"kind": "account_key"})
	}
	defer r.Close()
	st, err := r.Lstat(base)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, newProblem(err, CodeFailed, nil)
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, newProblem(err, CodeFailed, nil)
		}
		data := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
		if err := writeFile(r, base, data, 0o600, nil); err != nil {
			return nil, newProblem(err, CodeSaveFailed, map[string]string{"kind": "account_key"})
		}
		return key, nil
	case err != nil:
		return nil, newProblem(err, CodeAccountKeyDamaged, nil)
	case !st.Mode().IsRegular():
		return nil, newProblem(fmt.Errorf("%s is not a regular file", path), CodeAccountKeyDamaged, nil)
	}
	if st.Mode().Perm()&0o077 != 0 {
		if err := r.Chmod(base, 0o600); err != nil {
			return nil, newProblem(err, CodeSaveFailed, map[string]string{"kind": "account_key"})
		}
	}
	f, err := r.OpenFile(base, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return nil, newProblem(err, CodeAccountKeyDamaged, nil)
	}
	defer f.Close()
	b, err := readRegular(f)
	if err != nil {
		return nil, newProblem(err, CodeAccountKeyDamaged, nil)
	}
	key, err := parseAccountKey(b)
	if err != nil {
		return nil, newProblem(fmt.Errorf("%s: %w", path, err), CodeAccountKeyDamaged, nil)
	}
	return key, nil
}

func parseAccountKey(b []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("no PEM data")
	}
	switch block.Type {
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		if ec, ok := k.(*ecdsa.PrivateKey); ok {
			return ec, nil
		}
		return nil, fmt.Errorf("key is %T, not ECDSA", k)
	}
	return nil, fmt.Errorf("unexpected PEM block %q", block.Type)
}
