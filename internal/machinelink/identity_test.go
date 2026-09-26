package machinelink

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFingerprintIsStable(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	id, err := identityFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	// Join commands carry fingerprints: changing the format breaks every
	// command already handed out.
	if got, want := id.Fingerprint(), "Z287KN4CDZD0Z8A4XXJA514NKG"; got != want {
		t.Fatalf("fingerprint %s, want %s", got, want)
	}
	if other := mustIdentity(t).Fingerprint(); other == id.Fingerprint() || len(other) != FingerprintLen {
		t.Fatalf("another key's fingerprint is %q", other)
	}
}

func TestParseFingerprint(t *testing.T) {
	const fp = "Z287KN4CDZD0Z8A4XXJA514NKG"
	for _, in := range []string{fp, strings.ToLower(fp), "z287-kn4c-dzd0-z8a4-xxja-514n-kg", " Z287KN4CDZDOZ8A4XXJA5I4NKG ", "Z287KN4CDZD0Z8A4XXJA5l4NKG"} {
		got, err := ParseFingerprint(in)
		if err != nil || got != fp {
			t.Errorf("ParseFingerprint(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", fp[:25], fp + "0", "U287KN4CDZD0Z8A4XXJA514NKG", "Z287KN4CDZD0Z8A4XXJA514NK!"} {
		_, err := ParseFingerprint(in)
		if e := wantCode(t, err, CodeFingerprintInvalid); e.Hint == "" {
			t.Errorf("ParseFingerprint(%q): no hint", in)
		}
	}
}

func TestIdentityFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "link")
	path := filepath.Join(dir, "machine.key")
	id, created, err := LoadOrCreateIdentity(path)
	if err != nil || !created {
		t.Fatalf("LoadOrCreateIdentity = %v, %v", created, err)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, dir: 0o700} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s has mode %v, want %v", p, st.Mode().Perm(), want)
		}
	}
	again, created, err := LoadOrCreateIdentity(path)
	if err != nil || created {
		t.Fatalf("second LoadOrCreateIdentity = %v, %v", created, err)
	}
	if !again.PublicKey().Equal(id.PublicKey()) {
		t.Fatal("the key changed when loaded again")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("the directory holds %d files; a temporary file was left behind", len(entries))
	}
}

func TestIdentityFileRefusals(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := filepath.Join(dir, "good.key")
	if err := mustIdentity(t).Save(good); err != nil {
		t.Fatal(err)
	}
	pemData, _ := os.ReadFile(good)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKCS8PrivateKey(ec)

	loose := write("loose.key", pemData, 0o644)
	_, err := LoadIdentity(loose)
	if e := wantCode(t, err, CodeKeyFile); e.Hint != "Fix: sudo chmod 600 "+loose || !strings.Contains(e.Msg, "other users") {
		t.Errorf("loose key file: %q / %q", e.Msg, e.Hint)
	}
	for name, p := range map[string]string{
		"garbage": write("garbage.key", []byte("not a key"), 0o600),
		"ecdsa":   write("ecdsa.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}), 0o600),
		"huge":    write("huge.key", bytes.Repeat([]byte("A"), maxKeyFileBytes+1), 0o600),
		"dir":     filepath.Join(dir, "sub"),
	} {
		if name == "dir" {
			os.Mkdir(p, 0o700)
		}
		_, err := LoadIdentity(p)
		if e := wantCode(t, err, CodeKeyFile); strings.Contains(e.Error(), "-----BEGIN") || e.Hint == "" || e.Params["path"] != p {
			t.Errorf("%s: %q / %q / %v", name, e.Msg, e.Hint, e.Params)
		}
	}
	if _, err := LoadIdentity(filepath.Join(dir, "missing.key")); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}
}

func TestDashboardFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dashboard.json")
	d := Dashboard{Address: "panel.example.com", Key: mustIdentity(t).PublicKey(), MachineID: "abcdefghij", Name: "home-server",
		JoinedAt: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDashboard(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != d.Address || !got.Key.Equal(d.Key) || got.MachineID != d.MachineID || got.Name != d.Name || !got.JoinedAt.Equal(d.JoinedAt) {
		t.Fatalf("loaded %+v, saved %+v", got, d)
	}
	if got.Fingerprint() != Fingerprint(d.Key) {
		t.Fatal("fingerprint differs")
	}

	// Anyone may read it, but only its owner may change it.
	os.Chmod(path, 0o644)
	if _, err := LoadDashboard(path); err != nil {
		t.Fatalf("a readable file: %v", err)
	}
	os.Chmod(path, 0o664)
	_, err = LoadDashboard(path)
	if e := wantCode(t, err, CodeKeyFile); e.Hint != "Fix: sudo chmod 600 "+path {
		t.Errorf("group-writable file: hint %q", e.Hint)
	}

	os.WriteFile(path, []byte(`{"address":"panel.example.com"}`), 0o600)
	os.Chmod(path, 0o600)
	_, err = LoadDashboard(path)
	if e := wantCode(t, err, CodeKeyFile); !strings.Contains(e.Msg, "damaged") {
		t.Errorf("damaged file: %q", e.Msg)
	}

	if err := (Dashboard{Address: "panel.example.com"}).Save(filepath.Join(dir, "other.json")); err == nil {
		t.Error("saved a dashboard without a key")
	}
}
