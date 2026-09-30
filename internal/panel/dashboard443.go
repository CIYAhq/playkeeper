package panel

import (
	"context"
	"net/http"
	"net/netip"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/modpacks/share"
	"github.com/CIYAhq/playkeeper/internal/packs"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// Serve the dashboard on the standard HTTPS port (443), a switch in Machine
// settings that the agent keeps (internal/agent, dashboardport.go). The
// panel stays without CAP_NET_BIND_SERVICE: port 443 is the one the agent
// opens and hands over for the public page (pageports.go), and while the
// switch is on it answers the machine's name with the dashboard, with the
// name's certificate from Let's Encrypt and never the self-signed one. Its
// root shows the public page to someone who isn't signed in while a server
// is on the page and customers don't sign in here with Whop, so anyone who
// types a server's address still sees how to join, with a way to sign in.
//
// The panel's own port keeps answering everything. Once the dashboard's
// address has lost its port (api.Dashboard443.Port), which waits for a
// browser from outside the machine to reach port 443, a page opened at the
// machine's name there is sent to port 443 with a temporary redirect: old
// links and bookmarks keep working, and turning the switch off never leaves
// a browser remembering a redirect to a port that stopped answering. Its
// API, /mcp, webhooks, the names service's check, resource packs and
// machine links answer there as before, for whatever keeps the old address.

// reportAgainAfter is how soon the panel tells the agent again that port
// 443 was reached from an address, after telling it failed. Each address
// has its own wait, so the machine's own, which the agent refuses, never
// holds up a browser from outside; reporters bounds how many are kept.
const (
	reportAgainAfter = 30 * time.Second
	reporters        = 32
)

// reachPath answers a browser's check that it reaches port 443 at the
// machine's name (see hReach).
const reachPath = "/api/public/reach"

// reachLimits fit one check each time the setting's page loads.
var reachLimits = publicLimits{perMinute: 30, open: 4, read: 10 * time.Second, write: 10 * time.Second}

// reachGIF is a transparent pixel, what hReach answers with.
var reachGIF = []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\x00\x00\x00!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")

// hReach answers the check the dashboard's page makes from a browser that
// opened it at the panel's port: a request that reaches port 443 is what
// shows a browser can (see noteReached). The page asks from another origin
// without CORS, so all it learns is that an answer arrived: the resource
// policy lets that opaque answer through, and it holds nothing.
func (s *Server) hReach(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != reachPath || r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/gif")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("Content-Length", strconv.Itoa(len(reachGIF)))
	w.Write(reachGIF)
}

// reachSource is what the dashboard's pages may connect to besides their
// own origin: the dashboard on port 443 at the machine's name, so a page
// opened at the panel's port can check that a browser reaches it there (see
// hReach), even one loaded before the switch was turned on.
func (s *Server) reachSource() string {
	host := s.page.hostNow()
	if !reDomainName.MatchString(host) {
		return ""
	}
	return " https://" + strings.ToLower(host)
}

// reDomainName is a DNS name, and nothing a Content Security Policy would
// read as more than one.
var reDomainName = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// httpsHandler serves port 443: the machine's name with the dashboard while
// the switch is on, and otherwise the public page.
func (s *Server) httpsHandler() http.Handler {
	page := s.securityHeaders(s.logPageRequests(s.pageHandler(true)))
	dash := s.dashboard443Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.noteReached(r)
		if s.page.dashboardAt(r.Host) {
			dash.ServeHTTP(w, r)
			return
		}
		page.ServeHTTP(w, r)
	})
}

// dashboardAt reports whether port 443 answers host with the dashboard.
func (p *pageSite) dashboardAt(host string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dashboard && pageHost(host, p.host)
}

