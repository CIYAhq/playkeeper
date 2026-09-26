package machinelink

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFrames(t *testing.T) {
	var buf bytes.Buffer
	in := hello{V: 1, Mode: modeJoin, Code: "7KQ2-M9XD", Name: "home"}
	if err := writeFrame(&buf, in); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("PRI * HTTP/2.0")
	var out hello
	if err := readFrame(&buf, &out); err != nil || out != in {
		t.Fatalf("readFrame = %+v, %v", out, err)
	}
	if rest := buf.String(); rest != "PRI * HTTP/2.0" {
		t.Fatalf("readFrame consumed what follows the frame: %q left", rest)
	}

	if err := writeFrame(io.Discard, hello{Name: strings.Repeat("x", maxFrame)}); !errors.Is(err, errFrameTooLarge) {
		t.Errorf("writing a huge frame: %v", err)
	}
	for name, tc := range map[string]struct {
		raw  []byte
		want error
	}{
		"huge":      {[]byte{0, 0x10, 0, 0}, errFrameTooLarge},
		"not JSON":  {frame([]byte("nope")), errBadFrame},
		"two":       {frame([]byte(`{"v":1} {"v":2}`)), errBadFrame},
		"truncated": {frame([]byte(`{"v":1}`))[:8], io.ErrUnexpectedEOF},
		"empty":     {nil, io.EOF},
	} {
		if err := readFrame(bytes.NewReader(tc.raw), &out); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
}

func TestWireErrorsAreCleaned(t *testing.T) {
	sent := errJoinRateLimited(90 * time.Second)
	got := sent.wire().err()
	if got.Code != sent.Code || got.Msg != sent.Msg || got.Hint != sent.Hint || got.RetryAfter != sent.RetryAfter || got.Params["wait"] != "1 minute" {
		t.Fatalf("round trip: %+v, sent %+v", got, sent)
	}

	params := map[string]string{}
	for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		params[k] = "v"
	}
	hostile := (&wireError{Code: "rm -rf", Message: "evil\x1b[2J\r\nFAKE LOG LINE" + strings.Repeat("x", 1000), Hint: "\x07", Params: params, RetryAfterMs: 1 << 50}).err()
	if hostile.Code != CodeProtocol {
		t.Errorf("code %q", hostile.Code)
	}
	if strings.ContainsAny(hostile.Msg, "\x1b\r\n") || !strings.HasSuffix(hostile.Msg, "…") || len([]rune(hostile.Msg)) != 401 {
		t.Errorf("message not cleaned: %q", hostile.Msg)
	}
	if hostile.Hint != "" || hostile.Params != nil || hostile.RetryAfter != 24*time.Hour {
		t.Errorf("cleaned to %+v", hostile)
	}
	if e := (&wireError{}).err(); e.Code != CodeProtocol || e.Msg == "" {
		t.Errorf("empty refusal: %+v", e)
	}
}

func TestAllowlistRefusesBadRoutes(t *testing.T) {
	good := Route{Method: "GET", Pattern: "/v1/machine"}
	for _, r := range []Route{
		{Method: "CONNECT", Pattern: "/v1/x"},
		{Method: "get", Pattern: "/v1/x"},
		{Method: "GET", Pattern: "v1/x"},
		{Method: "GET", Pattern: "/_link/ping"},
		{Method: "GET", Pattern: "/v1/x/"},
		{Method: "GET", Pattern: "/v1/{rest...}/x"},
		{Method: "GET", Pattern: "/v1/{a...}{b...}"},
		{Method: "GET", Pattern: "/v1/./x"},
		{Method: "GET", Pattern: "/v1//x"},
		{Method: "GET", Pattern: "/v1/x?y"},
		{Method: "GET", Pattern: "/v1/a b"},
		{Method: "GET", Pattern: "/v1/{"},
		{Method: "GET", Pattern: "/v1/machine"},
	} {
		if _, err := newAllowlist([]Route{good, r}); err == nil {
			t.Errorf("route %s %s was allowed", r.Method, r.Pattern)
		}
	}
	if _, err := newAllowlist(nil); err == nil {
		t.Error("an empty route table was allowed")
	}
}

func TestAllowlistMatch(t *testing.T) {
	a, err := newAllowlist(testRoutes())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, target string
		pattern        string
		stream         bool
	}{
		{"GET", "/v1/machine", "/v1/machine", false},
		{"HEAD", "/v1/machine", "/v1/machine", false},
		{"GET", "/v1/servers/abc/logs?tail=100", "/v1/servers/{id}/logs", true},
		{"DELETE", "/v1/servers/abc/whitelist/Steve", "/v1/servers/{id}/whitelist/{name}", false},
		{"POST", "/v1/machine", "", false},
		{"GET", "/v1/servers/abc/start", "", false},
		{"GET", "/V1/machine", "", false},
		{"GET", "/v1/machine/", "", false},
		{"GET", "/v1/servers/../machine", "", false},
		{"GET", "//v1/machine", "", false},
		{"POST", "/v1/servers/a%2Fb/start", "", false},
		{"GET", "/_link/ping", "", false},
		{"GET", "/v1/secret", "", false},
		{"GET", "/v1/servers/abc/map/tiles/world/1/0_0.png", "/v1/servers/{id}/map/{rest...}", false},
		{"GET", "/v1/servers/abc/map/../start", "", false},
		{"GET", "/v1/servers/abc/map/tiles%2F..%2F..%2Fstart", "", false},
		{"POST", "/v1/servers/abc/map/tiles", "", false},
	} {
		u, err := url.Parse("http://machine" + tc.target)
		if err != nil {
			t.Fatal(err)
		}
		r, ok := a.match(tc.method, u)
		if ok != (tc.pattern != "") || r.Pattern != tc.pattern || r.Stream != tc.stream {
			t.Errorf("match(%s %s) = %+v, %v; want %q", tc.method, tc.target, r, ok, tc.pattern)
		}
	}
}

func TestStripHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Connection", "X-Private, keep-alive")
	for _, k := range []string{"X-Private", "Keep-Alive", "Cookie", "Authorization", "Proxy-Authorization", "Upgrade", "Te", "X-Kept"} {
		h.Set(k, "v")
	}
	stripHeaders(h)
	if len(h) != 1 || h.Get("X-Kept") != "v" {
		t.Fatalf("left %v", h)
	}
}

func TestActors(t *testing.T) {
	for in, want := range map[string]string{
		"alice":                 "alice",
		"  schedule:nightly  ":  "schedule:nightly",
		"Zoë":                   "Zoë",
		strings.Repeat("a", 64): strings.Repeat("a", 64),
		strings.Repeat("a", 65): "",
		"":                      "",
		"bad\x01actor":          "",
		"line\nbreak":           "",
	} {
		got, ok := cleanActor(in)
		if got != want || ok != (want != "") {
			t.Errorf("cleanActor(%q) = %q, %v", in, got, ok)
		}
	}
	for m, want := range map[string]bool{"GET": false, "HEAD": false, "POST": true, "PUT": true, "PATCH": true, "DELETE": true} {
		if mutating(m) != want {
			t.Errorf("mutating(%s) = %v", m, !want)
		}
	}
}
