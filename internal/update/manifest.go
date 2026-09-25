package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Asset names a release carries next to the tarball and get.sh.
const (
	ManifestFile  = "playkeeper-release.json"
	SignatureFile = "playkeeper-release.json.sig"
	Platform      = "linux-amd64"
	TarballFile   = "playkeeper-linux-amd64.tar.gz"
)

// Manifest describes one release. The release workflow signs its exact bytes.
// Fields added by later releases are ignored, so older installs can still
// read newer manifests.
type Manifest struct {
	Schema  int     `json:"schema"`
	Version string  `json:"version"`
	Date    string  `json:"date"`
	Notes   string  `json:"notes"`
	Assets  []Asset `json:"assets"`
}

// Asset is a downloadable build of a release.
type Asset struct {
	Platform string `json:"platform"`
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	// Binary is the path of the playkeeper binary inside the tarball.
	Binary       string `json:"binary"`
	BinarySHA256 string `json:"binarySha256"`
}

// Asset returns the build for this platform.
func (m *Manifest) Asset() (Asset, error) {
	for _, a := range m.Assets {
		if a.Platform == Platform {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("release %s has no %s build", m.Version, Platform)
}

var (
	reSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reBinary = regexp.MustCompile(`^playkeeper-[0-9A-Za-z.+-]+-linux-amd64/playkeeper$`)
)

// maxNotes bounds the release notes an install shows.
const maxNotes = 16 << 10

func (m *Manifest) validate() error {
	if m.Schema != 1 {
		return fmt.Errorf("release manifest schema %d is not supported by this Playkeeper; install the release with the one-line installer instead", m.Schema)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return fmt.Errorf("release manifest: %w", err)
	}
	if len(m.Notes) > maxNotes {
		return errors.New("release manifest: notes are too long")
	}
	a, err := m.Asset()
	if err != nil {
		return err
	}
	switch {
	case a.File != TarballFile:
		return fmt.Errorf("release manifest: unexpected file %q for %s", a.File, Platform)
	case !reSHA256.MatchString(a.SHA256) || !reSHA256.MatchString(a.BinarySHA256):
		return errors.New("release manifest: malformed SHA-256")
	case a.Size <= 0 || a.Size > MaxTarballBytes:
		return fmt.Errorf("release manifest: implausible tarball size %d", a.Size)
	case !reBinary.MatchString(a.Binary) || a.Binary != "playkeeper-"+m.Version+"-linux-amd64/playkeeper":
		return fmt.Errorf("release manifest: unexpected binary path %q", a.Binary)
	}
	return nil
}

// MaxTarballBytes bounds a release download.
const MaxTarballBytes = 200 << 20

// KeyID names a public key in signature files: the first 8 bytes of its
// SHA-256, in hex.
func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return hex.EncodeToString(s[:8])
}

// ParseKeys reads a key file: one "ed25519 <base64 public key>" per line;
// blank lines and lines starting with # are ignored.
func ParseKeys(data []byte) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || f[0] != "ed25519" {
			return nil, fmt.Errorf("key file line %d: want \"ed25519 <base64 key>\"", i+1)
		}
		b, err := base64.StdEncoding.DecodeString(f[1])
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("key file line %d: not a base64 Ed25519 public key", i+1)
		}
		keys = append(keys, ed25519.PublicKey(b))
	}
	return keys, nil
}

//go:embed release.pub
var releaseKeys []byte

// TrustedKeys are the release signing keys compiled into this build. A build
// without any cannot install updates from the dashboard.
func TrustedKeys() []ed25519.PublicKey {
	keys, err := ParseKeys(releaseKeys)
	if err != nil {
		return nil
	}
	return keys
}

// GenerateKey returns a new key pair as a public key file line and a private
// key in the form the release workflow reads.
func GenerateKey() (publicLine, private string, err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return PublicLine(priv), "ed25519-private " + base64.StdEncoding.EncodeToString(priv.Seed()), nil
}

// PublicLine is the key file line for a private key's public half.
func PublicLine(priv ed25519.PrivateKey) string {
	return "ed25519 " + base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
}

// ParsePrivateKey reads a key made by GenerateKey.
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	f := strings.Fields(s)
	if len(f) != 2 || f[0] != "ed25519-private" {
		return nil, errors.New("not a Playkeeper release signing key (want \"ed25519-private <base64 seed>\")")
	}
	seed, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("the release signing key is not a base64 Ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// Sign returns the signature file for a manifest: "ed25519 <key id>
// <base64 signature>".
func Sign(priv ed25519.PrivateKey, manifest []byte) []byte {
	pub := priv.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(priv, manifest)
	return []byte(fmt.Sprintf("ed25519 %s %s\n", KeyID(pub), base64.StdEncoding.EncodeToString(sig)))
}

// ErrNoTrustedKey means this build has no release signing key.
var ErrNoTrustedKey = errors.New("this Playkeeper build has no release signing key, so it cannot check a release's authenticity")

// VerifyManifest checks that manifest was signed by one of keys, then parses
// and validates it. Nothing in an unverified manifest is trusted.
func VerifyManifest(manifest, signature []byte, keys []ed25519.PublicKey) (*Manifest, error) {
	if len(keys) == 0 {
		return nil, ErrNoTrustedKey
	}
	byID := map[string]ed25519.PublicKey{}
	for _, k := range keys {
		byID[KeyID(k)] = k
	}
	verified := false
	for _, line := range strings.Split(string(signature), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "ed25519" {
			continue
		}
		key, ok := byID[f[1]]
		if !ok {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(f[2])
		if err == nil && ed25519.Verify(key, manifest, sig) {
			verified = true
			break
		}
	}
	if !verified {
		return nil, errors.New("the release manifest is not signed by a Playkeeper release key, so it was not trusted")
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(manifest))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("release manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}
