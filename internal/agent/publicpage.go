package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/certs"
)

// The public page at the machine's address: what anyone who types a
// server's address into a browser sees. The panel serves it on ports 443
// and 80 (pageports.go hands it the ports) and asks the agent what it
// shows. A server is on the page until its owner turns that off, and the
// page names who's playing only while the owner shows them. It never
// holds an IP address, a crash, a backup or anything about the machine.

// maxPublicNames bounds the players' names the page lists.
const maxPublicNames = 100

// devPageHost is the address a development install's page answers for
// while it has none.
const devPageHost = "localhost"

// publicPageSettings are the server's switches; a server whose row can't
// be read is off the page.
func (s *server) publicPageSettings() api.PublicPageSettings {
	var set api.PublicPageSettings
	if err := s.db.QueryRow(`SELECT public_page, public_page_players, public_about, public_stream FROM servers WHERE id = ?`, s.id).Scan(&set.Enabled, &set.Players, &set.About, &set.Stream); err != nil {
		return api.PublicPageSettings{}
	}
	return set
}

func (s *server) publicPageView() api.PublicPageView {
	return api.PublicPageView{PublicPageSettings: s.publicPageSettings(), Host: s.serverPageHost(), Board: s.publicBoard()}
}

// serverPageHost is where the server's page answers: its own address, or
// the machine's name.
func (s *server) serverPageHost() string {
	for _, js := range s.ownAddresses(s.address()) {
		if js.id == s.id {
			return js.own
		}
	}
	return s.pageHost()
}

// pageHost is the address the page answers for: the machine's name, or ""
// without one.
func (a *Agent) pageHost() string {
	st := a.address()
	switch {
	case st.Kind != api.AddressNone && st.Host != "":
		return st.Host
	case a.cfg.Dev:
		return devPageHost
	}
	return ""
}

// hPublicPage is the server's public page for its Settings.
func (s *server) hPublicPage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.publicPageView())
}

// hPublicPageSet changes the server's switches for the public page.
func (s *server) hPublicPageSet(w http.ResponseWriter, r *http.Request) {
	var req api.PublicPageRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if req.Enabled == nil && req.Players == nil && req.About == nil && req.Stream == nil {
		writeError(w, errInvalid("Say what to change."))
		return
	}
	about, stream, changed, err := pageText(req)
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := s.db.Exec(`UPDATE servers SET public_page = COALESCE(?, public_page), public_page_players = COALESCE(?, public_page_players),
		public_about = COALESCE(?, public_about), public_stream = COALESCE(?, public_stream) WHERE id = ?`, req.Enabled, req.Players, about, stream, s.id)
	if err != nil {
		writeError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, errNotFound("Server"))
		return
	}
	v := s.publicPageView()
	detail := fmt.Sprintf("page %s, players %s", onOff(v.Enabled), onOff(v.Players))
	if len(changed) > 0 {
		detail += ", " + strings.Join(changed, ", ")
	}
	s.audit(actor, "public_page.changed", "public_page", "changed", detail)
	writeJSON(w, http.StatusOK, v)
}

