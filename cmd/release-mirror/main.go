// Command release-mirror keeps playkeeper.io's copy of the latest release's
// signed manifest and its signature: the two small files installed
// Playkeepers (0.4.9 and later) check for a new release, at update.CheckURL.
// It asks GitHub's latest release every minute whether they changed, and
// writes them into the site's web root only once their signature verifies
// against the release keys compiled into it (internal/update/release.pub),
// so the site never serves a manifest without its signature. They're dated
// the release's date, so their ETag and Last-Modified stay the same across
// restarts of the site and a check keeps getting 304 Not Modified. The
// site's container runs it (site/release-mirror.sh); it is not part of the
// Playkeeper release.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/CIYAhq/playkeeper/internal/update"
)

func main() {
	dir := flag.String("dir", "", "the folder to keep the manifest and its signature in (required)")
	from := flag.String("from", update.DefaultReleaseURL, "the latest release's download address")
	every := flag.Duration("every", time.Minute, "how often to ask whether the latest release changed, give or take a tenth")
	timeout := flag.Duration("timeout", 20*time.Second, "how long one look may take")
	once := flag.Bool("once", false, "look once and exit, with 1 if the folder has no copy of the latest release")
	flag.Parse()
	if *dir == "" || flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	log.SetFlags(0)
	log.SetPrefix("release-mirror: ")
	m := &mirror{dir: *dir, src: update.Source{BaseURL: *from, Client: &http.Client{Timeout: *timeout}}, keys: update.TrustedKeys()}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		log.Fatal(err)
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		version, changed, err := m.refresh(ctx)
		cancel()
		switch {
		case err != nil:
			log.Print(err)
		case changed:
			log.Printf("serving Playkeeper %s", version)
		}
		if *once {
			if _, ok := m.served(); !ok {
				os.Exit(1)
			}
			return
		}
		time.Sleep(time.Duration(float64(*every) * (0.9 + 0.2*rand.Float64())))
	}
}

// mirror keeps the latest release's manifest and signature in dir.
type mirror struct {
	dir  string
	src  update.Source
	keys []ed25519.PublicKey
	// cached is what src served last, with its validators.
	cached *update.Cached
	// renamed is told each file as it's put in place (tests).
	renamed func(name string)
}

// held is the release a folder holds.
type held struct {
	manifest, signature []byte
	version             string
}

// served is the release dir holds, if its files are there and verify.
func (m *mirror) served() (held, bool) {
	raw, err1 := os.ReadFile(filepath.Join(m.dir, update.ManifestFile))
	sig, err2 := os.ReadFile(filepath.Join(m.dir, update.SignatureFile))
	if err1 != nil || err2 != nil {
		return held{}, false
	}
	man, err := update.VerifyManifest(raw, sig, m.keys)
	if err != nil {
		return held{}, false
	}
	return held{raw, sig, man.Version}, true
}

// refresh asks src whether the latest release changed since it last did, and
// puts a release that verifies and isn't the one dir holds in its place:
// the manifest first, so a check that sees the new signature finds the new
// manifest too. It says the version dir holds now and whether it changed.
func (m *mirror) refresh(ctx context.Context) (string, bool, error) {
	rel, cached, err := m.src.LatestSince(ctx, m.keys, m.cached)
	if err != nil {
		return "", false, fmt.Errorf("the latest release: %w", err)
	}
	m.cached = cached
	was, had := m.served()
	if had && bytes.Equal(was.manifest, rel.Raw) && bytes.Equal(was.signature, rel.Signature) {
		return was.version, false, nil
	}
	date, err := time.Parse(time.RFC3339, rel.Manifest.Date)
	if err != nil {
		date = time.Now()
	}
	tmpManifest, err := stage(m.dir, update.ManifestFile, rel.Raw, date)
	if err != nil {
		return "", false, err
	}
	defer os.Remove(tmpManifest)
	tmpSig, err := stage(m.dir, update.SignatureFile, rel.Signature, date)
	if err != nil {
		return "", false, err
	}
	defer os.Remove(tmpSig)
	if err := m.rename(tmpManifest, update.ManifestFile); err != nil {
		return "", false, err
	}
	if err := m.rename(tmpSig, update.SignatureFile); err != nil {
		if had {
			if back, serr := stage(m.dir, update.ManifestFile, was.manifest, date); serr == nil {
				_ = os.Rename(back, filepath.Join(m.dir, update.ManifestFile))
			}
		}
		return "", false, err
	}
	return rel.Manifest.Version, true, nil
}

func (m *mirror) rename(tmp, name string) error {
	if err := os.Rename(tmp, filepath.Join(m.dir, name)); err != nil {
		return err
	}
	if m.renamed != nil {
		m.renamed(name)
	}
	return nil
}

// stage writes b to a new file beside name in dir, readable by nginx's
// workers and dated date, and returns its path.
func stage(dir, name string, b []byte, date time.Time) (string, error) {
	f, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return "", err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err == nil {
		err = os.Chtimes(f.Name(), date, date)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
