package certs

import (
	"bytes"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func wantFile(t *testing.T, file, content string, mode os.FileMode) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != content {
		t.Errorf("%s = %q, want %q", file, b, content)
	}
	wantMode(t, file, mode)
}

func wantMode(t *testing.T, file string, mode os.FileMode) {
	t.Helper()
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != mode {
		t.Errorf("%s has mode %v, want %v", file, st.Mode().Perm(), mode)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := writeFile(r, "a.pem", []byte("one"), 0o600, nil); err != nil {
		t.Fatal(err)
	}
	wantFile(t, filepath.Join(dir, "a.pem"), "one", 0o600)
	if err := writeFile(r, "a.pem", []byte("two"), 0o644, nil); err != nil {
		t.Fatal(err)
	}
	wantFile(t, filepath.Join(dir, "a.pem"), "two", 0o644)

	own := &Owner{UID: os.Getuid(), GID: os.Getgid()}
	if err := writeFile(r, "b.pem", []byte("three"), 0o640, own); err != nil {
		t.Fatal(err)
	}
	wantFile(t, filepath.Join(dir, "b.pem"), "three", 0o640)
	st, _ := os.Stat(filepath.Join(dir, "b.pem"))
	if sys := st.Sys().(*syscall.Stat_t); int(sys.Uid) != own.UID || int(sys.Gid) != own.GID {
		t.Errorf("owner %d:%d, want %d:%d", sys.Uid, sys.Gid, own.UID, own.GID)
	}

	if err := os.MkdirAll(filepath.Join(dir, "c.pem", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(r, "c.pem", []byte("four"), 0o600, nil); err == nil {
		t.Error("replacing a directory worked")
	}
	if err := writeFile(r, "../escape.pem", []byte("five"), 0o600, nil); err == nil {
		t.Error("writing outside the directory worked")
	}
	if got, want := dirNames(t, dir), []string{"a.pem", "b.pem", "c.pem"}; !slices.Equal(got, want) {
		t.Errorf("directory has %q, want %q (no temporary files)", got, want)
	}
}

func TestOpenDir(t *testing.T) {
	base := t.TempDir()
	r, err := openDir(filepath.Join(base, "certs"), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	wantMode(t, filepath.Join(base, "certs"), 0o700)

	own := &Owner{UID: os.Getuid(), GID: os.Getgid()}
	shared := filepath.Join(base, "shared")
	if err := os.Mkdir(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	r, err = openDir(shared, own)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(r, "x.pem", []byte("x"), 0o640, own); err != nil {
		t.Fatal(err)
	}
	r.Close()
	wantMode(t, shared, 0o750)
	wantFile(t, filepath.Join(shared, "x.pem"), "x", 0o640)

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "planted")); err != nil {
		t.Fatal(err)
	}
	if r, err := openDir(filepath.Join(base, "planted"), nil); err == nil {
		writeFile(r, "x.pem", []byte("x"), 0o600, nil)
		r.Close()
		t.Error("a symbolic link to another directory was followed")
	}
	if names := dirNames(t, outside); len(names) != 0 {
		t.Errorf("files were written through the link: %q", names)
	}
	if _, err := openDir(filepath.Join(base, "missing", "certs"), nil); err == nil {
		t.Error("openDir created a missing parent")
	}
	for _, d := range []string{"", ".", "/", ".."} {
		if r, err := openDir(d, nil); err == nil {
			r.Close()
			t.Errorf("openDir(%q) worked", d)
		}
	}
}

func TestLoadAccountKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent", "acme-account.key")
	k1, err := loadAccountKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if k1.Curve != elliptic.P256() {
		t.Errorf("curve %s, want P-256", k1.Curve.Params().Name)
	}
	wantMode(t, filepath.Join(dir, "agent"), 0o700)
	wantMode(t, path, 0o600)
	k2, err := loadAccountKey(path)
	if err != nil || !k1.Equal(k2) {
		t.Fatalf("loading the key again = %v; same key: %v", err, err == nil && k1.Equal(k2))
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if k3, err := loadAccountKey(path); err != nil || !k1.Equal(k3) {
		t.Errorf("loading a key readable by others = %v", err)
	}
	wantMode(t, path, 0o600)

	_, err = loadAccountKey("")
	wantProblem(t, err, CodeConfig, "no_account_key")

	der, err := x509.MarshalPKCS8PrivateKey(k1)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := filepath.Join(dir, "pkcs8.key")
	if err := os.WriteFile(pkcs8, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if k, err := loadAccountKey(pkcs8); err != nil || !k1.Equal(k) {
		t.Errorf("loading a PKCS #8 ECDSA key = %v", err)
	}

	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	edDER, _ := x509.MarshalPKCS8PrivateKey(edKey)
	damaged := map[string][]byte{
		"garbage.key": []byte("not a key"),
		"ed25519.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: edDER}),
		"cert.key":    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}}),
		"huge.key":    bytes.Repeat([]byte("A"), maxPEMSize+1),
	}
	for name, data := range damaged {
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := loadAccountKey(file)
		wantProblem(t, err, CodeAccountKeyDamaged, "")
		if b, _ := os.ReadFile(file); !bytes.Equal(b, data) {
			t.Errorf("%s was replaced", name)
		}
	}
	link := filepath.Join(dir, "link.key")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	_, err = loadAccountKey(link)
	wantProblem(t, err, CodeAccountKeyDamaged, "")
	if err := os.Mkdir(filepath.Join(dir, "dir.key"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = loadAccountKey(filepath.Join(dir, "dir.key"))
	wantProblem(t, err, CodeAccountKeyDamaged, "")
}

func TestReadCertificate(t *testing.T) {
	ca := newTestCA(t)
	dir := t.TempDir()
	nb := time.Now().Add(-time.Hour).Truncate(time.Second)
	na := nb.Add(90 * 24 * time.Hour)
	file := filepath.Join(dir, "mc.example.com.pem")
	if err := os.WriteFile(file, ca.bundle(t, []string{"MC.example.com", "www.example.com"}, nb, na), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := ReadCertificate(file)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := parseBundle(b)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Names, []string{"mc.example.com", "www.example.com"}) || c.File != file ||
		!c.NotBefore.Equal(nb) || !c.NotAfter.Equal(na) || !c.RenewAt.Equal(RenewAt(nb, na)) ||
		c.Issuer != "Playkeeper Test T1" || c.Serial != hex.EncodeToString(cert.Leaf.SerialNumber.Bytes()) ||
		c.SHA256 != fingerprint(cert.Leaf.Raw) || len(c.SHA256) != 95 {
		t.Errorf("ReadCertificate = %+v", c)
	}

	link := filepath.Join(dir, "link.pem")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCertificate(link); !errors.Is(err, syscall.ELOOP) {
		t.Errorf("reading through a symbolic link = %v", err)
	}
	huge := filepath.Join(dir, "huge.pem")
	if err := os.WriteFile(huge, bytes.Repeat([]byte("A"), maxPEMSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCertificate(huge); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("reading a huge file = %v", err)
	}
	other := newKey(t)
	mismatched, err := encodeBundle(ca.issue(t, &newKey(t).PublicKey, []string{"mc.example.com"}, nb, na), other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mismatched.pem"), mismatched, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCertificate(filepath.Join(dir, "mismatched.pem")); err == nil {
		t.Error("a certificate with another key was accepted")
	}
	if _, err := ReadCertificate(filepath.Join(dir, "missing.pem")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("reading a missing file = %v", err)
	}
}

func TestCertificateInfoIssuer(t *testing.T) {
	cases := []struct {
		org      []string
		cn, want string
	}{
		{[]string{"Let's Encrypt"}, "R12", "Let's Encrypt R12"},
		{[]string{"Let's Encrypt"}, "E7", "Let's Encrypt E7"},
		{nil, "Pebble Intermediate CA 645fc5", "Pebble Intermediate CA 645fc5"},
		{[]string{"Example"}, "Example CA", "Example CA"},
		{[]string{"Let's Encrypt"}, "", "Let's Encrypt"},
		{nil, "", ""},
	}
	for _, c := range cases {
		leaf := &x509.Certificate{
			Issuer:       pkix.Name{Organization: c.org, CommonName: c.cn},
			SerialNumber: big.NewInt(0x1234),
			DNSNames:     []string{"Mc.Example.com"},
		}
		info := certificateInfo("f.pem", leaf)
		if info.Issuer != c.want || info.Serial != "1234" || !slices.Equal(info.Names, []string{"mc.example.com"}) {
			t.Errorf("certificateInfo(O=%q, CN=%q) = %+v, want issuer %q", c.org, c.cn, info, c.want)
		}
	}
}
