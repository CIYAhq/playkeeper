package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/modpacks/share"
	"github.com/CIYAhq/playkeeper/internal/pagestream"
	"github.com/CIYAhq/playkeeper/internal/webmap"
)

// The public page of a server on a joined machine. A joined machine serves
// no page, so the zone points the server's name at the dashboard's machine
// (fleetdns.go), whose page shows the server there, asking its machine what
// the page shows of it (the agent's hPublicPageShown). The name answers
// only while that machine says the server is on the page; otherwise it
// gets the page's one 404, like a name nobody has. The page there shows
// that server alone, and nothing that says which machine runs it: its
// address is the name the zone gives it, it has no Bedrock address, which
// would be the machine's, and its links are under the dashboard's own
// address, and only those the dashboard saw that machine make for that
// server.

// maxShownBytes is as much as the dashboard reads of a joined machine's
// answer on what the page shows of a server, whose About, board and
// players' names fit many times over. An answer cut there doesn't parse.
const maxShownBytes = 64 << 10

// maxPageNames bounds the players' names the page lists, as the agent's
// own page does.
const maxPageNames = 100

// joinedAt is the server on a joined machine whose page the Host header
// host asks for: the one the zone gives that name, unless it's the
// dashboard's machine's name or one of its servers' own addresses.
func (s *Server) joinedAt(host string) (joinedName, bool) {
	if s.page.answers(host) {
		return joinedName{}, false
	}
	return s.zoneName(host)
}

// joinedPageOn reports whether host is the name of a server on a joined
// machine that is on the page.
func (s *Server) joinedPageOn(ctx context.Context, host string) bool {
	j, ok := s.joinedAt(host)
	if !ok {
		return false
	}
	_, ok = s.joinedPage(ctx, j)
	return ok
}

// anyJoinedPageOn reports whether a server on a joined machine is on the
// page at its name.
func (s *Server) anyJoinedPageOn(ctx context.Context) bool {
	for _, j := range s.zoneNames() {
		if _, ok := s.joinedPage(ctx, j); ok {
			return true
		}
	}
	return false
}

// joinedAnswers are the page's answers from the joined machine id. Each
// machine has its own, so one that is slow to answer holds up only its own
// servers' pages, and the zone bounds them: a page and an icon for each
// server it names.
func (s *Server) joinedAnswers(id string) *answerCache {
	p := s.page
	p.jmu.Lock()
	defer p.jmu.Unlock()
	c, ok := p.joined[id]
	if !ok {
		c = newAnswerCache(0)
		p.joined[id] = c
	}
	return c
}

// joinedPage is what the page shows at j's address, from j's machine at
// most once every pageCacheFor.
func (s *Server) joinedPage(ctx context.Context, j joinedName) (api.PublicPage, bool) {
	a, ok := s.joinedAnswers(j.machineID).get(ctx, s.now, "page "+j.id+" "+j.address, func(ctx context.Context) (pageAnswer, bool) {
		m, ok := s.joinedMachine(j)
		if !ok {
			return pageAnswer{}, false
		}
		shown, ok := askShown(ctx, m, j.id)
		if !ok {
			return pageAnswer{}, false
		}
		b, err := json.Marshal(api.PublicPage{Address: j.address, Servers: []api.PublicServer{s.joinedServer(ctx, shown, j)}})
		return pageAnswer{body: b}, err == nil
	})
	var page api.PublicPage
	if !ok || json.Unmarshal(a.body, &page) != nil {
		return api.PublicPage{}, false
	}
	return page, true
}

// joinedMachine is j's machine while the dashboard knows it runs j: not
// once j has moved, while it moves, or while two machines claim it.
func (s *Server) joinedMachine(j joinedName) (machine, bool) {
	m, err := s.machineForServer(j.id)
	if err != nil || m.ID != j.machineID || m.Kind != remoteKind {
		return machine{}, false
	}
	return m, true
}

