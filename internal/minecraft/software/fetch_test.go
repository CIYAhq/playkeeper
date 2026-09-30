package software

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpstreamErrors(t *testing.T) {
	const evil = "https://evil.example.com/manifest.json"
	status := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.Error(w, http.StatusText(code), code) }
	}
	body := func(s string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(s)) }
	}
	redirect := func(to string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) }
	}
	tests := []struct {
		name    string
		handler http.HandlerFunc
		kind    Kind
		msg     string
	}{
		{"not found", status(404), KindNotFound, "Mojang does not have its version manifest."},
		{"rate limited", status(429), KindRateLimited, "Mojang is limiting requests from this host, so Playkeeper could not load its version manifest (HTTP 429)."},
		{"server error", status(503), KindUpstreamStatus, "Mojang answered HTTP 503 when Playkeeper asked for its version manifest."},
		{"forbidden", status(403), KindUpstreamStatus, "Mojang answered HTTP 403 when Playkeeper asked for its version manifest."},
		{"not JSON", body("<html>Service Unavailable</html>"), KindMalformed, "Mojang sent its version manifest in a form Playkeeper could not read ("},
		{"no versions", body(`{"latest":{},"versions":[]}`), KindMalformed, "Mojang sent its version manifest in a form Playkeeper could not read (it lists no versions)."},
		{"declared too large", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(maxMetadata+1))
			w.WriteHeader(http.StatusOK)
		}, KindTooLarge, "Mojang sent more than 8 MiB for its version manifest, far more than expected, so Playkeeper stopped reading."},
		{"streamed too large", func(w http.ResponseWriter, r *http.Request) {
			chunk := bytes.Repeat([]byte(" "), 1<<20)
			for range maxMetadata>>20 + 1 {
				if _, err := w.Write(chunk); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}, KindTooLarge, "Mojang sent more than 8 MiB for its version manifest"},
		{"redirect to another host", redirect(evil), KindHostNotAllowed,
			"Mojang pointed Playkeeper to https://evil.example.com for its version manifest. Playkeeper only downloads from piston-meta.mojang.com, piston-data.mojang.com over HTTPS, so it refused."},
		{"redirect to plain HTTP", redirect("http://piston-meta.mojang.com/mc/game/version_manifest_v2.json"), KindHostNotAllowed,
			"Mojang pointed Playkeeper to http://piston-meta.mojang.com for its version manifest."},
		{"redirect loop", redirect(mojangManifestURL), KindUpstreamStatus, "Mojang redirected Playkeeper too many times for its version manifest."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeNet(t)
			f.handle(mojangManifestURL, tt.handler)
			f.handle(evil, func(w http.ResponseWriter, r *http.Request) { t.Error("followed a redirect to another host") })
			_, err := f.sources().Catalog(context.Background(), Vanilla)
			e := wantKind(t, err, tt.kind)
			if !strings.Contains(e.Msg, tt.msg) {
				t.Errorf("got %q, want it to contain %q", e.Msg, tt.msg)
			}
			if e.Params["upstream"] != "Mojang" || e.Params["what"] != "its version manifest" {
				t.Errorf("got params %v", e.Params)
			}
		})
	}
}

func TestUpstreamFollowsRedirectsOnItsHosts(t *testing.T) {
	f := newFakeNet(t)
	moved := "https://piston-data.mojang.com/moved/version_manifest_v2.json"
	f.handle(mojangManifestURL, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, moved, http.StatusMovedPermanently) })
	f.serve(moved, readFixture(t, "mojang/version_manifest_v2.json"))
	hc := f.client()
	man, err := mojangManifest(context.Background(), hc)
	if err != nil {
		t.Fatal(err)
	}
	if man["26.2"].Type != "release" || man["26.4-snapshot-1"].Type != "snapshot" {
		t.Errorf("got %d versions, 26.2 %+v", len(man), man["26.2"])
	}
	if hc.CheckRedirect != nil {
		t.Error("the caller's HTTP client was changed")
	}
}

// NeoForge's Maven sometimes answers 404 for files it has, or a 5xx; other
// hosts' 404s count the first time, and so does any other answer.
func TestUpstreamAsksNeoForgeAgainAfterA404OrA5xx(t *testing.T) {
	defer func(w time.Duration) { flakyWait = w }(flakyWait)
	flakyWait = time.Millisecond
	f := newFakeNet(t)
	metadata := neoforgeMaven + "/maven-metadata.xml"
	misses := []int{http.StatusNotFound, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusNotFound, http.StatusGatewayTimeout}
	if len(misses) != flakyRetries {
		t.Fatalf("the test misses %d times, and NeoForge is asked again %d times", len(misses), flakyRetries)
	}
	f.handle(metadata, func(w http.ResponseWriter, r *http.Request) {
		if len(misses) > 0 {
			w.WriteHeader(misses[0])
			misses = misses[1:]
			return
		}
		_, _ = w.Write([]byte(`<metadata><versioning><versions><version>26.2.0.88</version></versions></versioning></metadata>`))
	})
	got, err := neoforgeVersions(context.Background(), f.client())
	if err != nil {
		t.Fatal(err)
	}
	if len(got["26.2"]) != 1 || f.hitCount(metadata) != flakyRetries+1 {
		t.Errorf("got %v after %d requests", got, f.hitCount(metadata))
	}

	for _, c := range []struct {
		status int
		kind   Kind
		asked  int
	}{
		{http.StatusNotFound, KindNotFound, flakyRetries + 1},
		{http.StatusServiceUnavailable, KindUpstreamStatus, flakyRetries + 1},
		{http.StatusForbidden, KindUpstreamStatus, 1},
	} {
		f.status(metadata, c.status)
		before := f.hitCount(metadata)
		_, err = neoforgeVersions(context.Background(), f.client())
		wantKind(t, err, c.kind)
		if n := f.hitCount(metadata) - before; n != c.asked {
			t.Errorf("HTTP %d: asked %d times, want %d", c.status, n, c.asked)
		}
	}

	f.status(mojangManifestURL, http.StatusNotFound)
	_, err = mojangManifest(context.Background(), f.client())
	wantKind(t, err, KindNotFound)
	if n := f.hitCount(mojangManifestURL); n != 1 {
		t.Errorf("asked Mojang %d times, want once", n)
	}
}

