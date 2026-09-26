package curseforge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadKey(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(p, mode)
		return p
	}
	missing := filepath.Join(dir, "missing")
	own := "own-key-0123456789-abcdefghij$xyz"
	link := filepath.Join(dir, "link")
	os.Symlink(write("target", own, 0o600), link)
	cases := []struct {
		name, path, build string
		source            KeySource
		value             string
		err               string
	}{
		{"nothing", missing, "", KeyNone, "", ""},
		{"build key", missing, testKey, KeyBuild, testKey, ""},
		{"mangled build key", missing, "$10$", KeyNone, "", "malformed"},
		{"own key wins", write("own", own+"\n", 0o600), testKey, KeyFile, own, ""},
		{"empty file turns it off", write("off", "\n", 0o600), testKey, KeyDisabled, "", ""},
		{"readable by others", write("open", own, 0o644), testKey, "", "", "chmod 600"},
		{"symlink", link, "", "", "", "not a regular file"},
		{"not a key", write("junk", "my key has spaces in it, sadly", 0o600), "", "", "", "does not look like"},
		{"too large", write("big", strings.Repeat("a", 5000), 0o600), "", "", "", "too large"},
	}
	for _, c := range cases {
		k, err := LoadKey(c.path, c.build)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.err)
			}
			if err != nil {
				for _, secret := range []string{own, "sadly", "aaaaaaaa", c.build} {
					if secret != "" && strings.Contains(err.Error(), secret) {
						t.Errorf("%s: the key leaked into %q", c.name, err)
					}
				}
			}
			if k.Usable() {
				t.Errorf("%s: usable key despite the error", c.name)
			}
			continue
		}
		if err != nil || k.Source() != c.source || k.value != c.value || k.Usable() != (c.value != "") {
			t.Errorf("%s: %v (%q, usable %v), %v", c.name, k.Source(), k.value, k.Usable(), err)
		}
	}
}

func TestKeyNeverPrints(t *testing.T) {
	k := mustKey(t)
	holder := struct{ K Key }{k}
	b, _ := json.Marshal(holder)
	for _, s := range []string{
		fmt.Sprint(k), fmt.Sprintf("%v %+v %#v %s %q", k, k, k, k, k),
		fmt.Sprintf("%v %+v %#v", holder, holder, holder), string(b),
	} {
		if strings.Contains(s, "test-key") {
			t.Errorf("the key shows in %q", s)
		}
	}
	if !strings.Contains(k.String(), "redacted") {
		t.Errorf("String() = %q", k.String())
	}
}

func TestZeroKeyIsNotUsable(t *testing.T) {
	var k Key
	if k.Usable() || k.Source() != KeyNone {
		t.Errorf("zero key: usable %v, source %v", k.Usable(), k.Source())
	}
	if _, err := NewKey("short", KeyFile); err == nil {
		t.Error("a short key was accepted")
	}
}
