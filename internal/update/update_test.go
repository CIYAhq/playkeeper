package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionOrdering(t *testing.T) {
	ordered := []string{"0.1.0", "0.2.0-ci.1", "0.2.0-ci.2", "0.2.0-ci.10", "0.2.0-rc.1", "0.2.0", "v0.2.1", "0.10.0", "1.0.0"}
	for i := 0; i+1 < len(ordered); i++ {
		if c, err := CompareVersions(ordered[i], ordered[i+1]); err != nil || c != -1 {
			t.Errorf("%s must be older than %s (got %d, %v)", ordered[i], ordered[i+1], c, err)
		}
	}
	if c, _ := CompareVersions("0.2.0+abc", "0.2.0"); c != 0 {
		t.Error("build metadata must not affect the order")
	}
	for _, bad := range []string{"dev", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "1.2.3-a..b", ""} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("%q must not parse as a release version", bad)
		}
	}
}

type testRelease struct {
	priv     ed25519.PrivateKey
	keys     []ed25519.PublicKey
	tarball  string
	manifest []byte
	sig      []byte
	binary   []byte
}

// newTestRelease builds a real release tarball, its manifest and signature.
func newTestRelease(t *testing.T, version string) *testRelease {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	r := &testRelease{priv: priv, keys: []ed25519.PublicKey{pub}, binary: []byte("#!/bin/sh\necho playkeeper " + version + "\n")}
	r.tarball = filepath.Join(dir, TarballFile)
	writeTarball(t, r.tarball, map[string][]byte{
		"playkeeper-" + version + "-linux-amd64/install.sh": []byte("#!/bin/sh\n"),
		"playkeeper-" + version + "-linux-amd64/playkeeper": r.binary,
	})
	m, err := BuildManifest(version, "2026-09-25T12:00:00Z", "- Updates from the dashboard", r.tarball)
	if err != nil {
		t.Fatal(err)
	}
	r.manifest, r.sig = m, Sign(priv, m)
	return r
}

func writeTarball(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg})
		tw.Write(data)
	}
	tw.Close()
	gz.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyManifestsSignedByATrustedKeyAreAccepted(t *testing.T) {
	r := newTestRelease(t, "0.2.0")
	m, err := VerifyManifest(r.manifest, r.sig, r.keys)
	if err != nil || m.Version != "0.2.0" || m.Notes != "- Updates from the dashboard" {
		t.Fatalf("a correctly signed manifest must verify: %+v %v", m, err)
	}
	tampered := bytes.Replace(r.manifest, []byte(`"0.2.0"`), []byte(`"9.9.9"`), 1)
	if _, err := VerifyManifest(tampered, r.sig, r.keys); err == nil {
		t.Fatal("a changed manifest must be refused")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := VerifyManifest(r.manifest, r.sig, []ed25519.PublicKey{other}); err == nil {
		t.Fatal("a manifest signed by another key must be refused")
	}
	if _, err := VerifyManifest(r.manifest, r.sig, nil); err != ErrNoTrustedKey {
		t.Fatalf("a build without keys must refuse every manifest, got %v", err)
	}
	if _, err := VerifyManifest(r.manifest, []byte("ed25519 0000 AAAA\ngarbage\n"), r.keys); err == nil {
		t.Fatal("a signature file without a valid signature must be refused")
	}
}

func TestSignedButMalformedManifestsAreRefused(t *testing.T) {
	r := newTestRelease(t, "0.2.0")
	for name, edit := range map[string][2]string{
		"future schema":      {`"schema": 1`, `"schema": 2`},
		"binary elsewhere":   {`linux-amd64/playkeeper"`, `linux-amd64/../../bin/sh"`},
		"other platform":     {`"linux-amd64"`, `"linux-arm64"`},
		"bad checksum":       {`"sha256": "`, `"sha256": "zz`},
		"not a version":      {`"version": "0.2.0"`, `"version": "latest"`},
		"another file":       {`"file": "playkeeper-linux-amd64.tar.gz"`, `"file": "evil.tar.gz"`},
		"version mismatched": {`"version": "0.2.0"`, `"version": "0.2.1"`},
	} {
		changed := bytes.Replace(r.manifest, []byte(edit[0]), []byte(edit[1]), 1)
		if bytes.Equal(changed, r.manifest) {
			t.Fatalf("%s: edit did not apply", name)
		}
		if _, err := VerifyManifest(changed, Sign(r.priv, changed), r.keys); err == nil {
			t.Errorf("%s: a signed but malformed manifest must be refused", name)
		}
	}
	extra := bytes.Replace(r.manifest, []byte(`"schema": 1,`), []byte(`"schema": 1, "newField": true,`), 1)
	if _, err := VerifyManifest(extra, Sign(r.priv, extra), r.keys); err != nil {
		t.Fatalf("fields added by later releases must be ignored: %v", err)
	}
}

func TestKeysRoundTrip(t *testing.T) {
	pubLine, private, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ParsePrivateKey(private)
	if err != nil || PublicLine(priv) != pubLine {
		t.Fatalf("the private key must match its public line: %v", err)
	}
	keys, err := ParseKeys([]byte("# comment\n\n" + pubLine + "\n"))
	if err != nil || len(keys) != 1 {
		t.Fatalf("key file: %v %v", keys, err)
	}
	for _, bad := range []string{"ed25519 not-base64", "rsa AAAA", "ed25519 AAAA"} {
		if _, err := ParseKeys([]byte(bad)); err == nil {
			t.Errorf("%q must not parse as a key", bad)
		}
	}
	if _, err := ParsePrivateKey(pubLine); err == nil {
		t.Error("a public key must not parse as a private key")
	}
	if keys, err := ParseKeys(releaseKeys); err != nil {
		t.Errorf("the committed key file must parse: %v (%d keys)", err, len(keys))
	}
}

func TestDownloadsAreCheckedAgainstTheSignedManifest(t *testing.T) {
	r := newTestRelease(t, "0.2.0")
	good, _ := os.ReadFile(r.tarball)
	serve := func(body []byte) Source {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			switch filepath.Base(req.URL.Path) {
			case ManifestFile:
				w.Write(r.manifest)
			case SignatureFile:
				w.Write(r.sig)
			case TarballFile:
				w.Write(body)
			default:
				http.NotFound(w, req)
			}
		}))
		t.Cleanup(srv.Close)
		return Source{BaseURL: srv.URL, Client: srv.Client()}
	}
	ctx := context.Background()
	rel, err := serve(good).Latest(ctx, r.keys)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := rel.Manifest.Asset()
	dst := filepath.Join(t.TempDir(), "dl.tar.gz")
	if err := serve(good).Download(ctx, a, dst); err != nil {
		t.Fatalf("the signed tarball must download: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "playkeeper")
	if err := ExtractBinary(dst, a, bin); err != nil {
		t.Fatalf("the signed binary must extract: %v", err)
	}
	if b, _ := os.ReadFile(bin); !bytes.Equal(b, r.binary) {
		t.Fatal("the extracted binary differs")
	}
	flipped := append([]byte(nil), good...)
	flipped[len(flipped)/2] ^= 0xff
	if err := serve(flipped).Download(ctx, a, dst); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("a changed tarball must be refused: %v", err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("a refused download must be deleted")
	}
	long := append(append([]byte(nil), good...), make([]byte, 1<<20)...)
	want := fmt.Sprintf("is %d bytes, the signed manifest says %d", a.Size+1, a.Size)
	if err := serve(long).Download(ctx, a, dst); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("a longer tarball must be refused for its size, reading at most one byte more than signed: %v", err)
	}
	if _, err := serve(good).Latest(ctx, []ed25519.PublicKey{}); err != ErrNoTrustedKey {
		t.Fatalf("without keys nothing is fetched: %v", err)
	}
}

