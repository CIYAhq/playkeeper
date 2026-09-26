package panel

// Wave 7 (0.4.0): the one wave 7 route that is not plain JSON.

import (
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
)

// maxRecoveryKey bounds the recovery key file the dashboard passes on.
const maxRecoveryKey = 1 << 20

// hRecoveryKey passes the recovery key file through as a download, as
// hDownload does a backup: whatever the machine sends, the answer is bytes
// to save, with a name the panel checks and a sandbox, so a joined machine
// can't turn it into a page or a script on the dashboard's origin. It is
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
	if resp.StatusCode >= 400 {
		s.agentFailure(w, agentclient.DecodeError(resp))
		return
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxRecoveryKey+1))
	if resp.StatusCode != http.StatusOK || err != nil || len(b) > maxRecoveryKey {
		s.agentFailure(w, agentclient.ErrBadAnswer)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+recoveryKeyFileName(resp.Header.Get("Content-Disposition"))+`"`)
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(b)
}

var reRecoveryKeyFile = regexp.MustCompile(`^playkeeper-recovery-key-[a-z0-9-]{1,64}\.txt$`)

// recoveryKeyFileName is the name the key downloads as: the agent's, when it
// has that shape, else a plain one.
func recoveryKeyFileName(disposition string) string {
	if _, params, err := mime.ParseMediaType(disposition); err == nil && reRecoveryKeyFile.MatchString(params["filename"]) {
		return params["filename"]
	}
	return "playkeeper-recovery-key.txt"
}