// dashboard443Handler is the dashboard as port 443 serves it at the
// machine's name: the panel's own handler, but for its root, which shows the
// public page to someone who isn't signed in (see rootShowsPage), and the
// page's data, which that page reads.
func (s *Server) dashboard443Handler() http.Handler {
	dash := s.Handler()
	pageData := s.securityHeaders(s.logPageRequests(s.page.group.handler(pageDataPrefix)))
	pageRoot := s.securityHeaders(s.logPageRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex")
		w.Header().Set("Content-Security-Policy", pageCSP)
		s.page.signInRoot.ServeHTTP(w, r)
	})))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == pageDataPrefix || strings.HasPrefix(p, pageDataPrefix+"/"):
			w.Header().Set("X-Robots-Tag", "noindex")
			pageData.ServeHTTP(w, r)
		case p == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			// Whether the root is the page or the dashboard depends on the
			// session cookie, so no cache may answer for another.
			w.Header().Add("Vary", "Cookie")
			if s.rootShowsPage(r) {
				pageRoot.ServeHTTP(w, r)
				return
			}
			dash.ServeHTTP(w, r)
		default:
			dash.ServeHTTP(w, r)
		}
	})
}

// rootShowsPage reports whether the dashboard's root at the machine's name
// shows the public page instead: to someone who isn't signed in, while a
// server is on the page there, unless customers sign in here with Whop,
// who come for the dashboard.
func (s *Server) rootShowsPage(r *http.Request) bool {
	if _, err := s.sessionFrom(r); err == nil {
		return false
	}
	if s.whopSignInOn() {
		return false
	}
	_, ok := s.pageData(r.Context(), r.Host)
	return ok
}

// noteReached tells the agent, once, that a browser from outside the
// machine reached the dashboard's port 443: a request there from a public
// address shows no firewall in front of the machine drops the port. The
// agent refuses the machine's own addresses.
func (s *Server) noteReached(r *http.Request) {
	from, ok := publicClient(r.RemoteAddr)
	if !ok {
		return
	}
	p := s.page
	now := s.now()
	key := from
	p.mu.Lock()
	host := p.host
	due := p.dashboard && !p.reached && !p.reporting && host != "" && now.Sub(p.reportedAt[key]) >= reportAgainAfter
	if due {
		if p.reportedAt == nil || len(p.reportedAt) >= reporters {
			p.reportedAt = map[netip.Addr]time.Time{}
		}
		p.reporting, p.reportedAt[key] = true, now
	}
	p.mu.Unlock()
	if !due {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var v api.Dashboard443
		_, err := s.agent.Do(ctx, http.MethodPost, "/v1/dashboard-443/reached", nil, api.Dashboard443Reached{Host: host, From: from.String()}, &v)
		p.mu.Lock()
		p.reporting = false
		if err == nil && v.Reached && p.host == host {
			p.reached, p.gen = true, p.gen+1
		}
		p.mu.Unlock()
		switch {
		case err != nil:
			s.log.Info("could not tell the agent the dashboard was reached on port 443", "err", err)
		case v.Reached:
			s.log.Info("the dashboard answers without a port", "url", "https://"+host)
			// Whop's webhook and the store's marks follow the address.
			s.kickWhop()
		}
	}()
}

// publicClient is the address a connection came from, when that's a public
// one: not a private network, shared address space such as a VPN's, a
// loopback or link-local address.
func publicClient(remote string) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return netip.Addr{}, false
	}
	ip := ap.Addr().Unmap().WithZone("")
	return ip, ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedSpace.Contains(ip)
}

// sharedSpace is RFC 6598's shared address space, which carrier NAT and
// VPNs such as Tailscale use.
var sharedSpace = netip.MustParsePrefix("100.64.0.0/10")

// panelPortHandler is the panel's own port: the dashboard, which sends a
// browser opening one of its pages at the machine's name to port 443 while
// the dashboard's address has no port there.
func (s *Server) panelPortHandler() http.Handler {
	dash := s.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if to, ok := s.to443(r); ok {
			w.Header().Set("Cache-Control", "no-store")
			s.securityHeaders(http.RedirectHandler(to, http.StatusTemporaryRedirect)).ServeHTTP(w, r)
			return
		}
		dash.ServeHTTP(w, r)
	})
}