// publicPageState is the address the page answers for and whether any
// server is on it: the panel holds ports 443 and 80 only while one is.
func (a *Agent) publicPageState() api.PublicPageState {
	host := a.pageHost()
	if host == "" {
		return api.PublicPageState{}
	}
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM servers WHERE public_page = 1`).Scan(&n); err != nil {
		return api.PublicPageState{Host: host}
	}
	return api.PublicPageState{Host: host, On: n > 0, Hosts: a.ownPageHosts()}
}

func (a *Agent) hPublicPageState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.publicPageState())
}

// writePageGone is the answer for a page that is off and an address that
// isn't this machine's alike.
func writePageGone(w http.ResponseWriter) {
	writeErr(w, http.StatusNotFound, api.CodeNotFound, "There's no server page here.", "")
}

// hPublicPageData is what the page shows to a browser that asked for the
// address in ?host=: 404 unless that is the machine's address and a server
// is on the page.
func (a *Agent) hPublicPageData(w http.ResponseWriter, r *http.Request) {
	page, ok := a.publicPage(r.Context(), r.URL.Query().Get("host"))
	if !ok {
		writePageGone(w)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// hPublicPageIcon is the icon of a server on the page, by its slug.
func (a *Agent) hPublicPageIcon(w http.ResponseWriter, r *http.Request) {
	s := a.pageServer(r.URL.Query().Get("host"), r.PathValue("slug"))
	if s == nil {
		writePageGone(w)
		return
	}
	b, err := s.readIcon()
	if err != nil {
		writePageGone(w)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

// pageServer is the server on the page at host with slug, or nil. A
// server's own address shows only that server.
func (a *Agent) pageServer(host, slug string) *server {
	st := a.publicPageState()
	if !st.On {
		return nil
	}
	if !sameHost(host, st.Host) {
		if s := a.ownPageServer(host); s != nil {
			if row, err := s.row(); err == nil && row.Slug == slug {
				return s
			}
		}
		return nil
	}
	for _, s := range a.serverList() {
		if row, err := s.row(); err == nil && row.Slug == slug && s.publicPageSettings().Enabled {
			return s
		}
	}
	return nil
}

// publicPage is what the page shows at host: every server on it at the
// machine's name, or only the server whose own address host is.
func (a *Agent) publicPage(ctx context.Context, host string) (api.PublicPage, bool) {
	st := a.publicPageState()
	if !st.On {
		return api.PublicPage{}, false
	}
	var only *server
	address := st.Host
	if !sameHost(host, st.Host) {
		if only = a.ownPageServer(host); only == nil {
			return api.PublicPage{}, false
		}
		address = only.serverPageHost()
	}
	addr := a.address()
	joins := a.joinAddresses(addr, a.joinServers())
	page := api.PublicPage{Address: address, Servers: []api.PublicServer{}}
	for _, j := range joins {
		if only != nil && j.ServerID != only.id {
			continue
		}
		s := a.serverByID(j.ServerID)
		if s == nil {
			continue
		}
		if ps, ok := s.publicServer(ctx, st.Host, j); ok {
			page.Servers = append(page.Servers, ps)
		}
	}
	if len(page.Servers) == 0 {
		return api.PublicPage{}, false
	}
	return page, true
}

// publicServer is what the page shows of the server, and false while it
// is off the page or not created yet.
func (s *server) publicServer(ctx context.Context, host string, j api.JoinAddress) (api.PublicServer, bool) {
	set := s.publicPageSettings()
	if !set.Enabled {
		return api.PublicServer{}, false
	}
	st := s.Status(ctx)
	sc := st.Config
	if sc == nil {
		return api.PublicServer{}, false
	}
	ps := api.PublicServer{Slug: st.Slug, Name: st.Name, MOTD: sc.MOTD, MinecraftVersion: sc.MinecraftVersion, Type: s.serverType(sc), InviteOnly: sc.Whitelist,
		Address: publicJoinAddress(host, j, s.gamePort), State: publicState(st)}
	if _, err := s.readIcon(); err == nil {
		ps.HasIcon = true
	}
	if crossplayOn(sc) {
		// Bedrock follows A records only, which a working own address has.
		bedrock := host
		if j.OwnAddress != "" && j.Published {
			bedrock = j.OwnAddress
		}
		ps.Bedrock = &api.BedrockJoin{Host: bedrock, Port: sc.CrossplayPort}
	}
	if ps.State == api.PublicOnline && st.Players != nil {
		p := &api.PublicPlayers{Online: st.Players.Online, Max: st.Players.Max}
		if p.Max <= 0 {
			p.Max = sc.MaxPlayers
		}
		if set.Players && len(st.Players.Names) > 0 {
			p.Names = slices.Clone(st.Players.Names)
			slices.SortFunc(p.Names, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
			p.Names = p.Names[:min(len(p.Names), maxPublicNames)]
		}
		ps.Players = p
	}
	if rec, err := s.packRecord(); err == nil && rec != nil {
		ps.Modpack = &api.PublicModpack{Name: rec.Pack.Name, Version: rec.Pack.VersionNumber}
	}
	if rec, err := s.activeMap(); err == nil && rec != nil && rec.public {
		ps.Map = s.mapLink(rec)
	}
	if on, token := s.packsPublic(); on {
		ps.Pack = s.panelLink("/packs/" + token)
	}
	ps.About, ps.Stream, ps.Board = set.About, s.pageStream(set.Stream), s.publicBoard()
	return ps, true
}

// publicJoinAddress is what players type for the server: its own address
// once that works; else the machine's address for the one on 25565, which
// its A record reaches without an SRV record; else the server's address
// under the machine's once that works, or the machine's address with the
// port.
func publicJoinAddress(host string, j api.JoinAddress, port int) string {
	if (j.OwnAddress != "" || port != certs.MinecraftPort) && j.Published && j.Address != "" {
		return j.Address
	}
	return hostPort(host, port)
}

// publicState is how the page says the server is doing. A crash or a
// failed start reads as offline.
func publicState(st api.ServerStatus) string {
	switch {
	case st.Phase == api.PhaseOnline:
		return api.PublicOnline
	case st.Desired == api.DesiredSleeping:
		if st.Sleep != nil && st.Sleep.Listening {
			return api.PublicSleeping
		}
		return api.PublicOffline
	case st.Desired == api.DesiredRunning && slices.Contains([]api.Phase{api.PhasePulling, api.PhaseStartingContainer, api.PhaseDownloading, api.PhaseStarting, api.PhasePreparingWorld}, st.Phase):
		return api.PublicStarting
	}
	return api.PublicOffline
}

// sameHost reports whether the Host header host names addr.
func sameHost(host, addr string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host != "" && host == strings.ToLower(addr)
}
