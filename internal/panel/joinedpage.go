package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/modpacks/share"
	"github.com/CIYAhq/playkeeper/internal/pagetext"
	"github.com/CIYAhq/playkeeper/internal/webmap"
)

// The public page of a server on a joined machine. A joined machine serves
// no page, so the zone points the server's name at the dashboard's machine
// (fleetdns.go), whose page shows the server there, asking its machine what
// the page shows of it (the agent's hPublicPageShown). The name answers
// only while both the dashboard's own record and the machine say the server
// is on the page: every change to that goes through the dashboard, which
// keeps it (public_pages). Otherwise the name gets the page's one 404, like
// a name nobody has. The page there shows that server alone, and nothing
// that says which machine runs it: its address is the name the zone gives
// it, it has no Bedrock address, which would be the machine's, and its
// links are under the dashboard's own address, and only those the dashboard
// saw that machine make for that server. What else the machine says the
// page shows is held to what the agent itself allows.

// maxShownBytes is as much as the dashboard reads of a joined machine's
// answer about a server's page, whose About, board and players' names fit
// many times over. An answer cut there doesn't parse.
const maxShownBytes = 64 << 10

// maxPageNames bounds the players' names the page lists, as the agent's
// own page does.
const maxPageNames = 100

// pageLineMax bounds a line the page shows that the agent sets no bound on
// itself: the Minecraft version, and a modpack's name and version.
const pageLineMax = 100

// joinedAt is the server on a joined machine whose page the Host header
// host asks for: the one the zone gives that name. It comes before the
// dashboard's machine's own names, as the zone gives no name another server
// on the dashboard's machine has. Those leave out a copy a move is making
// or left there, whose name stays with the server even once the zone no
// longer gives it (see pageHidden).
func (s *Server) joinedAt(host string) (joinedName, bool) {
	return s.zoneName(host)
}

// pageAnswers reports whether the page answers the Host header host: the
// name of a server on a joined machine while that server is on the page,
// or else one of the dashboard's machine's names.
func (s *Server) pageAnswers(ctx context.Context, host string) bool {
	if j, ok := s.joinedAt(host); ok {
		_, on := s.joinedPage(ctx, j)
		return on
	}
	return s.page.answers(host)
}

// anyJoinedPageOn reports whether a server on a joined machine is on the
// page at its name, and known false when one the dashboard's record has on
// was left unanswered and none said it was on: its machine didn't answer,
// or not before ctx ended. One the record doesn't have on is off then, as
// its page can't be served without that answer anyway, so a machine that
// never answers can't keep the ports open for the page's 404. Each machine
// is asked about its servers in turn, the machines side by side, and only
// until ctx ends, so a machine slow to answer holds up no look of the port
// keeper past its deadline.
func (s *Server) anyJoinedPageOn(ctx context.Context) (on, known bool) {
	byMachine := map[string][]joinedName{}
	for _, j := range s.zoneNames() {
		byMachine[j.machineID] = append(byMachine[j.machineID], j)
	}
	const (
		allOff = iota
		someOn
		unanswered
	)
	type reply struct {
		machine string
		answer  int
	}
	answers := make(chan reply, len(byMachine))
	for id, names := range byMachine {
		go func() {
			answer := allOff
			for _, j := range names {
				if ctx.Err() != nil {
					// The look's end speaks for the servers left.
					return
				}
				switch on, known := s.joinedPageState(ctx, j); {
				case on:
					answers <- reply{id, someOn}
					return
				case !known:
					answer = unanswered
				}
			}
			answers <- reply{id, answer}
		}()
	}
	known = true
	for len(byMachine) > 0 {
		select {
		case r := <-answers:
			delete(byMachine, r.machine)
			switch r.answer {
			case someOn:
				return true, true
			case unanswered:
				known = false
			}
		case <-ctx.Done():
			for _, names := range byMachine {
				if slices.ContainsFunc(names, func(j joinedName) bool { return s.pageHeldOn(j.id) }) {
					return false, false
				}
			}
			return false, known
		}
	}
	return false, known
}

// joinedAnswers are the page's answers from the joined machine id. Each
// machine has its own, so one that is slow to answer holds up only its own
// servers' pages, and the zone bounds them: a page, an icon and a card for
// each server it names.
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
	a, ok := s.joinedAnswer(ctx, j)
	var page api.PublicPage
	if !ok || json.Unmarshal(a.body, &page) != nil {
		return api.PublicPage{}, false
	}
	return page, true
}

