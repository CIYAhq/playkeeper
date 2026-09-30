package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/update"
)

// upstream stands in for GitHub's latest release: a manifest and a signature,
// each with an ETag, and 304 Not Modified to a request that names it.
type upstream struct {
	mu          sync.Mutex
	files       map[string][]byte
	status      int
	conditional int
	srv         *httptest.Server
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{files: map[string][]byte{}}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.status != 0 {
			w.WriteHeader(u.status)
			return
		}
		b, ok := u.files[path.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		sum := sha256.Sum256(b)
		etag := `"` + hex.EncodeToString(sum[:8]) + `"`
		w.Header().Set("ETag", etag)
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			u.conditional++
			if inm == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.Write(b)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) serve(manifest, sig []byte) {
	u.mu.Lock()
	u.files[update.ManifestFile], u.files[update.SignatureFile], u.status = manifest, sig, 0
	u.mu.Unlock()
}

// release is a manifest for version, released on date, with a tarball for
// this platform, and its signature by priv.
func release(t *testing.T, priv ed25519.PrivateKey, version, date string) (manifest, sig []byte) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	binary := []byte("playkeeper " + version)
	tw.WriteHeader(&tar.Header{Name: update.BinaryPath(version, update.Platform), Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	tw.Write(binary)
	tw.Close()
	gz.Close()
	tarball := filepath.Join(t.TempDir(), update.TarballFile)
	if err := os.WriteFile(tarball, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := update.BuildManifest(version, date, "- What changed in "+version, tarball)
	if err != nil {
		t.Fatal(err)
	}
	return m, update.Sign(priv, m)
}

func (m *mirror) files(t *testing.T) (manifest, sig []byte) {
	t.Helper()
	manifest, _ = os.ReadFile(filepath.Join(m.dir, update.ManifestFile))
	sig, _ = os.ReadFile(filepath.Join(m.dir, update.SignatureFile))
	return manifest, sig
}

// The site serves a release only once its signature verifies, the manifest
// put in place before its signature, dated the release's date; it asks
// GitHub only whether the release changed, leaves files that didn't alone,
// and keeps the release it has when GitHub's doesn't verify or doesn't
// answer.
func TestTheSiteServesOnlyReleasesThatVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	up := newUpstream(t)
	var order []string
	m := &mirror{dir: t.TempDir(), src: update.Source{BaseURL: up.srv.URL, Client: up.srv.Client()}, keys: []ed25519.PublicKey{pub}, renamed: func(n string) { order = append(order, n) }}
	ctx := context.Background()
	man, sig := release(t, priv, "0.4.9", "2026-10-01T09:30:00Z")
	up.serve(man, sig)
	if v, changed, err := m.refresh(ctx); err != nil || !changed || v != "0.4.9" {
		t.Fatalf("the first look: %s %v %v", v, changed, err)
	}
	if gm, gs := m.files(t); !bytes.Equal(gm, man) || !bytes.Equal(gs, sig) {
		t.Fatal("the site's files aren't the release's")
	}
	if !slices.Equal(order, []string{update.ManifestFile, update.SignatureFile}) {
		t.Errorf("the files were put in place in the order %v: the manifest must come first", order)
	}
	date := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	before := map[string]os.FileInfo{}
	for _, name := range []string{update.ManifestFile, update.SignatureFile} {
		st, err := os.Stat(filepath.Join(m.dir, name))
		if err != nil || !st.ModTime().Equal(date) || st.Mode().Perm() != 0o644 {
			t.Fatalf("%s: %v, %v", name, st, err)
		}
		before[name] = st
	}

	if v, changed, err := m.refresh(ctx); err != nil || changed || v != "0.4.9" {
		t.Fatalf("a look at an unchanged release: %s %v %v", v, changed, err)
	}
	if up.conditional != 1 {
		t.Errorf("the second look sent %d conditional requests, want 1: it asks only whether the release changed", up.conditional)
	}
	for name, st := range before {
		if now, err := os.Stat(filepath.Join(m.dir, name)); err != nil || !os.SameFile(st, now) {
			t.Errorf("%s was written again though the release didn't change", name)
		}
	}

	_, other, _ := ed25519.GenerateKey(rand.Reader)
	next, nextSig := release(t, priv, "0.4.10", "2026-10-02T09:30:00Z")
	for _, c := range []struct {
		why    string
		answer func()
		fails  bool
	}{
		{"signed by another key", func() { up.serve(next, update.Sign(other, next)) }, true},
		{"while GitHub doesn't work", func() { up.mu.Lock(); up.status = http.StatusServiceUnavailable; up.mu.Unlock() }, true},
		// The same signature means the same manifest, so the look stops at
		// the signature's 304 and keeps the release it has.
		{"with the older signature", func() { up.serve(next, sig) }, false},
	} {
		c.answer()
		if _, changed, err := m.refresh(ctx); changed || (err != nil) != c.fails {
			t.Errorf("a release %s: changed %v, %v", c.why, changed, err)
		}
		if gm, gs := m.files(t); !bytes.Equal(gm, man) || !bytes.Equal(gs, sig) {
			t.Errorf("a release %s replaced 0.4.9", c.why)
		}
	}

	up.serve(next, nextSig)
	if v, changed, err := m.refresh(ctx); err != nil || !changed || v != "0.4.10" {
		t.Fatalf("the next release: %s %v %v", v, changed, err)
	}
	up.serve(man, sig)
	if v, changed, err := m.refresh(ctx); err != nil || !changed || v != "0.4.9" {
		t.Errorf("a release taken down on GitHub leaves the one before it latest there, and here: %s %v %v", v, changed, err)
	}
}
