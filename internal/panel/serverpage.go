package panel

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"html"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/sharecard"
)

// The public page at the machine's address: what anyone who types a
// server's address into a browser sees, on ports 443 and 80 (pageports.go).
// Those ports serve nothing else, unless Serve the dashboard on the
// standard HTTPS port is on: port 443 then answers the machine's name with
// the dashboard, where the page stays for someone who isn't signed in
// (dashboard443.go). Machine links never answer there. Every route of the
// page goes through its own public group, which limits each address and
// logs no path, and a Host other than the machine's address, a server's
// own address or the name of a server on a joined machine that is on the
// page (joinedpage.go) gets the group's one 404.

const (
	pageDataPrefix = "/api/public/server-page"
	acmePrefix     = "/.well-known/acme-challenge/"
)

// pageCacheFor is how long the page's answers are kept: however many
// people have the page open, the agent is asked about once in that time.
const pageCacheFor = 5 * time.Second

// pageCSP is the dashboard's Content Security Policy with the one addition
// the page needs: the players of the stream an owner offers, which load
// only once a visitor presses Watch live.
const pageCSP = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; font-src 'self'; frame-src https://player.twitch.tv https://www.youtube-nocookie.com; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// cardEvery is how often the share card's link changes, so a preview made
// later shows the server as it is then.
const cardEvery = 5 * time.Minute

var (
	// pageLimits: the page itself, opened now and then.
	pageLimits = publicLimits{perMinute: 60, open: 8, read: 10 * time.Second, write: 30 * time.Second, stall: 10 * time.Second}
	// pageDataLimits let a few tabs behind one address follow the status,
	// which the page asks for every 15 seconds, and load icons and faces.
	pageDataLimits = publicLimits{perMinute: 300, open: 16, read: 10 * time.Second, write: 30 * time.Second, stall: 10 * time.Second}
	// pageAssetLimits let the page's first load fetch its scripts and styles.
	pageAssetLimits = publicLimits{perMinute: 300, open: 16, read: 10 * time.Second, write: time.Minute, stall: 10 * time.Second}
	// acmeLimits fit Let's Encrypt's checks, a few for each certificate.
	acmeLimits = publicLimits{perMinute: 30, open: 4, read: 10 * time.Second, write: 10 * time.Second}
)

var rePageSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// pageSite is the panel's side of the public page: the address it answers
// for, the ports it serves on, and a short cache of the agent's answers.
type pageSite struct {
	group *publicGroup
	// signInRoot is the page at the dashboard's root on port 443, with its
	// way to sign in, limited as the page's own root is.
	signInRoot http.Handler
	// kick asks the port keeper to look again now.
	kick chan struct{}

	mu   sync.Mutex
	host string
	// hosts are the servers' own addresses the page also answers for.
	hosts []string
	// on is whether a server is on the page, and dashboard whether port 443
	// answers host with the dashboard; reached is set once a browser from
	// outside the machine has, and reporting while the agent is being told
	// so, when it was last told of each address in reportedAt
	// (dashboard443.go). gen counts the changes to dashboard and reached
	// made between the keeper's looks, which an answer the agent gave
	// before them doesn't undo.
	on, dashboard, reached, reporting bool
	reportedAt                        map[netip.Addr]time.Time
	gen                               uint64
	ports                             api.PublicPagePorts
	held                              [2]*pageListener
	next                              [2]time.Time

	// local are the page's answers from the dashboard's own agent, and
	// joined those from each joined machine it asked about its servers
	// (joinedpage.go), by machine.
	local  *answerCache
	jmu    sync.Mutex
	joined map[string]*answerCache
}

type pageAnswer struct {
	body        []byte
	contentType string
	ok          bool
	until       time.Time
	// answered is set when the answer is what was said, ok or not, rather
	// than one nobody gave (joinedpage.go).
	answered bool
}