// joinedPageState reports whether j is on the page at its name, and known
// false when that's unknown while the dashboard's record has it on: its
// machine didn't answer, or doesn't run it as far as the dashboard knows,
// as during a move. One the record doesn't have on is off then.
func (s *Server) joinedPageState(ctx context.Context, j joinedName) (on, known bool) {
	a, ok := s.joinedAnswer(ctx, j)
	if a.answered {
		return ok, true
	}
	return false, !s.pageHeldOn(j.id)
}

// joinedAnswer is the page's answer about j: answered when the dashboard's
// record says it's off the page, or its machine says it's on or off.
func (s *Server) joinedAnswer(ctx context.Context, j joinedName) (pageAnswer, bool) {
	return s.joinedAnswers(j.machineID).get(ctx, s.now, "page "+j.id+" "+j.address, func(ctx context.Context) (pageAnswer, bool) {
		m, ok := s.joinedMachine(j)
		if !ok {
			return pageAnswer{}, false
		}
		switch public, known := s.joinedPublic(ctx, m, j.id); {
		case !known:
			return pageAnswer{}, false
		case !public:
			return pageAnswer{answered: true}, false
		}
		var shown api.PublicServerShown
		switch status, ok := askJoined(ctx, m, "/v1/servers/"+url.PathEscape(j.id)+"/public-page/shown", &shown); {
		case status == http.StatusNotFound:
			return pageAnswer{answered: true}, false
		case !ok:
			return pageAnswer{}, false
		}
		b, err := json.Marshal(api.PublicPage{Address: j.address, Servers: []api.PublicServer{s.joinedServer(ctx, shown, j)}})
		return pageAnswer{body: b, answered: true}, err == nil
	})
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

// askJoined asks the joined machine m for path, and reads at most
// maxShownBytes of its JSON answer into out. status is its answer's, 0 for
// none.
func askJoined(ctx context.Context, m machine, path string, out any) (status int, ok bool) {
	resp, err := m.agent.Raw(ctx, http.MethodGet, path, nil, nil, nil, false)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || mediaType(resp.Header.Get("Content-Type")) != "application/json" {
		return resp.StatusCode, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxShownBytes))
	return resp.StatusCode, err == nil && json.Unmarshal(b, out) == nil
}

// joinedPublic reports whether the dashboard's record has server id, on the
// joined machine m, on the page, and known false while it can't say. A
// server whose page the dashboard never set takes m's word for it once
// (pageRecordOr).
func (s *Server) joinedPublic(ctx context.Context, m machine, id string) (public, known bool) {
	if on, known := s.pageRecord(id); known {
		return on, true
	}
	var v api.PublicPageView
	if _, ok := askJoined(ctx, m, "/v1/servers/"+url.PathEscape(id)+"/public-page", &v); !ok {
		return false, false
	}
	return s.pageRecordOr(id, v.Enabled), true
}

// pageHeldOn reports whether the dashboard's record has server id on the
// page.
func (s *Server) pageHeldOn(id string) bool {
	on, known := s.pageRecord(id)
	return on && known
}

// pageRecord is whether the dashboard's record has server id on the page,
// and whether it has a record of it at all.
func (s *Server) pageRecord(id string) (on, known bool) {
	err := s.db.QueryRow(`SELECT enabled FROM public_pages WHERE server_id = ?`, id).Scan(&on)
	return on, err == nil
}

// setPageRecord records that the dashboard turned server id's page on or
// off.
func (s *Server) setPageRecord(id string, on bool) {
	if _, err := s.db.Exec(`INSERT INTO public_pages(server_id, enabled, changed_at) VALUES(?,?,?)
		ON CONFLICT(server_id) DO UPDATE SET enabled = excluded.enabled, changed_at = excluded.changed_at`, id, on, millis(s.now())); err != nil {
		s.log.Error("record whether a server is on the public page", "server", id, "err", err)
	}
}

// pageRecordOr is the dashboard's record of whether server id is on the
// page; for a server it has no record of, on as its machine says, which is
// kept as the record from then on.
func (s *Server) pageRecordOr(id string, machineSays bool) bool {
	if _, err := s.db.Exec(`INSERT INTO public_pages(server_id, enabled, changed_at) VALUES(?,?,?) ON CONFLICT(server_id) DO NOTHING`, id, machineSays, millis(s.now())); err != nil {
		s.log.Error("record whether a server is on the public page", "server", id, "err", err)
		return false
	}
	on, known := s.pageRecord(id)
	return on && known
}

