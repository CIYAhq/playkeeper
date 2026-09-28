package service

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/usage"
)

const (
	sourceURL = "https://github.com/CIYAhq/playkeeper"
	// aboutURL says what installs send and how to turn it off.
	aboutURL = sourceURL + "#usage-stats"
)

// Handler is the service's HTTP API.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST "+usage.PathInstall, s.limited(s.install))
	mux.HandleFunc("POST "+usage.PathHeartbeat, s.limited(s.heartbeat))
	mux.HandleFunc("GET /v1/summary", s.limited(s.summary))
	return s.recoverer(mux)
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

func (s *Service) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("Handler panic", "panic", v, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// limited applies the per-address limit; the address goes no further.
func (s *Service) limited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		addr, ok := s.clientAddr(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "The request's client address could not be read.")
			return
		}
		if ok, wait := s.perIP.allow(addrBucket(addr)); !ok {
			rateLimited(w, wait)
			return
		}
		h(w, r)
	}
}

func rateLimited(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(wait.Seconds())), 10))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests; try again later.")
}

func (s *Service) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "playkeeper-stats: anonymous install and usage counts for Playkeeper.\nWhat installs send, and how to turn it off: %s\nSource code (AGPL-3.0): %s\n", aboutURL, sourceURL)
}

func (s *Service) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.db.PingContext(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "database unavailable")
		return
	}
	fmt.Fprintln(w, "ok")
}

// report reads a report into v: JSON of at most usage.MaxBody bytes and
// nothing after it. Fields the report type doesn't have are dropped.
func report(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Reports are JSON.")
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, usage.MaxBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The report could not be read.")
		return false
	}
	if len(body) > usage.MaxBody {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "The report is too large.")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(v); err != nil || dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_request", "The report is not JSON.")
		return false
	}
	return true
}

// accepted answers a stored report, or why it wasn't stored.
func (s *Service) accepted(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, errTooManyNew):
		rateLimited(w, time.Hour)
	default:
		s.logErr("Could not store a report", err)
		writeError(w, http.StatusInternalServerError, "internal", "The report could not be stored.")
	}
}

// checked refuses a report its Check refuses, and one for an install that
// sent too many.
func (s *Service) checked(w http.ResponseWriter, id string, err error) bool {
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(err.Error(), usage.ErrInvalid.Error()+": ")+" is not valid.")
		return false
	}
	if ok, wait := s.perID.allow(id); !ok {
		rateLimited(w, wait)
		return false
	}
	return true
}

func (s *Service) install(w http.ResponseWriter, r *http.Request) {
	var e usage.Install
	if !report(w, r, &e) || !s.checked(w, e.ID, e.Check()) {
		return
	}
	s.accepted(w, s.recordInstall(r.Context(), e))
}

func (s *Service) heartbeat(w http.ResponseWriter, r *http.Request) {
	var h usage.Heartbeat
	if !report(w, r, &h) || !s.checked(w, h.ID, h.Check()) {
		return
	}
	s.accepted(w, s.recordHeartbeat(r.Context(), h))
}

// summary answers the counts, to a request that has the read token.
func (s *Service) summary(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ReadToken == "" {
		writeError(w, http.StatusForbidden, "not_configured", "Reading the counts needs "+EnvReadToken+" on the service.")
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(s.cfg.ReadToken)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="playkeeper-stats"`)
		writeError(w, http.StatusUnauthorized, "unauthorized", "The counts need the read token.")
		return
	}
	sum, err := s.Summary(r.Context())
	if err != nil {
		s.logErr("Could not count", err)
		writeError(w, http.StatusInternalServerError, "internal", "The counts could not be read.")
		return
	}
	writeJSON(w, http.StatusOK, sum)
}
