package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stdout returns what run writes to standard output, which goes to a file so
// that a private key never reaches the test log.
func stdout(t *testing.T, run func() error) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved := os.Stdout
	os.Stdout = f
	err = run()
	os.Stdout = saved
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tarball(t *testing.T, dir, version string) string {
	t.Helper()
	path := filepath.Join(dir, "playkeeper-linux-amd64.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho playkeeper\n")
	if err := tw.WriteHeader(&tar.Header{Name: "playkeeper-" + version + "-linux-amd64/playkeeper", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAManifestSignedWithANewKeyVerifiesAgainstItsKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "release.pub")
	t.Setenv("PK_TEST_SIGNING_KEY", strings.TrimSpace(stdout(t, func() error { return keygen([]string{"--public-key-file", keyFile}) })))
	m := stdout(t, func() error {
		return manifest([]string{"--version", "9.9.9", "--tarball", tarball(t, dir, "9.9.9"), "--notes", "A test release.", "--date", "2026-09-25T20:00:00Z"})
	})
	file := filepath.Join(dir, "playkeeper-release.json")
	if err := os.WriteFile(file, []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sign([]string{"--key-env", "PK_TEST_SIGNING_KEY", file}); err != nil {
		t.Fatal(err)
	}
	if out := stdout(t, func() error { return verify([]string{"--public-key-file", keyFile, file}) }); out != file+": release 9.9.9, signed by a key in "+keyFile+"\n" {
		t.Fatalf("verify printed %q", out)
	}
	pub := stdout(t, func() error { return pubkey([]string{"--key-env", "PK_TEST_SIGNING_KEY"}) })
	if keys, _ := os.ReadFile(keyFile); string(keys) != pub {
		t.Fatalf("pubkey printed %q, the key file holds %q", pub, keys)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(m, "A test release.", "A changed release.", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verify([]string{"--public-key-file", keyFile, file}); err == nil {
		t.Fatal("a changed manifest must not verify")
	}
}

func TestSigningNeedsTheKeyInTheEnvironment(t *testing.T) {
	t.Setenv("PK_TEST_SIGNING_KEY", " ")
	for name, run := range map[string]func() error{
		"sign":   func() error { return sign([]string{"--key-env", "PK_TEST_SIGNING_KEY", "playkeeper-release.json"}) },
		"pubkey": func() error { return pubkey([]string{"--key-env", "PK_TEST_SIGNING_KEY"}) },
	} {
		if err := run(); err == nil || err.Error() != "PK_TEST_SIGNING_KEY is empty; it must hold the release signing key (see CONTRIBUTING.md)" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAManifestTakesItsNotesFromItsVersionsChangelogSection(t *testing.T) {
	dir := t.TempDir()
	changelog := filepath.Join(dir, "CHANGELOG.md")
	if err := os.WriteFile(changelog, []byte("# Changelog\n\n## 9.9.9\n\n- Fixed a thing.\n\n## 9.9.8\n\n- Older.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tb := tarball(t, dir, "9.9.9")
	m := stdout(t, func() error {
		return manifest([]string{"--version", "9.9.9", "--tarball", tb, "--changelog", changelog})
	})
	if !strings.Contains(m, "Fixed a thing.") || strings.Contains(m, "Older.") {
		t.Fatalf("the notes must be 9.9.9's section only: %s", m)
	}
	if err := manifest([]string{"--version", "9.9.10", "--tarball", tb, "--changelog", changelog}); err == nil || !strings.Contains(err.Error(), `has no "## 9.9.10" section`) {
		t.Fatalf("a version without a section: %v", err)
	}
}
