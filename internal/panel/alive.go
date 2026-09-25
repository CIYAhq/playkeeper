package panel

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/names"
)

// The names service checks every few hours that a free name's address runs
// a dashboard holding the name's key, by asking port 8443 to sign a fresh
// nonce (see names.AliveHandler). The key is root's, so the panel passes
// the check on to the agent.

// hNamesAlive passes a liveness check on to the agent with the Host header
// it came with, and answers with the agent's answer.
func (s *Server) hNamesAlive(w http.ResponseWriter, r *http.Request, _ *session) {
	if ok, wait := s.alive.allow(limitKey(clientIP(r))); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, api.CodeRateLimited, "Too many liveness checks from this address.", "")
		return
	}
	path := "/v1/address/alive/" + url.PathEscape(r.PathValue("nonce"))
	resp, err := s.agent.Raw(r.Context(), http.MethodGet, path, url.Values{"host": {r.Host}}, nil, nil, false)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, api.CodeAgentUnavailable, "The Playkeeper agent is not running.", "")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<10))
}

// aliveHandler serves the liveness check and nothing else.
func (s *Server) aliveHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(names.AlivePattern, func(w http.ResponseWriter, r *http.Request) { s.hNamesAlive(w, r, nil) })
	return s.securityHeaders(s.logRequests(mux))
}

// serveAlive answers liveness checks on port 8443 until ctx ends when the
// panel listens on another port, since the names service always asks 8443.
// Development servers skip it: several can run on one machine.
func (s *Server) serveAlive(ctx context.Context, tc *tls.Config) {
	if s.cfg.PanelPort == names.AlivePort || s.cfg.Dev {
		return
	}
	srv := &http.Server{
		Addr:              net.JoinHostPort(s.cfg.PanelBind, strconv.Itoa(names.AlivePort)),
		Handler:           s.aliveHandler(),
		TLSConfig:         tc,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	go func() {
		if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("the free address service cannot check this dashboard, because port 8443 is not available", "err", err)
		}
	}()
}