// answerCache keeps a machine's answers for the page for pageCacheFor,
// failures too, so a flood of requests for what isn't there doesn't reach
// the machine either, and asks the machine about each once for everyone
// who wants it meanwhile. A cache with a bound is emptied once it holds
// more answers than that.
type answerCache struct {
	fetch   sync.Mutex
	mu      sync.Mutex
	answers map[string]pageAnswer
	bound   int
}

func newAnswerCache(bound int) *answerCache {
	return &answerCache{answers: map[string]pageAnswer{}, bound: bound}
}

// get answers key from the cache, or from ask.
func (c *answerCache) get(ctx context.Context, now func() time.Time, key string, ask func(context.Context) (pageAnswer, bool)) (pageAnswer, bool) {
	lookup := func() (pageAnswer, bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		a, ok := c.answers[key]
		return a, ok && now().Before(a.until)
	}
	if a, ok := lookup(); ok {
		return a, a.ok
	}
	c.fetch.Lock()
	defer c.fetch.Unlock()
	if a, ok := lookup(); ok {
		return a, a.ok
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	a, ok := ask(ctx)
	a.ok, a.until = ok, now().Add(pageCacheFor)
	c.mu.Lock()
	if c.bound > 0 && len(c.answers) > c.bound {
		clear(c.answers)
	}
	c.answers[key] = a
	c.mu.Unlock()
	return a, ok
}

func (c *answerCache) forget() {
	c.mu.Lock()
	clear(c.answers)
	c.mu.Unlock()
}

func (s *Server) newPageSite() *pageSite {
	p := &pageSite{kick: make(chan struct{}, 1), local: newAnswerCache(64), joined: map[string]*answerCache{}}
	p.ports = api.PublicPagePorts{HTTPS: api.PagePort{Port: 443, State: api.PortOff}, HTTP: api.PagePort{Port: 80, State: api.PortOff}}
	p.group = newPublicGroup([]publicRoute{
		{prefix: pageDataPrefix, limits: pageDataLimits, handler: readOnly(http.HandlerFunc(s.hPageData))},
		{prefix: "/assets/", limits: pageAssetLimits, cache: "public, max-age=31536000, immutable", handler: readOnly(http.HandlerFunc(s.hPageAsset))},
		{prefix: acmePrefix, limits: acmeLimits, handler: readOnly(http.HandlerFunc(s.hPageACME))},
		{prefix: "/", limits: pageLimits, handler: readOnly(http.HandlerFunc(s.hPageRoot))},
	}, s.now)
	p.signInRoot = p.group.guard(publicRoute{prefix: "/", limits: pageLimits, handler: readOnly(http.HandlerFunc(s.hPageSignInRoot))}, newLimiter(pageLimits.perMinute, time.Minute, s.now))
	return p
}

// kickPage asks the port keeper to look again now.
func (s *Server) kickPage() {
	select {
	case s.page.kick <- struct{}{}:
	default:
	}
}

func (p *pageSite) hostNow() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.host
}

// dashboardOnly reports whether the ports are held for the dashboard alone,
// with no server on the page.
func (p *pageSite) dashboardOnly() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dashboard && !p.on
}

// answers reports whether the Host header host names the machine or one of
// the servers' own addresses the page answers for.
func (p *pageSite) answers(host string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pageHost(host, p.host) || slices.ContainsFunc(p.hosts, func(h string) bool { return pageHost(host, h) })
}

