package panel

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
)

// Each server's file browser. The agent of the machine that runs the server
// checks every path and keeps everything inside the server's folder; the
// panel checks who may look and change, streams the bytes through, and names
// downloads itself.

// maxSaveBytes is the most the editor saves, as the agent's maxEditBytes.
const maxSaveBytes = 2 << 20

// maxDownloadPaths caps the files one download names.
const maxDownloadPaths = 1000

var reUploadID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func init() {
	pathKeys = append(pathKeys, "up")
}

// uploadRoutes send an upload's files, piece by piece. A folder of small
// files is many requests, so they count against a larger bucket of their
// own, apart from the 30 actions a minute.
var uploadRoutes = map[string]bool{
	"POST /api/servers/{id}/files/uploads/{up}/files":    true,
	"PUT /api/servers/{id}/files/uploads/{up}/files/{n}": true,
}

func (s *Server) fileRoutes() []Route {
	view := func(method, p, agentPath string) Route {
		return Route{method, p, needSession, actViewFiles, s.serverProxy(method, agentPath)}
	}
	edit := func(method, p, agentPath string) Route {
		return Route{method, p, needSessionCSRF, actEditFiles, s.serverProxy(method, agentPath)}
	}
	return []Route{
		view("GET", "/api/servers/{id}/files", "/v1/servers/{id}/files"),
		view("GET", "/api/servers/{id}/files/content", "/v1/servers/{id}/files/content"),
		{"GET", "/api/servers/{id}/files/download", needSession, actViewFiles, s.hFileDownload},
		{"PUT", "/api/servers/{id}/files/content", needSessionCSRF, actEditFiles, s.hFileSave},
		edit("POST", "/api/servers/{id}/files/folder", "/v1/servers/{id}/files/folder"),
		edit("POST", "/api/servers/{id}/files/move", "/v1/servers/{id}/files/move"),
		edit("POST", "/api/servers/{id}/files/delete", "/v1/servers/{id}/files/delete"),
		edit("POST", "/api/servers/{id}/files/uploads", "/v1/servers/{id}/files/uploads"),
		{"GET", "/api/servers/{id}/files/uploads/{up}", needSession, actEditFiles, s.serverProxy("GET", "/v1/servers/{id}/files/uploads/{up}")},
		edit("DELETE", "/api/servers/{id}/files/uploads/{up}", "/v1/servers/{id}/files/uploads/{up}"),
		edit("POST", "/api/servers/{id}/files/uploads/{up}/files", "/v1/servers/{id}/files/uploads/{up}/files"),
		{"PUT", "/api/servers/{id}/files/uploads/{up}/files/{n}", needSessionCSRF, actEditFiles, s.hFileUploadPiece},
	}
}

// hFileSave relays a file the editor saves, its text as the body, to the
// agent, which checks it and the version it was opened with.
func (s *Server) hFileSave(w http.ResponseWriter, r *http.Request, sess *session) {
	if r.ContentLength > maxSaveBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "The file would be larger than 2 MB, the most the editor saves.", "Make it smaller, or upload it instead.")
		return
	}
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	q := url.Values{"path": {r.URL.Query().Get("path")}}
	if v := r.URL.Query().Get("expect"); v != "" {
		q.Set("expect", v)
	}
	body := http.MaxBytesReader(w, r.Body, maxSaveBytes+1)
	resp, err := m.agent.Raw(r.Context(), "PUT", agentPath("/v1/servers/{id}/files/content", r), q, body,
		map[string]string{"X-Playkeeper-Actor": sess.User.Username, "Content-Type": "text/plain; charset=utf-8"}, true)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	defer resp.Body.Close()
	relayJSON(w, resp, 1<<20)
}

// hFileUploadPiece streams part of an announced file to the agent, from the
// byte it asked for; after a dropped connection the browser asks where the
// upload stands and sends the rest.
func (s *Server) hFileUploadPiece(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	off := r.URL.Query().Get("offset")
	n, err := strconv.ParseInt(off, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != off || !reUploadID.MatchString(r.PathValue("up")) || !reUploadFile.MatchString(r.PathValue("n")) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Say which file and from which byte.", "")
		return
	}
	resp, err := m.agent.Raw(r.Context(), "PUT", agentPath("/v1/servers/{id}/files/uploads/{up}/files/{n}", r), url.Values{"offset": {off}}, r.Body,
		map[string]string{"X-Playkeeper-Actor": sess.User.Username, "Content-Type": "application/octet-stream"}, true)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	defer resp.Body.Close()
	relayJSON(w, resp, 1<<20)
}

var reUploadFile = regexp.MustCompile(`^[0-9]{1,4}$`)

// hFileDownload streams a file, or a zip of folders and files, as a download
// the panel describes itself: the machine chooses the bytes, but not their
// type or name, so nothing it sends can render on the panel's origin.
func (s *Server) hFileDownload(w http.ResponseWriter, r *http.Request, sess *session) {
	paths := r.URL.Query()["path"]
	if len(paths) == 0 || len(paths) > maxDownloadPaths {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Say which files to download.", "")
		return
	}
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	resp, err := m.agent.Raw(r.Context(), "GET", agentPath("/v1/servers/{id}/files/download", r), url.Values{"path": paths}, nil,
		map[string]string{"X-Playkeeper-Actor": sess.User.Username}, true)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		s.agentFailure(w, agentclient.DecodeError(resp))
		return
	}
	if resp.StatusCode != http.StatusOK {
		s.agentFailure(w, agentclient.ErrBadAnswer)
		return
	}
	zipped := resp.Header.Get("Content-Type") == "application/zip"
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", attachment(downloadName(paths, zipped)))
	h.Set("Content-Security-Policy", "sandbox")
	if !zipped && resp.ContentLength >= 0 {
		h.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, resp.Body); err != nil {
		// The download can't finish, so the browser should say it failed
		// rather than keep a file that looks whole.
		panic(http.ErrAbortHandler)
	}
}

// downloadName is what a download is saved as: the file's name, a folder's
// name with .zip, or, for the server's folder or several files, their
// folder's name with .zip.
func downloadName(paths []string, zipped bool) string {
	name := path.Base(paths[0])
	if len(paths) > 1 {
		name = path.Base(path.Dir(paths[0]))
	}
	if name == "." || name == "/" || name == "" {
		name = "server-files"
	}
	if zipped {
		name += ".zip"
	}
	return name
}

// attachment is a Content-Disposition for name with anything that could
// break the header out of it: control characters, quotes and backslashes.
func attachment(name string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, name)
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": clean}); v != "" {
		return v
	}
	return `attachment; filename="download"`
}
