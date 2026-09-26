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

	"github.com/CIYAhq/playkeeper/internal/names"
)

// The names service checks every few hours that a free name's address runs
// a dashboard holding the name's key, by asking port 8443 to sign a fresh
// nonce (see names.AliveHandler). The key is root's, so the panel passes
// the check on to the agent. The check is a route of the public group.

// aliveLimits fit the names service's checks: one every few hours for each
// name, one at a time, each a short answer from the agent.
var aliveLimits = publicLimits{perMinute: 20, open: 2, read: 10 * time.Second, write: 10 * time.Second}

// aliveRoute is the public group's handler for names.AlivePath: the check
// at names.AlivePattern, and 404 for any other method or path under it.
func (s *Server) aliveRoute() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(names.AlivePattern, s.hNamesAlive)
	mux.Handle(names.AlivePath, http.NotFoundHandler())
	return mux
}

// hNamesAlive passes a liveness check on to the agent with the Host header
// it came with, and answers with the agent's answer.
func (s *Server) hNamesAlive(w http.ResponseWriter, r *http.Request) {
	path := "/v1/address/alive/" + url.PathEscape(r.PathValue("nonce"))
	resp, err := s.agent.Raw(r.Context(), http.MethodGet, path, url.Values{"host": {r.Host}}, nil, nil, false)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<10))
}

// aliveHandler serves the liveness check and nothing else.
func (s *Server) aliveHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(names.AlivePath, s.public.handler(names.AlivePath))
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