// to443 is where a request to the panel's port goes instead: the same page
// on port 443, while the dashboard answers the machine's name there without
// a port and the panel holds the port. Only a browser opening a page goes;
// everything else keeps answering here.
func (s *Server) to443(r *http.Request) (string, bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead || !dashboardPage(r.URL.Path) {
		return "", false
	}
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" && dest != "document" {
		return "", false
	}
	p := s.page
	p.mu.Lock()
	host, held, port := p.host, p.held[0], 443
	if held != nil {
		port = held.port
	}
	live := p.dashboard && p.reached && held != nil
	p.mu.Unlock()
	if !live || !pageHost(r.Host, host) {
		return "", false
	}
	return hostURL(strings.ToLower(host), port) + r.URL.RequestURI(), true
}

// dashboardPage reports whether a path is one of the dashboard's pages,
// which a browser opens: what the UI answers, the invite, shared map and
// friends' pack pages among them, and none of its API, /mcp, built files,
// resource packs, the names service's check, or a friends' pack's files.
func dashboardPage(p string) bool {
	switch {
	case p == "/":
		return true
	case strings.HasPrefix(p, "/api/"), p == "/api", p == "/mcp", strings.HasPrefix(p, "/mcp/"), p == "/healthz",
		strings.HasPrefix(p, "/assets/"), strings.HasPrefix(p, "/.well-known/"), strings.HasPrefix(p, packs.PathPrefix):
		return false
	case strings.HasPrefix(p, share.PathPrefix):
		token := strings.TrimPrefix(p, share.PathPrefix)
		return token != "" && !strings.Contains(token, "/")
	}
	// The UI's built files have an extension and its pages don't, but for
	// the Files tab's, whose paths end in a file's name.
	return path.Ext(p) == "" || reFilesPage.MatchString(strings.TrimPrefix(p, "/"))
}

// hostURL is https://host, with port unless it's 443.
func hostURL(host string, port int) string {
	if port != 443 && port != 0 {
		host += ":" + strconv.Itoa(port)
	}
	return "https://" + host
}

// dashboardPortView is Machine settings › Serve the dashboard on the
// standard HTTPS port (443).
type dashboardPortView struct {
	api.Dashboard443
	// URL is the dashboard's address without a port at the machine's name,
	// and Old with the panel's port; both "" without a name that works.
	URL       string `json:"url,omitempty"`
	Old       string `json:"old,omitempty"`
	PanelPort int    `json:"panelPort"`
	// Outside are the places outside Playkeeper that keep the dashboard's
	// address, each with the change it needs.
	Outside []outsideChange `json:"outside"`
}

// outsideChange is a place outside Playkeeper that keeps the dashboard's
// address, and the change it needs.
type outsideChange struct {
	// Kind is "whop_signin", the Whop app customers sign in through (App),
	// which must list Add as a redirect URL beside Keep; "whop_webhook", the
	// webhook Whop sends the store's events to, which Playkeeper moves to
	// Add itself (Automatic); or "mcp", AI agents set up with Keep, which
	// keeps working, and may move to Add.
	Kind      string `json:"kind"`
	App       string `json:"app,omitempty"`
	Add       string `json:"add"`
	Keep      string `json:"keep,omitempty"`
	Automatic bool   `json:"automatic,omitempty"`
	// Done says the change is made: Whop takes Add, or the webhook is there.
	Done bool `json:"done"`
}

