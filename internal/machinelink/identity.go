// Package machinelink connects other machines to one Playkeeper dashboard.
//
// One machine runs the dashboard (the Hub). Another machine joins it once
// with a one-time code, then keeps one connection open to it (a Link): the
// machine dials out, both sides prove who they are with the Ed25519 keys
// they swapped when it joined, and the dashboard sends the machine's agent
// the same allowlisted requests it sends its own agent, as HTTP/2 inside
// that TLS 1.3 connection. Machines need no open port, and they can only
// answer the dashboard, never ask it, or each other, anything.
//
// The package keeps no global state and owns no tables: callers pass in
// keys, a Store, clocks and handlers, and choose where files live.
package machinelink

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// crockford is Crockford's base32 alphabet: digits and capitals without I,
// L, O and U, so codes survive being read out and typed again.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var b32 = base32.NewEncoding(crockford).WithPadding(base32.NoPadding)

// FingerprintLen is the length of a key fingerprint: 128 bits of SHA-256 in
// Crockford base32.
const FingerprintLen = 26

const maxKeyFileBytes = 16 << 10

// Identity is one side's long-term key: the dashboard's or a machine's. The
// other side pins its public key.
type Identity struct {
	key  ed25519.PrivateKey
	cert tls.Certificate
}

// NewIdentity makes a new random identity.
func NewIdentity() (*Identity, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return identityFromKey(key)
}

func identityFromKey(key ed25519.PrivateKey) (*Identity, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	// Nobody checks the names or dates: each side pins the other's key.
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Playkeeper machine link " + Fingerprint(key.Public().(ed25519.PublicKey))},
		NotBefore:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Identity{key: key, cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}}, nil
}

// PublicKey is the key the other side pins.
func (id *Identity) PublicKey() ed25519.PublicKey { return id.key.Public().(ed25519.PublicKey) }

// Fingerprint is the short form of the public key that people compare and
// that the join command carries.
func (id *Identity) Fingerprint() string { return Fingerprint(id.PublicKey()) }

// Fingerprint returns the fingerprint of a public key: the first 128 bits of
// a domain-separated SHA-256 of the key, 26 characters of Crockford base32.
// Finding another key with the same fingerprint takes about 2^128 tries.
func Fingerprint(pub ed25519.PublicKey) string {
	h := sha256.New()
	h.Write([]byte("playkeeper machinelink key v1\x00"))
	h.Write(pub)
	return b32.EncodeToString(h.Sum(nil)[:16])
}

// ParseFingerprint reads a fingerprint as typed or pasted: case, spaces and
// dashes don't matter, and O, I and L count as 0, 1 and 1.
func ParseFingerprint(s string) (string, error) {
	n := normalizeCrockford(s)
	if len(n) != FingerprintLen || !isCrockford(n) {
		return "", &Error{Code: CodeFingerprintInvalid,
			Msg:  fmt.Sprintf("The dashboard fingerprint should be %d letters and digits.", FingerprintLen),
			Hint: "Copy the command again from the dashboard (Settings › Machines)."}
	}
	return n, nil
}

func normalizeCrockford(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch r {
		case ' ', '-', '\t':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isCrockford(s string) bool {
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(crockford, s[i]) < 0 {
			return false
		}
	}
	return true
}

// LoadOrCreateIdentity reads the identity at path, or creates one there
// (mode 0600, in a 0700 directory) when the file does not exist yet.
func LoadOrCreateIdentity(path string) (id *Identity, created bool, err error) {
	id, err = LoadIdentity(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return id, false, err
	}
	if id, err = NewIdentity(); err != nil {
		return nil, false, err
	}
	if err := id.Save(path); err != nil {
		return nil, false, err
	}
	return id, true, nil
}

// LoadIdentity reads a private key written by Save. Like ssh, it refuses a
// key file that other users of the computer can read.
func LoadIdentity(path string) (*Identity, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, keyFileError(path, "is not a regular file", "")
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, keyFileError(path, "can be read by other users of this computer", "sudo chmod 600 "+path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxKeyFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxKeyFileBytes {
		return nil, keyFileError(path, "is too large to be a key", "")
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		return nil, keyFileError(path, "does not contain a private key", "")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, keyFileError(path, "does not contain a readable private key", "")
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, keyFileError(path, "does not contain an Ed25519 key", "")
	}
	return identityFromKey(key)
}

func keyFileError(path, what, fix string) *Error {
	e := &Error{Code: CodeKeyFile, Params: map[string]string{"path": path},
		Msg: "The machine link key " + path + " " + what + ", so Playkeeper won't use it."}
	if fix != "" {
		e.Hint = "Fix: " + fix
	} else {
		e.Hint = "Move it away and connect the machine again with a new command from the dashboard."
	}
	return e
}

// Save writes the private key to path, readable only by its owner. The file
// is replaced atomically, so a crash leaves the old key or the new one.
func (id *Identity) Save(path string) error {
	der, err := x509.MarshalPKCS8PrivateKey(id.key)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
