package panel

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Worlds people upload, for a new server or to replace a server's world.
// The agent checks every name, size and byte; the panel streams the bytes
// through and gives the slow steps as long as they take.

const maxImportAnswer = 4 << 20

var reImportFile = regexp.MustCompile(`^[0-9]{1,2}$`)

func init() {
	pathKeys = append(pathKeys, "imp", "n")
}

// hWorldUpload streams part of an announced archive to the agent, from the
// byte it asked for; after a dropped connection the browser asks where the
// upload stands and sends the rest.
func (s *Server) hWorldUpload(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	off := r.URL.Query().Get("offset")
	n, err := strconv.ParseInt(off, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != off || !reImportFile.MatchString(r.PathValue("n")) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Say which file and from which byte.", "")
		return
	}
	resp, err := m.agent.Raw(r.Context(), "PUT", agentPath("/v1/world-imports/{imp}/files/{n}", r), url.Values{"offset": {off}}, r.Body,
		map[string]string{"X-Playkeeper-Actor": sess.User.Username, "Content-Type": "application/octet-stream"}, true)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	defer resp.Body.Close()
	relayJSON(w, resp, 1<<20)
}

// forwardLong is forward for a POST that can take longer than the agent
// client's minute: checking a large world reads all of it.
func (s *Server) forwardLong(pattern string) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		m, ok := s.target(w, r)
		if !ok {
			return
		}
		body := map[string]any{}
		b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request body.", "")
			return
		}
		if len(bytes.TrimSpace(b)) > 0 {
			if err := json.Unmarshal(b, &body); err != nil {
				writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Request body must be a JSON object.", "")
				return
			}
		}
		body["actor"] = sess.User.Username
		out, err := json.Marshal(body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request body.", "")
			return
		}
		resp, err := m.agent.Raw(r.Context(), "POST", agentPath(pattern, r), nil, bytes.NewReader(out), map[string]string{"Content-Type": "application/json"}, true)
		if err != nil {
			s.agentFailure(w, err)
			return
		}
		defer resp.Body.Close()
		relayJSON(w, resp, maxImportAnswer)
	}
}

// relayJSON passes an agent's JSON answer on, up to limit bytes.
func relayJSON(w http.ResponseWriter, resp *http.Response, limit int64) {
	if resp.StatusCode == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit || !json.Valid(b) {
		writeErr(w, http.StatusBadGateway, api.CodeInternal, "The agent's answer could not be read.", "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	w.Write(b)
}