// joinedServer is what the page shows of j from what its machine said. Its
// slug and address are the dashboard's, and it has no Bedrock address.
// Everything else is held to what the agent allows: its name, description,
// About and board within their bounds and of the characters it takes, a
// type and state the page knows, players only while it's online and no
// more names than are playing, and a stream only as a channel's page.
func (s *Server) joinedServer(ctx context.Context, shown api.PublicServerShown, j joinedName) api.PublicServer {
	in := shown.PublicServer
	sv := api.PublicServer{Slug: j.label, Address: j.address, InviteOnly: in.InviteOnly, HasIcon: in.HasIcon,
		Name: shownLine(in.Name, api.ServerNameMax, nameRune), MOTD: shownLine(in.MOTD, api.ServerMOTDMax, nameRune),
		MinecraftVersion: shownLine(in.MinecraftVersion, pageLineMax, pagetext.Printable), About: shownAbout(in.About), Board: shownBoard(in.Board, s.now())}
	if sv.Name == "" {
		sv.Name = j.label
	}
	if _, ok := minecraft.TypeByID(in.Type); ok {
		sv.Type = in.Type
	}
	switch in.State {
	case api.PublicOnline, api.PublicStarting, api.PublicSleeping:
		sv.State = in.State
	default:
		sv.State = api.PublicOffline
	}
	if p := in.Players; p != nil && sv.State == api.PublicOnline {
		online := max(p.Online, 0)
		var names []string
		for _, n := range p.Names {
			if minecraft.ValidPlayerName(n) && len(names) < min(online, maxPageNames) {
				names = append(names, n)
			}
		}
		sv.Players = &api.PublicPlayers{Online: online, Max: max(p.Max, 0), Names: names}
	}
	if pack := in.Modpack; pack != nil {
		if name := shownLine(pack.Name, pageLineMax, pagetext.Printable); name != "" {
			sv.Modpack = &api.PublicModpack{Name: name, Version: shownLine(pack.Version, pageLineMax, pagetext.Printable)}
		}
	}
	if st := in.Stream; st != nil {
		if parsed, ok := pagetext.Stream(st.URL); ok && parsed == *st {
			sv.Stream = &parsed
		}
	}
	sv.Map, sv.Pack = s.joinedPageLinks(ctx, shown, j)
	return sv
}

// nameRune is whether a server's name or description, as the agent takes
// them, may hold r: no control characters and no Minecraft formatting code.
func nameRune(r rune) bool {
	return unicode.IsPrint(r) && r != '§'
}

// shownLine is line with the characters keep refuses left out, at most max
// of what is left, trimmed.
func shownLine(line string, max int, keep func(rune) bool) string {
	var b strings.Builder
	n := 0
	for _, r := range line {
		if n == max {
			break
		}
		if keep(r) {
			b.WriteRune(r)
			n++
		}
	}
	return strings.TrimSpace(b.String())
}

// shownAbout is the owner's words for the page within the agent's bounds:
// at most api.PublicAboutLines lines and api.PublicAboutMax characters, of
// those it takes.
func shownAbout(about string) string {
	lines := strings.SplitN(about, "\n", api.PublicAboutLines+1)
	lines = lines[:min(len(lines), api.PublicAboutLines)]
	keep := func(r rune) bool { return r == '\n' || pagetext.Printable(r) }
	return shownLine(strings.Join(lines, "\n"), api.PublicAboutMax, keep)
}

// shownBoard is a status board within the agent's bounds: as many numbers
// and checklist items as it takes, each line as long, and a next session
// from a day ago to api.BoardNextWithin from now.
func shownBoard(b *api.PublicBoard, now time.Time) *api.PublicBoard {
	if b == nil {
		return nil
	}
	out := &api.PublicBoard{Headline: shownLine(b.Headline, api.BoardHeadlineMax, pagetext.Printable), Live: b.Live, UpdatedAt: b.UpdatedAt}
	if b.Next != nil && !b.Next.Before(now.Add(-24*time.Hour)) && !b.Next.After(now.Add(api.BoardNextWithin)) {
		out.Next = b.Next
	}
	for _, st := range b.Stats[:min(len(b.Stats), api.BoardStatsMax)] {
		out.Stats = append(out.Stats, api.BoardStat{Label: shownLine(st.Label, api.BoardStatLabelMax, pagetext.Printable), Value: shownLine(st.Value, api.BoardStatValueMax, pagetext.Printable)})
	}
	for _, it := range b.Checklist[:min(len(b.Checklist), api.BoardItemsMax)] {
		out.Checklist = append(out.Checklist, api.BoardItem{Label: shownLine(it.Label, api.BoardItemLabelMax, pagetext.Printable), Done: it.Done})
	}
	return out
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