func (s *Server) hDashboardPort(w http.ResponseWriter, r *http.Request, sess *session) {
	v, err := s.dashboardPortView(r.Context(), sess, r.URL.Query().Get("check") == "1")
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// dashboardPortView reads the switch from the agent, with the changes it
// needs outside Playkeeper; fresh asks Whop again instead of trusting its
// last answers.
func (s *Server) dashboardPortView(ctx context.Context, sess *session, fresh bool) (dashboardPortView, error) {
	var addr api.Address
	if _, err := s.agent.Do(ctx, http.MethodGet, "/v1/address", nil, nil, &addr); err != nil {
		return dashboardPortView{}, err
	}
	v := dashboardPortView{PanelPort: s.cfg.PanelPort, Outside: []outsideChange{}}
	if addr.Dashboard != nil {
		v.Dashboard443 = *addr.Dashboard
	}
	if host := s.certifiedHost(addr); host != "" {
		v.URL, v.Old = hostURL(host, 443), hostURL(host, s.cfg.PanelPort)
		v.Outside = s.outsideChanges(ctx, sess, v.URL, v.Old, fresh)
	}
	return v, nil
}

// outsideChanges are the places outside Playkeeper that keep the
// dashboard's address old, at the panel's port, with the change each needs
// to reach it at url, without a port. Only the owner sees Whop's.
func (s *Server) outsideChanges(ctx context.Context, sess *session, url, old string, fresh bool) []outsideChange {
	out := []outsideChange{}
	if permit(sess.Access, actSellOnWhop, "") == nil {
		if a, ok, err := s.storedWhop(); err == nil && ok {
			var id, secret string
			if err := s.db.QueryRowContext(ctx, `SELECT oauth_client_id, oauth_client_secret FROM whop_account WHERE id = 1`).Scan(&id, &secret); err == nil && id != "" {
				c := outsideChange{Kind: "whop_signin", App: id, Add: url + whopSignInCallback, Keep: old + whopSignInCallback}
				if base, err := whop.OAuthURL(s.cfg.WhopAPIURL); err == nil {
					o := s.whopOAuthAt(base, id, secret, c.Add)
					c.Done, _ = s.whopAccepts(ctx, o, fresh)
				}
				out = append(out, c)
			}
			if a.WebhookID != "" {
				add := url + whopWebhookPath
				out = append(out, outsideChange{Kind: "whop_webhook", Add: add, Automatic: true, Done: a.WebhookURL == add})
			}
		}
	}
	var tokens int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE revoked_at = 0 AND expires_at > ?`, s.now().UnixMilli()).Scan(&tokens); err == nil && tokens > 0 {
		out = append(out, outsideChange{Kind: "mcp", Add: url + "/mcp", Keep: old + "/mcp"})
	}
	return out
}

// hDashboardPortSet turns the switch on or off. The agent refuses to turn
// it on while another program has port 443, and says which.
func (s *Server) hDashboardPortSet(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		On bool `json:"on"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	p := s.page
	p.mu.Lock()
	held := p.held[0] != nil
	p.mu.Unlock()
	ctx := asActor(r.Context(), sess.User.Username)
	var st api.Dashboard443
	if _, err := s.agent.Do(ctx, http.MethodPut, "/v1/dashboard-443", nil, api.Dashboard443Request{On: req.On, Held: held, Actor: sess.User.Username}, &st); err != nil {
		s.agentFailure(w, err)
		return
	}
	if !req.On {
		// Browsers stop being sent to port 443 at once; the keeper gives the
		// port back at its look, unless the page keeps it.
		p.mu.Lock()
		p.dashboard, p.reached, p.gen = false, false, p.gen+1
		p.mu.Unlock()
	}
	s.kickPage()
	s.kickWhop()
	v, err := s.dashboardPortView(context.WithoutCancel(r.Context()), sess, false)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// hDashboardPortRetry tries port 443 again, once the owner freed it.
func (s *Server) hDashboardPortRetry(w http.ResponseWriter, r *http.Request, sess *session) {
	ctx := asActor(r.Context(), sess.User.Username)
	if _, err := s.agent.Do(ctx, http.MethodPost, "/v1/public-page/ports/retry", nil, api.ActionRequest{Actor: sess.User.Username}, nil); err != nil {
		s.agentFailure(w, err)
		return
	}
	s.page.retryNow()
	s.kickPage()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