// pageHandler serves the public page on one of its ports; tls says which.
func (s *Server) pageHandler(tls bool) http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.page.group.routes {
		mux.Handle(rt.prefix, rt.handler)
		if !strings.HasSuffix(rt.prefix, "/") {
			mux.Handle(rt.prefix+"/", rt.handler)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Let's Encrypt's checks on port 80 are passed on whatever name they
		// carry, as the agent's own responder answers them: the agent answers
		// only its pending checks, and after the address changes the page's
		// name catches up only at the keeper's next look.
		check := !tls && strings.HasPrefix(r.URL.Path, acmePrefix)
		if !check && !s.pageAnswers(r.Context(), r.Host) {
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
			return
		}
		if !tls && !check {
			if to, ok := s.pageRedirect(r); ok {
				http.Redirect(w, r, to, http.StatusPermanentRedirect)
				return
			}
			// Held for the dashboard alone, port 80 only sends browsers to
			// it over HTTPS: with no server on the page there's nothing to
			// show over plain HTTP.
			if s.page.dashboardOnly() {
				w.Header().Set("Cache-Control", "no-store")
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("X-Robots-Tag", "noindex")
		w.Header().Set("Content-Security-Policy", pageCSP)
		mux.ServeHTTP(w, r)
	})
}

// pageChanged forgets the page's answers after the owner changed what it
// shows, or the zone changed the servers on joined machines it names, and
// asks the port keeper to look again.
func (s *Server) pageChanged() {
	s.page.local.forget()
	s.page.jmu.Lock()
	s.page.joined = map[string]*answerCache{}
	s.page.jmu.Unlock()
	s.kickPage()
}

// pageHost reports whether the Host header host names addr.
func pageHost(host, addr string) bool {
	host = hostName(host)
	return host != "" && host == strings.ToLower(addr)
}

// hostName is the name in the Host header host, without its port.
func hostName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// hPageRoot serves the page itself: the dashboard's index.html with its
// root marked as the server page, and a title and description of what it
// shows for link previews.
func (s *Server) hPageRoot(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
	case "/favicon.svg":
		s.hPageAsset(w, r)
		return
	default:
		http.NotFound(w, r)
		return
	}
	s.writePage(w, r, false)
}

// hPageSignInRoot serves the page at the dashboard's root on port 443, with
// a way to sign in to the dashboard.
func (s *Server) hPageSignInRoot(w http.ResponseWriter, r *http.Request) {
	s.writePage(w, r, true)
}

// writePage answers with the page for the address r asked for; signIn
// adds its way to sign in, for the page at the dashboard's own root.
func (s *Server) writePage(w http.ResponseWriter, r *http.Request, signIn bool) {
	page, _ := s.pageData(r.Context(), r.Host)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	card := scheme + "://" + r.Host + pageDataPrefix + "/card.png?at=" + strconv.FormatInt(s.now().Unix()/int64(cardEvery.Seconds()), 10)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(serverPageHTML(s.indexHTML(), page, card, signIn))
}

func (s *Server) indexHTML() []byte {
	if s.static != nil {
		if b, err := fs.ReadFile(s.static, "index.html"); err == nil {
			return b
		}
	}
	return []byte(uiMissing)
}

// serverPageHTML is index with its root marked as the server page and the
// head describing page for link previews, with its share card at card, and
// marked as offering to sign in when signIn is set. Everything from the page
// is escaped.
func serverPageHTML(index []byte, page api.PublicPage, card string, signIn bool) []byte {
	title, desc := pageMeta(page)
	head := "<title>" + html.EscapeString(title) + "</title>"
	if desc != "" {
		head += `<meta name="description" content="` + html.EscapeString(desc) + `"><meta property="og:description" content="` + html.EscapeString(desc) + `">`
	}
	head += `<meta property="og:title" content="` + html.EscapeString(title) + `"><meta property="og:type" content="website"><meta name="robots" content="noindex">`
	if len(page.Servers) > 0 && card != "" {
		head += `<meta property="og:image" content="` + html.EscapeString(card) + `"><meta property="og:image:width" content="` + strconv.Itoa(sharecard.Width) + `"><meta property="og:image:height" content="` + strconv.Itoa(sharecard.Height) +
			`"><meta name="twitter:card" content="summary_large_image"><meta name="twitter:image" content="` + html.EscapeString(card) + `">`
	} else {
		head += `<meta name="twitter:card" content="summary">`
	}
	out := string(index)
	if i := strings.Index(out, "<title>"); i >= 0 {
		if j := strings.Index(out[i:], "</title>"); j >= 0 {
			out = out[:i] + head + out[i+j+len("</title>"):]
		}
	}
	if loc := reRoot.FindStringIndex(out); loc != nil {
		root := `<div id="root" data-page="server">`
		if signIn {
			root = `<div id="root" data-page="server" data-sign-in="true">`
		}
		out = out[:loc[0]] + root + out[loc[1]:]
	}
	return []byte(out)
}

