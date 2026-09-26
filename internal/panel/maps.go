package panel

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/webmap"
)

// Each server's live map. The agent is the only hop to squaremap in the
// server's container; the panel forwards the Map tab's calls for signed-in
// accounts, and serves the shared map at /map/<link token> to anyone while
// its switch is on.

// maxMapBytes bounds a tile or a JSON answer about the map; squaremap's
// tiles are at most about 1 MB.
const maxMapBytes = 2 << 20

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

// The shared map is in the public group (publicRoutes): its page at
// /map/<link token> and the calls the page makes under
// /api/public/map/<link token>/. The group limits each address, answers
// no-store, and turns every 404 and failure into its one 404, so a map
// that is off or not shared, a stopped server, an old, unknown or
// malformed link token and an agent that doesn't answer all look alike
// and never name a server.

const (
	mapPagePrefix = "/map/"
	mapDataPrefix = "/api/public/map/"
)

// mapPageLimits: people open a shared map now and then.
var mapPageLimits = publicLimits{perMinute: 60, open: 4, read: 10 * time.Second, write: 30 * time.Second, stall: 10 * time.Second}

// mapDataLimits let one viewer load a screen of tiles at once, pan and
// zoom, and ask who is playing every few seconds.
var mapDataLimits = publicLimits{perMinute: 600, open: 24, read: 10 * time.Second, write: 30 * time.Second, stall: 10 * time.Second}

func writeMapUnavailable(w http.ResponseWriter) {
	writeErr(w, http.StatusNotFound, api.CodeNotFound, "This map isn't available.", "Ask whoever shared it for a new link.")
}

// readOnly answers anything but GET and HEAD with the group's 404.
func readOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// mapPage serves the UI for /map/<link token> without asking the agent:
// the page asks the calls below, whose 404 then says the map isn't
// available.
func (s *Server) mapPage() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(mapPagePrefix+"{token}", func(w http.ResponseWriter, r *http.Request) {
		page := []byte(uiMissing)
		if s.static != nil {
			if b, err := fs.ReadFile(s.static, "index.html"); err == nil {
				page = b
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	return readOnly(mux)
}

// mapData serves the shared map's details, worlds, players, tiles, the
// server's icon and players' faces.
func (s *Server) mapData() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(mapDataPrefix+"{token}", s.publicMap(mapPart("")))
	mux.HandleFunc(mapDataPrefix+"{token}/worlds", s.publicMap(mapPart("worlds")))
	mux.HandleFunc(mapDataPrefix+"{token}/players", s.publicMap(mapPart("players")))
	mux.HandleFunc(mapDataPrefix+"{token}/icon", s.publicMap(mapPart("icon")))
	mux.HandleFunc(mapDataPrefix+"{token}/tiles/{world}/{zoom}/{tile}", s.publicMap(mapTile))
	mux.HandleFunc(mapDataPrefix+"{token}/faces/{name}", s.hPublicMapFace)
	return readOnly(mux)
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

// publicMap forwards one part of the shared map, which part names from the
// request's validated path.
func (s *Server) publicMap(part func(r *http.Request) (string, bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
func (s *Server) hPublicMapFace(w http.ResponseWriter, r *http.Request) {
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
