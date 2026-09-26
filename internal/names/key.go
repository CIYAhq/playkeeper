package names

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const keyFileHeader = "# Playkeeper names key: proves this install owns its playkeeper.io address.\n# Keep it secret and keep it with the backups; without it the address cannot be moved or renewed.\n"

// maxKeyFile bounds what LoadOrCreateKey reads.
const maxKeyFile = 4 << 10

// LoadOrCreateKey reads the install's key from path, or creates a new one
// there (mode 0600) if the file does not exist. A key file other users can
// read is refused, since whoever has the key can move the address.
func LoadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	key, err := loadKey(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return key, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".names-key-*")
	if err != nil {
		return nil, fmt.Errorf("could not create the names key: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(keyFileHeader + "ed25519-private " + base64.StdEncoding.EncodeToString(priv.Seed()) + "\n")
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("could not write the names key: %w", err)
	}
	// A hard link never replaces a key another process created meanwhile.
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return loadKey(path)
		}
		return nil, fmt.Errorf("could not save the names key: %w", err)
	}
	return priv, nil
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxKeyFile {
		return nil, fmt.Errorf("the names key %s is not a regular key file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("the names key %s can be read by other users of this machine; run: chmod 600 %s", path, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := ParseKey(b)
	if err != nil {
		return nil, fmt.Errorf("the names key %s: %w", path, err)
	}
	return key, nil
}

// ParseKey reads a key file: one "ed25519-private <base64 seed>" line;
// blank lines and lines starting with # are ignored.
func ParseKey(data []byte) (ed25519.PrivateKey, error) {
	var key ed25519.PrivateKey
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if key != nil || len(f) != 2 || f[0] != "ed25519-private" {
			return nil, errors.New(`want one "ed25519-private <base64 seed>" line`)
		}
		seed, err := base64.StdEncoding.DecodeString(f[1])
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, errors.New("the key is not a base64 Ed25519 seed")
		}
		key = ed25519.NewKeyFromSeed(seed)
	}
	if key == nil {
		return nil, errors.New("the file holds no key")
	}
	return key, nil
}

// KeyID names a public key in logs and the UI: the first 8 bytes of its
// SHA-256, in hex.
func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return hex.EncodeToString(s[:8])
}
