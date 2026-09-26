package panel

import (
	"maps"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/modpacks/share"
	"github.com/CIYAhq/playkeeper/internal/names"
	"github.com/CIYAhq/playkeeper/internal/packs"
)

// publicRoutes is the public group: the only routes the panel answers
// without a sign-in. Every one goes through publicGroup.guard, which limits
// each address, caps downloads, sets deadlines and caching, and answers
// what is unknown, switched off or on a stopped server with one 404; the
// log shows only its prefix. A later route, such as /join/, /packs/ or
// /map/, plugs in here with its prefix, limits, caching and handler, which
// answers those cases with 404, 410 or the agent's 503 (see hidden).
func (s *Server) publicRoutes() []publicRoute {
	return []publicRoute{
		{prefix: packs.PathPrefix, limits: packLimits, cache: packCache,
			handler: packs.NewHandler(packs.Store{Dir: s.cfg.ResourcePacksDir()}, s.activePacks.has)},
		{prefix: names.AlivePath, limits: aliveLimits, handler: s.aliveRoute()},
		// Wave 4: the friends' pack pages, /packs/<token>.
		{prefix: share.PathPrefix, limits: friendsPackLimits, handler: s.friendsPacks()},
	}
}

// packLimits let a group of friends behind one address join at once: each
// game downloads the pack once when it joins, and a pack may be 250 MiB.
var packLimits = publicLimits{perMinute: 60, open: 8, download: true, read: 10 * time.Second, write: 30 * time.Minute, stall: time.Minute}

// packCache lets caches keep a pack, which its SHA-1 names, as long as they
// ask again before each use, so a pack no server offers any more isn't
// served from a cache.
const packCache = "public, no-cache"

// publicDownloads is how many downloads the public group serves at once,
// to every address together.
const publicDownloads = 32

// publicNotFoundAfter is the soonest the public group answers 404, so that
// what is unknown answers no sooner than what the agent was asked about.
const publicNotFoundAfter = 200 * time.Millisecond

// publicRoute is one route of the public group.
type publicRoute struct {
	// prefix is the path prefix the route serves, logged in place of the
	// full path.
	prefix string
	limits publicLimits
	// cache is the Cache-Control of the route's successful answers; every
	// other answer, and every answer of a route without it, is no-store.
	cache   string
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
		start := time.Now()
		before := w.Header().Clone()
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
		release := func() {}
		if rt.limits.download {
			select {
			case g.downloads <- struct{}{}:
				release = sync.OnceFunc(func() { <-g.downloads })
			default:
				refuse(w, http.StatusServiceUnavailable, 5*time.Second)
				return
			}
		}
		defer release()
		rc := http.NewResponseController(w)
		end := start.Add(rt.limits.write)
		_ = rc.SetReadDeadline(start.Add(rt.limits.read))
		_ = rc.SetWriteDeadline(end)
		pw := &publicWriter{ResponseWriter: w, rc: rc, stall: rt.limits.stall, end: end, cache: rt.cache}
		rt.handler.ServeHTTP(pw, r)
		release()
		if !pw.hidden {
			return
		}
		if wait := publicNotFoundAfter - time.Since(start); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-t.C:
			case <-r.Context().Done():
			}
			t.Stop()
		}
		h := w.Header()
		clear(h)
		maps.Copy(h, before)
		h.Set("Cache-Control", "no-store")
		http.NotFound(w, r)
	})
}

// hidden reports whether the guard answers in place of a route with its
// 404: what is unknown, switched off (403, 404, 410) or on a stopped server
// (the agent's 503), or failed on the panel's side, must look alike to
// someone without a sign-in.
func hidden(status int) bool {
	return status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusGone || status >= 500
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

// publicWriter is what a public route answers through. It holds back the
// answers hidden reports for the guard's 404, gives the others the route's
// Cache-Control, and moves the write deadline on before each write, so
// that a client that stops reading is dropped after stall, while one that
// keeps reading has until end.
type publicWriter struct {
	http.ResponseWriter
	rc    *http.ResponseController
	stall time.Duration
	end   time.Time
	cache string

	wrote, hidden bool
}

func (w *publicWriter) WriteHeader(status int) {
	switch {
	case w.wrote:
		return
	case hidden(status):
		w.wrote, w.hidden = true, true
		return
	case status >= 200:
		w.wrote = true
	}
	cache := "no-store"
	if w.cache != "" && (status < 300 || status == http.StatusNotModified) {
		cache = w.cache
	}
	w.Header().Set("Cache-Control", cache)
	w.ResponseWriter.WriteHeader(status)
}

func (w *publicWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.hidden {
		return len(p), nil
	}
	if w.stall > 0 {
		d := time.Now().Add(w.stall)
		if d.After(w.end) {
			d = w.end
		}
		_ = w.rc.SetWriteDeadline(d)
	}
	return w.ResponseWriter.Write(p)
}

func (w *publicWriter) FlushError() error {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.hidden {
		return nil
	}
	return w.rc.Flush()
}

func (w *publicWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