// askShown asks the machine m what the page shows of its server id.
func askShown(ctx context.Context, m machine, id string) (api.PublicServerShown, bool) {
	resp, err := m.agent.Raw(ctx, http.MethodGet, "/v1/servers/"+url.PathEscape(id)+"/public-page/shown", nil, nil, nil, false)
	if err != nil {
		return api.PublicServerShown{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || mediaType(resp.Header.Get("Content-Type")) != "application/json" {
		return api.PublicServerShown{}, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxShownBytes))
	var shown api.PublicServerShown
	if err != nil || json.Unmarshal(b, &shown) != nil {
		return api.PublicServerShown{}, false
	}
	return shown, true
}

// joinedServer is what the page shows of j from what its machine said. Its
// slug and address are the dashboard's, and it has no Bedrock address. A
// state the page doesn't know reads as offline, and players count only
// while it's online; a player's name that isn't one, and a stream that
// isn't a channel's page, are left out.
func (s *Server) joinedServer(ctx context.Context, shown api.PublicServerShown, j joinedName) api.PublicServer {
	sv := shown.PublicServer
	sv.Slug, sv.Address, sv.Bedrock = j.label, j.address, nil
	switch sv.State {
	case api.PublicOnline, api.PublicStarting, api.PublicSleeping:
	default:
		sv.State = api.PublicOffline
	}
	if p := sv.Players; p != nil && sv.State == api.PublicOnline {
		var names []string
		for _, n := range p.Names {
			if minecraft.ValidPlayerName(n) && len(names) < maxPageNames {
				names = append(names, n)
			}
		}
		sv.Players = &api.PublicPlayers{Online: max(p.Online, 0), Max: max(p.Max, 0), Names: names}
	} else {
		sv.Players = nil
	}
	if st := sv.Stream; st != nil {
		sv.Stream = nil
		if parsed, ok := pagestream.Parse(st.URL); ok && parsed == *st {
			sv.Stream = &parsed
		}
	}
	sv.Map, sv.Pack = s.joinedPageLinks(ctx, shown, j)
	return sv
}

// joinedPageLinks are j's shared map's and friends' pack page's links as its
// page shows them: under the dashboard's own address, and only for a token
// the dashboard saw j's machine make for j (see recordLink). There are none
// while the dashboard has no address with a certificate.
func (s *Server) joinedPageLinks(ctx context.Context, shown api.PublicServerShown, j joinedName) (mapURL, packURL string) {
	mapOK := webmap.ValidShareToken(shown.MapToken) && s.linkMadeFor(mapLink, shown.MapToken, j.id, j.machineID)
	packOK := share.ValidToken(shown.PackToken) && s.linkMadeFor(packLink, shown.PackToken, j.id, j.machineID)
	if !mapOK && !packOK {
		return "", ""
	}
	base, err := s.dashboardURL(ctx)
	if err != nil || base == "" {
		return "", ""
	}
	if mapOK {
		mapURL = base + mapPagePrefix + shown.MapToken
	}
	if packOK {
		packURL = base + share.PathPrefix + shown.PackToken
	}
	return mapURL, packURL
}

// hJoinedPageIcon serves the icon of j, a server on a joined machine, while
// its page shows it with one, under the slug the page gives it.
func (s *Server) hJoinedPageIcon(w http.ResponseWriter, r *http.Request, j joinedName, slug string) {
	page, ok := s.joinedPage(r.Context(), j)
	if !ok || len(page.Servers) != 1 || page.Servers[0].Slug != slug || !page.Servers[0].HasIcon {
		http.NotFound(w, r)
		return
	}
	a, ok := s.joinedAnswers(j.machineID).get(r.Context(), s.now, "icon "+j.id, func(ctx context.Context) (pageAnswer, bool) {
		m, ok := s.joinedMachine(j)
		if !ok {
			return pageAnswer{}, false
		}
		return pagePNG(ctx, m.agent, "/v1/servers/"+url.PathEscape(j.id)+"/icon", nil)
	})
	if !ok {
		http.NotFound(w, r)
		return
	}
	writePNG(w, a.body)
}
