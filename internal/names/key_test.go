package names

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadOrCreateKeyCreatesAPrivateFileAndReadsItBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names.key")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "# Playkeeper names key") || !strings.Contains(string(b), "\ned25519-private ") {
		t.Errorf("unexpected key file:\n%s", b)
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !k1.Equal(k2) {
		t.Error("a second load made a new key instead of reading the file")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestConcurrentCreatorsEndUpWithTheSameKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names.key")
	keys := make([]ed25519.PrivateKey, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := LoadOrCreateKey(path)
			if err != nil {
				t.Error(err)
			}
			keys[i] = k
		}()
	}
	wg.Wait()
	for _, k := range keys[1:] {
		if !k.Equal(keys[0]) {
			t.Fatal("two installs of the same key file got different keys")
		}
	}
}

func TestKeyFilesOthersCanReadOrThatAreNotRegularAreRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "names.key")
	if _, err := LoadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("a world-readable key must be refused with the fix, got %v", err)
	}
	link := filepath.Join(dir, "link.key")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(link); err == nil || !strings.Contains(err.Error(), "not a regular key file") {
		t.Errorf("a symlinked key must be refused, got %v", err)
	}
}

func TestParseKeyRejectsMalformedFiles(t *testing.T) {
	for name, data := range map[string]string{
		"empty":        "",
		"comment only": "# nothing\n",
		"wrong kind":   "ed25519 AAAA\n",
		"not base64":   "ed25519-private !!!\n",
		"short seed":   "ed25519-private AAAA\n",
		"two keys":     "ed25519-private " + strings.Repeat("A", 43) + "=\ned25519-private " + strings.Repeat("A", 43) + "=\n",
	} {
		if _, err := ParseKey([]byte(data)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := ParseKey([]byte("\n# c\n  ed25519-private " + strings.Repeat("A", 43) + "=  \n")); err != nil {
		t.Errorf("a valid key with comments and spaces: %v", err)
	}
}
