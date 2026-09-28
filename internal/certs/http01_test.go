package certs

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHTTP01Handler(t *testing.T) {
	h := &HTTP01Responder{}
	release, err := h.Present("tok_EN-1", "tok_EN-1.thumb")
	if err != nil {
		t.Fatal(err)
	}
	if h.srv != nil {
		t.Error("the responder listens although Addr is empty")
	}
	cases := []struct {
		method, path string
		code         int
		body         string
	}{
		{"GET", "/.well-known/acme-challenge/tok_EN-1", 200, "tok_EN-1.thumb"},
		{"HEAD", "/.well-known/acme-challenge/tok_EN-1", 200, ""},
		{"POST", "/.well-known/acme-challenge/tok_EN-1", 405, ""},
		{"GET", "/.well-known/acme-challenge/other", 404, ""},
		{"GET", "/.well-known/acme-challenge/tok.EN", 404, ""},
		{"GET", "/.well-known/acme-challenge/tok_EN-1/x", 404, ""},
		{"GET", "/.well-known/acme-challenge/", 404, ""},
		{"GET", "/", 404, ""},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.code {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, w.Code, c.code)
		}
		if c.body != "" && w.Body.String() != c.body {
			t.Errorf("%s %s body = %q", c.method, c.path, w.Body)
		}
		if c.code == 200 && w.Header().Get("Content-Type") != "text/plain" {
			t.Errorf("%s %s Content-Type = %q", c.method, c.path, w.Header().Get("Content-Type"))
		}
		if c.code == 405 && w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s %s Allow = %q", c.method, c.path, w.Header().Get("Allow"))
		}
	}
	release()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/acme-challenge/tok_EN-1", nil))
	if w.Code != 404 {
		t.Errorf("a released token is still answered: %d", w.Code)
	}
}

func TestHTTP01ListensWhilePending(t *testing.T) {
	addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
	h := &HTTP01Responder{Addr: addr}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	get := func(token string) (string, error) {
		resp, err := client.Get("http://" + addr + challengePath + token)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return strconv.Itoa(resp.StatusCode), nil
		}
		return string(b), nil
	}
	if _, err := get("a"); err == nil {
		t.Fatal("the responder listens before a check is pending")
	}
	releaseA, err := h.Present("a", "a.key")
	if err != nil {
		t.Fatal(err)
	}
	releaseB, err := h.Present("b", "b.key")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := get("a"); got != "a.key" {
		t.Errorf("GET a = %q, %v", got, err)
	}
	releaseA()
	releaseA()
	if got, err := get("b"); got != "b.key" {
		t.Errorf("GET b after releasing a = %q, %v", got, err)
	}
	if got, err := get("a"); got != "404" {
		t.Errorf("GET a after releasing it = %q, %v", got, err)
	}
	releaseB()
	if _, err := get("b"); err == nil {
		t.Error("the responder still listens after the last check")
	}
	releaseC, err := h.Present("c", "c.key")
	if err != nil {
		t.Fatalf("listening again for the next check: %v", err)
	}
	if got, err := get("c"); got != "c.key" {
		t.Errorf("GET c = %q, %v", got, err)
	}
	releaseC()
}

func TestHTTP01PortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	h := &HTTP01Responder{Addr: ln.Addr().String()}
	_, err = h.Present("a", "a.key")
	p := wantProblem(t, err, CodePort80Busy, "")
	if p.Params["port"] != port || !strings.Contains(p.Message, "port "+port) || p.Hint == "" {
		t.Errorf("problem = %+v", p.Note)
	}
	if h.pending != 0 || len(h.tokens) != 0 || h.srv != nil {
		t.Errorf("a failed Present left state behind: %d pending", h.pending)
	}
}

func TestHTTP01ListenProblems(t *testing.T) {
	listenErr := func(call string, errno syscall.Errno) error {
		return &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError(call, errno)}
	}
	cases := []struct {
		err  error
		code string
	}{
		{listenErr("bind", syscall.EADDRINUSE), CodePort80Busy},
		{listenErr("bind", syscall.EACCES), CodePort80Denied},
		{listenErr("bind", syscall.EPERM), CodePort80Denied},
		{listenErr("socket", syscall.EAFNOSUPPORT), CodePort80Failed},
	}
	for _, c := range cases {
		p := listenProblem(":80", c.err)
		if p.Code != c.code || p.Params["port"] != "80" || !strings.Contains(p.Message, "port 80") {
			t.Errorf("listenProblem(%v) = %s %v %q", c.err, p.Code, p.Params, p.Message)
		}
	}
	h := &HTTP01Responder{}
	for _, token := range []string{"", "a/b", "../x", "a.b", strings.Repeat("a", 257)} {
		if _, err := h.Present(token, "k"); err == nil {
			t.Errorf("Present(%q) accepted", token)
		} else {
			wantProblem(t, err, CodeCAError, "")
		}
	}
}

// When port 80 is taken by something that passes checks on to the
// responder (Playkeeper's public page), a check goes ahead without the
// responder listening; when its holder doesn't, it fails as a busy port.
func TestHTTP01GoesAheadOnlyWhenThePortsHolderPassesChecksOn(t *testing.T) {
	h := &HTTP01Responder{}
	holder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.URL.Path, challengePath)
		if keyAuth, ok := h.KeyAuthorization(token); ok {
			io.WriteString(w, keyAuth)
			return
		}
		http.NotFound(w, r)
	}))
	defer holder.Close()
	addr := ":" + strings.TrimPrefix(holder.URL[strings.LastIndex(holder.URL, ":"):], ":")
	relays := func(token, keyAuth string) bool {
		resp, err := http.Get(holder.URL + challengePath + token)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode == 200 && string(b) == keyAuth
	}

	h.Addr = addr
	if _, err := h.Present("tok1", "tok1.key"); err == nil {
		t.Fatal("a check went ahead on a busy port nobody passes on")
	} else if p, ok := err.(*Problem); !ok || p.Code != CodePort80Busy {
		t.Fatalf("a busy port: %v", err)
	}
	if _, ok := h.KeyAuthorization("tok1"); ok {
		t.Fatal("a refused check stays pending")
	}

	h.Shared = func(string, string) bool { return false }
	if _, err := h.Present("tok2", "tok2.key"); err == nil {
		t.Fatal("a check went ahead although the port's holder doesn't pass it on")
	}

	h.Shared = relays
	release, err := h.Present("tok3", "tok3.key")
	if err != nil {
		t.Fatalf("the holder passes checks on, yet: %v", err)
	}
	if h.srv != nil {
		t.Fatal("the responder listens beside the port's holder")
	}
	if !relays("tok3", "tok3.key") {
		t.Fatal("the check isn't answered through the holder")
	}
	release()
	if _, ok := h.KeyAuthorization("tok3"); ok {
		t.Fatal("a released check is still answered")
	}
}
