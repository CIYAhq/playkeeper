package panel

import (
	"io"
	"net/http"
	"net/url"
	"slices"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// addonIconTypes are the image types the agent serves add-on icons as.
var addonIconTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// hAddonIcon serves a plugin's or mod's icon from the panel's own origin,
// as its Content-Security-Policy loads no images from elsewhere. The
// server's machine fetches it, from Modrinth's, Hangar's and CurseForge's
// file hosts only.
func (s *Server) hAddonIcon(w http.ResponseWriter, r *http.Request, _ *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	resp, err := m.agent.Raw(r.Context(), "GET", "/v1/addons/icon", url.Values{"url": {r.URL.Query().Get("url")}}, nil, nil, false)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, io.LimitReader(resp.Body, 1<<20))
		return
	}
	ct := resp.Header.Get("Content-Type")
	if !slices.Contains(addonIconTypes, ct) {
		writeErr(w, http.StatusBadGateway, api.CodeInternal, "The icon isn't a PNG, JPEG, GIF or WebP image.", "")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}
