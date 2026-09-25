package panel

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/webmap"
)

// Each server's live map. The agent is the only hop to squaremap in the
// server's container; the panel forwards the Map tab's calls for signed-in
// accounts, and serves the shared map at /map/<link token> to anyone while
// its switch is on.

const (
	// maxMapBytes bounds a tile or a JSON answer about the map; squaremap's
	// tiles are at most about 1 MB.
	maxMapBytes = 2 << 20
	// Requests a viewer's address may make to shared maps per minute: a
	// screen of tiles, panning and zooming, and players every few seconds.
	publicMapRequests = 600
)

var (
	reMapWorld = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]{0,99}$`)
	reMapZoom  = regexp.MustCompile(`^[0-9]{1,2}$`)
	reMapTile  = regexp.MustCompile(`^-?[0-9]{1,7}_-?[0-9]{1,7}\.png$`)
)

func init() {
	pathKeys = append(pathKeys, "world", "zoom", "tile")
}

// mapProxy forwards the Map tab's worlds, players and tiles. A tile's
// validators pass both ways, so the browser revalidates a tile it has
// instead of downloading it again.
func (s *Server) mapProxy(pattern string) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, _ *session) {
		m, ok := s.target(w, r)
		if !ok {
			return
		}
		cond := map[string]string{}
		for _, k := range []string{"If-None-Match", "If-Modified-Since"} {
			if v := r.Header.Get(k); v != "" && len(v) <= 200 {
				cond[k] = v
			}
		}
		resp, err := m.agent.Raw(r.Context(), "GET", agentPath(pattern, r), nil, nil, cond, false)
		if err != nil {
			s.agentFailure(w, err)
			return
		}
		defer resp.Body.Close()
		h := w.Header()
		for _, k := range []string{"ETag", "Last-Modified", "Cache-Control"} {
			if v := resp.Header.Get(k); v != "" {
				h.Set(k, v)
			}
		}
		if h.Get("Cache-Control") == "" {
			h.Set("Cache-Control", "no-store")
		}
		if resp.StatusCode == http.StatusNotModified {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxMapBytes+1))
		if err != nil || len(body) > maxMapBytes {
			h.Set("Cache-Control", "no-store")
			writeErr(w, http.StatusBadGateway, api.CodeInternal, "The map sent more than Playkeeper accepts.", "")
			return
		}
		ct := "application/json"
		if resp.StatusCode == http.StatusOK && mediaType(resp.Header.Get("Content-Type")) == "image/png" {
			ct = "image/png"
		}
		h.Set("Content-Type", ct)
		h.Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
}

func mediaType(v string) string {
	t, _, err := mime.ParseMediaType(v)
	if err != nil {
		return ""
	}
	return t
}

// The shared map. Every answer is no-store. A map that is off or not
// shared, a stopped server, an old, unknown or malformed link token and an
// agent that doesn't answer all get the same 404, which never names a
// server.

func writeMapUnavailable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	writeErr(w, http.StatusNotFound, api.CodeNotFound, "This map isn't available.", "Ask whoever shared it for a new link.")
}

// mapTokenPaths are the paths that carry a shared map's link token as
// their next segment.
var mapTokenPaths = []string{"/map/", "/api/public/map/"}

// redactMapToken hides a shared map's link token in a request path, for the
// request log: whoever reads the log could otherwise open the map. It hides
// mistyped and malformed tokens too, since they may hold most of a real
// one.
func redactMapToken(p string) string {
	c := path.Clean("/" + p)
	for _, prefix := range mapTokenPaths {
		if len(c) > len(prefix) && strings.EqualFold(c[:len(prefix)], prefix) {
			_, rest, more := strings.Cut(c[len(prefix):], "/")
			if more {
				return prefix + "[token]/" + rest
			}
			return prefix + "[token]"
		}
	}
	return p
}

// publicMapAllowed rate-limits shared maps per viewer address.
func (s *Server) publicMapAllowed(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if ok, wait := s.mapViews.allow("ip:" + clientIP(r)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, api.CodeRateLimited, "Too many requests in a short time. Wait a moment.", "")
		return false
	}
	return true
}

// sharedMap asks the agent for part of the shared map with the link token
// token: "" for the page's details, or worlds, players, a tile or the icon.
// It reports false for anything but a complete answer of the expected type.
func (s *Server) sharedMap(r *http.Request, token, part string) (contentType string, body []byte, ok bool) {
	if !webmap.ValidShareToken(token) {
		return "", nil, false
	}
	p := "/v1/public-maps/" + token
	if part != "" {
		p += "/" + part
	}
	resp, err := s.agent.Raw(r.Context(), "GET", p, nil, nil, nil, false)
	if err != nil {
		return "", nil, false
	}
	defer resp.Body.Close()
	ct := mediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || ct != "application/json" && ct != "image/png" {
		return "", nil, false
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxMapBytes+1))
	if err != nil || len(body) > maxMapBytes {
		return "", nil, false
	}
	return ct, body, true
}

// hMapPage serves the UI for /map/<link token>: 200 while the map is
// shared, otherwise 404 with the same page, which then says the map isn't
// available.
func (s *Server) hMapPage(w http.ResponseWriter, r *http.Request, _ *session) {
	if !s.publicMapAllowed(w, r) {
		return
	}
	status := http.StatusNotFound
	if _, _, ok := s.sharedMap(r, r.PathValue("token"), ""); ok {
		status = http.StatusOK
	}
	page := []byte(uiMissing)
	if s.static != nil {
		if b, err := fs.ReadFile(s.static, "index.html"); err == nil {
			page = b
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(page)
}

// publicMap forwards one part of the shared map, which part names from the
// request's validated path.
func (s *Server) publicMap(part func(r *http.Request) (string, bool)) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, _ *session) {
		if !s.publicMapAllowed(w, r) {
			return
		}
		p, ok := part(r)
		if !ok {
			writeMapUnavailable(w)
			return
		}
		ct, body, ok := s.sharedMap(r, r.PathValue("token"), p)
		if !ok {
			writeMapUnavailable(w)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	}
}

func mapPart(name string) func(*http.Request) (string, bool) {
	return func(*http.Request) (string, bool) { return name, true }
}

func mapTile(r *http.Request) (string, bool) {
	world, zoom, tile := r.PathValue("world"), r.PathValue("zoom"), r.PathValue("tile")
	if !reMapWorld.MatchString(world) || strings.Contains(world, "..") || !reMapZoom.MatchString(zoom) || !reMapTile.MatchString(tile) {
		return "", false
	}
	return "tiles/" + world + "/" + zoom + "/" + tile, true
}

// hPublicMapFace serves the face of a player the shared map shows right
// now. The agent's list is empty while the players switch is off, so no
// name gets a face then. Faces come from the panel's cache, so viewers'
// addresses never reach Mojang.
func (s *Server) hPublicMapFace(w http.ResponseWriter, r *http.Request, _ *session) {
	if !s.publicMapAllowed(w, r) {
		return
	}
	token, name := r.PathValue("token"), r.PathValue("name")
	if !webmap.ValidShareToken(token) || !minecraft.ValidPlayerName(name) {
		writeMapUnavailable(w)
		return
	}
	var listed struct {
		Players []struct {
			Name string `json:"name"`
			UUID string `json:"uuid"`
		} `json:"players"`
	}
	if status, err := s.agent.Do(r.Context(), "GET", "/v1/public-maps/"+token+"/players", nil, nil, &listed); err != nil || status != http.StatusOK {
		writeMapUnavailable(w)
		return
	}
	for _, p := range listed.Players {
		if !strings.EqualFold(p.Name, name) {
			continue
		}
		uuid := strings.ToLower(p.UUID)
		if !reUUID.MatchString(uuid) {
			uuid = ""
		}
		if st, img := s.head(r.Context(), p.Name, strings.ReplaceAll(uuid, "-", "")); st == headOK {
			w.Header().Set("Content-Type", "image/png")
			w.Write(img)
			return
		}
		break
	}
	writeMapUnavailable(w)
}
