package certs

import (
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// challengePath is where HTTP-01 checks fetch their token.
const challengePath = "/.well-known/acme-challenge/"

var reToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// HTTP01Responder answers the HTTP-01 checks of Let's Encrypt, its requests
// for http://<name>/.well-known/acme-challenge/<token>.
type HTTP01Responder struct {
	// Addr is where to listen while a check is pending, normally ":80".
	// Empty means the responder does not listen itself: serve it as an
	// http.Handler on port 80 instead.
	Addr string
	// Shared, when set, is asked when Addr is taken: whether whatever
	// listens there passes the check for token on to this responder and
	// answers keyAuth, as Playkeeper's public page does. Then the check goes
	// ahead without listening.
	Shared func(token, keyAuth string) bool

	mu      sync.Mutex
	tokens  map[string]string
	pending int
	srv     *http.Server
}

// ServeHTTP answers GET and HEAD requests for pending tokens.
func (h *HTTP01Responder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.URL.Path, challengePath)
	if !ok || !reToken.MatchString(token) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	keyAuth, ok := h.KeyAuthorization(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, keyAuth)
}

// KeyAuthorization is the answer to the pending check for token.
func (h *HTTP01Responder) KeyAuthorization(token string) (string, bool) {
	if !reToken.MatchString(token) {
		return "", false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	keyAuth, ok := h.tokens[token]
	return keyAuth, ok
}

// Present answers the check for token with keyAuth until release is
// called. While any token is pending the responder listens on Addr; it
// stops listening when the last one is released. When Addr is taken and
// Shared reports that its holder passes the check on, it goes ahead
// without listening. Errors are *Problems that explain a busy or forbidden
// port.
func (h *HTTP01Responder) Present(token, keyAuth string) (release func(), err error) {
	if !reToken.MatchString(token) {
		return nil, newProblem(errors.New("the certificate authority sent an invalid token"), CodeCAError, nil)
	}
	h.mu.Lock()
	if h.tokens == nil {
		h.tokens = map[string]string{}
	}
	h.tokens[token] = keyAuth
	h.pending++
	var once sync.Once
	release = func() { once.Do(func() { h.release(token) }) }
	if h.Addr == "" || h.srv != nil {
		h.mu.Unlock()
		return release, nil
	}
	ln, err := net.Listen("tcp", h.Addr)
	if err == nil {
		srv := &http.Server{
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       30 * time.Second,
			MaxHeaderBytes:    8 << 10,
			ErrorLog:          log.New(io.Discard, "", 0),
		}
		go srv.Serve(ln)
		h.srv = srv
		h.mu.Unlock()
		return release, nil
	}
	h.mu.Unlock()
	// The holder is asked without the lock, as it passes the check back to
	// this responder.
	if errors.Is(err, syscall.EADDRINUSE) && h.Shared != nil && h.Shared(token, keyAuth) {
		return release, nil
	}
	release()
	return nil, listenProblem(h.Addr, err)
}

func (h *HTTP01Responder) release(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.tokens, token)
	h.pending--
	if h.pending == 0 && h.srv != nil {
		h.srv.Close()
		h.srv = nil
	}
}

func listenProblem(addr string, err error) *Problem {
	params := map[string]string{"addr": addr}
	if _, port, e := net.SplitHostPort(addr); e == nil {
		params["port"] = port
	}
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return newProblem(err, CodePort80Busy, params)
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return newProblem(err, CodePort80Denied, params)
	}
	return newProblem(err, CodePort80Failed, params)
}