// NeoForge's Maven sometimes doesn't answer in time. It's asked again as
// after a 404; another host's timeout counts the first time, and so does the
// caller's own deadline.
func TestUpstreamAsksNeoForgeAgainWhenItDoesNotAnswerInTime(t *testing.T) {
	defer func(w time.Duration) { flakyWait = w }(flakyWait)
	flakyWait = time.Millisecond
	f := newFakeNet(t)
	metadata := neoforgeMaven + "/maven-metadata.xml"
	var slow atomic.Int32
	f.handle(metadata, func(w http.ResponseWriter, r *http.Request) {
		if slow.Add(-1) >= 0 {
			noAnswer(w, r)
			return
		}
		_, _ = w.Write([]byte(`<metadata><versioning><versions><version>26.2.0.88</version></versions></versioning></metadata>`))
	})
	hc := f.client()
	hc.Timeout = 100 * time.Millisecond

	slow.Store(flakyRetries)
	got, err := neoforgeVersions(context.Background(), hc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["26.2"]) != 1 || f.hitCount(metadata) != flakyRetries+1 {
		t.Errorf("got %v after %d requests", got, f.hitCount(metadata))
	}

	slow.Store(flakyRetries + 1)
	before := f.hitCount(metadata)
	_, err = neoforgeVersions(context.Background(), hc)
	wantKind(t, err, KindUnreachable)
	if n := f.hitCount(metadata) - before; n != flakyRetries+1 {
		t.Errorf("no answer in time: asked %d times, want %d", n, flakyRetries+1)
	}

	slow.Store(1)
	before = f.hitCount(metadata)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err = neoforgeVersions(ctx, f.client()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want context.DeadlineExceeded", err)
	}
	if n := f.hitCount(metadata) - before; n != 1 {
		t.Errorf("the caller's deadline: asked %d times, want once", n)
	}

	f.handle(mojangManifestURL, noAnswer)
	_, err = mojangManifest(context.Background(), hc)
	wantKind(t, err, KindUnreachable)
	if n := f.hitCount(mojangManifestURL); n != 1 {
		t.Errorf("asked Mojang %d times, want once", n)
	}
}

func TestUpstreamUnreachable(t *testing.T) {
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}}}
	_, err := Sources{Client: hc}.Catalog(context.Background(), Vanilla)
	e := wantKind(t, err, KindUnreachable)
	if !strings.HasPrefix(e.Msg, "Playkeeper could not reach Mojang to load its version manifest (") || !strings.Contains(e.Hint, "piston-meta.mojang.com") {
		t.Errorf("got %q, hint %q", e.Msg, e.Hint)
	}
}

func TestUpstreamStopsWhenCanceled(t *testing.T) {
	f := newFakeNet(t)
	serveMojang(t, f, mojangFiles(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.sources().Catalog(ctx, Vanilla); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestChecksumFiles(t *testing.T) {
	const file = "https://maven.fabricmc.net/net/fabricmc/fabric-loader/0.19.5/fabric-loader-0.19.5.jar"
	sum := hexSum(SHA512, []byte("loader"))
	tests := []struct {
		name, body string
		kind       Kind
	}{
		{"digest only", sum, ""},
		{"uppercase with a newline", strings.ToUpper(sum) + "\n", ""},
		{"digest and file name", sum + "  fabric-loader-0.19.5.jar\n", ""},
		{"empty", "", KindMalformed},
		{"only spaces", "  \n", KindMalformed},
		{"too short", sum[:127], KindMalformed},
		{"not hex", strings.Repeat("zz", 64), KindMalformed},
		{"an HTML page", "<html><body>Not Found</body></html>", KindMalformed},
		{"too long", strings.Repeat("a", 2000), KindTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeNet(t)
			f.serve(file+".sha512", []byte(tt.body))
			u := upstream{name: "Fabric", hosts: []string{"maven.fabricmc.net"}, hc: f.client()}
			h, err := u.checksum(context.Background(), file, SHA512, "fabric-loader-0.19.5.jar")
			if tt.kind != "" {
				wantKind(t, err, tt.kind)
				return
			}
			if err != nil || h != (Hash{Algorithm: SHA512, Value: sum}) {
				t.Errorf("got %+v, %v", h, err)
			}
		})
	}
}