func TestExtractBinaryReadsOnlyTheSignedBinary(t *testing.T) {
	r := newTestRelease(t, "0.2.0")
	m, _ := VerifyManifest(r.manifest, r.sig, r.keys)
	a, _ := m.Asset()
	evil := filepath.Join(t.TempDir(), "evil.tar.gz")
	writeTarball(t, evil, map[string][]byte{a.Binary: []byte("#!/bin/sh\nrm -rf /\n")})
	dst := filepath.Join(t.TempDir(), "playkeeper")
	if err := ExtractBinary(evil, a, dst); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("a binary that does not match the manifest must be refused: %v", err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("nothing may be left behind")
	}
	empty := filepath.Join(t.TempDir(), "empty.tar.gz")
	writeTarball(t, empty, map[string][]byte{"other/file": []byte("x")})
	if err := ExtractBinary(empty, a, dst); err == nil {
		t.Fatal("a tarball without the binary must be refused")
	}
}

func TestReleaseLocationsMustBeHTTPSUnlessLocal(t *testing.T) {
	for _, ok := range []string{DefaultReleaseURL, "http://127.0.0.1:8765", "http://localhost:8765/", "http://[::1]:9/x"} {
		if _, err := CheckReleaseURL(ok); err != nil {
			t.Errorf("%s must be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://example.com/releases", "ftp://example.com", "file:///tmp", "not a url", "http://10.0.0.5"} {
		if _, err := CheckReleaseURL(bad); err == nil {
			t.Errorf("%s must be refused", bad)
		}
	}
}

func TestChangelogSection(t *testing.T) {
	cl := []byte("# Changelog\n\n## 0.2.0 (2026-10-01)\n\n- Updates from the dashboard\n- Live versions\n\n## 0.1.0\n\n- First release\n")
	if s, ok := ChangelogSection(cl, "0.2.0"); !ok || s != "- Updates from the dashboard\n- Live versions" {
		t.Fatalf("got %q %v", s, ok)
	}
	if s, ok := ChangelogSection(cl, "v0.1.0"); !ok || s != "- First release" {
		t.Fatalf("got %q %v", s, ok)
	}
	if _, ok := ChangelogSection(cl, "0.3.0"); ok {
		t.Fatal("a version without a section must not get notes")
	}
}