// reRoot is the UI's root element in index.html.
var reRoot = regexp.MustCompile(`<div id=(?:"root"|root)>`)

// pageMeta is the page's title and a line on what it shows.
func pageMeta(page api.PublicPage) (title, desc string) {
	switch len(page.Servers) {
	case 0:
		return "Playkeeper", ""
	case 1:
	default:
		return page.Address + " · Minecraft servers", strconv.Itoa(len(page.Servers)) + " Minecraft servers. Join at " + page.Address + "."
	}
	sv := page.Servers[0]
	parts := []string{publicStateText(sv)}
	if sv.Board != nil && sv.Board.Headline != "" {
		parts = append(parts, sv.Board.Headline)
	}
	if sv.MinecraftVersion != "" {
		parts = append(parts, "Minecraft: Java Edition "+sv.MinecraftVersion)
	}
	parts = append(parts, "Join at "+sv.Address)
	return sv.Name + " · Minecraft server", strings.Join(parts, " · ")
}

func publicStateText(sv api.PublicServer) string {
	switch sv.State {
	case api.PublicOnline:
		if sv.Players != nil {
			return "Online, " + strconv.Itoa(sv.Players.Online) + " of " + strconv.Itoa(sv.Players.Max) + " playing"
		}
		return "Online"
	case api.PublicStarting:
		return "Starting"
	case api.PublicSleeping:
		return "Asleep, joining wakes it up"
	}
	return "Offline"
}

// hPageData serves what the page shows, the servers' icons and the faces
// of players the owner shows.
func (s *Server) hPageData(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, pageDataPrefix)
	switch {
	case rest == "":
		page, ok := s.pageData(r.Context(), r.Host)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, page)
	case strings.HasPrefix(rest, "/icons/"):
		slug := strings.TrimPrefix(rest, "/icons/")
		if !rePageSlug.MatchString(slug) {
			http.NotFound(w, r)
			return
		}
		if j, ok := s.joinedAt(r.Host); ok {
			s.hJoinedPageIcon(w, r, j, slug)
			return
		}
		a, ok := s.pageCached(r.Context(), "icon "+slug+" "+r.Host, func(ctx context.Context) (pageAnswer, bool) {
			return pagePNG(ctx, s.agent, "/v1/public-page/icons/"+url.PathEscape(slug), url.Values{"host": {r.Host}})
		})
		if !ok {
			http.NotFound(w, r)
			return
		}
		writePNG(w, a.body)
	case strings.HasPrefix(rest, "/faces/"):
		s.hPageFace(w, r, strings.TrimPrefix(rest, "/faces/"))
	case rest == "/card.png":
		s.hPageCard(w, r)
	default:
		http.NotFound(w, r)
	}
}

// hPageFace serves the face of a player the page lists right now, which it
// does only while the owner shows who's playing. Faces come from the
// panel's cache, so visitors never contact Mojang.
func (s *Server) hPageFace(w http.ResponseWriter, r *http.Request, name string) {
	if !minecraft.ValidPlayerName(name) {
		http.NotFound(w, r)
		return
	}
	page, ok := s.pageData(r.Context(), r.Host)
	if !ok {
		http.NotFound(w, r)
		return
	}
	for _, sv := range page.Servers {
		if sv.Players == nil {
			continue
		}
		for _, n := range sv.Players.Names {
			if !strings.EqualFold(n, name) {
				continue
			}
			if st, img := s.head(r.Context(), n, ""); st == headOK {
				writePNG(w, img)
				return
			}
			http.NotFound(w, r)
			return
		}
	}
	http.NotFound(w, r)
}

