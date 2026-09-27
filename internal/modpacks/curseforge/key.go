package curseforge

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// BuildKey is the API key a release build carries. On tag pushes the release
// workflow passes the CURSEFORGE_API_KEY repository secret to
// scripts/package.sh, which builds with
//
//	-ldflags "-X github.com/CIYAhq/playkeeper/internal/modpacks/curseforge.BuildKey=$CURSEFORGE_API_KEY"
//
// It is empty in development builds and whenever the secret is missing, and
// nothing changes it at run time.
var BuildKey string

// KeySource says where a key came from.
type KeySource string

const (
	// KeyNone: no key anywhere, so CurseForge is not offered.
	KeyNone KeySource = "none"
	// KeyBuild: the key the release build carries.
	KeyBuild KeySource = "build"
	// KeyFile: the owner's own key from the key file.
	KeyFile KeySource = "file"
	// KeyDisabled: the key file is empty, which turns CurseForge off.
	KeyDisabled KeySource = "disabled"
)

// Key is an API key. It prints as a placeholder, so it cannot end up in a log
// or a message by accident.
type Key struct {
	value  string
	source KeySource
}

// Usable reports whether there is a key to use.
func (k Key) Usable() bool { return k.value != "" }

// Source says where the key came from.
func (k Key) Source() KeySource {
	if k.source == "" {
		return KeyNone
	}
	return k.source
}

// Ending is the key's last four characters, which the owner sees in place of
// the key to tell one key from another.
func (k Key) Ending() string {
	if len(k.value) < 4 {
		return ""
	}
	return k.value[len(k.value)-4:]
}

func (k Key) String() string   { return "CurseForge API key (" + string(k.Source()) + ", redacted)" }
func (k Key) GoString() string { return k.String() }

// NewKey checks and wraps a key the caller already has.
func NewKey(s string, source KeySource) (Key, error) {
	s = strings.TrimSpace(s)
	if !plausibleKey(s) {
		return Key{}, errors.New("that does not look like a CurseForge API key: keys are 20 to 200 visible characters without spaces")
	}
	return Key{value: s, source: source}, nil
}

// maxKeyFile bounds what LoadKey reads.
const maxKeyFile = 4 << 10

// LoadKey picks the key to use. path is the owner's key file: when it
// exists, it wins, and an empty file turns CurseForge off. Otherwise build
// (normally BuildKey) is used; with neither, the key is not usable. The key
// file must be a regular file only its owner can read.
func LoadKey(path, build string) (Key, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if strings.TrimSpace(build) == "" {
			return Key{source: KeyNone}, nil
		}
		k, err := NewKey(build, KeyBuild)
		if err != nil {
			return Key{source: KeyNone}, fmt.Errorf("this build's CurseForge API key is malformed, so CurseForge is not offered: %w", err)
		}
		return k, nil
	}
	if err != nil {
		return Key{}, fmt.Errorf("could not read the CurseForge key file %s: %w", path, err)
	}
	switch {
	case !fi.Mode().IsRegular():
		return Key{}, fmt.Errorf("the CurseForge key file %s is not a regular file", path)
	case fi.Mode().Perm()&0o077 != 0:
		return Key{}, fmt.Errorf("the CurseForge key file %s can be read or changed by other users; make it private with chmod 600", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return Key{}, fmt.Errorf("could not read the CurseForge key file %s: %w", path, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil {
		return Key{}, fmt.Errorf("could not read the CurseForge key file %s: %w", path, err)
	}
	if len(b) > maxKeyFile {
		return Key{}, fmt.Errorf("the CurseForge key file %s is too large to hold a key", path)
	}
	if strings.TrimSpace(string(b)) == "" {
		return Key{source: KeyDisabled}, nil
	}
	k, err := NewKey(string(b), KeyFile)
	if err != nil {
		return Key{}, fmt.Errorf("the CurseForge key file %s: %w", path, err)
	}
	return k, nil
}

func plausibleKey(s string) bool {
	if len(s) < 20 || len(s) > 200 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
