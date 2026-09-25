package panel

import (
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/modpacks/share"
	"github.com/CIYAhq/playkeeper/internal/packs"
)

// publicRoutes is the public group: the only routes the panel answers
// without a sign-in. Every one goes through publicGroup.guard and is
// logged by its prefix only.
func (s *Server) publicRoutes() []publicRoute {
	return []publicRoute{
		{packs.PathPrefix, packLimits, packs.NewHandler(packs.Store{Dir: s.cfg.ResourcePacksDir()}, s.activePacks.has)},
		// Wave 4: the friends' pack pages, /packs/<token>.
		{share.PathPrefix, friendsPackLimits, s.friendsPacks()},
	}
}

// packLimits let a group of friends behind one address join at once: each
// game downloads the pack once when it joins, and a pack may be 250 MiB.
var packLimits = publicLimits{perMinute: 60, open: 8, download: true, read: 10 * time.Second, write: 30 * time.Minute, stall: time.Minute}

// publicDownloads is how many downloads the public group serves at once,
// to every address together.
const publicDownloads = 32

// publicRoute is one route of the public group.
type publicRoute struct {
	// prefix is the path prefix the route serves, logged in place of the
	// full path.
	prefix  string
	limits  publicLimits
	handler http.Handler
}

// publicLimits bound what one address can make a public route do. An
// address is an IPv4 address or an IPv6 /64, as a network usually gets a
// whole /64.
type publicLimits struct {
	// perMinute is how many requests an address may make a minute, and open
	// how many it may have open at once.
	perMinute, open int
	// download marks responses that hold one of the publicDownloads slots.
	download bool
	// read bounds reading the request, write bounds writing the response,
	// and stall bounds one write waiting on a client that stopped reading.
	// A read deadline that passes during a long response cancels the
	// request's context, so such handlers mustn't depend on it.
	read, write, stall time.Duration
}

type publicGroup struct {
	routes    []publicRoute
	downloads chan struct{}

	mu   sync.Mutex
	open map[string]int
}

func newPublicGroup(routes []publicRoute, now func() time.Time) *publicGroup {
	g := &publicGroup{downloads: make(chan struct{}, publicDownloads), open: map[string]int{}}
	for _, rt := range routes {
		rt.handler = g.guard(rt, newLimiter(rt.limits.perMinute, time.Minute, now))
		g.routes = append(g.routes, rt)
	}
	return g
}

// handler is the guarded handler of the route that serves prefix.
func (g *publicGroup) handler(prefix string) http.Handler {
	for _, rt := range g.routes {
		if rt.prefix == prefix {
			return rt.handler
		}
	}
	return http.NotFoundHandler()
}

// logPath is p, or only its route's prefix when p is a public route's:
// public paths may hold codes that work without a sign-in.
func (g *publicGroup) logPath(p string) string {
	for _, rt := range g.routes {
		if strings.HasPrefix(p, rt.prefix) {
			return rt.prefix + "…"
		}
	}
	return p
}

func (g *publicGroup) guard(rt publicRoute, perMinute *limiter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		key := rt.prefix + " " + addressKey(r.RemoteAddr)
		if ok, wait := perMinute.allow(key); !ok {
			refuse(w, http.StatusTooManyRequests, wait)
			return
		}
		if !g.enter(key, rt.limits.open) {
			refuse(w, http.StatusTooManyRequests, time.Second)
			return
		}
		defer g.leave(key)
		if rt.limits.download {
			select {
			case g.downloads <- struct{}{}:
				defer func() { <-g.downloads }()
			default:
				refuse(w, http.StatusServiceUnavailable, 5*time.Second)
				return
			}
		}
		rc := http.NewResponseController(w)
		start := time.Now()
		end := start.Add(rt.limits.write)
		_ = rc.SetReadDeadline(start.Add(rt.limits.read))
		_ = rc.SetWriteDeadline(end)
		rt.handler.ServeHTTP(&stallWriter{ResponseWriter: w, rc: rc, stall: rt.limits.stall, end: end}, r)
	})
}

func (g *publicGroup) enter(key string, limit int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open[key] >= limit {
		return false
	}
	g.open[key]++
	return true
}

func (g *publicGroup) leave(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open[key]--; g.open[key] <= 0 {
		delete(g.open, key)
	}
}

func refuse(w http.ResponseWriter, status int, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	http.Error(w, http.StatusText(status), status)
}

// addressKey is the address a request came from, taken from the connection
// and never from headers a client chooses: an IPv4 address or an IPv6 /64.
func addressKey(remote string) string {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return remote
	}
	a := ap.Addr().Unmap().WithZone("")
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return p.String()
}

// stallWriter moves the write deadline on before each write, so that a
// client that stops reading is dropped after stall, while one that keeps
// reading has until end.
type stallWriter struct {
	http.ResponseWriter
	rc    *http.ResponseController
	stall time.Duration
	end   time.Time
}

func (w *stallWriter) Write(p []byte) (int, error) {
	if w.stall > 0 {
		d := time.Now().Add(w.stall)
		if d.After(w.end) {
			d = w.end
		}
		_ = w.rc.SetWriteDeadline(d)
	}
	return w.ResponseWriter.Write(p)
}

func (w *stallWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
