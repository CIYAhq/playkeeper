package panel

// Wave 7 (0.4.0): the one wave 7 route that is not plain JSON.

import (
	"io"
	"net/http"
)

// hRecoveryKey passes the recovery key file through as a download. It is
// never cached, and the agent audits who took it without its content.
func (s *Server) hRecoveryKey(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	resp, err := m.agent.Raw(r.Context(), "GET", agentPath("/v1/servers/{id}/offsite/recovery-key", r), nil, nil, map[string]string{"X-Playkeeper-Actor": sess.User.Username}, false)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, io.LimitReader(resp.Body, 1<<20))
		return
	}
	for _, h := range []string{"Content-Type", "Content-Disposition", "Content-Length"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(http.StatusOK)
	io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}
