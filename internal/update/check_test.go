package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"
)

// location is a release location that answers the way nginx serves static
// files: each file with a Last-Modified and, unless it keeps to
// Last-Modified alone like Python's http.server, an ETag, and 304 Not
// Modified to a request that names them. With neither, it sends every file
// in full. It counts each file's requests and the 304s among them.
type location struct {
	mu          sync.Mutex
	validators  string
	files       map[string][]byte
	modified    time.Time
	requests    map[string]int
	notModified map[string]int
	// status answers every request instead, with header.
	status int
	header http.Header
	srv    *httptest.Server
}

func newLocation(t *testing.T, validators string) *location {
	t.Helper()
	l := &location{validators: validators, files: map[string][]byte{}, requests: map[string]int{}, notModified: map[string]int{}}
	l.srv = httptest.NewServer(http.HandlerFunc(l.serve))
	t.Cleanup(l.srv.Close)
	return l
}

func (l *location) publish(r *testRelease, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files[ManifestFile], l.files[SignatureFile], l.modified = r.manifest, r.sig, at.UTC().Truncate(time.Second)
}

func (l *location) serve(w http.ResponseWriter, req *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	name := path.Base(req.URL.Path)
	l.requests[name]++
	if l.status != 0 {
		for k, v := range l.header {
			w.Header()[k] = v
		}
		w.WriteHeader(l.status)
		return
	}
	b, ok := l.files[name]
	if !ok {
		http.NotFound(w, req)
		return
	}
	etag := fmt.Sprintf(`"%x-%x"`, l.modified.Unix(), len(b))
	if l.validators != "none" {
		w.Header().Set("Last-Modified", l.modified.Format(http.TimeFormat))
	}
	if l.validators == "etag" {
		w.Header().Set("ETag", etag)
	}
	unchanged := false
	switch inm := req.Header.Get("If-None-Match"); {
	case l.validators == "etag" && inm != "":
		unchanged = inm == etag
	case l.validators != "none" && req.Header.Get("If-Modified-Since") != "":
		since, err := http.ParseTime(req.Header.Get("If-Modified-Since"))
		unchanged = err == nil && !l.modified.After(since)
	}
	if unchanged {
		l.notModified[name]++
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(b)
}

func (l *location) counts(name string) (requests, notModified int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.requests[name], l.notModified[name]
}

func (l *location) source() Source { return Source{BaseURL: l.srv.URL, Client: l.srv.Client()} }

// newerRelease is r's next release, signed with r's key.
func newerRelease(t *testing.T, r *testRelease, version string) *testRelease {
	t.Helper()
	n := newTestRelease(t, version)
	n.priv, n.keys, n.sig = r.priv, r.keys, Sign(r.priv, n.manifest)
	return n
}

// A check asks only whether the release changed: a release that didn't costs
// one 304 for its signature, and its manifest isn't asked for again until a
// new signature comes with a new release.
func TestAReleaseThatDidNotChangeCostsOneNotModified(t *testing.T) {
	for _, validators := range []string{"etag", "last-modified"} {
		t.Run(validators, func(t *testing.T) {
			r := newTestRelease(t, "0.2.0")
			l := newLocation(t, validators)
			l.publish(r, time.Now().Add(-time.Hour))
			ctx := context.Background()
			rel, first, err := l.source().LatestSince(ctx, r.keys, nil)
			if err != nil || rel.Manifest.Version != "0.2.0" || !bytes.Equal(rel.Raw, r.manifest) || !bytes.Equal(rel.Signature, r.sig) {
				t.Fatalf("the first check: %+v %v", rel, err)
			}
			if first.SignatureSeen.LastModified == "" || (validators == "etag") != (first.SignatureSeen.ETag != "") {
				t.Fatalf("the first check keeps the signature's validators: %+v", first.SignatureSeen)
			}
			again, second, err := l.source().LatestSince(ctx, r.keys, first)
			if err != nil || again.Manifest.Version != "0.2.0" || !bytes.Equal(second.Manifest, r.manifest) || !bytes.Equal(second.Signature, r.sig) {
				t.Fatalf("the check of an unchanged release: %+v %v", again, err)
			}
			if n, not := l.counts(SignatureFile); n != 2 || not != 1 {
				t.Errorf("the signature was asked for %d times with %d 304s, want 2 with 1: a check of an unchanged release asks only whether it changed", n, not)
			}
			if n, _ := l.counts(ManifestFile); n != 1 {
				t.Errorf("the manifest was fetched %d times, want once: the same signature means the same manifest", n)
			}

			next := newerRelease(t, r, "0.2.1")
			l.publish(next, time.Now())
			rel, _, err = l.source().LatestSince(ctx, r.keys, second)
			if err != nil || rel.Manifest.Version != "0.2.1" || !bytes.Equal(rel.Raw, next.manifest) {
				t.Fatalf("a new release is found: %+v %v", rel, err)
			}
			if n, _ := l.counts(ManifestFile); n != 2 {
				t.Errorf("the new release's manifest was fetched %d times in all, want 2", n)
			}
		})
	}
}

// A location that says nothing to tell its files apart sends them in full,
// and the same signature again still spares the manifest.
func TestTheSameSignatureAgainSparesTheManifest(t *testing.T) {
	r := newTestRelease(t, "0.2.0")
	l := newLocation(t, "none")
	l.publish(r, time.Now())
	ctx := context.Background()
	_, first, err := l.source().LatestSince(ctx, r.keys, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, _, err := l.source().LatestSince(ctx, r.keys, first); err != nil || rel.Manifest.Version != "0.2.0" {
		t.Fatalf("%+v %v", rel, err)
	}
	if n, _ := l.counts(ManifestFile); n != 1 {
		t.Errorf("the manifest was fetched %d times, want once", n)
	}
}

// What a 304 keeps is verified again, as the files it stands for were: with
// this build's keys, never trusted because it was once.
func TestWhatANotModifiedKeepsIsVerifiedAgain(t *testing.T) {
	r := newTestRelease(t, "0.2.0")
	l := newLocation(t, "etag")
	l.publish(r, time.Now())
	ctx := context.Background()
	_, first, err := l.source().LatestSince(ctx, r.keys, nil)
	if err != nil {
		t.Fatal(err)
	}
	tampered := *first
	tampered.Manifest = bytes.Replace(first.Manifest, []byte(`"0.2.0"`), []byte(`"9.9.9"`), 1)
	if _, _, err := l.source().LatestSince(ctx, r.keys, &tampered); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Errorf("a kept manifest that no longer matches its signature: %v", err)
	}
	other := newTestRelease(t, "0.2.0")
	if _, _, err := l.source().LatestSince(ctx, other.keys, first); err == nil {
		t.Error("a kept release signed with a key this build doesn't trust was accepted")
	}
	if _, not := l.counts(SignatureFile); not != 2 {
		t.Fatalf("both checks were to be answered 304, got %d", not)
	}
}

// A location that is busy says how long to leave it alone, and the check
// keeps what it said.
func TestALocationsRetryAfterIsKept(t *testing.T) {
	r := newTestRelease(t, "0.2.0")
	l := newLocation(t, "etag")
	l.publish(r, time.Now())
	ctx := context.Background()
	for _, c := range []struct {
		status int
		after  string
		want   time.Duration
	}{
		{http.StatusServiceUnavailable, "120", 2 * time.Minute},
		{http.StatusTooManyRequests, time.Now().Add(time.Hour).UTC().Format(http.TimeFormat), time.Hour},
		{http.StatusInternalServerError, "120", 0},
		{http.StatusServiceUnavailable, "", 0},
	} {
		l.mu.Lock()
		l.status, l.header = c.status, http.Header{}
		if c.after != "" {
			l.header.Set("Retry-After", c.after)
		}
		l.mu.Unlock()
		_, _, err := l.source().LatestSince(ctx, r.keys, nil)
		var se *StatusError
		if !errors.As(err, &se) || se.Code != c.status || !strings.Contains(err.Error(), fmt.Sprintf("answered HTTP %d", c.status)) {
			t.Fatalf("HTTP %d: %v", c.status, err)
		}
		if d := se.RetryAfter - c.want; d < -2*time.Second || d > 0 {
			t.Errorf("HTTP %d with Retry-After %q: waits %v, want %v", c.status, c.after, se.RetryAfter, c.want)
		}
	}
}