// hPageCard serves the page's share card: the picture link previews show,
// drawn from what the page shows now. A server on a joined machine has its
// card drawn among that machine's answers, so drawing it holds up no other
// page.
func (s *Server) hPageCard(w http.ResponseWriter, r *http.Request) {
	page, ok := s.pageData(r.Context(), r.Host)
	if !ok {
		http.NotFound(w, r)
		return
	}
	card := shareCard(page)
	key, _ := json.Marshal(card)
	answers, cardKey := s.page.local, "card "+string(key)
	if j, ok := s.joinedAt(r.Host); ok {
		answers, cardKey = s.joinedAnswers(j.machineID), "card "+j.id
	}
	a, ok := answers.get(r.Context(), s.now, cardKey, func(context.Context) (pageAnswer, bool) {
		b, err := sharecard.PNG(card)
		if err != nil {
			s.log.Warn("could not draw the public page's share card", "err", err)
			return pageAnswer{}, false
		}
		return pageAnswer{body: b, contentType: "image/png"}, true
	})
	if !ok {
		http.NotFound(w, r)
		return
	}
	writePNG(w, a.body)
}

// shareCard is what the share card shows of page: its one server, or the
// address and how many servers answer there.
func shareCard(page api.PublicPage) sharecard.Card {
	if len(page.Servers) != 1 {
		online := 0
		for _, sv := range page.Servers {
			if sv.State == api.PublicOnline {
				online++
			}
		}
		return sharecard.Card{Name: page.Address, Status: strconv.Itoa(len(page.Servers)) + " Minecraft servers · " + strconv.Itoa(online) + " online", Online: online > 0, Address: page.Address}
	}
	sv := page.Servers[0]
	c := sharecard.Card{Name: sv.Name, Online: sv.State == api.PublicOnline, Address: sv.Address}
	switch sv.State {
	case api.PublicOnline:
		c.Status = "Online"
		if sv.Players != nil {
			c.Status += " · " + strconv.Itoa(sv.Players.Online) + " of " + strconv.Itoa(sv.Players.Max) + " playing"
		}
	case api.PublicStarting:
		c.Status = "Starting"
	case api.PublicSleeping:
		c.Status = "Asleep · joining wakes it up"
	default:
		c.Status = "Offline"
	}
	if sv.Board != nil {
		c.Headline = sv.Board.Headline
	}
	return c
}

func writePNG(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Write(b)
}

// pageData is what the page shows to a browser that asked for host, from
// the agent at most once every pageCacheFor, or at the name of a server on
// a joined machine from that machine (joinedPage).
func (s *Server) pageData(ctx context.Context, host string) (api.PublicPage, bool) {
	if j, ok := s.joinedAt(host); ok {
		return s.joinedPage(ctx, j)
	}
	a, ok := s.pageCached(ctx, "page "+host, func(ctx context.Context) (pageAnswer, bool) {
		var page api.PublicPage
		status, err := s.agent.Do(ctx, http.MethodGet, "/v1/public-page", url.Values{"host": {host}}, nil, &page)
		if err != nil || status != http.StatusOK || len(page.Servers) == 0 {
			return pageAnswer{}, false
		}
		b, err := json.Marshal(page)
		return pageAnswer{body: b}, err == nil
	})
	var page api.PublicPage
	if !ok || json.Unmarshal(a.body, &page) != nil {
		return api.PublicPage{}, false
	}
	return page, true
}

// pagePNG is a PNG the agent c serves at path for the page.
func pagePNG(ctx context.Context, c *agentclient.Client, path string, q url.Values) (pageAnswer, bool) {
	resp, err := c.Raw(ctx, http.MethodGet, path, q, nil, nil, false)
	if err != nil {
		return pageAnswer{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || mediaType(resp.Header.Get("Content-Type")) != "image/png" {
		return pageAnswer{}, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxMapBytes+1))
	if err != nil || len(b) > maxMapBytes {
		return pageAnswer{}, false
	}
	return pageAnswer{body: b, contentType: "image/png"}, true
}

// pageCached answers key from the dashboard's own agent's answers, or from
// get.
func (s *Server) pageCached(ctx context.Context, key string, get func(context.Context) (pageAnswer, bool)) (pageAnswer, bool) {
	return s.page.local.get(ctx, s.now, key, get)
}

// hPageAsset serves the page's scripts, styles and the favicon from the
// built UI.
func (s *Server) hPageAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if s.static == nil || !fs.ValidPath(name) || (name != "favicon.svg" && !strings.HasPrefix(name, "assets/")) {
		http.NotFound(w, r)
		return
	}
	if st, err := fs.Stat(s.static, name); err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, s.static, name)
}

