package software

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
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