// hPageACME passes Let's Encrypt's check on to the agent, which answers it
// while it gets a certificate for an own domain over HTTP-01.
func (s *Server) hPageACME(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, acmePrefix)
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	resp, err := s.agent.Raw(r.Context(), http.MethodGet, "/v1/acme-challenge/"+url.PathEscape(token), nil, nil, nil, false)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.NotFound(w, r)
		return
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(b)
}

// Dashboard routes.

// hPublicPage is a server's public page for its Settings, with whether
// browsers reach it.
func (s *Server) hPublicPage(w http.ResponseWriter, r *http.Request, _ *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	var v api.PublicPageView
	if _, err := m.agent.Do(r.Context(), http.MethodGet, agentPath("/v1/servers/{id}/public-page", r), nil, nil, &v); err != nil {
		s.agentFailure(w, err)
		return
	}
	s.pageView(m, r.PathValue("id"), &v)
	writeJSON(w, http.StatusOK, v)
}

// hPublicPageSet changes a server's switches for the page, and records
// whether its page is on as the dashboard set it, which a server on a
// joined machine needs beside its machine's word (see joinedpage.go). Off
// is recorded before the machine is asked, so a machine that refuses it
// can't keep its page up; on, only once the machine takes it.
func (s *Server) hPublicPageSet(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	var req api.PublicPageRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Request body must be a JSON object.", "")
		return
	}
	id := r.PathValue("id")
	if req.Enabled != nil && !*req.Enabled {
		s.setPageRecord(id, false)
		s.pageChanged()
	}
	req.Actor = sess.User.Username
	var v api.PublicPageView
	if _, err := m.agent.Do(asActor(r.Context(), sess.User.Username), http.MethodPost, agentPath("/v1/servers/{id}/public-page", r), nil, req, &v); err != nil {
		s.agentFailure(w, err)
		return
	}
	if req.Enabled != nil && *req.Enabled {
		s.setPageRecord(id, true)
	}
	s.pageChanged()
	s.pageView(m, id, &v)
	writeJSON(w, http.StatusOK, v)
}

// pageView is v, server id's page as its machine m says, as Settings show
// it: on the dashboard's ports, which serve every server's page. A server on
// a joined machine has its page at the name the zone gives it, never at an
// address its machine has of its own, and is on it only while the
// dashboard's record says so too (pageRecordOr). Its name has no
// certificate but the domain's wildcard, so without it port 443 can't serve
// its page.
func (s *Server) pageView(m machine, id string, v *api.PublicPageView) {
	ports := s.page.portsNow()
	if m.Kind != localKind {
		v.Host = s.zoneAddress(id)
		v.Enabled = s.pageRecordOr(id, v.Enabled) && v.Enabled
		if ports.HTTPS.State == api.PortOpen && !s.pageCertified(v.Host) {
			ports.HTTPS.State = api.PortNoCertificate
		}
	}
	v.Ports = &ports
}

// pageCertified reports whether port 443 has a certificate for host.
func (s *Server) pageCertified(host string) bool {
	if s.pageCerts == nil || host == "" {
		return false
	}
	_, err := s.pageCerts.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
	return err == nil
}

// hPublicPagePortsRetry tries the ports found busy again, once the owner
// freed one.
func (s *Server) hPublicPagePortsRetry(w http.ResponseWriter, r *http.Request, sess *session) {
	if _, ok := s.target(w, r); !ok {
		return
	}
	ctx := asActor(r.Context(), sess.User.Username)
	if _, err := s.agent.Do(ctx, http.MethodPost, "/v1/public-page/ports/retry", nil, api.ActionRequest{Actor: sess.User.Username}, nil); err != nil {
		s.agentFailure(w, err)
		return
	}
	s.page.retryNow()
	s.kickPage()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
